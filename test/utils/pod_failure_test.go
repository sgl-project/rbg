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

package utils

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

// Exercise the command boundary without needing a cluster: failure injection
// must target the suite's kubeconfig and preserve command arguments verbatim.
func TestPodFailureInjection(t *testing.T) {
	binDir := t.TempDir()
	argsFile := filepath.Join(binDir, "args")
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "kubectl"), []byte(`#!/bin/sh
printf '%s\n' "$@" > "$RBG_E2E_EXEC_ARGS_FILE"
if [ "$RBG_E2E_EXEC_FAIL" = "true" ]; then
    printf 'injected exec failure' >&2
    exit 1
fi
`), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RBG_E2E_EXEC_ARGS_FILE", argsFile)

	// Isolate the suite flag instead of depending on the developer's kubeconfig.
	originalFlags := flag.CommandLine
	flag.CommandLine = flag.NewFlagSet(t.Name(), flag.ContinueOnError)
	t.Cleanup(func() { flag.CommandLine = originalFlags })
	kubeconfig := filepath.Join(binDir, "config with spaces")
	flag.CommandLine.String(clientconfig.KubeconfigFlagName, kubeconfig, "")
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "test-ns", Name: "nginx-0"}}

	t.Run("restart nginx in the selected cluster", func(t *testing.T) {
		t.Setenv("RBG_E2E_EXEC_FAIL", "false")
		require.NoError(t, RestartNginxContainer(context.Background(), pod))
		args, err := os.ReadFile(argsFile)
		require.NoError(t, err)
		require.Equal(t, []string{
			"--kubeconfig", kubeconfig, "exec", "-n", "test-ns", "nginx-0", "-c", "nginx", "--", "nginx", "-s", "quit",
		}, strings.Split(strings.TrimSuffix(string(args), "\n"), "\n"))
	})

	t.Run("report failed injection with pod and command output", func(t *testing.T) {
		t.Setenv("RBG_E2E_EXEC_FAIL", "true")
		err := ExecInPod(context.Background(), pod, "worker", "sh", "-c", "touch /tmp/failure-marker")
		require.ErrorContains(t, err, "test-ns/nginx-0 container worker")
		require.ErrorContains(t, err, "injected exec failure")
		var exitErr *exec.ExitError
		require.True(t, errors.As(err, &exitErr), "preserve the command error")
		args, readErr := os.ReadFile(argsFile)
		require.NoError(t, readErr)
		require.Equal(t, []string{
			"--kubeconfig", kubeconfig, "exec", "-n", "test-ns", "nginx-0", "-c", "worker", "--", "sh", "-c", "touch /tmp/failure-marker",
		}, strings.Split(strings.TrimSuffix(string(args), "\n"), "\n"))
	})
}
