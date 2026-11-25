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
//  1. Checks spec.baseURL against the operator policy. A source that fails
//     it gets BaseURLAllowed=False and no adapter (an existing one is
//     deleted), since the adapter would send the API key to that URL.
//  2. Resolves the user-supplied sink to an address via URIResolver and
//     records its URI, CA certificates and OIDC audience in status.
//  3. For an OIDC sink, makes sure the adapter has a ServiceAccount
//     identity (a generated one unless spec.serviceAccountName is set).
//  4. Renders and reconciles the adapter Deployment that ferries events
//     from the yipyap console into the resolved sink.
//  5. Surfaces the source's Ready / SinkProvided / Deployed conditions
//     plus the known set of CloudEvent type attributes this source emits.
//  6. Reads the adapter's own status into the Connected condition, so a
//     rejected API key or a failing sink makes the source NotReady with a
//     plain reason (see connection.go).
package yipyapsource

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	"github.com/YipYap-run/knative-source/pkg/baseurl"
	"github.com/YipYap-run/knative-source/pkg/cacerts"
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

	// baseURLPolicy is the operator allowlist for spec.baseURL, read from
	// YIPYAP_ALLOWED_BASE_URL_HOSTS. Nil means baseurl.Default().
	baseURLPolicy *baseurl.Policy

	// adapterObserver reads the running adapter's connection state for the
	// Connected condition. Nil leaves Connected alone.
	adapterObserver adapterObserver

	// enqueueAfter schedules another reconcile of src. Nil disables
	// rechecks (table tests).
	enqueueAfter func(src interface{}, after time.Duration)
}

// Check that our Reconciler implements Interface.
var _ reconcileryipyapsource.Interface = (*Reconciler)(nil)

// ReconcileKind implements Interface.ReconcileKind.
func (r *Reconciler) ReconcileKind(ctx context.Context, src *v1alpha1.YipYapSource) pkgreconciler.Event {
	src.Status.InitializeConditions()
	src.Status.ObservedGeneration = src.Generation
	src.Status.CloudEventAttributes = knownCloudEventAttributes

	// The webhook enforces the same policy, but sources created before it
	// existed, or while the webhook was bypassed, reach here too. Never run
	// an adapter that would send the API key to a URL the operator has not
	// allowed.
	policy := r.baseURLPolicy
	if policy == nil {
		policy = baseurl.Default()
	}
	if err := policy.Check(src.Spec.BaseURL); err != nil {
		src.Status.MarkBaseURLNotAllowed("BaseURLNotAllowed", "spec.baseURL %s", err.Error())
		return r.stopAdapter(ctx, src)
	}
	src.Status.MarkBaseURLAllowed()

	// Resolve the sink.
	dest := src.Spec.Sink.DeepCopy()
	if dest.Ref != nil && dest.Ref.Namespace == "" {
		dest.Ref.Namespace = src.GetNamespace()
	}
	addr, err := r.sinkResolver.AddressableFromDestinationV1(ctx, *dest, src)
	if err != nil {
		src.Status.MarkNoSink("NotFound", "%s", err.Error())
		return fmt.Errorf("resolve sink: %w", err)
	}
	if addr.CACerts != nil && *addr.CACerts != "" {
		// The adapter refuses to start on a bundle it cannot parse. Say so
		// here instead, and leave any running adapter on its old config.
		if _, err := cacerts.Parse(*addr.CACerts); err != nil {
			src.Status.MarkSinkAddress(nil)
			src.Status.MarkNoSink("InvalidCACerts", "sink CA certificates: %s", err.Error())
			return nil
		}
	}
	src.Status.MarkSinkAddress(addr)

	var pullSecrets []corev1.LocalObjectReference
	if resources.SinkHasAudience(addr) {
		if err := r.ensureOIDCServiceAccount(ctx, src); err != nil {
			src.Status.MarkNotDeployed("ServiceAccountFailed", "%s", err.Error())
			return err
		}
		if pullSecrets, err = r.pinnedPullSecrets(ctx, src); err != nil {
			src.Status.MarkNotDeployed("ServiceAccountFailed", "%s", err.Error())
			return err
		}
		src.Status.MarkOIDCIdentity(resources.OIDCServiceAccountName(src))
	} else {
		src.Status.MarkOIDCIdentity("")
	}

	// Reconcile the adapter Deployment.
	expected := resources.MakeAdapterDeploymentForSink(src, addr, r.adapterImage)
	expected.Spec.Template.Spec.ImagePullSecrets = pullSecrets
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
	case !metav1.IsControlledBy(current, src):
		// Never update or delete a Deployment this source does not own.
		msg := fmt.Sprintf("Deployment %q exists and is not owned by YipYapSource %q", current.Name, src.Name)
		src.Status.MarkNotDeployed("NotOwned", "%s", msg)
		return errors.New(msg)
	case current.DeletionTimestamp != nil:
		// The old Deployment is still being deleted, for example after a
		// selector change whose delete has not finished. Updating or deleting
		// it again does nothing useful, and a create would hit AlreadyExists.
		// Its removal fires a delete event on the owned-Deployment informer,
		// which enqueues this source again.
		src.Status.MarkConnecting(reasonAdapterStarting, "%s", msgAdapterStarting)
		src.Status.MarkDeploying("Terminating", "waiting for Deployment %q to finish deleting", current.Name)
		src.Status.DeploymentName = current.Name
		return nil
	case !equality.Semantic.DeepEqual(expected.Spec.Selector, current.Spec.Selector):
		// spec.selector is immutable on a Deployment, so an update cannot fix
		// it. Delete the old Deployment and create it again with the new
		// selector. The adapter pod restarts, as it would on a template change.
		current, err = r.recreateDeployment(ctx, current, expected)
		if err != nil {
			src.Status.MarkNotDeployed("RecreateFailed", "%s", err.Error())
			return err
		}
	default:
		// Compare only the fields this controller sets. The live copy has
		// server defaults and may carry annotations such as
		// kubectl.kubernetes.io/restartedAt, which a full template compare
		// would see as drift on every pass.
		if resources.NeedsUpdate(expected, current) {
			updated := resources.MergeForUpdate(expected, current)
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

	// Last, so that when the adapter reports a problem (for example a
	// rejected API key) that is the reason Ready shows, not the Deployment
	// unavailability it causes.
	r.propagateConnection(ctx, src)

	return nil
}

// stopAdapter deletes the source's own adapter Deployment, if there is one,
// and marks Deployed=False. A Deployment the source does not own is left
// alone. It returns nil once nothing of ours is running, so the source is
// not requeued until its spec or the policy changes.
func (r *Reconciler) stopAdapter(ctx context.Context, src *v1alpha1.YipYapSource) error {
	const reason = "BaseURLNotAllowed"
	const msg = "the adapter does not run while spec.baseURL is not allowed"
	src.Status.DeploymentName = ""

	name := resources.DeploymentName(src)
	current, err := r.deploymentLister.Deployments(src.Namespace).Get(name)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return fmt.Errorf("get Deployment: %w", err)
	case !metav1.IsControlledBy(current, src), current.DeletionTimestamp != nil:
	default:
		uid := current.UID
		propagation := metav1.DeletePropagationBackground
		err := r.kubeClientSet.AppsV1().Deployments(src.Namespace).Delete(ctx, name, metav1.DeleteOptions{
			Preconditions:     &metav1.Preconditions{UID: &uid},
			PropagationPolicy: &propagation,
		})
		if err != nil && !apierrors.IsNotFound(err) {
			src.Status.MarkNotDeployed("DeleteFailed", "%s", err.Error())
			return fmt.Errorf("delete adapter Deployment for disallowed baseURL: %w", err)
		}
	}
	src.Status.MarkNotDeployed(reason, "%s", msg)
	src.Status.MarkConnecting(reason, "%s", msg)
	return nil
}

// ensureOIDCServiceAccount creates the generated OIDC ServiceAccount for src
// if it does not exist. An account of that name that src does not own is an
// error: running the adapter as it would let the source borrow an identity
// someone else's EventPolicy may trust.
func (r *Reconciler) ensureOIDCServiceAccount(ctx context.Context, src *v1alpha1.YipYapSource) error {
	want := resources.MakeOIDCServiceAccount(src)
	sas := r.kubeClientSet.CoreV1().ServiceAccounts(src.Namespace)
	current, err := sas.Get(ctx, want.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := sas.Create(ctx, want, metav1.CreateOptions{}); err != nil {
			// AlreadyExists included: the next pass checks the owner.
			return fmt.Errorf("create OIDC ServiceAccount: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("get OIDC ServiceAccount: %w", err)
	case !metav1.IsControlledBy(current, src):
		return fmt.Errorf("ServiceAccount %q exists and is not owned by YipYapSource %q", current.Name, src.Name)
	}
	return nil
}

// pinnedPullSecrets returns the imagePullSecrets of spec.serviceAccountName
// for an adapter that runs as the generated OIDC account instead (see
// resources.AdapterServiceAccountName), so a source that pins an account for
// a private registry still pulls its image. Nil when no account is pinned.
func (r *Reconciler) pinnedPullSecrets(ctx context.Context, src *v1alpha1.YipYapSource) ([]corev1.LocalObjectReference, error) {
	name := src.Spec.ServiceAccountName
	if name == "" {
		return nil, nil
	}
	sa, err := r.kubeClientSet.CoreV1().ServiceAccounts(src.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get ServiceAccount %q for its imagePullSecrets: %w", name, err)
	}
	if len(sa.ImagePullSecrets) == 0 {
		return nil, nil
	}
	return append([]corev1.LocalObjectReference(nil), sa.ImagePullSecrets...), nil
}

// recreateDeployment deletes current (guarded by its UID so a Deployment
// recreated by someone else is not removed) and creates expected in its place.
func (r *Reconciler) recreateDeployment(ctx context.Context, current, expected *appsv1.Deployment) (*appsv1.Deployment, error) {
	deployments := r.kubeClientSet.AppsV1().Deployments(current.Namespace)
	uid := current.UID
	// Background propagation removes the Deployment object right away (its
	// old ReplicaSets are collected afterwards), so the create below does not
	// race a foreground deletion.
	propagation := metav1.DeletePropagationBackground
	err := deployments.Delete(ctx, current.Name, metav1.DeleteOptions{
		Preconditions:     &metav1.Preconditions{UID: &uid},
		PropagationPolicy: &propagation,
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("delete Deployment with outdated selector: %w", err)
	}
	created, err := deployments.Create(ctx, expected, metav1.CreateOptions{})
	if err != nil {
		// AlreadyExists means the delete has not finished yet. Returning the
		// error requeues the key and the next pass creates it.
		return nil, fmt.Errorf("create Deployment after selector change: %w", err)
	}
	return created, nil
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
