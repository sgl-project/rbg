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
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

const lifecycleHooksFlag = "--feature-role-instance-lifecycle-hooks"

func TestRoleInstanceLifecycleHooksChartToggle(t *testing.T) {
	disabled := renderChart(t)
	if got := lifecycleHooksManagerFlag(t, disabled); got != "false" {
		t.Errorf("default lifecycle flag = %q, want false", got)
	}
	enabled := renderChart(t, "--set", "controller.features.roleInstanceLifecycleHooks.enabled=true")
	if got := lifecycleHooksManagerFlag(t, enabled); got != "true" {
		t.Errorf("enabled lifecycle flag = %q, want true", got)
	}
}

func lifecycleHooksManagerFlag(t *testing.T, manifest string) string {
	t.Helper()
	obj, ok := splitObjects(t, manifest)["Deployment/"+managerDeploymentName]
	if !ok {
		t.Fatalf("Deployment %q not found in rendered chart", managerDeploymentName)
	}
	var deploy appsv1.Deployment
	if err := yaml.Unmarshal([]byte(obj.raw), &deploy); err != nil {
		t.Fatalf("unmarshal Deployment: %v", err)
	}
	for _, container := range deploy.Spec.Template.Spec.Containers {
		for _, arg := range container.Args {
			if strings.HasPrefix(arg, lifecycleHooksFlag+"=") {
				return strings.TrimPrefix(arg, lifecycleHooksFlag+"=")
			}
		}
	}
	return ""
}
