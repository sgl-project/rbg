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
)

// TestRenderChartIgnoresHelmWarnings puts a helm wrapper first on PATH that prints
// the warning helm 3 writes to stderr for a group-readable kubeconfig, then runs the
// real helm. renderChart must still return a manifest stream that parses.
func TestRenderChartIgnoresHelmWarnings(t *testing.T) {
	requireHelm(t)
	helm, err := exec.LookPath("helm")
	require.NoError(t, err)

	dir := t.TempDir()
	quotedHelm := "'" + strings.ReplaceAll(helm, "'", "'\\''") + "'"
	wrapper := "#!/bin/sh\n" +
		"echo 'WARNING: Kubernetes configuration file is group-readable. This is insecure. Location: /tmp/kubeconfig' >&2\n" +
		"exec " + quotedHelm + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "helm"), []byte(wrapper), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	objs := splitObjects(t, renderChart(t))
	require.Contains(t, objs, "Deployment/"+managerDeploymentName)
}
