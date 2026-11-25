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

// Package rbac checks the RBAC shipped by the Helm chart and by the static
// manifests under config/. Both install paths are checked against the same
// least-privilege rules and against each other.
package rbac

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

const (
	releaseNamespace = "yipyap-sources"
	controllerSA     = "yipyap-source-controller"
	webhookSA        = "yipyap-source-webhook"
	webhookCertName  = "yipyap-source-webhook-certs"
)

var ownWebhookConfigs = []string{
	"defaulting.webhook.sources.yipyap.run",
	"validation.webhook.sources.yipyap.run",
	"config.webhook.sources.yipyap.run",
}

// manifests is the RBAC slice of one install path.
type manifests struct {
	clusterRoles        map[string]*rbacv1.ClusterRole
	clusterRoleBindings []*rbacv1.ClusterRoleBinding
	roles               map[string]*rbacv1.Role // key: namespace/name
	roleBindings        []*rbacv1.RoleBinding
}

func newManifests() *manifests {
	return &manifests{
		clusterRoles: map[string]*rbacv1.ClusterRole{},
		roles:        map[string]*rbacv1.Role{},
	}
}

func (m *manifests) add(t *testing.T, data []byte) {
	t.Helper()
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("decode manifest: %v", err)
		}
		if obj == nil {
			continue
		}
		u := &unstructured.Unstructured{Object: obj}
		var target any
		switch u.GetKind() {
		case "ClusterRole":
			cr := &rbacv1.ClusterRole{}
			m.clusterRoles[u.GetName()] = cr
			target = cr
		case "ClusterRoleBinding":
			crb := &rbacv1.ClusterRoleBinding{}
			m.clusterRoleBindings = append(m.clusterRoleBindings, crb)
			target = crb
		case "Role":
			r := &rbacv1.Role{}
			m.roles[u.GetNamespace()+"/"+u.GetName()] = r
			target = r
		case "RoleBinding":
			rb := &rbacv1.RoleBinding{}
			m.roleBindings = append(m.roleBindings, rb)
			target = rb
		default:
			continue
		}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, target); err != nil {
			t.Fatalf("convert %s %s: %v", u.GetKind(), u.GetName(), err)
		}
	}
}

func isSubject(subjects []rbacv1.Subject, sa string) bool {
	for _, s := range subjects {
		if s.Kind == "ServiceAccount" && s.Name == sa && s.Namespace == releaseNamespace {
			return true
		}
	}
	return false
}

// clusterRules returns every cluster-wide rule bound to the service account
// through a ClusterRoleBinding to a ClusterRole defined in this install.
// Aggregated ClusterRoles from Knative Eventing (addressable-resolver) are
// outside this install and are skipped.
func (m *manifests) clusterRules(sa string) []rbacv1.PolicyRule {
	var out []rbacv1.PolicyRule
	for _, b := range m.clusterRoleBindings {
		if b.RoleRef.Kind != "ClusterRole" || !isSubject(b.Subjects, sa) {
			continue
		}
		if cr, ok := m.clusterRoles[b.RoleRef.Name]; ok {
			out = append(out, cr.Rules...)
		}
	}
	return out
}

// namespacedRules returns the Role rules bound to the service account in ns.
func (m *manifests) namespacedRules(sa, ns string) []rbacv1.PolicyRule {
	var out []rbacv1.PolicyRule
	for _, b := range m.roleBindings {
		if b.Namespace != ns || b.RoleRef.Kind != "Role" || !isSubject(b.Subjects, sa) {
			continue
		}
		if r, ok := m.roles[ns+"/"+b.RoleRef.Name]; ok {
			out = append(out, r.Rules...)
		}
	}
	return out
}

// bindingNamespaces returns every namespace with a RoleBinding for sa.
func (m *manifests) bindingNamespaces(sa string) []string {
	var out []string
	for _, b := range m.roleBindings {
		if isSubject(b.Subjects, sa) && !slices.Contains(out, b.Namespace) {
			out = append(out, b.Namespace)
		}
	}
	return out
}

func covers(rule rbacv1.PolicyRule, group, resource string) bool {
	return (slices.Contains(rule.APIGroups, group) || slices.Contains(rule.APIGroups, "*")) &&
		(slices.Contains(rule.Resources, resource) || slices.Contains(rule.Resources, "*"))
}

func grants(rules []rbacv1.PolicyRule, group, resource, verb string, named bool) bool {
	for _, r := range rules {
		if !covers(r, group, resource) {
			continue
		}
		if named != (len(r.ResourceNames) > 0) {
			continue
		}
		if slices.Contains(r.Verbs, verb) || slices.Contains(r.Verbs, "*") {
			return true
		}
	}
	return false
}

func verbsFor(rules []rbacv1.PolicyRule, group, resource string) []string {
	set := map[string]bool{}
	for _, r := range rules {
		if covers(r, group, resource) {
			for _, v := range r.Verbs {
				set[v] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func checkPolicy(t *testing.T, m *manifests) {
	t.Helper()

	whCluster := m.clusterRules(webhookSA)
	ctlCluster := m.clusterRules(controllerSA)
	if len(whCluster) == 0 || len(ctlCluster) == 0 {
		t.Fatalf("no ClusterRole bound to the webhook (%d rules) or controller (%d rules)", len(whCluster), len(ctlCluster))
	}

	// Webhook informers need unfiltered list/watch on webhook configurations.
	for _, res := range []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"} {
		for _, verb := range []string{"list", "watch"} {
			if !grants(whCluster, "admissionregistration.k8s.io", res, verb, false) {
				t.Errorf("webhook lacks %s on %s without resourceNames; its informers can never sync", verb, res)
			}
		}
		for _, verb := range []string{"get", "update", "patch"} {
			if !grants(whCluster, "admissionregistration.k8s.io", res, verb, true) {
				t.Errorf("webhook lacks %s on its own %s", verb, res)
			}
		}
		for _, r := range whCluster {
			if !covers(r, "admissionregistration.k8s.io", res) {
				continue
			}
			if len(r.ResourceNames) == 0 {
				for _, v := range r.Verbs {
					if v != "list" && v != "watch" {
						t.Errorf("webhook has %s on every %s; writes must be limited by resourceNames", v, res)
					}
				}
				continue
			}
			for _, n := range r.ResourceNames {
				if !slices.Contains(ownWebhookConfigs, n) {
					t.Errorf("webhook may write %s %q, which this install does not own", res, n)
				}
			}
		}
	}

	// Nothing namespaced that only the release namespace needs may be
	// granted cluster-wide.
	for _, c := range []struct {
		sa    string
		rules []rbacv1.PolicyRule
	}{{webhookSA, whCluster}, {controllerSA, ctlCluster}} {
		if v := verbsFor(c.rules, "", "secrets"); len(v) > 0 {
			t.Errorf("%s has cluster-wide secrets access %v", c.sa, v)
		}
		if v := verbsFor(c.rules, "coordination.k8s.io", "leases"); len(v) > 0 {
			t.Errorf("%s has cluster-wide leases access %v", c.sa, v)
		}
		if v := verbsFor(c.rules, "rbac.authorization.k8s.io", "clusterroles"); len(v) > 0 {
			t.Errorf("%s has clusterroles access %v", c.sa, v)
		}
	}

	// The controller's generated reconciler records events on YipYapSources
	// in their own namespaces. The webhook never records events.
	if v := verbsFor(ctlCluster, "", "events"); !slices.Equal(v, []string{"create", "patch", "update"}) {
		t.Errorf("%s events verbs = %v, want [create patch update]", controllerSA, v)
	}
	if v := verbsFor(whCluster, "", "events"); len(v) > 0 {
		t.Errorf("%s has events access %v; nothing in the webhook records events", webhookSA, v)
	}
	if v := verbsFor(m.namespacedRules(webhookSA, releaseNamespace), "", "events"); len(v) > 0 {
		t.Errorf("%s has release-namespace events access %v", webhookSA, v)
	}

	// Both binaries read ConfigMaps only in the release namespace (sharedmain
	// config and the informed watcher), never cluster-wide or in source
	// namespaces.
	for _, c := range []struct {
		sa    string
		rules []rbacv1.PolicyRule
	}{{webhookSA, whCluster}, {controllerSA, ctlCluster}} {
		if v := verbsFor(c.rules, "", "configmaps"); len(v) > 0 {
			t.Errorf("%s has cluster-wide configmaps access %v", c.sa, v)
		}
		for _, ns := range m.bindingNamespaces(c.sa) {
			if ns == releaseNamespace {
				continue
			}
			if v := verbsFor(m.namespacedRules(c.sa, ns), "", "configmaps"); len(v) > 0 {
				t.Errorf("%s has configmaps access %v in namespace %s", c.sa, v, ns)
			}
		}
	}
	if v := verbsFor(whCluster, "eventing.knative.dev", "eventtypes"); len(v) > 0 {
		t.Errorf("webhook has eventtypes access %v", v)
	}
	if v := verbsFor(whCluster, "sources.yipyap.run", "yipyapsources"); !slices.Equal(v, []string{"get", "list", "watch"}) {
		t.Errorf("webhook yipyapsources verbs = %v, want read only", v)
	}
	for _, sub := range []string{"yipyapsources/finalizers", "yipyapsources/status"} {
		if v := verbsFor(whCluster, "sources.yipyap.run", sub); len(v) > 0 {
			t.Errorf("webhook has %s access %v", sub, v)
		}
	}

	// The validating webhook runs a SubjectAccessReview for the requesting
	// user on the API key Secret. It needs create on subjectaccessreviews and
	// nothing else in authorization.k8s.io; the controller needs none of it.
	if v := verbsFor(whCluster, "authorization.k8s.io", "subjectaccessreviews"); !slices.Equal(v, []string{"create"}) {
		t.Errorf("webhook subjectaccessreviews verbs = %v, want [create]", v)
	}
	if v := verbsFor(ctlCluster, "authorization.k8s.io", "subjectaccessreviews"); len(v) > 0 {
		t.Errorf("controller has subjectaccessreviews access %v", v)
	}

	// Release-namespace Roles carry what was removed from the ClusterRoles.
	whNS := m.namespacedRules(webhookSA, releaseNamespace)
	for _, verb := range []string{"list", "watch"} {
		if !grants(whNS, "", "secrets", verb, false) {
			t.Errorf("webhook Role lacks %s on secrets", verb)
		}
	}
	for _, r := range whNS {
		if covers(r, "", "secrets") && len(r.ResourceNames) == 0 && slices.ContainsFunc(r.Verbs, func(v string) bool { return v != "list" && v != "watch" }) {
			t.Errorf("webhook Role grants %v on every Secret in the namespace; limit to %s", r.Verbs, webhookCertName)
		}
		if covers(r, "", "secrets") && len(r.ResourceNames) > 0 && !slices.Equal(r.ResourceNames, []string{webhookCertName}) {
			t.Errorf("webhook Role names unexpected Secrets %v", r.ResourceNames)
		}
	}
	if !grants(whNS, "", "secrets", "update", true) {
		t.Errorf("webhook Role lacks update on %s", webhookCertName)
	}
	for _, sa := range []struct {
		name  string
		rules []rbacv1.PolicyRule
	}{{webhookSA, whNS}, {controllerSA, m.namespacedRules(controllerSA, releaseNamespace)}} {
		if v := verbsFor(sa.rules, "coordination.k8s.io", "leases"); !slices.Equal(v, []string{"create", "get", "update"}) {
			t.Errorf("%s release-namespace leases verbs = %v, want [create get update]", sa.name, v)
		}
		if v := verbsFor(sa.rules, "", "configmaps"); !slices.Equal(v, []string{"get", "list", "watch"}) {
			t.Errorf("%s release-namespace configmaps verbs = %v, want [get list watch]", sa.name, v)
		}
	}

	// The controller lists adapter pods to read their status endpoint for
	// the Connected condition. List is all it needs; the webhook needs none.
	ctlPods := verbsFor(ctlCluster, "", "pods")
	for _, ns := range m.bindingNamespaces(controllerSA) {
		if ns != releaseNamespace {
			ctlPods = append(ctlPods, verbsFor(m.namespacedRules(controllerSA, ns), "", "pods")...)
		}
	}
	if len(ctlPods) == 0 {
		t.Errorf("%s cannot list pods; the Connected condition can never read the adapter", controllerSA)
	}
	for _, v := range ctlPods {
		if v != "list" {
			t.Errorf("%s has %s on pods; only list is needed", controllerSA, v)
		}
	}
	if v := verbsFor(whCluster, "", "pods"); len(v) > 0 {
		t.Errorf("%s has pods access %v", webhookSA, v)
	}

	// The controller creates a per-source OIDC ServiceAccount for sinks
	// with an audience. Get and create only, wherever it writes Deployments;
	// never token minting, updates or deletes.
	ctlSA := verbsFor(ctlCluster, "", "serviceaccounts")
	for _, ns := range m.bindingNamespaces(controllerSA) {
		if ns != releaseNamespace {
			ctlSA = append(ctlSA, verbsFor(m.namespacedRules(controllerSA, ns), "", "serviceaccounts")...)
		}
	}
	slices.Sort(ctlSA)
	ctlSA = slices.Compact(ctlSA)
	if !slices.Equal(ctlSA, []string{"create", "get"}) {
		t.Errorf("%s serviceaccounts verbs = %v, want [create get]", controllerSA, ctlSA)
	}
	if v := verbsFor(ctlCluster, "", "serviceaccounts/token"); len(v) > 0 {
		t.Errorf("%s can mint ServiceAccount tokens %v", controllerSA, v)
	}

	// The Knative source-observer aggregate must target the real API group.
	obs, ok := m.clusterRoles["yipyap-source-observer"]
	if !ok {
		t.Fatal("yipyap-source-observer ClusterRole missing")
	}
	if obs.Labels["duck.knative.dev/source"] != "true" {
		t.Error("yipyap-source-observer lacks the duck.knative.dev/source label")
	}
	if !grants(obs.Rules, "sources.yipyap.run", "yipyapsources", "list", false) {
		t.Errorf("yipyap-source-observer rules %+v do not read sources.yipyap.run/yipyapsources", obs.Rules)
	}
	for _, r := range obs.Rules {
		for _, g := range r.APIGroups {
			if g != "sources.yipyap.run" {
				t.Errorf("yipyap-source-observer names foreign apiGroup %q", g)
			}
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func loadConfig(t *testing.T) *manifests {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "config", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("glob config: %v (%d files)", err, len(files))
	}
	m := newManifests()
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		m.add(t, data)
	}
	return m
}

func loadChart(t *testing.T, extraArgs ...string) *manifests {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		if os.Getenv("CI") != "" || os.Getenv("YIPYAP_REQUIRE_HELM") != "" {
			t.Fatalf("helm is required to render the chart: %v", err)
		}
		t.Skip("helm not on PATH; set YIPYAP_REQUIRE_HELM=1 to make this a failure")
	}
	chart := filepath.Join(repoRoot(t), "chart", "yipyap-source")
	// The NetworkPolicy is on by default and fails the render without the
	// API server range, so use the chart's CI values.
	args := append([]string{"template", "yipyap-source", chart,
		"-f", filepath.Join(chart, "ci", "apiserver-cidrs-values.yaml")}, extraArgs...)
	cmd := exec.Command(helm, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template: %v: %s", err, stderr.String())
	}
	m := newManifests()
	m.add(t, out)
	return m
}

func TestConfigRBAC(t *testing.T) {
	checkPolicy(t, loadConfig(t))
}

func TestChartRBAC(t *testing.T) {
	checkPolicy(t, loadChart(t))
}

// With scopedNamespaces set, the controller keeps its release-namespace
// grants even when the release namespace is not in the list.
func TestChartRBACScoped(t *testing.T) {
	checkPolicy(t, loadChart(t, "--set", "controller.scopedNamespaces={team-a}"))
}

// canonicalRules turns a rule list into a sorted, order-independent form.
func canonicalRules(rules []rbacv1.PolicyRule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		norm := func(s []string) string {
			c := slices.Clone(s)
			sort.Strings(c)
			return strings.Join(c, ",")
		}
		out = append(out, fmt.Sprintf("groups=%s resources=%s names=%s verbs=%s",
			norm(r.APIGroups), norm(r.Resources), norm(r.ResourceNames), norm(r.Verbs)))
	}
	sort.Strings(out)
	return out
}

// TestChartMatchesConfig keeps the two install paths in step: with default
// values the chart must grant exactly what config/ grants.
func TestChartMatchesConfig(t *testing.T) {
	chart := loadChart(t)
	cfg := loadConfig(t)

	for _, sa := range []string{controllerSA, webhookSA} {
		if got, want := canonicalRules(chart.clusterRules(sa)), canonicalRules(cfg.clusterRules(sa)); !slices.Equal(got, want) {
			t.Errorf("%s cluster rules differ\nchart:  %q\nconfig: %q", sa, got, want)
		}
		if got, want := canonicalRules(chart.namespacedRules(sa, releaseNamespace)), canonicalRules(cfg.namespacedRules(sa, releaseNamespace)); !slices.Equal(got, want) {
			t.Errorf("%s release-namespace rules differ\nchart:  %q\nconfig: %q", sa, got, want)
		}
	}
	const observer = "yipyap-source-observer"
	if chart.clusterRoles[observer] == nil || cfg.clusterRoles[observer] == nil {
		t.Fatalf("%s missing from chart or config", observer)
	}
	if got, want := canonicalRules(chart.clusterRoles[observer].Rules), canonicalRules(cfg.clusterRoles[observer].Rules); !slices.Equal(got, want) {
		t.Errorf("%s rules differ\nchart:  %q\nconfig: %q", observer, got, want)
	}
}
