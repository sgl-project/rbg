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

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

const chartVersionMarkerKey = "ConfigMap/rbgs-chart-version"

// renderUpgradeFailure renders the chart with Helm's upgrade release context and
// returns the expected template error.
func renderUpgradeFailure(t *testing.T, extraArgs ...string) string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("helm is required in CI: %v", err)
		}
		t.Skip("helm not installed; skipping chart render test")
	}
	args := append([]string{"template", "rbgs", chartDir(t), "--is-upgrade"}, extraArgs...)
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err == nil {
		t.Fatal("helm template unexpectedly accepted an upgrade without a version marker")
	}
	return string(out)
}

func chartVersion(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(chartDir(t), "Chart.yaml"))
	if err != nil {
		t.Fatalf("read Chart.yaml: %v", err)
	}
	var chart struct {
		Version string `json:"version"`
	}
	if err := yaml.Unmarshal(contents, &chart); err != nil {
		t.Fatalf("parse Chart.yaml: %v", err)
	}
	if chart.Version == "" {
		t.Fatal("Chart.yaml has no version")
	}
	return chart.Version
}

func chartVersionMarker(t *testing.T, manifest string) corev1.ConfigMap {
	t.Helper()
	obj, ok := splitObjects(t, manifest)[chartVersionMarkerKey]
	if !ok {
		t.Fatalf("%s not rendered", chartVersionMarkerKey)
	}
	var marker corev1.ConfigMap
	if err := yaml.Unmarshal([]byte(obj.raw), &marker); err != nil {
		t.Fatalf("unmarshal version marker: %v", err)
	}
	return marker
}

// TestChartVersionMarkerRecordsSuccessfulReleaseVersion verifies the bridge artifact
// that later chart versions use to determine the source version of an upgrade. It is a
// post hook rather than a regular manifest so a failed upgrade keeps the last
// successfully recorded version.
func TestChartVersionMarkerRecordsSuccessfulReleaseVersion(t *testing.T) {
	marker := chartVersionMarker(t, renderChart(t))

	if got, want := marker.Name, "rbgs-chart-version"; got != want {
		t.Errorf("marker name = %q, want %q", got, want)
	}
	if got, want := marker.Data["chartVersion"], chartVersion(t); got != want {
		t.Errorf("marker chartVersion = %q, want chart version %q", got, want)
	}
	if got, want := marker.Annotations["helm.sh/hook"], "post-install,post-upgrade"; got != want {
		t.Errorf("marker hook = %q, want %q", got, want)
	}
	if got, want := marker.Annotations["helm.sh/hook-delete-policy"], "before-hook-creation"; got != want {
		t.Errorf("marker hook delete policy = %q, want %q", got, want)
	}
}

// TestUpgradeWithoutVersionMarkerFails makes the strict source-version contract explicit:
// every upgrade must read the ConfigMap that an earlier successful install or upgrade wrote.
func TestUpgradeWithoutVersionMarkerFails(t *testing.T) {
	output := renderUpgradeFailure(t)
	if want := "ConfigMap default/rbgs-chart-version is missing"; !strings.Contains(output, want) {
		t.Fatalf("upgrade failure does not name the missing marker %q:\n%s", want, output)
	}
}
