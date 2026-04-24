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

// Package yipyapsource contains the controller for YipYapSource custom resources.
//
// This is a stub for Task 4.1 (repo bootstrap). The real reconciler lands in
// Task 4.3 (sink resolution) and Task 4.4 (adapter Deployment management).
package yipyapsource

import (
	"context"

	pkgreconciler "knative.dev/pkg/reconciler"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
	reconcileryipyapsource "github.com/YipYap-run/knative-source/pkg/client/injection/reconciler/sources/v1alpha1/yipyapsource"
)

// Reconciler reconciles a YipYapSource object.
//
// Placeholder: fields and logic land in Tasks 4.3 / 4.4 / 4.5.
type Reconciler struct{}

// Check that our Reconciler implements Interface.
var _ reconcileryipyapsource.Interface = (*Reconciler)(nil)

// ReconcileKind implements Interface.ReconcileKind.
//
// Stub: returns nil. Task 4.3 wires sink resolution; Task 4.4 manages the
// adapter Deployment; Task 4.5 wires status conditions.
func (r *Reconciler) ReconcileKind(ctx context.Context, src *v1alpha1.YipYapSource) pkgreconciler.Event {
	return nil
}
