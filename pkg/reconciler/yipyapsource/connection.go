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
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	"knative.dev/pkg/apis"
	"knative.dev/pkg/logging"

	"github.com/YipYap-run/knative-source/pkg/adapterstatus"
	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
	"github.com/YipYap-run/knative-source/pkg/reconciler/yipyapsource/resources"
)

// The Connected condition comes from the adapter itself. The adapter has no
// Kubernetes credentials (its pod mounts no token), so it cannot write the
// source's status. Instead the controller lists the source's adapter pod and
// reads its status endpoint (adapterstatus.Path) over the pod network. When
// that endpoint cannot be reached, for example because a NetworkPolicy in
// the source's namespace blocks the controller, the controller falls back to
// the pod's Ready condition with a generic message.

// Reasons the controller sets on Connected itself. The adapter's own reasons
// are in package adapterstatus.
const (
	reasonAdapterStarting = "AdapterStarting"
	reasonAdapterNotReady = "AdapterNotReady"
	reasonStatusUnknown   = "AdapterStatusUnknown"
)

const (
	msgAdapterStarting = "waiting for the adapter pod to start"
	msgAdapterNotReady = "the adapter is not ready and its status endpoint could not be read; check the adapter pod logs"
)

// connectionRecheck is how often a source whose Connected condition is not
// True is reconciled again, so a fixed API key or sink shows up without
// waiting for the next Deployment change.
const connectionRecheck = 30 * time.Second

// statusFetchTimeout bounds one read of an adapter's status endpoint.
const statusFetchTimeout = 2 * time.Second

// statusBodyLimit bounds how much of the status response is read.
const statusBodyLimit = 4 << 10

// adapterObservation is what the controller learned about a source's
// running adapter.
type adapterObservation struct {
	// PodFound is false when no adapter pod exists yet.
	PodFound bool
	// PodRunning is true once the pod's phase is Running.
	PodRunning bool
	// PodReady mirrors the pod's Ready condition (the /readyz probe).
	PodReady bool
	// Status is the adapter's own report, or nil when it could not be read.
	Status *adapterstatus.Status
}

// adapterObserver looks up a source's adapter. The real one is
// podObserver; tests swap in a fake.
type adapterObserver interface {
	Observe(ctx context.Context, src *v1alpha1.YipYapSource) (adapterObservation, error)
}

// connectedCondition maps an observation onto the Connected condition. The
// adapter's report wins when it could be read; the pod's readiness is the
// fallback.
func connectedCondition(obs adapterObservation) (corev1.ConditionStatus, string, string) {
	if !obs.PodFound {
		return corev1.ConditionUnknown, reasonAdapterStarting, msgAdapterStarting
	}
	if st := obs.Status; st != nil {
		if st.Ready {
			return corev1.ConditionTrue, "", ""
		}
		reason := st.Reason
		if !adapterstatus.KnownReason(reason) {
			reason = reasonAdapterNotReady
		}
		msg := adapterstatus.CleanMessage(st.Message)
		if msg == "" {
			msg = "the adapter reports it is not ready"
		}
		return corev1.ConditionFalse, reason, msg
	}
	switch {
	case !obs.PodRunning:
		return corev1.ConditionUnknown, reasonAdapterStarting, msgAdapterStarting
	case obs.PodReady:
		return corev1.ConditionTrue, "", ""
	default:
		return corev1.ConditionFalse, reasonAdapterNotReady, msgAdapterNotReady
	}
}

// propagateConnection sets the Connected condition from the adapter and
// schedules a recheck while it is not True. Call it last, after Deployed, so
// a connection problem is the reason Ready shows.
func (r *Reconciler) propagateConnection(ctx context.Context, src *v1alpha1.YipYapSource) {
	if r.adapterObserver == nil {
		return
	}
	existing := src.Status.GetCondition(v1alpha1.YipYapSourceConditionConnected)
	obs, err := r.adapterObserver.Observe(ctx, src)
	switch {
	case err != nil:
		logging.FromContext(ctx).Warnw("could not look up the adapter pod", zap.Error(err))
		if !isAPIKeyRejected(existing) {
			src.Status.MarkConnecting(reasonStatusUnknown, "could not look up the adapter pod")
		}
	case obs.Status != nil, !isAPIKeyRejected(existing):
		markConnection(src, obs)
	}
	if c := src.Status.GetCondition(v1alpha1.YipYapSourceConditionConnected); c != nil && !c.IsTrue() && r.enqueueAfter != nil {
		r.enqueueAfter(src, connectionRecheck)
	}
	elevateAuthFailure(src)
}

func markConnection(src *v1alpha1.YipYapSource, obs adapterObservation) {
	status, reason, msg := connectedCondition(obs)
	switch status {
	case corev1.ConditionTrue:
		src.Status.MarkConnected()
	case corev1.ConditionFalse:
		src.Status.MarkNotConnected(reason, "%s", msg)
	default:
		src.Status.MarkConnecting(reason, "%s", msg)
	}
}

func isAPIKeyRejected(c *apis.Condition) bool {
	return c != nil && c.IsFalse() && c.Reason == adapterstatus.ReasonAPIKeyRejected
}

func elevateAuthFailure(src *v1alpha1.YipYapSource) {
	c := src.Status.GetCondition(v1alpha1.YipYapSourceConditionConnected)
	if !isAPIKeyRejected(c) {
		return
	}
	src.GetConditionSet().Manage(&src.Status).SetCondition(apis.Condition{
		Type:    v1alpha1.YipYapSourceConditionReady,
		Status:  corev1.ConditionFalse,
		Reason:  c.Reason,
		Message: c.Message,
	})
}

// podObserver finds a source's adapter pod with the Kubernetes API and reads
// its status endpoint over the pod network.
type podObserver struct {
	kube   kubernetes.Interface
	client *http.Client
	// port is the adapter's health port. Swapped in tests.
	port int32
}

func newPodObserver(kube kubernetes.Interface) *podObserver {
	return &podObserver{
		kube: kube,
		client: &http.Client{
			Timeout: statusFetchTimeout,
			// The status endpoint never redirects. Do not follow one to
			// another address.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{
				Proxy:             nil,
				DisableKeepAlives: true,
				DialContext:       (&net.Dialer{Timeout: statusFetchTimeout}).DialContext,
			},
		},
		port: resources.AdapterHTTPPort,
	}
}

// Observe implements adapterObserver.
func (o *podObserver) Observe(ctx context.Context, src *v1alpha1.YipYapSource) (adapterObservation, error) {
	pods, err := o.kube.CoreV1().Pods(src.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(resources.Labels(src)).String(),
	})
	if err != nil {
		return adapterObservation{}, fmt.Errorf("list adapter pods: %w", err)
	}
	pod := pickAdapterPod(src, pods.Items)
	if pod == nil {
		return adapterObservation{}, nil
	}
	obs := adapterObservation{
		PodFound:   true,
		PodRunning: pod.Status.Phase == corev1.PodRunning,
		PodReady:   podIsReady(pod),
	}
	if obs.PodRunning && pod.Status.PodIP != "" {
		st, err := o.fetch(ctx, pod.Status.PodIP)
		if err != nil {
			logging.FromContext(ctx).Debugw("adapter status endpoint unreadable; using pod readiness",
				zap.String("pod", pod.Name), zap.Error(err))
		} else {
			obs.Status = st
		}
	}
	return obs, nil
}

func (o *podObserver) fetch(ctx context.Context, ip string) (*adapterstatus.Status, error) {
	if net.ParseIP(ip) == nil {
		return nil, fmt.Errorf("pod IP %q is not an IP address", ip)
	}
	ctx, cancel := context.WithTimeout(ctx, statusFetchTimeout)
	defer cancel()
	u := "http://" + net.JoinHostPort(ip, strconv.Itoa(int(o.port))) + adapterstatus.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status endpoint returned %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return nil, fmt.Errorf("status endpoint returned content type %q", ct)
	}
	var st adapterstatus.Status
	if err := json.NewDecoder(io.LimitReader(resp.Body, statusBodyLimit)).Decode(&st); err != nil {
		return nil, fmt.Errorf("decode adapter status: %w", err)
	}
	return &st, nil
}

// pickAdapterPod returns the newest live adapter pod for src. A pod only
// counts when its controller is a ReplicaSet of the source's Deployment and
// it is not on the host network, so a pod someone else labels to match is
// not queried.
func pickAdapterPod(src *v1alpha1.YipYapSource, pods []corev1.Pod) *corev1.Pod {
	prefix := resources.DeploymentName(src) + "-"
	var best *corev1.Pod
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp != nil || p.Spec.HostNetwork {
			continue
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		owner := metav1.GetControllerOf(p)
		if owner == nil || owner.Kind != "ReplicaSet" || !strings.HasPrefix(owner.Name, prefix) {
			continue
		}
		if best == nil || best.CreationTimestamp.Before(&p.CreationTimestamp) {
			best = p
		}
	}
	return best
}

func podIsReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
