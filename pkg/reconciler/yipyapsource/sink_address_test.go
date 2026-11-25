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
	"encoding/pem"
	"net/http/httptest"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgotesting "k8s.io/client-go/testing"

	"knative.dev/pkg/apis"
	duckv1 "knative.dev/pkg/apis/duck/v1"
	fakekubeclient "knative.dev/pkg/client/injection/kube/client/fake"
	"knative.dev/pkg/configmap"
	"knative.dev/pkg/controller"
	logtesting "knative.dev/pkg/logging/testing"
	"knative.dev/pkg/resolver"
	"knative.dev/pkg/tracker"

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
	"github.com/YipYap-run/knative-source/pkg/baseurl"
	fakeyipyapclient "github.com/YipYap-run/knative-source/pkg/client/injection/client/fake"
	reconcileryipyapsource "github.com/YipYap-run/knative-source/pkg/client/injection/reconciler/sources/v1alpha1/yipyapsource"
	"github.com/YipYap-run/knative-source/pkg/reconciler/yipyapsource/resources"
	rttesting "github.com/YipYap-run/knative-source/pkg/reconciler/yipyapsource/testing"

	. "knative.dev/pkg/reconciler/testing" //nolint:staticcheck // match upstream idiom
)

func testCAPEM(t *testing.T) string {
	t.Helper()
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
}

// TestSinkAddress covers sink CA certificates and OIDC audiences: the
// reconciler records them in status, passes them to the adapter, and
// creates the per-source OIDC ServiceAccount.
func TestSinkAddress(t *testing.T) {
	knownAttrs := knownCloudEventAttributes
	ca := testCAPEM(t)
	aud := "https://broker.example/ys-unit-ns/default"
	badCA := "not a certificate"

	httpsSink, err := apis.ParseURL("https://broker.default.svc/broker")
	if err != nil {
		t.Fatal(err)
	}
	specFor := func(caCerts, audience *string, sa string) v1alpha1.YipYapSourceSpec {
		return specWith(func(s *v1alpha1.YipYapSourceSpec) {
			s.Sink = duckv1.Destination{URI: httpsSink, CACerts: caCerts, Audience: audience}
			s.ServiceAccountName = sa
		})
	}
	addrFor := func(caCerts, audience *string) *duckv1.Addressable {
		return &duckv1.Addressable{URL: httpsSink, CACerts: caCerts, Audience: audience}
	}
	deploymentFor := func(spec v1alpha1.YipYapSourceSpec, addr *duckv1.Addressable) runtime.Object {
		src := newSource(rttesting.WithYipYapSourceSpec(spec))
		return resources.MakeAdapterDeploymentForSink(src, addr, adapterImg)
	}
	oidcSA := resources.MakeOIDCServiceAccount(newSource())
	foreignSA := oidcSA.DeepCopy()
	foreignSA.OwnerReferences = nil

	pinnedSA := &corev1.ServiceAccount{
		ObjectMeta:       metav1.ObjectMeta{Namespace: sourceNS, Name: "sa-pinned"},
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "regcred"}},
	}
	withOIDCIdentityAndPullSecrets := func(obj runtime.Object, names ...string) runtime.Object {
		d := obj.(*appsv1.Deployment)
		// Spelled out rather than taken from the renderer, so the row
		// fails if the pinned account ever gets the audience token.
		d.Spec.Template.Spec.ServiceAccountName = oidcSA.Name
		for _, n := range names {
			d.Spec.Template.Spec.ImagePullSecrets = append(d.Spec.Template.Spec.ImagePullSecrets, corev1.LocalObjectReference{Name: n})
		}
		return d
	}

	caSpec := specFor(&ca, nil, "")
	oidcSpec := specFor(nil, &aud, "")
	pinnedSpec := specFor(nil, &aud, "sa-pinned")
	badCASpec := specFor(&badCA, nil, "")
	depName := resources.DeploymentName(newSource())

	table := TableTest{
		{
			Name:    "sink CA certificates reach status and the adapter",
			Key:     sourceNS + "/" + sourceName,
			Objects: []runtime.Object{newSource(rttesting.WithYipYapSourceSpec(caSpec))},
			WantCreates: []runtime.Object{
				deploymentFor(caSpec, addrFor(&ca, nil)),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(caSpec),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSinkAddress(addrFor(&ca, nil)),
					rttesting.WithYipYapSourceDeploymentName(depName),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
		{
			Name:    "unparsable sink CA certificates: no adapter, plain reason",
			Key:     sourceNS + "/" + sourceName,
			Objects: []runtime.Object{newSource(rttesting.WithYipYapSourceSpec(badCASpec))},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(badCASpec),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSinkNotFound("InvalidCACerts",
						"sink CA certificates: CA bundle has data that is not a PEM block"),
				),
			}},
		},
		{
			Name:    "OIDC sink: generated ServiceAccount, projected token, status.auth",
			Key:     sourceNS + "/" + sourceName,
			Objects: []runtime.Object{newSource(rttesting.WithYipYapSourceSpec(oidcSpec))},
			WantCreates: []runtime.Object{
				resources.MakeOIDCServiceAccount(newSource(rttesting.WithYipYapSourceSpec(oidcSpec))),
				deploymentFor(oidcSpec, addrFor(nil, &aud)),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(oidcSpec),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSinkAddress(addrFor(nil, &aud)),
					rttesting.WithYipYapSourceOIDCIdentity(oidcSA.Name),
					rttesting.WithYipYapSourceDeploymentName(depName),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
		{
			Name: "OIDC sink: existing owned ServiceAccount is reused",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(rttesting.WithYipYapSourceSpec(oidcSpec)),
				oidcSA,
			},
			WantCreates: []runtime.Object{
				deploymentFor(oidcSpec, addrFor(nil, &aud)),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(oidcSpec),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSinkAddress(addrFor(nil, &aud)),
					rttesting.WithYipYapSourceOIDCIdentity(oidcSA.Name),
					rttesting.WithYipYapSourceDeploymentName(depName),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
		{
			Name: "OIDC sink: ServiceAccount of that name not owned by the source",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(rttesting.WithYipYapSourceSpec(oidcSpec)),
				foreignSA,
			},
			WantErr: true,
			WantEvents: []string{
				`Warning InternalError ServiceAccount "` + oidcSA.Name + `" exists and is not owned by YipYapSource "` + sourceName + `"`,
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(oidcSpec),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSinkAddress(addrFor(nil, &aud)),
					rttesting.WithYipYapSourceNotDeployed("ServiceAccountFailed",
						`ServiceAccount "`+oidcSA.Name+`" exists and is not owned by YipYapSource "`+sourceName+`"`),
				),
			}},
		},
		{
			// The pinned account never gets an audience-bound token: the
			// audience is the source author's choice. The pod runs as the
			// generated account and keeps the pinned one's pull secrets.
			Name: "OIDC sink with spec.serviceAccountName: generated identity, pinned pull secrets",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(rttesting.WithYipYapSourceSpec(pinnedSpec)),
				pinnedSA,
			},
			WantCreates: []runtime.Object{
				resources.MakeOIDCServiceAccount(newSource(rttesting.WithYipYapSourceSpec(pinnedSpec))),
				withOIDCIdentityAndPullSecrets(deploymentFor(pinnedSpec, addrFor(nil, &aud)), "regcred"),
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(pinnedSpec),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSinkAddress(addrFor(nil, &aud)),
					rttesting.WithYipYapSourceOIDCIdentity(oidcSA.Name),
					rttesting.WithYipYapSourceDeploymentName(depName),
					rttesting.WithYipYapSourceConnecting(reasonAdapterStarting, msgAdapterStarting),
				),
			}},
		},
		{
			Name: "OIDC sink with a missing spec.serviceAccountName",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(rttesting.WithYipYapSourceSpec(pinnedSpec)),
			},
			WantCreates: []runtime.Object{
				resources.MakeOIDCServiceAccount(newSource(rttesting.WithYipYapSourceSpec(pinnedSpec))),
			},
			WantErr: true,
			WantEvents: []string{
				`Warning InternalError get ServiceAccount "sa-pinned" for its imagePullSecrets: serviceaccounts "sa-pinned" not found`,
			},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithYipYapSourceSpec(pinnedSpec),
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSinkAddress(addrFor(nil, &aud)),
					rttesting.WithYipYapSourceNotDeployed("ServiceAccountFailed",
						`get ServiceAccount "sa-pinned" for its imagePullSecrets: serviceaccounts "sa-pinned" not found`),
				),
			}},
		},
		{
			Name: "sink drops CA and audience: stale status fields cleared",
			Key:  sourceNS + "/" + sourceName,
			Objects: []runtime.Object{
				newSource(
					rttesting.WithYipYapSourceSinkAddress(addrFor(&ca, &aud)),
					rttesting.WithYipYapSourceOIDCIdentity(oidcSA.Name),
				),
			},
			WantCreates: []runtime.Object{expectedDeployment(t)},
			WantStatusUpdates: []clientgotesting.UpdateActionImpl{{
				Object: newSource(
					rttesting.WithInitYipYapSourceConditions,
					rttesting.WithYipYapSourceBaseURLAllowed,
					rttesting.WithYipYapSourceCloudEventAttributes(knownAttrs),
					rttesting.WithYipYapSourceSink(sinkURL),
					rttesting.WithYipYapSourceDeploymentName(depName),
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
			sinkResolver:     resolver.NewURIResolverFromTracker(ctx, tracker.New(func(types.NamespacedName) {}, 0)),
		}
		return reconcileryipyapsource.NewReconciler(ctx, logger,
			fakeyipyapclient.Get(ctx),
			ls.GetYipYapSourceLister(),
			controller.GetEventRecorder(ctx),
			r)
	}))
}
