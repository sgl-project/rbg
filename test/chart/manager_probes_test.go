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
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

const healthProbeFlag = "--health-probe-bind-address"

// assertManagerProbes checks that the manager container has liveness and readiness
// probes on the controller's /healthz and /readyz endpoints, at the port given by
// --health-probe-bind-address. Without a readiness probe a new manager Pod counts as
// ready as soon as its container starts, so a rollout can replace every replica before
// any new one serves the webhooks, and all RBG admission requests fail meanwhile.
func assertManagerProbes(t *testing.T, deploy appsv1.Deployment) {
	t.Helper()
	if len(deploy.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected one manager container, got %d", len(deploy.Spec.Template.Spec.Containers))
	}
	c := deploy.Spec.Template.Spec.Containers[0]

	var bindAddr string
	for _, arg := range c.Args {
		if strings.HasPrefix(arg, healthProbeFlag+"=") {
			bindAddr = strings.TrimPrefix(arg, healthProbeFlag+"=")
		}
	}
	if bindAddr == "" {
		t.Fatalf("manager container has no %s argument", healthProbeFlag)
	}
	port := bindAddr[strings.LastIndex(bindAddr, ":")+1:]

	for _, tc := range []struct {
		name  string
		probe *corev1.Probe
		path  string
	}{
		{"livenessProbe", c.LivenessProbe, "/healthz"},
		{"readinessProbe", c.ReadinessProbe, "/readyz"},
	} {
		if tc.probe == nil || tc.probe.HTTPGet == nil {
			t.Errorf("%s: want an httpGet probe on %s, got %+v", tc.name, tc.path, tc.probe)
			continue
		}
		if tc.probe.HTTPGet.Path != tc.path {
			t.Errorf("%s: path = %q, want %q", tc.name, tc.probe.HTTPGet.Path, tc.path)
		}
		if got := tc.probe.HTTPGet.Port.String(); got != port {
			t.Errorf("%s: port = %s, want %s to match %s=%s", tc.name, got, port, healthProbeFlag, bindAddr)
		}
	}
}

func deploymentFrom(t *testing.T, manifest, name string) appsv1.Deployment {
	t.Helper()
	obj, ok := splitObjects(t, manifest)["Deployment/"+name]
	if !ok {
		t.Fatalf("Deployment %q not found", name)
	}
	var deploy appsv1.Deployment
	if err := yaml.Unmarshal([]byte(obj.raw), &deploy); err != nil {
		t.Fatalf("unmarshal Deployment: %v", err)
	}
	return deploy
}

// TestChartManagerHasHealthProbes covers the Helm install path.
func TestChartManagerHasHealthProbes(t *testing.T) {
	assertManagerProbes(t, deploymentFrom(t, renderChart(t), managerDeploymentName))
}

// TestKustomizeManagerHasHealthProbes covers the kustomize base that
// deploy/kubectl/manifests.yaml is built from, so the two install paths stay in step.
func TestKustomizeManagerHasHealthProbes(t *testing.T) {
	path := filepath.Join(chartDir(t), "..", "..", "..", "config", "manager", "manager.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	assertManagerProbes(t, deploymentFrom(t, string(raw), "controller-manager"))
}
