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

package chart

import (
	"os"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

// The admission webhooks use failurePolicy: Fail, so RBG writes fail while no manager
// pod is Ready. These tests check that both install paths ship a PodDisruptionBudget
// that lets a drain evict one manager pod at a time, and a spread constraint that
// prefers a different node for each replica.

func selectsPods(t *testing.T, sel *metav1.LabelSelector, podLabels map[string]string) bool {
	t.Helper()
	if sel == nil {
		return false
	}
	s, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		t.Fatalf("invalid label selector %v: %v", sel, err)
	}
	// An empty selector matches every pod in the namespace.
	return !s.Empty() && s.Matches(labels.Set(podLabels))
}

func pdbFrom(t *testing.T, manifest, name string) (policyv1.PodDisruptionBudget, bool) {
	t.Helper()
	obj, ok := splitObjects(t, manifest)["PodDisruptionBudget/"+name]
	if !ok {
		return policyv1.PodDisruptionBudget{}, false
	}
	var pdb policyv1.PodDisruptionBudget
	if err := yaml.Unmarshal([]byte(obj.raw), &pdb); err != nil {
		t.Fatalf("unmarshal PodDisruptionBudget: %v", err)
	}
	return pdb, true
}

// assertManagerPDB checks for a PodDisruptionBudget that selects the manager pods with
// maxUnavailable: 1. minAvailable: 1 would block draining a single-replica install, and
// without AlwaysAllow a drain cannot evict manager pods that are all failing readiness.
func assertManagerPDB(t *testing.T, manifest string, deploy appsv1.Deployment) {
	t.Helper()
	pdb, ok := pdbFrom(t, manifest, managerDeploymentName)
	if !ok {
		t.Fatalf("PodDisruptionBudget %q not found", managerDeploymentName)
	}
	if pdb.Namespace != deploy.Namespace {
		t.Errorf("PodDisruptionBudget namespace = %q, want the Deployment's %q", pdb.Namespace, deploy.Namespace)
	}
	if pdb.Spec.MinAvailable != nil {
		t.Errorf("minAvailable = %s, want unset", pdb.Spec.MinAvailable)
	}
	if got := pdb.Spec.MaxUnavailable; got == nil || got.String() != "1" {
		t.Errorf("maxUnavailable = %v, want 1", got)
	}
	if got := ptr.Deref(pdb.Spec.UnhealthyPodEvictionPolicy, ""); got != policyv1.AlwaysAllow {
		t.Errorf("unhealthyPodEvictionPolicy = %q, want %s", got, policyv1.AlwaysAllow)
	}
	if !selectsPods(t, pdb.Spec.Selector, deploy.Spec.Template.Labels) {
		t.Errorf("selector %v does not select the manager pod labels %v", pdb.Spec.Selector, deploy.Spec.Template.Labels)
	}
}

// assertManagerSpread checks for a kubernetes.io/hostname spread constraint that
// selects the manager pods and never blocks scheduling.
func assertManagerSpread(t *testing.T, deploy appsv1.Deployment) {
	t.Helper()
	constraints := deploy.Spec.Template.Spec.TopologySpreadConstraints
	for _, c := range constraints {
		if c.TopologyKey != corev1.LabelHostname {
			continue
		}
		if c.MaxSkew != 1 {
			t.Errorf("maxSkew = %d, want 1", c.MaxSkew)
		}
		if c.WhenUnsatisfiable != corev1.ScheduleAnyway {
			t.Errorf("whenUnsatisfiable = %s, want %s", c.WhenUnsatisfiable, corev1.ScheduleAnyway)
		}
		if !selectsPods(t, c.LabelSelector, deploy.Spec.Template.Labels) {
			t.Errorf("labelSelector %v does not select the manager pod labels %v", c.LabelSelector, deploy.Spec.Template.Labels)
		}
		return
	}
	t.Errorf("no %s topology spread constraint, got %+v", corev1.LabelHostname, constraints)
}

// TestChartManagerDisruptionDefaults covers the Helm install path with default values.
func TestChartManagerDisruptionDefaults(t *testing.T) {
	manifest := renderChart(t)
	deploy := deploymentFrom(t, manifest, managerDeploymentName)
	assertManagerPDB(t, manifest, deploy)
	assertManagerSpread(t, deploy)
	if a := deploy.Spec.Template.Spec.Affinity; a != nil {
		t.Errorf("affinity = %+v, want unset by default", a)
	}
}

// TestChartManagerDisruptionOverrides checks that controller.podDisruptionBudget,
// controller.topologySpreadConstraints and controller.affinity can turn the defaults
// off or replace them.
func TestChartManagerDisruptionOverrides(t *testing.T) {
	off := renderChart(t,
		"--set", "controller.podDisruptionBudget.enabled=false",
		"--set-json", "controller.topologySpreadConstraints=[]",
	)
	if _, ok := pdbFrom(t, off, managerDeploymentName); ok {
		t.Error("PodDisruptionBudget rendered with controller.podDisruptionBudget.enabled=false")
	}
	offSpec := deploymentFrom(t, off, managerDeploymentName).Spec.Template.Spec
	if got := offSpec.TopologySpreadConstraints; len(got) != 0 {
		t.Errorf("topologySpreadConstraints = %+v, want none when set to []", got)
	}

	replaced := renderChart(t,
		"--set-json", `controller.topologySpreadConstraints=[{"maxSkew":1,"topologyKey":"topology.kubernetes.io/zone",`+
			`"whenUnsatisfiable":"DoNotSchedule","labelSelector":{"matchLabels":{"control-plane":"rbgs-controller"}}}]`,
		"--set-json", `controller.affinity={"nodeAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":`+
			`{"nodeSelectorTerms":[{"matchExpressions":[{"key":"node-role.kubernetes.io/control-plane","operator":"DoesNotExist"}]}]}}}`,
	)
	spec := deploymentFrom(t, replaced, managerDeploymentName).Spec.Template.Spec
	if got := spec.TopologySpreadConstraints; len(got) != 1 ||
		got[0].TopologyKey != corev1.LabelTopologyZone || got[0].WhenUnsatisfiable != corev1.DoNotSchedule {
		t.Errorf("topologySpreadConstraints = %+v, want only the zone constraint from values", got)
	}
	if spec.Affinity == nil || spec.Affinity.NodeAffinity == nil ||
		spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Errorf("affinity = %+v, want the nodeAffinity from values", spec.Affinity)
	}
}

// TestKubectlManifestsManagerDisruption covers deploy/kubectl/manifests.yaml. It is
// built from config/default, and Project Check fails when the two drift, so this also
// covers config/manager.
func TestKubectlManifestsManagerDisruption(t *testing.T) {
	path := filepath.Join(chartDir(t), "..", "..", "kubectl", "manifests.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	manifest := string(raw)
	deploy := deploymentFrom(t, manifest, managerDeploymentName)
	assertManagerPDB(t, manifest, deploy)
	assertManagerSpread(t, deploy)
}
