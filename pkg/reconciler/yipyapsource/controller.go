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

package yipyapsource

import (
	"context"

	"github.com/kelseyhightower/envconfig"
	"go.uber.org/zap"

	"k8s.io/client-go/tools/cache"

	kubeclient "knative.dev/pkg/client/injection/kube/client"
	deploymentinformer "knative.dev/pkg/client/injection/kube/informers/apps/v1/deployment"
	"knative.dev/pkg/configmap"
	"knative.dev/pkg/controller"
	"knative.dev/pkg/logging"
	"knative.dev/pkg/resolver"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
	yipyapsourceinformer "github.com/YipYap-run/knative-source/pkg/client/injection/informers/sources/v1alpha1/yipyapsource"
	yipyapsourcereconciler "github.com/YipYap-run/knative-source/pkg/client/injection/reconciler/sources/v1alpha1/yipyapsource"
)

// envConfig pulls controller-wide settings out of env vars. The adapter image
// is the only required knob today.
type envConfig struct {
	Image string `envconfig:"YIPYAP_SOURCE_RA_IMAGE" required:"true"`
}

// NewController builds the YipYapSource controller Impl. It wires informers
// for both the YipYapSource itself and the Deployments it owns, reads the
// adapter image from the environment, and installs a URIResolver backed by
// the controller's built-in tracker for sink refs.
func NewController(
	ctx context.Context,
	cmw configmap.Watcher,
) *controller.Impl {
	env := &envConfig{}
	if err := envconfig.Process("", env); err != nil {
		logging.FromContext(ctx).Panicw("cannot process YIPYAP_SOURCE_RA_IMAGE", zap.Error(err))
	}

	deploymentInformer := deploymentinformer.Get(ctx)
	yipyapSourceInformer := yipyapsourceinformer.Get(ctx)

	r := &Reconciler{
		kubeClientSet:    kubeclient.Get(ctx),
		deploymentLister: deploymentInformer.Lister(),
		adapterImage:     env.Image,
	}
	impl := yipyapsourcereconciler.NewImpl(ctx, r)

	// URIResolver is driven by the controller's tracker so that changes to
	// Channel/Broker/etc. sink targets re-enqueue the owning YipYapSource.
	r.sinkResolver = resolver.NewURIResolverFromTracker(ctx, impl.Tracker)

	yipyapSourceInformer.Informer().AddEventHandler(controller.HandleAll(impl.Enqueue))

	// Re-enqueue the YipYapSource when its owned Deployment changes so the
	// Deployed/Ready conditions track availability promptly.
	deploymentInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: controller.FilterController(&v1alpha1.YipYapSource{}),
		Handler:    controller.HandleAll(impl.EnqueueControllerOf),
	})

	return impl
}
