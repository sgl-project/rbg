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

package chat

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// portForwardSession manages a kubectl port-forward subprocess lifetime.
type portForwardSession struct {
	cmd      *exec.Cmd
	stopChan chan struct{}
	doneChan chan error
}

// startPortForward spawns a kubectl port-forward to the given pod and waits
// until the tunnel is ready (or returns an error). readyTimeout controls how
// long to wait for the "Forwarding from" confirmation line. The caller must
// call Stop() when the session is no longer needed.
func startPortForward(kubeconfig, namespace, podName string, localPort, remotePort int32, readyTimeout time.Duration) (*portForwardSession, error) {
	args := []string{
		"port-forward",
		"-n", namespace,
		podName,
		fmt.Sprintf("%d:%d", localPort, remotePort),
	}
	if kubeconfig != "" {
		args = append([]string{"--kubeconfig", kubeconfig}, args...)
	}

	cmd := exec.Command("kubectl", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("port-forward stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("port-forward stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("port-forward start: %w", err)
	}

	stopChan := make(chan struct{})
	doneChan := make(chan error, 1)
	readyChan := make(chan struct{})

	// Capture stderr so it can be included in error messages on failure.
	var (
		stderrMu  sync.Mutex
		stderrBuf strings.Builder
	)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			stderrMu.Lock()
			stderrBuf.WriteString(line)
			stderrBuf.WriteByte('\n')
			stderrMu.Unlock()
		}
	}()

	// Read stdout; signal readyChan when forwarding is confirmed.
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "Forwarding from") {
				select {
				case <-readyChan:
				default:
					close(readyChan)
				}
			}
		}
	}()

	// Stop goroutine: kill process when stopChan is closed.
	go func() {
		<-stopChan
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	// Wait goroutine: relay exit error to doneChan.
	go func() {
		doneChan <- cmd.Wait()
	}()

	// Wait for ready signal, process exit, or timeout.
	select {
	case <-readyChan:
		// Tunnel is up.
	case err := <-doneChan:
		if err == nil {
			err = fmt.Errorf("port-forward exited unexpectedly")
		}
		stderrMu.Lock()
		stderrOutput := strings.TrimSpace(stderrBuf.String())
		stderrMu.Unlock()
		if stderrOutput != "" {
			return nil, fmt.Errorf("port-forward failed before becoming ready: %w\nkubectl stderr: %s", err, stderrOutput)
		}
		return nil, fmt.Errorf("port-forward failed before becoming ready: %w", err)
	case <-time.After(readyTimeout):
		close(stopChan)
		return nil, fmt.Errorf("timeout waiting for port-forward to become ready")
	}

	return &portForwardSession{
		cmd:      cmd,
		stopChan: stopChan,
		doneChan: doneChan,
	}, nil
}

// Stop terminates the port-forward subprocess.
func (s *portForwardSession) Stop() {
	select {
	case <-s.stopChan:
		// Already stopped.
	default:
		close(s.stopChan)
	}
	<-s.doneChan
}
