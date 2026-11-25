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
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakekube "k8s.io/client-go/kubernetes/fake"
	clientgotesting "k8s.io/client-go/testing"

	"github.com/YipYap-run/knative-source/pkg/adapterstatus"
	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
	"github.com/YipYap-run/knative-source/pkg/reconciler/yipyapsource/resources"
	rttesting "github.com/YipYap-run/knative-source/pkg/reconciler/yipyapsource/testing"
)

func TestConnectedCondition(t *testing.T) {
	notReady := func(reason, msg string) *adapterstatus.Status {
		return &adapterstatus.Status{Reason: reason, Message: msg}
	}
	tests := []struct {
		name       string
		obs        adapterObservation
		wantStatus corev1.ConditionStatus
		wantReason string
		wantMsg    string
	}{{
		name:       "no pod yet",
		obs:        adapterObservation{},
		wantStatus: corev1.ConditionUnknown,
		wantReason: reasonAdapterStarting,
		wantMsg:    msgAdapterStarting,
	}, {
		name:       "adapter ready",
		obs:        adapterReady,
		wantStatus: corev1.ConditionTrue,
	}, {
		name:       "API key rejected",
		obs:        adapterObservation{PodFound: true, PodRunning: true, Status: notReady(adapterstatus.ReasonAPIKeyRejected, "API key rejected (401)")},
		wantStatus: corev1.ConditionFalse,
		wantReason: "APIKeyRejected",
		wantMsg:    "API key rejected (401)",
	}, {
		name:       "API key forbidden",
		obs:        adapterObservation{PodFound: true, PodRunning: true, Status: notReady(adapterstatus.ReasonAPIForbidden, "API key forbidden (403)")},
		wantStatus: corev1.ConditionFalse,
		wantReason: "APIForbidden",
		wantMsg:    "API key forbidden (403)",
	}, {
		name:       "API unreachable",
		obs:        adapterObservation{PodFound: true, PodRunning: true, Status: notReady(adapterstatus.ReasonAPIUnreachable, "cannot reach the YipYap API")},
		wantStatus: corev1.ConditionFalse,
		wantReason: "APIUnreachable",
		wantMsg:    "cannot reach the YipYap API",
	}, {
		name:       "sink unavailable",
		obs:        adapterObservation{PodFound: true, PodRunning: true, Status: notReady(adapterstatus.ReasonSinkUnavailable, "sink is not accepting events (503)")},
		wantStatus: corev1.ConditionFalse,
		wantReason: "SinkUnavailable",
		wantMsg:    "sink is not accepting events (503)",
	}, {
		name:       "unknown reason and dirty message are cleaned",
		obs:        adapterObservation{PodFound: true, PodRunning: true, Status: notReady("Bogus Reason", "line one\nline two\x07")},
		wantStatus: corev1.ConditionFalse,
		wantReason: reasonAdapterNotReady,
		wantMsg:    "line one line two",
	}, {
		name:       "long message is cut",
		obs:        adapterObservation{PodFound: true, PodRunning: true, Status: notReady(adapterstatus.ReasonAPIUnreachable, strings.Repeat("x", 1000))},
		wantStatus: corev1.ConditionFalse,
		wantReason: "APIUnreachable",
		wantMsg:    strings.Repeat("x", adapterstatus.MaxMessageLen),
	}, {
		name:       "not ready without a message",
		obs:        adapterObservation{PodFound: true, PodRunning: true, Status: notReady(adapterstatus.ReasonAPIUnreachable, "")},
		wantStatus: corev1.ConditionFalse,
		wantReason: "APIUnreachable",
		wantMsg:    "the adapter reports it is not ready",
	}, {
		name:       "status unreadable, pod pending",
		obs:        adapterObservation{PodFound: true},
		wantStatus: corev1.ConditionUnknown,
		wantReason: reasonAdapterStarting,
		wantMsg:    msgAdapterStarting,
	}, {
		name:       "status unreadable, pod ready",
		obs:        adapterObservation{PodFound: true, PodRunning: true, PodReady: true},
		wantStatus: corev1.ConditionTrue,
	}, {
		name:       "status unreadable, pod not ready",
		obs:        adapterObservation{PodFound: true, PodRunning: true},
		wantStatus: corev1.ConditionFalse,
		wantReason: reasonAdapterNotReady,
		wantMsg:    msgAdapterNotReady,
	}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, reason, msg := connectedCondition(tc.obs)
			if status != tc.wantStatus || reason != tc.wantReason || msg != tc.wantMsg {
				t.Fatalf("connectedCondition() = (%s, %q, %q), want (%s, %q, %q)",
					status, reason, msg, tc.wantStatus, tc.wantReason, tc.wantMsg)
			}
		})
	}
}

type fakeObserver struct {
	obs adapterObservation
	err error
}

func (f fakeObserver) Observe(context.Context, *v1alpha1.YipYapSource) (adapterObservation, error) {
	return f.obs, f.err
}

func TestPropagateConnection_RechecksUntilConnected(t *testing.T) {
	tests := []struct {
		name        string
		observer    fakeObserver
		wantStatus  corev1.ConditionStatus
		wantReason  string
		wantRecheck bool
	}{
		{"connected", fakeObserver{obs: adapterReady}, corev1.ConditionTrue, "", false},
		{"key rejected", fakeObserver{obs: adapterObservation{PodFound: true, PodRunning: true, Status: &adapterstatus.Status{Reason: adapterstatus.ReasonAPIKeyRejected, Message: "API key rejected (401)"}}}, corev1.ConditionFalse, adapterstatus.ReasonAPIKeyRejected, true},
		{"starting", fakeObserver{}, corev1.ConditionUnknown, reasonAdapterStarting, true},
		{"lookup failed", fakeObserver{err: errors.New("forbidden")}, corev1.ConditionUnknown, reasonStatusUnknown, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var rechecks []time.Duration
			r := &Reconciler{
				adapterObserver: tc.observer,
				enqueueAfter:    func(_ interface{}, d time.Duration) { rechecks = append(rechecks, d) },
			}
			src := rttesting.NewYipYapSource(sourceName, sourceNS, rttesting.WithInitYipYapSourceConditions)
			r.propagateConnection(context.Background(), src)
			c := src.Status.GetCondition(v1alpha1.YipYapSourceConditionConnected)
			if c.Status != tc.wantStatus || c.Reason != tc.wantReason {
				t.Fatalf("Connected = %s/%q, want %s/%q", c.Status, c.Reason, tc.wantStatus, tc.wantReason)
			}
			if got := len(rechecks) == 1 && rechecks[0] == connectionRecheck; got != tc.wantRecheck {
				t.Fatalf("rechecks = %v, want recheck %v", rechecks, tc.wantRecheck)
			}
		})
	}
}

// adapterPod builds a pod the way the ReplicaSet of src's Deployment would.
func adapterPod(src *v1alpha1.YipYapSource, name, ip string, created time.Time, ready bool) *corev1.Pod {
	isController := true
	readyStatus := corev1.ConditionFalse
	if ready {
		readyStatus = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         src.Namespace,
			Labels:            resources.Labels(src),
			CreationTimestamp: metav1.NewTime(created),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "ReplicaSet",
				Name:       resources.DeploymentName(src) + "-5d4f8c",
				UID:        "rs-uid",
				Controller: &isController,
			}},
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			PodIP:      ip,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: readyStatus}},
		},
	}
}

func testSource() *v1alpha1.YipYapSource {
	return rttesting.NewYipYapSource(sourceName, sourceNS, rttesting.WithYipYapSourceUID(sourceUID))
}

// statusServer serves st on the adapter status path and returns the port.
func statusServer(t *testing.T, st adapterstatus.Status) int32 {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != adapterstatus.Path {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	}))
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(port)
	return int32(p)
}

func TestPodObserver_ReadsAdapterStatus(t *testing.T) {
	src := testSource()
	port := statusServer(t, adapterstatus.Status{Reason: adapterstatus.ReasonAPIKeyRejected, Message: "API key rejected (401)"})
	kube := fakekube.NewClientset(adapterPod(src, "adapter-1", "127.0.0.1", time.Now(), false))
	o := newPodObserver(kube)
	o.port = port

	obs, err := o.Observe(context.Background(), src)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.PodFound || !obs.PodRunning || obs.PodReady {
		t.Fatalf("pod flags = %+v", obs)
	}
	if obs.Status == nil || obs.Status.Reason != adapterstatus.ReasonAPIKeyRejected || obs.Status.Message != "API key rejected (401)" {
		t.Fatalf("Status = %+v", obs.Status)
	}

	// The pod list is scoped to the source's namespace and labels.
	var listed bool
	for _, a := range kube.Actions() {
		if la, ok := a.(clientgotesting.ListAction); ok && a.GetResource().Resource == "pods" {
			listed = true
			if la.GetNamespace() != sourceNS {
				t.Errorf("listed pods in %q, want %q", la.GetNamespace(), sourceNS)
			}
			if sel := la.GetListRestrictions().Labels.String(); sel != resources.SourceLabelKey+"="+resources.LabelValue(sourceName) {
				t.Errorf("label selector %q", sel)
			}
		}
	}
	if !listed {
		t.Fatal("no pod list action")
	}
}

func TestPodObserver_UnreachableFallsBackToReadiness(t *testing.T) {
	src := testSource()
	// Nothing listens on this port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	_ = l.Close()
	p, _ := strconv.Atoi(port)

	o := newPodObserver(fakekube.NewClientset(adapterPod(src, "adapter-1", "127.0.0.1", time.Now(), true)))
	o.port = int32(p)
	obs, err := o.Observe(context.Background(), src)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Status != nil || !obs.PodReady {
		t.Fatalf("obs = %+v, want no status and pod ready", obs)
	}
	if status, _, _ := connectedCondition(obs); status != corev1.ConditionTrue {
		t.Fatalf("Connected = %s, want True from pod readiness", status)
	}
}

func TestPodObserver_RejectsNonJSONAndRedirects(t *testing.T) {
	src := testSource()
	for name, h := range map[string]http.HandlerFunc{
		"text body": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ready":true}`))
		},
		"redirect": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
		},
		"server error": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"ready":true}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
			p, _ := strconv.Atoi(port)
			o := newPodObserver(fakekube.NewClientset(adapterPod(src, "adapter-1", "127.0.0.1", time.Now(), false)))
			o.port = int32(p)
			obs, err := o.Observe(context.Background(), src)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if obs.Status != nil {
				t.Fatalf("Status = %+v, want nil", obs.Status)
			}
		})
	}
}

func TestPodObserver_NoPod(t *testing.T) {
	o := newPodObserver(fakekube.NewClientset())
	obs, err := o.Observe(context.Background(), testSource())
	if err != nil || obs.PodFound {
		t.Fatalf("Observe = %+v, %v; want no pod", obs, err)
	}
}

func TestPodObserver_ListError(t *testing.T) {
	kube := fakekube.NewClientset()
	kube.PrependReactor("list", "pods", func(clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("pods is forbidden")
	})
	if _, err := newPodObserver(kube).Observe(context.Background(), testSource()); err == nil {
		t.Fatal("Observe returned no error for a failed list")
	}
}

func TestPickAdapterPod(t *testing.T) {
	src := testSource()
	now := time.Now()
	older := adapterPod(src, "older", "10.0.0.1", now.Add(-time.Hour), true)
	newer := adapterPod(src, "newer", "10.0.0.2", now, true)

	deleting := adapterPod(src, "deleting", "10.0.0.3", now.Add(time.Minute), true)
	ts := metav1.NewTime(now)
	deleting.DeletionTimestamp = &ts

	hostNet := adapterPod(src, "hostnet", "192.168.0.10", now.Add(time.Minute), true)
	hostNet.Spec.HostNetwork = true

	orphan := adapterPod(src, "orphan", "10.0.0.4", now.Add(time.Minute), true)
	orphan.OwnerReferences = nil

	foreignRS := adapterPod(src, "foreign", "10.0.0.5", now.Add(time.Minute), true)
	foreignRS.OwnerReferences[0].Name = "someone-else-abc"

	failed := adapterPod(src, "failed", "10.0.0.6", now.Add(time.Minute), false)
	failed.Status.Phase = corev1.PodFailed

	got := pickAdapterPod(src, []corev1.Pod{*older, *deleting, *hostNet, *orphan, *foreignRS, *failed, *newer})
	if got == nil || got.Name != "newer" {
		t.Fatalf("pickAdapterPod = %v, want newer", got)
	}
	if got := pickAdapterPod(src, []corev1.Pod{*hostNet, *orphan, *foreignRS}); got != nil {
		t.Fatalf("pickAdapterPod = %s, want nil", got.Name)
	}
}

func TestPodObserver_RejectsNonIPAddress(t *testing.T) {
	o := newPodObserver(fakekube.NewClientset())
	if _, err := o.fetch(context.Background(), "evil.example.com"); err == nil {
		t.Fatal("fetch accepted a host name as a pod IP")
	}
}
