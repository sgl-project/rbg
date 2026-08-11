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

// Package chart renders the rbgs Helm chart with `helm template` and asserts that
// the controller.deprecatedWorkloadTypes.enabled toggle changes exactly what it is
// meant to and nothing else.
//
// This complements the pure-function tests in hack/gen-helm-rbac (which never render
// the chart) and the live e2e suite in test/e2e/apicompat (which needs a cluster).
// It runs in seconds with no cluster, so it is the fast guard against a chart edit
// that over-broadens the toggle — e.g. gating a rule that should always be granted,
// or letting the RBAC and the manager flag disagree.
package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

const (
	controllerClusterRoleName = "rbgs-controller-role"
	managerDeploymentName     = "rbgs-controller-manager"
	deprecatedFlag            = "--enable-deprecated-workload-types"
)

// deprecatedGrants is the exact set of "apiGroup/resource" grants that the toggle
// gates. Keep it in sync with the deprecated workload types the validator switches on
// (api/workloads/constants: Deployment, StatefulSet, LeaderWorkerSet) and their
// status/finalizers subresources. When enabled these must be present in the
// ClusterRole; when disabled none of them may be.
var deprecatedGrants = map[string]bool{
	"apps/deployments":                                 true,
	"apps/statefulsets":                                true,
	"apps/deployments/status":                          true,
	"apps/statefulsets/status":                         true,
	"apps/deployments/finalizers":                      true,
	"apps/statefulsets/finalizers":                     true,
	"leaderworkerset.x-k8s.io/leaderworkersets":        true,
	"leaderworkerset.x-k8s.io/leaderworkersets/status": true,
}

// docSeparator splits a multi-document YAML stream on lines containing only "---".
var docSeparator = regexp.MustCompile(`(?m)^---\s*$`)

func chartDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller path")
	}
	// this file: <repo>/test/chart/deprecated_workload_types_test.go
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	dir := filepath.Join(repoRoot, "deploy", "helm", "rbgs")
	if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err != nil {
		t.Fatalf("chart not found at %s: %v", dir, err)
	}
	return dir
}

// renderChart runs `helm template` with the given extra args and returns the rendered
// manifest stream. It skips the test if helm is not installed, so local runs without
// helm do not fail; CI installs helm for the job that runs this package.
func renderChart(t *testing.T, extraArgs ...string) string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not installed; skipping chart render test")
	}
	args := append([]string{"template", "rbgs", chartDir(t)}, extraArgs...)
	cmd := exec.Command("helm", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return string(out)
}

// renderEnabled / renderUnset / renderDisabled cover the three value states the chart
// templates branch on. "unset" (map present, no enabled key) is the case the
// hasKey-based helm logic is most likely to get wrong, so it is exercised explicitly.
func renderEnabled(t *testing.T) string {
	return renderChart(t, "--set", "controller.deprecatedWorkloadTypes.enabled=true")
}

func renderUnset(t *testing.T) string {
	return renderChart(t, "--set-json", "controller.deprecatedWorkloadTypes={}")
}

func renderDisabled(t *testing.T) string {
	return renderChart(t, "--set", "controller.deprecatedWorkloadTypes.enabled=false")
}

type k8sObject struct {
	kind string
	name string
	raw  string
}

func splitObjects(t *testing.T, manifest string) map[string]k8sObject {
	t.Helper()
	objs := map[string]k8sObject{}
	for _, chunk := range docSeparator.Split(manifest, -1) {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		var meta struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(chunk), &meta); err != nil {
			t.Fatalf("parse rendered doc: %v\n%s", err, chunk)
		}
		if meta.Kind == "" {
			continue
		}
		key := meta.Kind + "/" + meta.Metadata.Name
		// Normalize by round-tripping through the generic decoder so formatting
		// differences (key order, indentation) do not show up as spurious deltas.
		var generic interface{}
		if err := yaml.Unmarshal([]byte(chunk), &generic); err != nil {
			t.Fatalf("normalize rendered doc: %v", err)
		}
		norm, err := yaml.Marshal(generic)
		if err != nil {
			t.Fatalf("re-marshal rendered doc: %v", err)
		}
		objs[key] = k8sObject{kind: meta.Kind, name: meta.Metadata.Name, raw: string(norm)}
	}
	return objs
}

// grantSet returns the "apiGroup/resource" grants in the controller ClusterRole found
// in the rendered manifest.
func grantSet(t *testing.T, manifest string) map[string]bool {
	t.Helper()
	objs := splitObjects(t, manifest)
	obj, ok := objs["ClusterRole/"+controllerClusterRoleName]
	if !ok {
		t.Fatalf("ClusterRole %q not found in rendered chart", controllerClusterRoleName)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal([]byte(obj.raw), &role); err != nil {
		t.Fatalf("unmarshal ClusterRole: %v", err)
	}
	grants := map[string]bool{}
	for _, rule := range role.Rules {
		for _, group := range rule.APIGroups {
			for _, res := range rule.Resources {
				grants[group+"/"+res] = true
			}
		}
	}
	return grants
}

// managerFlag returns the value of --enable-deprecated-workload-types on the manager
// container, or "" if the flag is absent.
func managerFlag(t *testing.T, manifest string) string {
	t.Helper()
	objs := splitObjects(t, manifest)
	obj, ok := objs["Deployment/"+managerDeploymentName]
	if !ok {
		t.Fatalf("Deployment %q not found in rendered chart", managerDeploymentName)
	}
	var deploy appsv1.Deployment
	if err := yaml.Unmarshal([]byte(obj.raw), &deploy); err != nil {
		t.Fatalf("unmarshal Deployment: %v", err)
	}
	for _, c := range deploy.Spec.Template.Spec.Containers {
		for _, arg := range c.Args {
			if strings.HasPrefix(arg, deprecatedFlag+"=") {
				return strings.TrimPrefix(arg, deprecatedFlag+"=")
			}
		}
	}
	return ""
}

// TestManagerFlagReflectsToggle pins the manager's --enable-deprecated-workload-types
// flag to the toggle. An unset toggle must render as enabled, matching values.yaml.
func TestManagerFlagReflectsToggle(t *testing.T) {
	if got := managerFlag(t, renderEnabled(t)); got != "true" {
		t.Errorf("enabled: manager flag = %q, want true", got)
	}
	if got := managerFlag(t, renderUnset(t)); got != "true" {
		t.Errorf("unset: manager flag = %q, want true (unset counts as enabled)", got)
	}
	if got := managerFlag(t, renderDisabled(t)); got != "false" {
		t.Errorf("disabled: manager flag = %q, want false", got)
	}
}

// TestClusterRoleGrantsReflectToggle asserts the deprecated-workload RBAC is present
// when enabled/unset and fully absent when disabled.
func TestClusterRoleGrantsReflectToggle(t *testing.T) {
	for _, tc := range []struct {
		name     string
		manifest string
		want     bool
	}{
		{"enabled", renderEnabled(t), true},
		{"unset", renderUnset(t), true},
		{"disabled", renderDisabled(t), false},
	} {
		grants := grantSet(t, tc.manifest)
		for grant := range deprecatedGrants {
			if got := grants[grant]; got != tc.want {
				t.Errorf("%s: grant %q present=%v, want %v", tc.name, grant, got, tc.want)
			}
		}
	}
}

// TestToggleDeltaIsScopedToDeprecatedRBACAndFlag renders the chart with the toggle on
// and off and asserts the two renders differ in exactly two objects — the controller
// ClusterRole and the manager Deployment — and in no others. This is the guard the
// gen-helm-rbac unit tests cannot give: it catches a chart edit that flips or removes
// something else along with the toggle.
func TestToggleDeltaIsScopedToDeprecatedRBACAndFlag(t *testing.T) {
	enabled := splitObjects(t, renderEnabled(t))
	disabled := splitObjects(t, renderDisabled(t))

	// No object may be added or removed by the toggle.
	if added := keysNotIn(disabled, enabled); len(added) > 0 {
		t.Errorf("disabling the toggle added objects: %v", added)
	}
	if removed := keysNotIn(enabled, disabled); len(removed) > 0 {
		t.Errorf("disabling the toggle removed objects: %v", removed)
	}

	var differing []string
	for key, e := range enabled {
		d, ok := disabled[key]
		if !ok {
			continue
		}
		if e.raw != d.raw {
			differing = append(differing, key)
		}
	}
	sort.Strings(differing)

	want := []string{
		"ClusterRole/" + controllerClusterRoleName,
		"Deployment/" + managerDeploymentName,
	}
	sort.Strings(want)
	if strings.Join(differing, ",") != strings.Join(want, ",") {
		t.Errorf("toggle changed objects %v, want exactly %v", differing, want)
	}
}

func keysNotIn(a, b map[string]k8sObject) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
