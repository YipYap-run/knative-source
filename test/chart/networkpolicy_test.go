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

// Package chart renders the Helm chart and checks the NetworkPolicies it
// produces.
package chart

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

const (
	controllerPolicy = "yipyap-source-controller"
	webhookPolicy    = "yipyap-source-webhook"
	ciValues         = "ci/apiserver-cidrs-values.yaml"
	ciCIDR           = "192.0.2.10/32"
	webhookPort      = 8443
)

func chartDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "chart", "yipyap-source"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func helmBin(t *testing.T) string {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		if os.Getenv("CI") != "" || os.Getenv("YIPYAP_REQUIRE_HELM") != "" {
			t.Fatalf("helm is required to render the chart: %v", err)
		}
		t.Skip("helm not on PATH; set YIPYAP_REQUIRE_HELM=1 to make this a failure")
	}
	return helm
}

// render runs helm template and returns stdout, stderr and the error.
func render(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(helmBin(t), append([]string{"template", "yipyap-source", chartDir(t)}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

type rendered struct {
	policies map[string]*networkingv1.NetworkPolicy
	cilium   map[string]map[string]any
}

func mustRender(t *testing.T, args ...string) rendered {
	t.Helper()
	out, stderr, err := render(t, args...)
	if err != nil {
		t.Fatalf("helm template %v: %v: %s", args, err, stderr)
	}
	r := rendered{policies: map[string]*networkingv1.NetworkPolicy{}, cilium: map[string]map[string]any{}}
	dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(out), 4096)
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				return r
			}
			t.Fatalf("decode manifest: %v", err)
		}
		if obj == nil {
			continue
		}
		u := &unstructured.Unstructured{Object: obj}
		switch u.GetKind() {
		case "NetworkPolicy":
			np := &networkingv1.NetworkPolicy{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj, np); err != nil {
				t.Fatalf("convert NetworkPolicy %s: %v", u.GetName(), err)
			}
			r.policies[u.GetName()] = np
		case "CiliumNetworkPolicy":
			r.cilium[u.GetName()] = obj
		}
	}
}

func ipBlocks(peers []networkingv1.NetworkPolicyPeer) []string {
	var out []string
	for _, p := range peers {
		if p.IPBlock != nil {
			out = append(out, p.IPBlock.CIDR)
		}
	}
	return out
}

func tcpPorts(ports []networkingv1.NetworkPolicyPort) []int {
	var out []int
	for _, p := range ports {
		if p.Port != nil {
			out = append(out, p.Port.IntValue())
		}
	}
	return out
}

// checkNoOpenRules fails on any rule that allows every peer, which is what
// an empty from/to list means, and on selector peers that are not the DNS
// rule (namespaceSelector {} alone matches every pod, never the API server).
func checkNoOpenRules(t *testing.T, np *networkingv1.NetworkPolicy) {
	t.Helper()
	for i, in := range np.Spec.Ingress {
		if len(in.From) == 0 {
			t.Errorf("%s ingress[%d] has no peers, which allows every source", np.Name, i)
		}
	}
	for i, eg := range np.Spec.Egress {
		if len(eg.To) == 0 {
			t.Errorf("%s egress[%d] has no peers, which allows every destination", np.Name, i)
		}
		for _, p := range eg.To {
			if p.NamespaceSelector != nil && p.PodSelector == nil {
				t.Errorf("%s egress[%d] has a bare namespaceSelector peer", np.Name, i)
			}
		}
	}
}

func hasDNSEgress(np *networkingv1.NetworkPolicy) bool {
	for _, eg := range np.Spec.Egress {
		for _, p := range eg.To {
			if p.PodSelector != nil && p.PodSelector.MatchLabels["k8s-app"] == "kube-dns" {
				return true
			}
		}
	}
	return false
}

// apiServerEgress returns the CIDRs and ports of the ipBlock egress rule.
func apiServerEgress(np *networkingv1.NetworkPolicy) ([]string, []int) {
	for _, eg := range np.Spec.Egress {
		if cidrs := ipBlocks(eg.To); len(cidrs) > 0 {
			return cidrs, tcpPorts(eg.Ports)
		}
	}
	return nil, nil
}

func webhookIngress(np *networkingv1.NetworkPolicy) []string {
	for _, in := range np.Spec.Ingress {
		if slices.Contains(tcpPorts(in.Ports), webhookPort) {
			return ipBlocks(in.From)
		}
	}
	return nil
}

func TestNetworkPolicyRequiresAPIServerCIDRs(t *testing.T) {
	const want = "networkPolicy.apiServerCIDRs is required when networkPolicy.enabled is true. " +
		"Set it to your kube-apiserver address range (kubectl get endpoints kubernetes -n default)"
	for name, args := range map[string][]string{
		"defaults":      nil,
		"explicit-on":   {"--set", "networkPolicy.enabled=true"},
		"empty-list":    {"--set", "networkPolicy.apiServerCIDRs=null"},
		"cilium-off":    {"--set", "networkPolicy.cilium.useEntities=false"},
		"webhook-cidrs": {"--set", "networkPolicy.webhookIngressCIDRs={10.0.0.0/24}"},
	} {
		t.Run(name, func(t *testing.T) {
			_, stderr, err := render(t, args...)
			if err == nil {
				t.Fatal("helm template succeeded without networkPolicy.apiServerCIDRs")
			}
			if !strings.Contains(stderr, want) {
				t.Errorf("error does not explain the fix\ngot:  %s\nwant: %s", stderr, want)
			}
			if !strings.Contains(stderr, "networkPolicy.enabled=false") {
				t.Errorf("error does not mention the opt-out: %s", stderr)
			}
		})
	}
}

func TestNetworkPolicyDisabledNeedsNoCIDRs(t *testing.T) {
	r := mustRender(t, "--set", "networkPolicy.enabled=false")
	if len(r.policies) != 0 || len(r.cilium) != 0 {
		t.Fatalf("policies rendered with networkPolicy.enabled=false: %d NetworkPolicy, %d Cilium", len(r.policies), len(r.cilium))
	}
}

func TestNetworkPolicyAPIServerIPBlocks(t *testing.T) {
	r := mustRender(t, "-f", filepath.Join(chartDir(t), ciValues))
	if len(r.cilium) != 0 {
		t.Errorf("CiliumNetworkPolicy rendered without cilium.useEntities: %v", len(r.cilium))
	}
	for _, name := range []string{controllerPolicy, webhookPolicy} {
		np := r.policies[name]
		if np == nil {
			t.Fatalf("NetworkPolicy %s not rendered", name)
		}
		checkNoOpenRules(t, np)
		if !hasDNSEgress(np) {
			t.Errorf("%s lost its DNS egress rule", name)
		}
		cidrs, ports := apiServerEgress(np)
		if !slices.Equal(cidrs, []string{ciCIDR}) {
			t.Errorf("%s API server egress CIDRs = %v, want [%s]", name, cidrs, ciCIDR)
		}
		if !slices.Equal(ports, []int{443, 6443}) {
			t.Errorf("%s API server egress ports = %v, want [443 6443]", name, ports)
		}
	}
	if got := webhookIngress(r.policies[webhookPolicy]); !slices.Equal(got, []string{ciCIDR}) {
		t.Errorf("webhook ingress peers = %v, want [%s]", got, ciCIDR)
	}
	if n := len(r.policies[controllerPolicy].Spec.Ingress); n != 0 {
		t.Errorf("controller has %d ingress rules with metrics labels unset, want 0", n)
	}
}

func TestNetworkPolicyCustomPortsAndWebhookCIDRs(t *testing.T) {
	r := mustRender(t,
		"--set", "networkPolicy.apiServerCIDRs={10.0.0.1/32,fd00::1/128}",
		"--set", "networkPolicy.apiServerPorts={6443}",
		"--set", "networkPolicy.webhookIngressCIDRs={10.1.0.0/16}",
	)
	for _, name := range []string{controllerPolicy, webhookPolicy} {
		np := r.policies[name]
		checkNoOpenRules(t, np)
		cidrs, ports := apiServerEgress(np)
		if !slices.Equal(cidrs, []string{"10.0.0.1/32", "fd00::1/128"}) {
			t.Errorf("%s egress CIDRs = %v", name, cidrs)
		}
		if !slices.Equal(ports, []int{6443}) {
			t.Errorf("%s egress ports = %v, want [6443]", name, ports)
		}
	}
	if got := webhookIngress(r.policies[webhookPolicy]); !slices.Equal(got, []string{"10.1.0.0/16"}) {
		t.Errorf("webhook ingress peers = %v, want [10.1.0.0/16]", got)
	}
}

func TestNetworkPolicyCiliumEntities(t *testing.T) {
	r := mustRender(t, "--set", "networkPolicy.cilium.useEntities=true")
	for _, name := range []string{controllerPolicy, webhookPolicy} {
		np := r.policies[name]
		if np == nil {
			t.Fatalf("NetworkPolicy %s not rendered", name)
		}
		// No CIDRs: no ipBlock rule, and no ingress rule left with an
		// empty peer list.
		checkNoOpenRules(t, np)
		if cidrs, _ := apiServerEgress(np); cidrs != nil {
			t.Errorf("%s has ipBlock egress %v without apiServerCIDRs", name, cidrs)
		}
	}
	for _, name := range []string{"yipyap-source-controller-apiserver", "yipyap-source-webhook-apiserver"} {
		obj := r.cilium[name]
		if obj == nil {
			t.Fatalf("CiliumNetworkPolicy %s not rendered", name)
		}
		egress, _, _ := unstructured.NestedSlice(obj, "spec", "egress")
		if len(egress) != 1 {
			t.Fatalf("%s egress = %v", name, egress)
		}
		entities, _, _ := unstructured.NestedStringSlice(egress[0].(map[string]any), "toEntities")
		if !slices.Equal(entities, []string{"kube-apiserver"}) {
			t.Errorf("%s toEntities = %v", name, entities)
		}
	}
	ingress, _, _ := unstructured.NestedSlice(r.cilium["yipyap-source-webhook-apiserver"], "spec", "ingress")
	if len(ingress) != 1 {
		t.Fatalf("webhook Cilium ingress = %v", ingress)
	}
	from, _, _ := unstructured.NestedStringSlice(ingress[0].(map[string]any), "fromEntities")
	if !slices.Equal(from, []string{"kube-apiserver"}) {
		t.Errorf("webhook fromEntities = %v", from)
	}
}

func TestNetworkPolicyRejectsBadValues(t *testing.T) {
	for name, args := range map[string][]string{
		"not-a-cidr":  {"--set", "networkPolicy.apiServerCIDRs={10.0.0.1}"},
		"string":      {"--set", "networkPolicy.apiServerCIDRs=10.0.0.1/32"},
		"empty-ports": {"-f", filepath.Join(chartDir(t), ciValues), "--set", "networkPolicy.apiServerPorts=null"},
		"bad-port":    {"-f", filepath.Join(chartDir(t), ciValues), "--set", "networkPolicy.apiServerPorts={70000}"},
		"unknown-key": {"-f", filepath.Join(chartDir(t), ciValues), "--set", "networkPolicy.apiServerCidrs={10.0.0.1/32}"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := render(t, args...); err == nil {
				t.Fatalf("helm template accepted %v", args)
			}
		})
	}
}

func TestChartLintWithCIValues(t *testing.T) {
	cmd := exec.Command(helmBin(t), "lint", chartDir(t), "-f", filepath.Join(chartDir(t), ciValues))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helm lint: %v\n%s", err, out)
	}
}

// TestNetworkPolicyControllerReachesAdapterStatus: the controller reads each
// adapter's status endpoint for the Connected condition. Only adapter pods
// (by the source label) on the adapter port are allowed, and the webhook
// gets no such rule.
func TestNetworkPolicyControllerReachesAdapterStatus(t *testing.T) {
	const adapterLabel = "sources.yipyap.run/source"
	const adapterPort = 8080
	r := mustRender(t, "-f", filepath.Join(chartDir(t), ciValues))

	adapterRule := func(np *networkingv1.NetworkPolicy) *networkingv1.NetworkPolicyEgressRule {
		for i, eg := range np.Spec.Egress {
			for _, p := range eg.To {
				if p.PodSelector == nil {
					continue
				}
				for _, e := range p.PodSelector.MatchExpressions {
					if e.Key == adapterLabel {
						return &np.Spec.Egress[i]
					}
				}
			}
		}
		return nil
	}

	rule := adapterRule(r.policies[controllerPolicy])
	if rule == nil {
		t.Fatal("controller NetworkPolicy has no egress rule to adapter pods")
	}
	if got := tcpPorts(rule.Ports); !slices.Equal(got, []int{adapterPort}) {
		t.Errorf("adapter egress ports = %v, want [%d]", got, adapterPort)
	}
	for _, p := range rule.To {
		if p.PodSelector == nil || len(p.PodSelector.MatchExpressions) != 1 ||
			p.PodSelector.MatchExpressions[0].Key != adapterLabel ||
			p.PodSelector.MatchExpressions[0].Operator != "Exists" ||
			len(p.PodSelector.MatchLabels) != 0 {
			t.Errorf("adapter egress peer %+v selects more than adapter pods", p)
		}
	}
	if adapterRule(r.policies[webhookPolicy]) != nil {
		t.Error("webhook NetworkPolicy allows egress to adapter pods")
	}
}
