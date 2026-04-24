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

	"knative.dev/pkg/configmap"
	"knative.dev/pkg/controller"

	yipyapsourceinformer "github.com/YipYap-run/knative-source/pkg/client/injection/informers/sources/v1alpha1/yipyapsource"
	yipyapsourcereconciler "github.com/YipYap-run/knative-source/pkg/client/injection/reconciler/sources/v1alpha1/yipyapsource"
)

// NewController registers the YipYapSource reconciler with the controller
// impl. Wire-up is intentionally minimal here (Task 4.1 scaffolding); informers
// for owned resources (Deployment, EventPolicy, etc.) are added in Tasks
// 4.3 / 4.4 / 4.6.
func NewController(
	ctx context.Context,
	cmw configmap.Watcher,
) *controller.Impl {
	yipyapSourceInformer := yipyapsourceinformer.Get(ctx)

	r := &Reconciler{}
	impl := yipyapsourcereconciler.NewImpl(ctx, r)

	yipyapSourceInformer.Informer().AddEventHandler(controller.HandleAll(impl.Enqueue))

	return impl
}
