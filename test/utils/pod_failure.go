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
	"flag"
	"fmt"
	"os/exec"

	corev1 "k8s.io/api/core/v1"
	clientconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
)

// RestartNginxContainer stops the fixture's nginx process gracefully. With the
// default Pod restart policy, kubelet restarts it and records a durable
// restartCount, unlike a patched Pod phase that kubelet can overwrite. Callers
// must observe the restart count or the resulting Pod replacement before
// asserting controller behavior.
func RestartNginxContainer(ctx context.Context, pod *corev1.Pod) error {
	return ExecInPod(ctx, pod, "nginx", "nginx", "-s", "quit")
}

// ExecInPod runs a command in a fixture container using the suite's kubeconfig.
// Failure injection must change the workload itself, not kubelet-owned status.
func ExecInPod(ctx context.Context, pod *corev1.Pod, containerName string, command ...string) error {
	kubeconfig := ""
	if kubeconfigFlag := flag.Lookup(clientconfig.KubeconfigFlagName); kubeconfigFlag != nil {
		kubeconfig = kubeconfigFlag.Value.String()
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "kubectl",
		kubectlExecArgs(kubeconfig, pod.Namespace, pod.Name, containerName, command...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("exec in pod %s/%s container %s: %w: %s", pod.Namespace, pod.Name, containerName, err, output)
	}
	return nil
}

func kubectlExecArgs(kubeconfig, namespace, podName, containerName string, command ...string) []string {
	args := make([]string, 0, 10+len(command))
	if kubeconfig != "" {
		args = append(args, "--kubeconfig", kubeconfig)
	}
	args = append(args, "exec", "-n", namespace, podName, "-c", containerName, "--")
	return append(args, command...)
}
