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
	"strconv"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
)

// managerGracefulShutdownSeconds is controller-runtime's default
// GracefulShutdownTimeout, which cmd/rbgs does not override.
const managerGracefulShutdownSeconds = 30

// assertManagerDrainsBeforeShutdown checks that the manager container sleeps in a
// preStop hook and that the Pod's grace period covers the sleep plus the manager's
// graceful shutdown. On SIGTERM the manager stops the webhook server at once, but the
// API server keeps routing admission requests to the Pod until the webhook Service's
// endpoints drop it; every RBG webhook has failurePolicy: Fail, so those requests are
// rejected. The sleep keeps the webhook server up for that window. It uses the exec
// form because the native sleep action needs Kubernetes 1.30 and RBG supports 1.28.
func assertManagerDrainsBeforeShutdown(t *testing.T, deploy appsv1.Deployment) {
	t.Helper()
	spec := deploy.Spec.Template.Spec
	if len(spec.Containers) != 1 {
		t.Fatalf("expected one manager container, got %d", len(spec.Containers))
	}
	c := spec.Containers[0]

	if c.Lifecycle == nil || c.Lifecycle.PreStop == nil || c.Lifecycle.PreStop.Exec == nil {
		t.Fatalf("manager container: want an exec preStop hook, got lifecycle %+v", c.Lifecycle)
	}
	cmd := c.Lifecycle.PreStop.Exec.Command
	if len(cmd) != 2 || cmd[0] != "/bin/sleep" {
		t.Fatalf("preStop command = %q, want [/bin/sleep <seconds>]", cmd)
	}
	sleep, err := strconv.Atoi(cmd[1])
	if err != nil || sleep <= 0 {
		t.Fatalf("preStop sleep = %q, want a positive number of seconds", cmd[1])
	}

	if spec.TerminationGracePeriodSeconds == nil {
		t.Fatalf("terminationGracePeriodSeconds is unset (default 30s), want at least %ds",
			sleep+managerGracefulShutdownSeconds)
	}
	if got, want := *spec.TerminationGracePeriodSeconds, int64(sleep+managerGracefulShutdownSeconds); got < want {
		t.Errorf("terminationGracePeriodSeconds = %d, want at least %d (%ds preStop sleep + %ds graceful shutdown)",
			got, want, sleep, managerGracefulShutdownSeconds)
	}
}

// TestChartManagerDrainsBeforeShutdown covers the Helm install path.
func TestChartManagerDrainsBeforeShutdown(t *testing.T) {
	assertManagerDrainsBeforeShutdown(t, deploymentFrom(t, renderChart(t), managerDeploymentName))
}

// TestKustomizeManagerDrainsBeforeShutdown covers the kustomize base that
// deploy/kubectl/manifests.yaml is built from.
func TestKustomizeManagerDrainsBeforeShutdown(t *testing.T) {
	path := filepath.Join(chartDir(t), "..", "..", "..", "config", "manager", "manager.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	assertManagerDrainsBeforeShutdown(t, deploymentFrom(t, string(raw), "controller-manager"))
}
