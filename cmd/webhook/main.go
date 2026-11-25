/*
Copyright 2026 The YipYap Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"os"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime/schema"
	o11yconfigmap "knative.dev/eventing/pkg/observability/configmap"
	kubeclient "knative.dev/pkg/client/injection/kube/client"
	"knative.dev/pkg/configmap"
	"knative.dev/pkg/controller"
	"knative.dev/pkg/injection/sharedmain"
	"knative.dev/pkg/logging"
	"knative.dev/pkg/signals"
	"knative.dev/pkg/webhook"
	"knative.dev/pkg/webhook/certificates"
	"knative.dev/pkg/webhook/configmaps"
	"knative.dev/pkg/webhook/resourcesemantics"
	"knative.dev/pkg/webhook/resourcesemantics/defaulting"
	"knative.dev/pkg/webhook/resourcesemantics/validation"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
	"github.com/YipYap-run/knative-source/pkg/baseurl"
	"github.com/YipYap-run/knative-source/pkg/secretaccess"
)

// types is the set of resources the webhook will default and validate.
var types = map[schema.GroupVersionKind]resourcesemantics.GenericCRD{
	v1alpha1.SchemeGroupVersion.WithKind("YipYapSource"): &v1alpha1.YipYapSource{},
}

// callbacks is the map of extra validating callbacks. It is empty because
// Validate() on the type does the work.
var callbacks = map[schema.GroupVersionKind]validation.Callback{}

const (
	admissionWebhookName = "yipyap-source-webhook"

	// Webhook admission-controller names. These are the canonical
	// `*.sources.yipyap.run` identifiers that MutatingWebhookConfiguration
	// and ValidatingWebhookConfiguration resources must match.
	defaultingWebhookName       = "defaulting.webhook.sources.yipyap.run"
	validationWebhookName       = "validation.webhook.sources.yipyap.run"
	configValidationWebhookName = "config.webhook.sources.yipyap.run"
)

// NewDefaultingAdmissionController sets up the mutating admission webhook
// that runs SetDefaults() on every YipYapSource create/update.
func NewDefaultingAdmissionController(ctx context.Context, cmw configmap.Watcher) *controller.Impl {
	return defaulting.NewAdmissionController(ctx,

		// Name of the resource webhook.
		defaultingWebhookName,

		// The path on which to serve the webhook.
		"/defaulting",

		// The resource to default.
		types,

		// A function that infuses the context passed to Validate/SetDefaults
		// with custom metadata.
		func(ctx context.Context) context.Context {
			return ctx
		},

		// Whether to disallow unknown fields.
		true,
	)
}

// NewValidationAdmissionController sets up the validating admission webhook
// that runs Validate() on every YipYapSource create/update. Each request
// context carries the operator's baseURL policy (from
// YIPYAP_ALLOWED_BASE_URL_HOSTS) and a SubjectAccessReview checker, so
// Validate can enforce https plus the host allowlist and confirm that the
// requesting user may read the API key Secret.
func NewValidationAdmissionController(ctx context.Context, cmw configmap.Watcher) *controller.Impl {
	policy, err := baseurl.FromEnv(os.LookupEnv)
	if err != nil {
		logging.FromContext(ctx).Fatalw("invalid base URL allowlist", zap.Error(err))
	}
	logging.FromContext(ctx).Infow("base URL allowlist", zap.Strings("hosts", policy.Hosts()))
	infuse := validationContext(policy, secretaccess.SARChecker{Client: kubeclient.Get(ctx)})

	return validation.NewAdmissionController(ctx,

		// Name of the resource webhook.
		validationWebhookName,

		// The path on which to serve the webhook.
		"/resource-validation",

		// The resources to validate.
		types,

		// A function that infuses the context passed to Validate/SetDefaults
		// with custom metadata.
		infuse,

		// Whether to disallow unknown fields.
		true,

		// Extra validating callbacks to be applied to resources.
		callbacks,
	)
}

// validationContext returns the per-request context infuser for the
// validating webhook.
func validationContext(policy *baseurl.Policy, checker v1alpha1.SecretReadChecker) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		ctx = baseurl.WithPolicy(ctx, policy)
		return v1alpha1.WithSecretReadChecker(ctx, checker)
	}
}

// NewConfigValidationController sets up a ConfigMap validation webhook so
// pinned configmap schemas (logging, observability) are rejected early.
// This is a stub wiring that validates the shared knative configmaps we
// already consume; add more Constructors here as we grow our own.
func NewConfigValidationController(ctx context.Context, cmw configmap.Watcher) *controller.Impl {
	return configmaps.NewAdmissionController(ctx,

		// Name of the configmap webhook.
		configValidationWebhookName,

		// The path on which to serve the webhook.
		"/config-validation",

		// The configmaps to validate.
		configmap.Constructors{
			logging.ConfigMapName(): logging.NewConfigFromConfigMap,
			o11yconfigmap.Name():    o11yconfigmap.Parse,
		},
	)
}

func main() {
	// Set up a signal context with our webhook options.
	ctx := webhook.WithOptions(signals.NewContext(), webhook.Options{
		ServiceName: admissionWebhookName,
		Port:        8443,
		SecretName:  "yipyap-source-webhook-certs",
	})

	sharedmain.WebhookMainWithContext(ctx, admissionWebhookName,
		certificates.NewController,
		NewDefaultingAdmissionController,
		NewValidationAdmissionController,
		NewConfigValidationController,
	)
}
