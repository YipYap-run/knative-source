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

package testing

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakekubeclientset "k8s.io/client-go/kubernetes/fake"
	appsv1listers "k8s.io/client-go/listers/apps/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	"go.uber.org/zap"

	fakekubeclient "knative.dev/pkg/client/injection/kube/client/fake"
	"knative.dev/pkg/configmap"
	"knative.dev/pkg/controller"
	"knative.dev/pkg/injection"
	fakedynamicclient "knative.dev/pkg/injection/clients/dynamicclient/fake"
	"knative.dev/pkg/logging"
	"knative.dev/pkg/reconciler"
	. "knative.dev/pkg/reconciler/testing" //nolint:staticcheck // dot import matches upstream idiom

	"github.com/YipYap-run/knative-source/pkg/apis/sources/v1alpha1"
	fakeyipyapclient "github.com/YipYap-run/knative-source/pkg/client/injection/client/fake"
	yipyaplisters "github.com/YipYap-run/knative-source/pkg/client/listers/sources/v1alpha1"
)

// maxEventBufferSize caps the number of events retained per test.
const maxEventBufferSize = 10

// Listers is the minimal lister surface our reconciler table tests need: just
// the YipYapSource and its owned Deployments.
type Listers struct {
	sorter ObjectSorter
}

// NewListers seeds an ObjectSorter with the provided objects so that each
// typed lister/indexer returns them.
func NewListers(objs []runtime.Object) Listers {
	scheme := NewScheme()
	ls := Listers{sorter: NewObjectSorter(scheme)}
	ls.sorter.AddObjects(objs...)
	return ls
}

// NewScheme builds the scheme that covers every object type our table tests
// can hand to the sorter or the fake clients.
func NewScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	for _, addTo := range []func(*runtime.Scheme) error{
		fakekubeclientset.AddToScheme,
		v1alpha1.AddToScheme,
	} {
		if err := addTo(scheme); err != nil {
			panic(err)
		}
	}
	return scheme
}

func (l *Listers) indexerFor(obj runtime.Object) cache.Indexer {
	return l.sorter.IndexerForObjectType(obj)
}

// GetKubeObjects returns objects that belong to the Kubernetes core scheme
// (Deployments, Events, etc.) — what we hand to fakekubeclient.
func (l *Listers) GetKubeObjects() []runtime.Object {
	return l.sorter.ObjectsForSchemeFunc(fakekubeclientset.AddToScheme)
}

// GetYipYapSourceObjects returns objects that belong to our CR scheme — what
// we hand to fakeyipyapclient.
func (l *Listers) GetYipYapSourceObjects() []runtime.Object {
	return l.sorter.ObjectsForSchemeFunc(v1alpha1.AddToScheme)
}

// GetDeploymentLister returns an apps/v1 DeploymentLister backed by the
// sorter's indexer.
func (l *Listers) GetDeploymentLister() appsv1listers.DeploymentLister {
	return appsv1listers.NewDeploymentLister(l.indexerFor(&appsv1.Deployment{}))
}

// GetYipYapSourceLister returns a YipYapSourceLister backed by the sorter's indexer.
func (l *Listers) GetYipYapSourceLister() yipyaplisters.YipYapSourceLister {
	return yipyaplisters.NewYipYapSourceLister(l.indexerFor(&v1alpha1.YipYapSource{}))
}

// Ctor builds the concrete controller.Reconciler under test; the factory
// below wires fakes + loggers + listers before invoking it.
type Ctor func(ctx context.Context, ls *Listers, cmw configmap.Watcher) controller.Reconciler

// MakeFactory returns a TableTest Factory that wires fake clients, the
// requested listers, and configures an event-recorder + configmap watcher
// that reconcilers typically depend on.
func MakeFactory(ctor Ctor) Factory {
	return func(t *testing.T, r *TableRow) (controller.Reconciler, ActionRecorderList, EventList) {
		ls := NewListers(r.Objects)

		ctx := r.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		logger := logging.FromContext(ctx)
		if logger == nil {
			logger = zap.NewNop().Sugar()
		}
		ctx = logging.WithLogger(ctx, logger)
		r.Ctx = ctx

		ctx, kubeClient := fakekubeclient.With(ctx, ls.GetKubeObjects()...)
		ctx, yipyapClient := fakeyipyapclient.With(ctx, ls.GetYipYapSourceObjects()...)
		ctx, _ = fakedynamicclient.With(ctx, NewScheme())

		// Register ducks (addressable, etc.) so the URIResolver constructor
		// finds its informer factory in the context.
		for _, duck := range injection.Fake.GetDucks() {
			ctx = duck(ctx)
		}

		eventRecorder := record.NewFakeRecorder(maxEventBufferSize)
		ctx = controller.WithEventRecorder(ctx, eventRecorder)

		var cms []*corev1.ConfigMap
		for _, obj := range r.Objects {
			if cm, ok := obj.(*corev1.ConfigMap); ok {
				cms = append(cms, cm)
			}
		}
		cmw := configmap.NewStaticWatcher(cms...)

		c := ctor(ctx, &ls, cmw)

		// Promote the reconciler so it leaves the "standby" branch behind
		// when Reconcile is called in-process.
		if la, ok := c.(reconciler.LeaderAware); ok {
			_ = la.Promote(reconciler.UniversalBucket(), func(reconciler.Bucket, types.NamespacedName) {})
		}

		for _, reactor := range r.WithReactors {
			kubeClient.PrependReactor("*", "*", reactor)
			yipyapClient.PrependReactor("*", "*", reactor)
		}

		actionRecorderList := ActionRecorderList{yipyapClient, kubeClient}
		eventList := EventList{Recorder: eventRecorder}
		return c, actionRecorderList, eventList
	}
}
