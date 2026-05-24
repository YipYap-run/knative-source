/*
Copyright 2026 Cyra.

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

	"k8s.io/apimachinery/pkg/runtime/schema"
	o11yconfigmap "knative.dev/eventing/pkg/observability/configmap"
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
)

// types is the set of resources the webhook will default and validate.
var types = map[schema.GroupVersionKind]resourcesemantics.GenericCRD{
	v1alpha1.SchemeGroupVersion.WithKind("YipYapSource"): &v1alpha1.YipYapSource{},
}

// callbacks is the map of extra validating callbacks; currently empty —
// Validate() on the type itself does the work.
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
// that runs Validate() on every YipYapSource create/update.
func NewValidationAdmissionController(ctx context.Context, cmw configmap.Watcher) *controller.Impl {
	return validation.NewAdmissionController(ctx,

		// Name of the resource webhook.
		validationWebhookName,

		// The path on which to serve the webhook.
		"/resource-validation",

		// The resources to validate.
		types,

		// A function that infuses the context passed to Validate/SetDefaults
		// with custom metadata.
		func(ctx context.Context) context.Context {
			return ctx
		},

		// Whether to disallow unknown fields.
		true,

		// Extra validating callbacks to be applied to resources.
		callbacks,
	)
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
