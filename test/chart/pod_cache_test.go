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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func TestPodCacheHelmControls(t *testing.T) {
	requireHelm(t)
	for _, tc := range []struct {
		name  string
		flags []string
		want  []string
	}{
		{name: "published image defaults"},
		{name: "explicit defaults", flags: []string{"--set", "controller.podCacheScope=all,controller.watchListClient=false"}},
		{name: "filtered", flags: []string{"--set", "controller.podCacheScope=filtered"}, want: []string{"--pod-cache-scope=filtered"}},
		{name: "watch list", flags: []string{"--set", "controller.watchListClient=true"}, want: []string{"--watch-list-client=true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := splitObjects(t, renderChart(t, tc.flags...))
			var deployment appsv1.Deployment
			require.NoError(t, yaml.Unmarshal([]byte(objs["Deployment/"+managerDeploymentName].raw), &deployment))
			require.Len(t, deployment.Spec.Template.Spec.Containers, 1)
			var flags []string
			for _, arg := range deployment.Spec.Template.Spec.Containers[0].Args {
				if strings.HasPrefix(arg, "--pod-cache-scope=") || strings.HasPrefix(arg, "--watch-list-client=") {
					flags = append(flags, arg)
				}
			}
			require.Equal(t, tc.want, flags)
		})
	}
	out, err := exec.Command("helm", "template", "rbgs", chartDir(t), "--set", "controller.podCacheScope=typo").CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(out), "controller.podCacheScope must be all or filtered")
	out, err = exec.Command("helm", "template", "rbgs", chartDir(t), "--set", "controller.watchListClient=typo").CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(out), "controller.watchListClient must be true or false")
}

func TestPodLabelMigrationDisabledByDefault(t *testing.T) {
	for _, scope := range []string{"all", "filtered"} {
		t.Run(scope, func(t *testing.T) {
			objs := splitObjects(t, renderChart(t, "--is-upgrade", "--set", "controller.podCacheScope="+scope))
			for key := range objs {
				require.NotContains(t, key, "pod-label-migration")
			}
		})
	}
}

func TestPodLabelMigrationHook(t *testing.T) {
	objs := splitObjects(t, renderChart(t, "--is-upgrade", "--namespace", "rbg-system", "--set",
		"podLabelMigration.enabled=true,controller.image.tag=test,controller.podSecurityContext.runAsNonRoot=true"))
	jobObject, ok := objs["Job/rbgs-pod-label-migration-1"]
	require.True(t, ok, "an enabled migration hook must render its Job")
	var job batchv1.Job
	require.NoError(t, yaml.Unmarshal([]byte(jobObject.raw), &job))
	require.Equal(t, "pre-upgrade", job.Annotations["helm.sh/hook"])
	require.Equal(t, "-2", job.Annotations["helm.sh/hook-weight"])
	require.Contains(t, job.Annotations["helm.sh/hook-delete-policy"], "before-hook-creation")
	require.NotNil(t, job.Spec.ActiveDeadlineSeconds)
	require.EqualValues(t, 300, *job.Spec.ActiveDeadlineSeconds)
	require.NotNil(t, job.Spec.TTLSecondsAfterFinished)
	require.EqualValues(t, 259200, *job.Spec.TTLSecondsAfterFinished)
	require.Contains(t, objs, "ServiceAccount/"+job.Spec.Template.Spec.ServiceAccountName)
	require.Contains(t, objs, "ClusterRoleBinding/rbgs-pod-label-migration-rbg-system")
	var role rbacv1.ClusterRole
	require.NoError(t, yaml.Unmarshal([]byte(objs["ClusterRole/rbgs-pod-label-migration-rbg-system"].raw), &role))
	require.Equal(t, []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "patch"}},
		{APIGroups: []string{"apps"}, Resources: []string{"deployments", "statefulsets"}, Verbs: []string{"list"}},
		{APIGroups: []string{"leaderworkerset.x-k8s.io"}, Resources: []string{"leaderworkersets"}, Verbs: []string{"list"}},
	}, role.Rules)
	for _, key := range []string{
		"ServiceAccount/rbgs-pod-label-migration",
		"ClusterRole/rbgs-pod-label-migration-rbg-system",
		"ClusterRoleBinding/rbgs-pod-label-migration-rbg-system",
	} {
		obj, ok := objs[key]
		require.True(t, ok, "%s is required before the hook runs", key)
		var metadata struct {
			Metadata metav1.ObjectMeta `json:"metadata"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(obj.raw), &metadata))
		require.Equal(t, "pre-upgrade", metadata.Metadata.Annotations["helm.sh/hook"])
		require.Equal(t, "-3", metadata.Metadata.Annotations["helm.sh/hook-weight"])
		require.Contains(t, metadata.Metadata.Annotations["helm.sh/hook-delete-policy"], "hook-succeeded")
	}
	require.True(t, *job.Spec.Template.Spec.SecurityContext.RunAsNonRoot)
	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	container := job.Spec.Template.Spec.Containers[0]
	require.Equal(t, "rolebasedgroup/rbgs-controller:test", container.Image)
	require.Contains(t, container.Args[0], "/kubectl-rbg migrate pod-labels -A")
	require.Contains(t, container.Args[0], "/kubectl-rbg migrate pod-labels check -A")

	withoutHooks := splitObjects(t, renderChart(t, "--is-upgrade", "--no-hooks", "--set", "podLabelMigration.enabled=true"))
	for key := range withoutHooks {
		require.NotContains(t, key, "pod-label-migration")
	}
}

func TestPodLabelMigrationHookReportsCommandFailure(t *testing.T) {
	objs := splitObjects(t, renderChart(t, "--is-upgrade", "--set", "podLabelMigration.enabled=true"))
	jobObject, ok := objs["Job/rbgs-pod-label-migration-1"]
	require.True(t, ok)
	var job batchv1.Job
	require.NoError(t, yaml.Unmarshal([]byte(jobObject.raw), &job))
	require.Len(t, job.Spec.Template.Spec.Containers, 1)

	dir := t.TempDir()
	tool := filepath.Join(dir, "kubectl-rbg")
	calls := filepath.Join(dir, "calls")
	require.NoError(t, os.WriteFile(tool, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$MIGRATION_TEST_CALLS\"\nexit 42\n"), 0o755))
	quotedTool := "'" + strings.ReplaceAll(tool, "'", "'\\''") + "'"
	script := strings.ReplaceAll(job.Spec.Template.Spec.Containers[0].Args[0], "/kubectl-rbg", quotedTool)
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(), "MIGRATION_TEST_CALLS="+calls, "MIGRATION_RATE_LIMIT=10", "MIGRATION_TIMEOUT=2m")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "a migration command failure should not fail the hook: %s", out)
	require.Contains(t, string(out), "WARNING")
	data, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Len(t, strings.Split(strings.TrimSpace(string(data)), "\n"), 2)
}
