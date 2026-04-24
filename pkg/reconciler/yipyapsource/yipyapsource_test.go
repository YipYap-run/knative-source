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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
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

	table := TableTest{
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
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
				),
			}},
		},
		{
			Name: "sink resolves, deployment exists + available",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(),
				withDeploymentStatus(expectedDeployment(t), corev1.ConditionTrue, "MinimumReplicasAvailable", ""),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceDeployed,
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
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceNotDeployed("MinimumReplicasUnavailable", "scheduling failed"),
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
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
					rttesting.WithYipYapSourceDeploying("DeploymentProgressing", "rolling"),
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
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeployment(t).Name),
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
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeploymentForSpec(t, saSpec).Name),
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
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(expectedDeploymentForSpec(t, ceSpec).Name),
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
