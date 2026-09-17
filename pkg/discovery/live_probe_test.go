/*
Copyright 2026 The RBG Authors.

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

package discovery

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// TestLiveProbe_ConfigMapVsPods is a live-layer verification harness. It is a
// no-op unless RBG_LIVE_PROBE=1 is set. It reads a live RoleBasedGroup from the
// cluster pointed at by KUBECONFIG, runs the PR-head ConfigBuilder against it
// (the exact code the RBG reconciler calls in rolebasedgroup_controller.go),
// then cross-checks every generated instance address against the real live Pods
// of the RBG: (1) the address's pod-name component must equal a real Pod name,
// and (2) the address resolves via cluster DNS iff that Pod has hostname=<pod>
// and subdomain=<svc>.
//
// This bypasses the RoleInstanceSet informer (which is blocked on this cluster
// by legacy restartPolicy data) — ConfigBuilder only needs the RBG + headless
// Services, both of which decode fine.
func TestLiveProbe_ConfigMapVsPods(t *testing.T) {
	if os.Getenv("RBG_LIVE_PROBE") != "1" {
		t.Skip("live probe disabled; set RBG_LIVE_PROBE=1 + KUBECONFIG to run")
	}
	ns := os.Getenv("RBG_PROBE_NS")
	if ns == "" {
		ns = "rbg-verify-pr471"
	}
	name := os.Getenv("RBG_PROBE_RBG")
	if name == "" {
		name = "discovery-cm-test"
	}

	cfg, err := ctrl.GetConfig()
	if err != nil {
		t.Fatalf("get kubeconfig: %v", err)
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("core scheme: %v", err)
	}
	if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("workload scheme: %v", err)
	}
	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	ctx := context.Background()

	rbg := &workloadsv1alpha2.RoleBasedGroup{}
	if err := cl.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, rbg); err != nil {
		t.Fatalf("get rbg %s/%s: %v", ns, name, err)
	}

	// Mirror the RBG reconciler: keep only stateful roles, pass nil role.
	statefulOnly := rbg.DeepCopy()
	statefulOnly.Spec.Roles = nil
	for i := range rbg.Spec.Roles {
		if workloadsv1alpha2.IsStatefulRole(&rbg.Spec.Roles[i]) {
			statefulOnly.Spec.Roles = append(statefulOnly.Spec.Roles, rbg.Spec.Roles[i])
		}
	}

	out, err := NewConfigBuilder(cl, statefulOnly, nil).Build()
	if err != nil {
		t.Fatalf("ConfigBuilder.Build: %v", err)
	}
	cfgYAML := string(out)
	t.Logf("PR-head ConfigBuilder output (live RBG %s/%s):\n%s", ns, name, cfgYAML)

	// Parse the generated YAML back so we can iterate addresses deterministically.
	var cc ClusterConfig
	if err := yaml.Unmarshal([]byte(cfgYAML), &cc); err != nil {
		t.Fatalf("parse built config: %v", err)
	}

	// Gather live pods for the RBG.
	pods := &corev1.PodList{}
	if err := cl.List(ctx, pods, client.InNamespace(ns)); err != nil {
		t.Fatalf("list pods: %v", err)
	}
	byName := map[string]corev1.Pod{}
	for _, p := range pods.Items {
		if !strings.HasPrefix(p.Name, name+"-") {
			continue
		}
		byName[p.Name] = p
	}
	t.Logf("live pods (%d) for rbg %s:", len(byName), name)
	for _, p := range pods.Items {
		if strings.HasPrefix(p.Name, name+"-") {
			t.Logf("  %s  hostname=%q subdomain=%q", p.Name, p.Spec.Hostname, p.Spec.Subdomain)
		}
	}

	// Sort role names for deterministic output (RolesInfo is a map).
	roleNames := make([]string, 0, len(cc.Roles))
	for n := range cc.Roles {
		roleNames = append(roleNames, n)
	}
	sort.Strings(roleNames)

	// Cross-check every address.
	var mismatches int
	for _, roleName := range roleNames {
		ri := cc.Roles[roleName]
		if len(ri.Instances) != ri.Size {
			t.Errorf("role %s: size=%d but len(instances)=%d (should match)", roleName, ri.Size, len(ri.Instances))
			mismatches++
		}
		for _, inst := range ri.Instances {
			// address = <podName>.<svc>[.ns.svc.cluster.local] ; take the first dot.
			dot := strings.IndexByte(inst.Address, '.')
			podName, svc := inst.Address, ""
			if dot > 0 {
				podName, svc = inst.Address[:dot], inst.Address[dot+1:]
			}
			pod, exists := byName[podName]
			switch {
			case !exists:
				t.Errorf("role %s: address %q -> pod %q does NOT exist (no matching Pod)", roleName, inst.Address, podName)
				mismatches++
			case pod.Spec.Hostname == podName && pod.Spec.Subdomain == svc:
				t.Logf("role %s: address %q -> pod %q EXISTS + resolves (hostname+subdomain set)", roleName, inst.Address, podName)
			default:
				t.Logf("role %s: address %q -> pod %q EXISTS (canonical name ok) but NOT resolvable: hostname=%q subdomain=%q",
					roleName, inst.Address, podName, pod.Spec.Hostname, pod.Spec.Subdomain)
			}
		}
	}
	if mismatches > 0 {
		t.Errorf("live probe found %d address/pod mismatches against PR-head ConfigBuilder output", mismatches)
	}
}
