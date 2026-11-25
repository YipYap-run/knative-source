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

package rbac

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The controller's Deployment informer lists and watches every namespace, so
// scoping can only move the write verbs into per-namespace Roles. Dropping
// the cluster-wide reads leaves the informer cache unsynced and the
// controller never reconciles.
func TestChartScopedNamespacesSplitsReadsAndWrites(t *testing.T) {
	scoped := []string{"team-a", "team-b"}
	m := loadChart(t, "--set", "controller.scopedNamespaces={team-a,team-b}")
	ctlCluster := m.clusterRules(controllerSA)

	if v := verbsFor(ctlCluster, "apps", "deployments"); !slices.Equal(v, []string{"get", "list", "watch"}) {
		t.Errorf("cluster-wide deployments verbs = %v, want [get list watch]", v)
	}
	// The YipYapSource informer is cluster-wide too.
	for _, verb := range []string{"get", "list", "watch"} {
		if !grants(ctlCluster, "sources.yipyap.run", "yipyapsources", verb, false) {
			t.Errorf("cluster-wide yipyapsources lacks %s", verb)
		}
	}
	// Pods are listed per source namespace with a direct client call, so the
	// grant belongs in the scoped Roles only.
	if v := verbsFor(ctlCluster, "", "pods"); len(v) > 0 {
		t.Errorf("cluster-wide pods verbs = %v, want none when scoped", v)
	}

	for _, ns := range scoped {
		rules := m.namespacedRules(controllerSA, ns)
		if v := verbsFor(rules, "apps", "deployments"); !slices.Equal(v, []string{"create", "delete", "patch", "update"}) {
			t.Errorf("namespace %s deployments verbs = %v, want [create delete patch update]", ns, v)
		}
		if v := verbsFor(rules, "", "pods"); !slices.Equal(v, []string{"list"}) {
			t.Errorf("namespace %s pods verbs = %v, want [list]", ns, v)
		}
		if v := verbsFor(rules, "", "configmaps"); len(v) > 0 {
			t.Errorf("namespace %s configmaps verbs = %v, want none", ns, v)
		}
	}

	// No namespace outside the list (and the release namespace) gets a
	// controller RoleBinding.
	for _, ns := range m.bindingNamespaces(controllerSA) {
		if ns != releaseNamespace && !slices.Contains(scoped, ns) {
			t.Errorf("unexpected controller RoleBinding in namespace %s", ns)
		}
	}

	// sharedmain reads config-logging, config-observability and
	// config-leader-election from the release namespace, which is not listed.
	rel := m.namespacedRules(controllerSA, releaseNamespace)
	if v := verbsFor(rel, "", "configmaps"); !slices.Equal(v, []string{"get", "list", "watch"}) {
		t.Errorf("release namespace configmaps verbs = %v, want [get list watch]", v)
	}
	if v := verbsFor(rel, "apps", "deployments"); len(v) > 0 {
		t.Errorf("release namespace deployments verbs = %v, want none when it is not listed", v)
	}
}

// Listing the release namespace adds its Deployment writes without
// disturbing the release-namespace config Role.
func TestChartScopedNamespacesIncludingReleaseNamespace(t *testing.T) {
	m := loadChart(t, "--set", "controller.scopedNamespaces={"+releaseNamespace+",team-a}")
	rel := m.namespacedRules(controllerSA, releaseNamespace)
	if v := verbsFor(rel, "", "configmaps"); !slices.Equal(v, []string{"get", "list", "watch"}) {
		t.Errorf("release namespace configmaps verbs = %v, want [get list watch]", v)
	}
	if v := verbsFor(rel, "apps", "deployments"); !slices.Equal(v, []string{"create", "delete", "patch", "update"}) {
		t.Errorf("release namespace deployments verbs = %v, want [create delete patch update]", v)
	}
	if v := verbsFor(rel, "coordination.k8s.io", "leases"); !slices.Equal(v, []string{"create", "get", "update"}) {
		t.Errorf("release namespace leases verbs = %v, want [create get update]", v)
	}
}

// A bad scopedNamespaces entry must fail the install. An empty string
// renders "namespace: \"\"", which helm installs into the release namespace,
// silently granting Deployment writes there. A duplicate renders the same
// Role twice and the install fails halfway.
func TestChartScopedNamespacesRejectsBadEntries(t *testing.T) {
	loadChart(t) // skips or fails the same way when helm is missing
	chart := filepath.Join(repoRoot(t), "chart", "yipyap-source")
	for _, c := range []struct{ name, flag, value, want string }{
		{"empty", "--set-json", `controller.scopedNamespaces=[""]`, "scopedNamespaces/0"},
		{"duplicate", "--set", "controller.scopedNamespaces={team-a,team-a}", "are equal"},
		{"not a DNS label", "--set", "controller.scopedNamespaces={Team_A}", "does not match pattern"},
		{"too long", "--set", "controller.scopedNamespaces={" + strings.Repeat("a", 64) + "}", "scopedNamespaces/0"},
		{"string not list", "--set", "controller.scopedNamespaces=team-a", "want array"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := exec.Command("helm", "template", "yipyap-source", chart,
				"-f", filepath.Join(chart, "ci", "apiserver-cidrs-values.yaml"),
				c.flag, c.value).CombinedOutput()
			if err == nil {
				t.Fatalf("helm template accepted %s", c.value)
			}
			if !strings.Contains(string(out), "scopedNamespaces") && !strings.Contains(string(out), c.want) {
				t.Errorf("error output does not identify scopedNamespaces or %q:\n%s", c.want, out)
			}
		})
	}
}
