/*
Copyright 2019 The Knative Authors

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

	reconcilersource "knative.dev/eventing/pkg/reconciler/source"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"

	"github.com/kelseyhightower/envconfig"
	"k8s.io/client-go/tools/cache"

	"knative.dev/pkg/configmap"
	"knative.dev/pkg/controller"
	"knative.dev/pkg/logging"
	"knative.dev/pkg/resolver"

	"github.com/YipYap-run/knative-source/pkg/reconciler"

	kubeclient "knative.dev/pkg/client/injection/kube/client"
	deploymentinformer "knative.dev/pkg/client/injection/kube/informers/apps/v1/deployment"
	yipyapsourceinformer "github.com/YipYap-run/knative-source/pkg/client/injection/informers/sources/v1alpha1/yipyapsource"
	"github.com/YipYap-run/knative-source/pkg/client/injection/reconciler/sources/v1alpha1/yipyapsource"
)

// NewController initializes the controller and is called by the generated code
// Registers event handlers to enqueue events
func NewController(
	ctx context.Context,
	cmw configmap.Watcher,
) *controller.Impl {
	deploymentInformer := deploymentinformer.Get(ctx)
	yipyapSourceInformer := yipyapsourceinformer.Get(ctx)

	r := &Reconciler{
		dr: &reconciler.DeploymentReconciler{KubeClientSet: kubeclient.Get(ctx)},
		// Config accessor takes care of tracing/config/logging config propagation to the receive adapter
		configAccessor: reconcilersource.WatchConfigurations(ctx, "yipyap-source", cmw),
	}
	if err := envconfig.Process("", r); err != nil {
		logging.FromContext(ctx).Panicf("required environment variable is not defined: %v", err)
	}

	impl := yipyapsource.NewImpl(ctx, r)

	r.sinkResolver = resolver.NewURIResolverFromTracker(ctx, impl.Tracker)

	yipyapSourceInformer.Informer().AddEventHandler(controller.HandleAll(impl.Enqueue))

	deploymentInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: controller.FilterController(&v1alpha1.YipYapSource{}),
		Handler:    controller.HandleAll(impl.EnqueueControllerOf),
	})

	return impl
}
