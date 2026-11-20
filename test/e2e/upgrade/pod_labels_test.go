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

package upgrade

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func TestExportReleaseChartWithoutCheckout(t *testing.T) {
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	cmd := exec.Command("git", "rev-parse", "--verify", "v0.8.0")
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		t.Skip("v0.8.0 tag is not available")
	}
	dir := t.TempDir()
	require.NoError(t, exportReleaseChart(root, "v0.8.0", dir))
	chart, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	require.NoError(t, err)
	cmd = exec.Command("git", "show", "v0.8.0:deploy/helm/rbgs/Chart.yaml")
	cmd.Dir = root
	expected, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, expected, chart)
	_, err = os.Stat(filepath.Join(dir, ".git"))
	require.True(t, os.IsNotExist(err))
	require.DirExists(t, filepath.Join(dir, "templates"))
	require.Error(t, exportReleaseChart(root, "nonexistent-kep284-test-tag", t.TempDir()))
}

func TestPodLabelDiffOnlyAllowsMarkerAddition(t *testing.T) {
	before := map[string]string{constants.GroupNameLabelKey: "group"}
	after := map[string]string{constants.GroupNameLabelKey: "group", constants.ManagedLabelKey: "true"}
	require.Empty(t, podLabelDiff(before, after))
	require.Equal(t, "true", after[constants.ManagedLabelKey])
	after[constants.GroupNameLabelKey] = "drift"
	require.NotEmpty(t, podLabelDiff(before, after))
	require.NotEmpty(t, podLabelDiff(map[string]string{constants.ManagedLabelKey: "false"}, map[string]string{constants.ManagedLabelKey: "true"}))
	require.NotEmpty(t, podLabelDiff(map[string]string{constants.ManagedLabelKey: "true"}, nil))
}
