package benchmark

import (
	"bufio"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrPortInUse indicates that the local port is already in use.
var ErrPortInUse = errors.New("port already in use")

// PortForwardResult holds the result of port-forward attempt.
type PortForwardResult struct {
	Err error
}

// startPortForward establishes a port-forward connection to a Pod using kubectl.
// It sends the result (success or error) to resultChan.
func startPortForward(kubeconfig, namespace, podName string, localPort, remotePort int, stopChan, readyChan chan struct{}, resultChan chan PortForwardResult) {
	// Build kubectl port-forward command
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

	// Capture stdout to detect when port-forward is ready
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		resultChan <- PortForwardResult{Err: fmt.Errorf("failed to get stdout pipe: %w", err)}
		return
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		resultChan <- PortForwardResult{Err: fmt.Errorf("failed to get stderr pipe: %w", err)}
		return
	}

	if err := cmd.Start(); err != nil {
		resultChan <- PortForwardResult{Err: fmt.Errorf("failed to start port-forward: %w", err)}
		return
	}

	// Collect stderr output for error detection
	var stderrOutput strings.Builder
	stderrDone := make(chan struct{})

	// Read stderr for error messages
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			stderrOutput.WriteString(line + "\n")
			fmt.Printf("kubectl: %s\n", line)
		}
	}()

	// Read stdout to detect when forwarding is established
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			// kubectl outputs "Forwarding from 127.0.0.1:PORT -> PORT" when ready
			if strings.Contains(line, "Forwarding from") {
				select {
				case <-readyChan:
					// Already closed
				default:
					close(readyChan)
				}
			}
			fmt.Printf("kubectl: %s\n", line)
		}
	}()

	// Wait for stop signal
	go func() {
		<-stopChan
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	// Wait for the command to finish
	err = cmd.Wait()

	// Wait for stderr to be fully read
	<-stderrDone

	resultChan <- PortForwardResult{Err: err}
}
