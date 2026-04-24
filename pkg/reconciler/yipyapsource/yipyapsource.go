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

// Package yipyapsource contains the controller reconciler for
// YipYapSource custom resources. It:
//
//  1. Resolves the user-supplied sink via URIResolver.
//  2. Renders and reconciles the adapter Deployment that ferries events
//     from the yipyap console into the resolved sink.
//  3. Surfaces the source's Ready / SinkProvided / Deployed conditions
//     plus the known set of CloudEvent type attributes this source emits.
package yipyapsource

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	appsv1listers "k8s.io/client-go/listers/apps/v1"

	duckv1 "knative.dev/pkg/apis/duck/v1"
	pkgreconciler "knative.dev/pkg/reconciler"
	"knative.dev/pkg/resolver"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
	reconcileryipyapsource "github.com/YipYap-run/knative-source/pkg/client/injection/reconciler/sources/v1alpha1/yipyapsource"
	"github.com/YipYap-run/knative-source/pkg/reconciler/yipyapsource/resources"
)

// knownCloudEventAttributes advertises the CloudEvent types this source is
// capable of producing. Source attribute is intentionally left empty because
// the concrete source is org-scoped and only becomes known at runtime (the
// adapter sets it per-event).
var knownCloudEventAttributes = []duckv1.CloudEventAttributes{
	{Type: "run.yipyap.alert.fired.v1"},
	{Type: "run.yipyap.alert.acknowledged.v1"},
	{Type: "run.yipyap.alert.resolved.v1"},
	{Type: "run.yipyap.alert.escalated.v1"},
	{Type: "run.yipyap.monitor.up.v1"},
	{Type: "run.yipyap.monitor.down.v1"},
	{Type: "run.yipyap.monitor.degraded.v1"},
	{Type: "run.yipyap.monitor.heartbeat_missed.v1"},
	{Type: "run.yipyap.maintenance.started.v1"},
	{Type: "run.yipyap.maintenance.ended.v1"},
}

// Reconciler reconciles YipYapSource resources. It is instantiated by
// NewController; its exported fields are deliberately unset on the zero
// value because the controller wires them in.
type Reconciler struct {
	// kubeClientSet creates/updates the adapter Deployment.
	kubeClientSet kubernetes.Interface

	// deploymentLister reads the current state of the adapter Deployment
	// from the shared informer cache.
	deploymentLister appsv1listers.DeploymentLister

	// sinkResolver resolves the YipYapSource.Spec.Sink destination into a URI.
	sinkResolver *resolver.URIResolver

	// adapterImage is the image the rendered Deployment runs; pulled from the
	// YIPYAP_SOURCE_RA_IMAGE env var when the controller starts.
	adapterImage string
}

// Check that our Reconciler implements Interface.
var _ reconcileryipyapsource.Interface = (*Reconciler)(nil)

// ReconcileKind implements Interface.ReconcileKind.
func (r *Reconciler) ReconcileKind(ctx context.Context, src *v1alpha1.YipYapSource) pkgreconciler.Event {
	src.Status.InitializeConditions()
	src.Status.ObservedGeneration = src.Generation
	src.Status.CloudEventAttributes = knownCloudEventAttributes

	// Resolve the sink.
	dest := src.Spec.Sink.DeepCopy()
	if dest.Ref != nil && dest.Ref.Namespace == "" {
		dest.Ref.Namespace = src.GetNamespace()
	}
	uri, err := r.sinkResolver.URIFromDestinationV1(ctx, *dest, src)
	if err != nil {
		src.Status.MarkNoSink("NotFound", "%s", err.Error())
		return fmt.Errorf("resolve sink: %w", err)
	}
	src.Status.MarkSink(uri)

	// Reconcile the adapter Deployment.
	expected := resources.MakeAdapterDeployment(src, uri, r.adapterImage)
	current, err := r.deploymentLister.Deployments(src.Namespace).Get(expected.Name)
	switch {
	case apierrors.IsNotFound(err):
		current, err = r.kubeClientSet.AppsV1().Deployments(src.Namespace).Create(ctx, expected, metav1.CreateOptions{})
		if err != nil {
			src.Status.MarkNotDeployed("CreateFailed", "%s", err.Error())
			return fmt.Errorf("create Deployment: %w", err)
		}
	case err != nil:
		return fmt.Errorf("get Deployment: %w", err)
	default:
		if !equality.Semantic.DeepEqual(expected.Spec.Template, current.Spec.Template) ||
			!equality.Semantic.DeepEqual(expected.Spec.Replicas, current.Spec.Replicas) {
			updated := current.DeepCopy()
			updated.Spec.Template = expected.Spec.Template
			updated.Spec.Replicas = expected.Spec.Replicas
			current, err = r.kubeClientSet.AppsV1().Deployments(src.Namespace).Update(ctx, updated, metav1.UpdateOptions{})
			if err != nil {
				src.Status.MarkNotDeployed("UpdateFailed", "%s", err.Error())
				return fmt.Errorf("update Deployment: %w", err)
			}
		}
	}
	src.Status.DeploymentName = current.Name

	// Surface Deployment availability on the source's Deployed condition.
	propagateDeploymentAvailability(&src.Status, current)

	return nil
}

// propagateDeploymentAvailability maps the Deployment's Available condition
// onto the YipYapSource status. If the Deployment has no Available condition
// yet (freshly created), we leave the status at the "Unknown" default set by
// InitializeConditions so callers see "Deploying" rather than a stale True.
func propagateDeploymentAvailability(status *v1alpha1.YipYapSourceStatus, dep *appsv1.Deployment) {
	for _, cond := range dep.Status.Conditions {
		if cond.Type != appsv1.DeploymentAvailable {
			continue
		}
		switch cond.Status {
		case corev1.ConditionTrue:
			status.MarkDeployed()
		case corev1.ConditionFalse:
			status.MarkNotDeployed(cond.Reason, "%s", cond.Message)
		case corev1.ConditionUnknown:
			status.MarkDeploying(cond.Reason, "%s", cond.Message)
		}
		return
	}
}
