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
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgotesting "k8s.io/client-go/testing"

	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"
	_ "knative.dev/pkg/client/injection/ducks/duck/v1/addressable/fake"
	fakekubeclient "knative.dev/pkg/client/injection/kube/client/fake"
	_ "knative.dev/pkg/client/injection/kube/informers/apps/v1/deployment/fake"
	"knative.dev/pkg/configmap"
	"knative.dev/pkg/controller"
	_ "knative.dev/pkg/injection/clients/dynamicclient/fake"
	logtesting "knative.dev/pkg/logging/testing"
	"knative.dev/pkg/resolver"
	"knative.dev/pkg/tracker"

	"github.com/YipYap-run/knative-source/pkg/adapterstatus"
	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
	"github.com/YipYap-run/knative-source/pkg/baseurl"
	fakeyipyapclient "github.com/YipYap-run/knative-source/pkg/client/injection/client/fake"
	_ "github.com/YipYap-run/knative-source/pkg/client/injection/informers/sources/v1alpha1/yipyapsource/fake"
	reconcileryipyapsource "github.com/YipYap-run/knative-source/pkg/client/injection/reconciler/sources/v1alpha1/yipyapsource"

	"github.com/YipYap-run/knative-source/pkg/reconciler/yipyapsource/resources"
	rttesting "github.com/YipYap-run/knative-source/pkg/reconciler/yipyapsource/testing"

	. "knative.dev/pkg/reconciler/testing" //nolint:staticcheck // match upstream idiom
)

const (
	sourceName  = "ys-unit"
	sourceNS    = "ys-unit-ns"
	sourceUID   = "ys-uid-0001"
	adapterImg  = "registry.test/adapter:unit"
	apiKeySec   = "yipyap-api-key"
	apiKeyField = "api-key"
)

var (
	sinkURL = func() *apis.URL {
		u, _ := apis.ParseURL("http://broker.default.svc/broker")
		return u
	}()

	// sinkDest is a URI-only destination so that the resolver does not need
	// the addressable-duck informer to be set up.
	sinkDest = duckv1.Destination{URI: sinkURL}
)

func baseSpec() v1alpha1.YipYapSourceSpec {
	return v1alpha1.YipYapSourceSpec{
		SourceSpec: duckv1.SourceSpec{Sink: sinkDest},
		APIKeyRef: v1alpha1.APIKeyReference{
			Name: apiKeySec,
			Key:  apiKeyField,
		},
		BaseURL: "https://console.yipyap.run",
		Mode:    v1alpha1.YipYapSourceModeAuto,
	}
}

func specWith(mutate func(*v1alpha1.YipYapSourceSpec)) v1alpha1.YipYapSourceSpec {
	s := baseSpec()
	mutate(&s)
	return s
}

func newSource(opts ...rttesting.YipYapSourceOption) *v1alpha1.YipYapSource {
	allOpts := append([]rttesting.YipYapSourceOption{
		rttesting.WithYipYapSourceUID(sourceUID),
		rttesting.WithYipYapSourceSpec(baseSpec()),
	}, opts...)
	return rttesting.NewYipYapSource(sourceName, sourceNS, allOpts...)
}

func expectedDeploymentForSpec(t *testing.T, spec v1alpha1.YipYapSourceSpec) *appsv1.Deployment {
	t.Helper()
	src := rttesting.NewYipYapSource(sourceName, sourceNS,
		rttesting.WithYipYapSourceUID(sourceUID),
		rttesting.WithYipYapSourceSpec(spec),
	)
	return resources.MakeAdapterDeployment(src, sinkURL, adapterImg)
}

func expectedDeployment(t *testing.T) *appsv1.Deployment {
	return expectedDeploymentForSpec(t, baseSpec())
}

func withDeploymentStatus(dep *appsv1.Deployment, status corev1.ConditionStatus, reason, msg string) *appsv1.Deployment {
	out := dep.DeepCopy()
	out.Status.Conditions = append(out.Status.Conditions, appsv1.DeploymentCondition{
		Type:    appsv1.DeploymentAvailable,
		Status:  status,
		Reason:  reason,
		Message: msg,
	})
	return out
}

func driftedDeployment(t *testing.T) *appsv1.Deployment {
	t.Helper()
	out := expectedDeployment(t).DeepCopy()
	out.Spec.Template.Spec.Containers[0].Image = "registry.test/adapter:STALE"
	return out
}

// restartedLiveDeployment is the informer's copy of the adapter Deployment
// rendered for spec: API server defaults filled in and a
// kubectl rollout restart annotation on the pod template.
func restartedLiveDeployment(t *testing.T, spec v1alpha1.YipYapSourceSpec) *appsv1.Deployment {
	t.Helper()
	return rttesting.WithRestartedAt(
		rttesting.WithServerDefaults(expectedDeploymentForSpec(t, spec)),
		"2026-11-24T10:00:00Z",
	)
}

// rollingUpdateDeployment models an adapter Deployment created by an older
// controller with the default RollingUpdate strategy, under which the old and
// new adapter pods overlap during a rollout.
func rollingUpdateDeployment(t *testing.T) *appsv1.Deployment {
	t.Helper()
	out := expectedDeployment(t).DeepCopy()
	maxUnavailable := intstr.FromString("25%")
	out.Spec.Strategy = appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxUnavailable: &maxUnavailable,
			MaxSurge:       &maxUnavailable,
		},
	}
	return out
}

// staleSelectorDeployment models an adapter Deployment created by an older
// controller whose selector no longer matches what MakeAdapterDeployment
// renders. spec.selector is immutable, so only delete and recreate can fix it.
func staleSelectorDeployment(t *testing.T) *appsv1.Deployment {
	t.Helper()
	out := expectedDeployment(t).DeepCopy()
	out.UID = "stale-dep-uid"
	out.Spec.Selector = &metav1.LabelSelector{
		MatchLabels: map[string]string{resources.SourceLabelKey: "stale"},
	}
	return out
}

// foreignDeployment has the adapter's name and a stale selector but no
// controller owner reference, so the reconciler must neither update nor
// delete it.
func foreignDeployment(t *testing.T) *appsv1.Deployment {
	t.Helper()
	out := staleSelectorDeployment(t)
	out.OwnerReferences = nil
	return out
}

// terminatingDeployment is an owned adapter Deployment with an outdated
// selector whose deletion has started but not finished (a finalizer holds it).
func terminatingDeployment(t *testing.T) *appsv1.Deployment {
	t.Helper()
	out := staleSelectorDeployment(t)
	now := metav1.Now()
	out.DeletionTimestamp = &now
	out.Finalizers = []string{"foregroundDeletion"}
	return out
}

func deploymentDelete(name string) clientgotesting.DeleteActionImpl {
	return clientgotesting.DeleteActionImpl{
		ActionImpl: clientgotesting.ActionImpl{
			Namespace: sourceNS,
			Verb:      "delete",
			Resource:  appsv1.SchemeGroupVersion.WithResource("deployments"),
		},
		Name: name,
	}
}

// observationKey carries the adapter observation a table row wants the
// reconciler to see.
type observationKey struct{}

func withObservation(obs adapterObservation) context.Context {
	return context.WithValue(context.Background(), observationKey{}, obs)
}

type observationErrorKey struct{}

func withObservationError(err error) context.Context {
	return context.WithValue(context.Background(), observationErrorKey{}, err)
}

// ctxObserver returns the row's observation, or "no adapter pod yet".
type ctxObserver struct{}

func (ctxObserver) Observe(ctx context.Context, _ *v1alpha1.YipYapSource) (adapterObservation, error) {
	if err, _ := ctx.Value(observationErrorKey{}).(error); err != nil {
		return adapterObservation{}, err
	}
	obs, _ := ctx.Value(observationKey{}).(adapterObservation)
	return obs, nil
}

func withStaleConnected(src *v1alpha1.YipYapSource) *v1alpha1.YipYapSource {
	for i := range src.Status.Conditions {
		if src.Status.Conditions[i].Type == v1alpha1.YipYapSourceConditionConnected {
			src.Status.Conditions[i].LastTransitionTime = apis.VolatileTime{
				Inner: metav1.NewTime(time.Now().Add(-time.Hour)),
			}
		}
	}
	return src
}

var adapterReady = adapterObservation{PodFound: true, PodRunning: true, PodReady: true, Status: &adapterstatus.Status{Ready: true}}

func TestAllCases(t *testing.T) {
	knownAttrs := knownCloudEventAttributes
	saSpec := specWith(func(s *v1alpha1.YipYapSourceSpec) {
		s.ServiceAccountName = "sa-pinned"
	})
	ceSpec := specWith(func(s *v1alpha1.YipYapSourceSpec) {
		s.CloudEventOverrides = &duckv1.CloudEventOverrides{
			Extensions: map[string]string{"env": "prod"},
		}
	})

	pollSpec := specWith(func(s *v1alpha1.YipYapSourceSpec) {
		s.Mode = v1alpha1.YipYapSourceModePoll
	})

	httpSpec := specWith(func(s *v1alpha1.YipYapSourceSpec) {
		s.BaseURL = "http://console.yipyap.run"
	})
	exfilSpec := specWith(func(s *v1alpha1.YipYapSourceSpec) {
		s.BaseURL = "https://exfil.example.net"
	})
	corpSpec := specWith(func(s *v1alpha1.YipYapSourceSpec) {
		s.BaseURL = "https://yipyap.corp.example"
	})
	corpPolicy, err := baseurl.Parse("yipyap.corp.example")
	if err != nil {
		t.Fatal(err)
	}
	corpCtx := baseurl.WithPolicy(context.Background(), corpPolicy)
	const stoppedMsg = "the adapter does not run while spec.baseURL is not allowed"
	exfilMsg := `spec.baseURL host "exfil.example.net" is not allowed by the operator; allowed hosts: console.yipyap.run`

	rejectedObjects := func() []runtime.Object {
		return []runtime.Object{
			withStaleConnected(newSource(
				rttesting.WithInitYipYapSourceConditions,
				rttesting.WithYipYapSourceDeployed,
				rttesting.WithYipYapSourceNotConnected(adapterstatus.ReasonAPIKeyRejected, "API key rejected (401)"),
			)),
			withDeploymentStatus(expectedDeployment(t), corev1.ConditionFalse, "MinimumReplicasUnavailable", "Deployment does not have minimum availability."),
		}
	}
	rejectedUpdate := func() clientgotesting.UpdateActionImpl {
		return clientgotesting.UpdateActionImpl{Object: newSource(
			rttesting.WithInitYipYapSourceConditions,
			rttesting.WithYipYapSourceBaseURLAllowed,
			rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
			rttesting.WithYipYapSourceSink(sinkURL),
			rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
			rttesting.WithYipYapSourceNotDeployed("MinimumReplicasUnavailable", "Deployment does not have minimum availability."),
			rttesting.WithYipYapSourceNotConnected(adapterstatus.ReasonAPIKeyRejected, "API key rejected (401)"),
		)}
	}

	table := TableTest{
		{
			Name: "existing source with http baseURL: adapter deleted, not ready",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(rttesting.WithYipYapSourceSpec(httpSpec)),
				withDeploymentStatus(expectedDeploymentForSpec(t, httpSpec), corev1.ConditionTrue, "MinimumReplicasAvailable", ""),
			},
			WantDeletes: []clientgotesting.DeleteActionImpl{
				deploymentDelete(expectedDeploymentForSpec(t, httpSpec).Name),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(httpSpec),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLNotAllowed("BaseURLNotAllowed", "spec.baseURL must use https"),
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceNotDeployed("BaseURLNotAllowed", stoppedMsg),
					rttesting.WithYipYapSourceConnecting("BaseURLNotAllowed", stoppedMsg),
				),
			}},
		},
		{
			Name: "unlisted baseURL host: no adapter created",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(rttesting.WithYipYapSourceSpec(exfilSpec)),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(exfilSpec),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLNotAllowed("BaseURLNotAllowed", exfilMsg),
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceNotDeployed("BaseURLNotAllowed", stoppedMsg),
					rttesting.WithYipYapSourceConnecting("BaseURLNotAllowed", stoppedMsg),
				),
			}},
		},
		{
			Name: "unlisted baseURL host: Deployment not owned by the source is left alone",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(rttesting.WithYipYapSourceSpec(exfilSpec)),
				foreignDeployment(t),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(exfilSpec),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLNotAllowed("BaseURLNotAllowed", exfilMsg),
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceNotDeployed("BaseURLNotAllowed", stoppedMsg),
					rttesting.WithYipYapSourceConnecting("BaseURLNotAllowed", stoppedMsg),
				),
			}},
		},
		{
			Name: "operator allowlist: listed host deploys",
			Key:  sourceNS + "/" + sourceName,
			Ctx:  corpCtx,
			Objects: []runtime.Object{
				newSource(rttesting.WithYipYapSourceSpec(corpSpec)),
			},
			WantCreates: []runtime.Object{
				expectedDeploymentForSpec(t, corpSpec),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(corpSpec),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeploymentForSpec(t, corpSpec).Name),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
		{
			Name:    "operator allowlist without the default host rejects it",
			Key:     sourceNS + "/" + sourceName,
			Ctx:     corpCtx,
			Objects: []runtime.Object{newSource()},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLNotAllowed("BaseURLNotAllowed",
						`spec.baseURL host "console.yipyap.run" is not allowed by the operator; allowed hosts: yipyap.corp.example`),
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceNotDeployed("BaseURLNotAllowed", stoppedMsg),
					rttesting.WithYipYapSourceConnecting("BaseURLNotAllowed", stoppedMsg),
				),
			}},
		},
		{
			Name: "missing sink errors",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(rttesting.WithYipYapSourceSpec(v1alpha1.YipYapSourceSpec{
					APIKeyRef: v1alpha1.APIKeyReference{Name: apiKeySec, Key: apiKeyField},
				})),
			},
			WantErr: true,
			WantEvents: []string{
				"Warning InternalError resolve sink: destination missing Ref and URI, expected at least one",
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(v1alpha1.YipYapSourceSpec{
						APIKeyRef: v1alpha1.APIKeyReference{Name: apiKeySec, Key: apiKeyField},
					}),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSinkNotFound("NotFound",
						"destination missing Ref and URI, expected at least one"),
				),
			}},
		},
		{
			Name:    "sink resolves, deployment created",
			Key:     sourceNS + "/" + sourceName,
			Objects: []runtime.Object{newSource()},
			WantCreates: []runtime.Object{
				expectedDeployment(t),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
		{
			Name: "sink resolves, deployment exists + available",
			Key:  sourceNS + "/" + sourceName,
			Ctx:  withObservation(adapterReady),
			Objects: []runtime.Object{
				newSource(),
				withDeploymentStatus(expectedDeployment(t), corev1.ConditionTrue, "MinimumReplicasAvailable", ""),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceDeployed,
					rttesting.WithYipYapSourceConnected,
				),
			}},
		},
		{
			Name: "sink resolves, deployment exists + unavailable",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(),
				withDeploymentStatus(expectedDeployment(t), corev1.ConditionFalse, "MinimumReplicasUnavailable", "scheduling failed"),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceNotDeployed("MinimumReplicasUnavailable", "scheduling failed"),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
		{
			Name: "sink resolves, deployment exists + progressing",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(),
				withDeploymentStatus(expectedDeployment(t), corev1.ConditionUnknown, "DeploymentProgressing", "rolling"),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceDeploying("DeploymentProgressing", "rolling"),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
		{
			// The adapter answers 401 from the API, so its readiness probe
			// fails and the Deployment goes unavailable. Ready shows the
			// adapter's reason, not the Deployment's.
			Name: "adapter reports a rejected API key",
			Key:  sourceNS + "/" + sourceName,
			Ctx: withObservation(adapterObservation{PodFound: true, PodRunning: true, Status: &adapterstatus.Status{
				Reason: adapterstatus.ReasonAPIKeyRejected, Message: "API key rejected (401)",
			}}),
			Objects: []runtime.Object{
				newSource(),
				withDeploymentStatus(expectedDeployment(t), corev1.ConditionFalse, "MinimumReplicasUnavailable", "Deployment does not have minimum availability."),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceNotDeployed("MinimumReplicasUnavailable", "Deployment does not have minimum availability."),
					rttesting.WithYipYapSourceNotConnected(adapterstatus.ReasonAPIKeyRejected, "API key rejected (401)"),
				),
			}},
		},
		{
			Name:    "rejected API key survives a Deployment outage",
			Key:     sourceNS + "/" + sourceName,
			Ctx:     withObservation(adapterObservation{PodFound: true}),
			Objects: rejectedObjects(),
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{
				rejectedUpdate(),
			},
		},
		{
			Name:    "rejected API key survives an unreadable status",
			Key:     sourceNS + "/" + sourceName,
			Ctx:     withObservation(adapterObservation{PodFound: true, PodRunning: true}),
			Objects: rejectedObjects(),
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{
				rejectedUpdate(),
			},
		},
		{
			Name:    "rejected API key survives a pod list error",
			Key:     sourceNS + "/" + sourceName,
			Ctx:     withObservationError(errors.New("forbidden")),
			Objects: rejectedObjects(),
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{
				rejectedUpdate(),
			},
		},
		{
			Name:    "rejected API key survives a ready pod without a status",
			Key:     sourceNS + "/" + sourceName,
			Ctx:     withObservation(adapterObservation{PodFound: true, PodRunning: true, PodReady: true}),
			Objects: rejectedObjects(),
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{
				rejectedUpdate(),
			},
		},
		{
			Name: "adapter reports a forbidden API key",
			Key:  sourceNS + "/" + sourceName,
			Ctx: withObservation(adapterObservation{PodFound: true, PodRunning: true, Status: &adapterstatus.Status{
				Reason: adapterstatus.ReasonAPIForbidden, Message: "API key forbidden (403)",
			}}),
			Objects: []runtime.Object{
				newSource(),
				withDeploymentStatus(expectedDeployment(t), corev1.ConditionFalse, "MinimumReplicasUnavailable", "Deployment does not have minimum availability."),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceNotDeployed("MinimumReplicasUnavailable", "Deployment does not have minimum availability."),
					rttesting.WithYipYapSourceNotConnected(adapterstatus.ReasonAPIForbidden, "API key forbidden (403)"),
				),
			}},
		},
		{
			// The source was NotReady for a rejected key; the key is fixed,
			// the adapter answers ready and the Deployment is available.
			Name: "adapter recovers from a rejected API key",
			Key:  sourceNS + "/" + sourceName,
			Ctx:  withObservation(adapterReady),
			Objects: []runtime.Object{
				newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceNotConnected(adapterstatus.ReasonAPIKeyRejected, "API key rejected (401)"),
				),
				withDeploymentStatus(expectedDeployment(t), corev1.ConditionTrue, "MinimumReplicasAvailable", ""),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceDeployed,
					rttesting.WithYipYapSourceConnected,
				),
			}},
		},
		{
			// A NetworkPolicy in the source namespace blocks the status
			// endpoint: fall back to pod readiness with a generic message.
			Name: "adapter status unreadable and pod not ready",
			Key:  sourceNS + "/" + sourceName,
			Ctx:  withObservation(adapterObservation{PodFound: true, PodRunning: true}),
			Objects: []runtime.Object{
				newSource(),
				withDeploymentStatus(expectedDeployment(t), corev1.ConditionFalse, "MinimumReplicasUnavailable", "Deployment does not have minimum availability."),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceNotDeployed("MinimumReplicasUnavailable", "Deployment does not have minimum availability."),
					rttesting.WithYipYapSourceNotConnected(reasonAdapterNotReady, msgAdapterNotReady),
				),
			}},
		},
		{
			Name: "deployment drifted, update path",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(),
				driftedDeployment(t),
			},
			WantUpdates: []clientgotesting.UpdateActionImpl{{
				Object: expectedDeployment(t),
			}},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
		{
			// The informer copy has API server defaults and a rollout
			// restart annotation. Neither is drift, so no Update is sent and
			// the restart is not undone.
			Name: "server-defaulted deployment with rollout restart, no update",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(),
				restartedLiveDeployment(t, baseSpec()),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
		{
			Name: "env var changed on restarted deployment, update keeps restartedAt",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(rttesting.WithYipYapSourceSpec(pollSpec)),
				restartedLiveDeployment(t, baseSpec()),
			},
			WantUpdates: []clientgotesting.UpdateActionImpl{{
				Object: func() runtime.Object {
					want := restartedLiveDeployment(t, baseSpec())
					want.Spec.Template.Spec = expectedDeploymentForSpec(t, pollSpec).Spec.Template.Spec
					return want
				}(),
			}},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(pollSpec),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
		{
			Name: "rolling update strategy switched to recreate",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(),
				rollingUpdateDeployment(t),
			},
			WantUpdates: []clientgotesting.UpdateActionImpl{{
				Object: expectedDeployment(t),
			}},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
		{
			Name: "selector changed, deployment deleted and recreated",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(),
				staleSelectorDeployment(t),
			},
			WantDeletes: []clientgotesting.DeleteActionImpl{
				deploymentDelete(expectedDeployment(t).Name),
			},
			WantCreates: []runtime.Object{
				expectedDeployment(t),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
		{
			Name: "deployment not owned by source, left alone",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(),
				foreignDeployment(t),
			},
			WantErr: true,
			WantEvents: []string{
				"Warning InternalError Deployment \"" + expectedDeployment(t).Name + "\" exists and is not owned by YipYapSource \"" + sourceName + "\"",
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceNotDeployed("NotOwned",
						"Deployment \""+expectedDeployment(t).Name+"\" exists and is not owned by YipYapSource \""+sourceName+"\""),
				),
			}},
		},
		{
			Name: "outdated deployment still terminating, no update, delete or create",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(),
				terminatingDeployment(t),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
					rttesting.WithYipYapSourceDeploying("Terminating",
						"waiting for Deployment \""+expectedDeployment(t).Name+"\" to finish deleting"),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
				),
			}},
		},
		{
			Name: "selector changed, delete fails",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(),
				staleSelectorDeployment(t),
			},
			WithReactors: []clientgotesting.ReactionFunc{
				InduceFailure("delete", "deployments"),
			},
			WantErr: true,
			WantDeletes: []clientgotesting.DeleteActionImpl{
				deploymentDelete(expectedDeployment(t).Name),
			},
			WantEvents: []string{
				"Warning InternalError delete Deployment with outdated selector: inducing failure for delete deployments",
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceNotDeployed("RecreateFailed",
						"delete Deployment with outdated selector: inducing failure for delete deployments"),
				),
			}},
		},
		{
			Name: "uses spec.serviceAccountName",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(rttesting.WithYipYapSourceSpec(saSpec)),
			},
			WantCreates: []runtime.Object{
				expectedDeploymentForSpec(t, saSpec),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(saSpec),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeploymentForSpec(t, saSpec).Name),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
		{
			Name: "propagates ce overrides",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(rttesting.WithYipYapSourceSpec(ceSpec)),
			},
			WantCreates: []runtime.Object{
				expectedDeploymentForSpec(t, ceSpec),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(ceSpec),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeploymentForSpec(t, ceSpec).Name),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
	}

	logger := logtesting.TestLogger(t)
	table.Test(t, rttesting.MakeFactory(func(ctx context.Context, ls *rttesting.Listers, cmw configmap.Watcher) controller.Reconciler {
		r := &Reconciler{
			kubeClientSet:    fakekubeclient.Get(ctx),
			deploymentLister: ls.GetDeploymentLister(),
			adapterImage:     adapterImg,
			baseURLPolicy:    baseurl.FromContext(ctx),
			adapterObserver:  ctxObserver{},
			// URIResolver backed by a no-op tracker. Table tests supply
			// URI-only destinations, so Ref lookups are never traversed.
			sinkResolver: resolver.NewURIResolverFromTracker(ctx, tracker.New(func(types.NamespacedName) {}, 0)),
		}
		return reconcileryipyapsource.NewReconciler(ctx, logger,
			fakeyipyapclient.Get(ctx),
			ls.GetYipYapSourceLister(),
			controller.GetEventRecorder(ctx),
			r)
	}))
}
