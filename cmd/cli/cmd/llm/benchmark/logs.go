package benchmark

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// streamJobLogs waits for the job's pod to be created and streams its logs to stdout.
// It returns the final job state after the pod terminates.
func streamJobLogs(ctx context.Context, clientset *kubernetes.Clientset, namespace, jobName string) (JobState, *batchv1.Job, error) {
	// Wait for the pod to be created
	podName, err := waitForJobPod(ctx, clientset, namespace, jobName)
	if err != nil {
		return "", nil, err
	}

	fmt.Printf("Streaming logs from pod %s...\n", podName)

	// Stream logs from the pod
	if err := streamPodLogs(ctx, clientset, namespace, podName); err != nil {
		// Log streaming error is not fatal, we still want to get the final job state
		fmt.Printf("Warning: failed to stream logs: %v\n", err)
	}

	// Get final job state
	job, err := clientset.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("failed to get job %s: %w", jobName, err)
	}

	// Wait a bit more if job is still running (pod may have just terminated)
	for i := 0; i < 10; i++ {
		state := deriveJobState(job)
		if state == JobStateSucceeded || state == JobStateFailed {
			return state, job, nil
		}
		time.Sleep(time.Second)
		job, err = clientset.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			return "", nil, fmt.Errorf("failed to get job %s: %w", jobName, err)
		}
	}

	return deriveJobState(job), job, nil
}

// waitForJobPod waits for a pod associated with the job to be created and returns its name.
func waitForJobPod(ctx context.Context, clientset *kubernetes.Clientset, namespace, jobName string) (string, error) {
	deadline := time.Now().Add(5 * time.Minute)
	labelSelector := fmt.Sprintf("job-name=%s", jobName)

	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timeout waiting for pod to be created for job %s", jobName)
		}

		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			return "", fmt.Errorf("failed to list pods for job %s: %w", jobName, err)
		}

		for _, pod := range pods.Items {
			// Return once we find a pod that is at least pending
			if pod.Status.Phase != "" {
				return pod.Name, nil
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// streamPodLogs streams the logs from a pod to stdout.
func streamPodLogs(ctx context.Context, clientset *kubernetes.Clientset, namespace, podName string) error {
	// Wait for pod to be running or terminated (both are fine for log streaming)
	if err := waitForPodReady(ctx, clientset, namespace, podName, false); err != nil {
		return err
	}

	req := clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Follow: true,
	})

	stream, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("failed to open log stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	reader := bufio.NewReader(stream)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			fmt.Print(line)
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("error reading log stream: %w", err)
		}
	}
}

// waitForPodReady waits for a pod to be in a state where it can be used.
// If requireRunning is true, the pod must reach Running state (used by dashboard).
// If requireRunning is false, terminated pods (Succeeded/Failed) are also acceptable (used by logs).
func waitForPodReady(ctx context.Context, clientset *kubernetes.Clientset, namespace, podName string, requireRunning bool) error {
	deadline := time.Now().Add(5 * time.Minute)

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for pod %s to be ready", podName)
		}

		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get pod %s: %w", podName, err)
		}

		// Pod is terminated
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			if requireRunning {
				return fmt.Errorf("pod %s is in %s phase, not running", podName, pod.Status.Phase)
			}
			// For log streaming, terminated pods are fine
			return nil
		}

		if pod.Status.Phase == corev1.PodRunning {
			// Check if Pod is ready via conditions
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					return nil
				}
			}
		}

		// Check if container is waiting due to image pull or other issues
		if pod.Status.Phase == corev1.PodPending {
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
					fmt.Printf("Pod %s is waiting: %s\n", podName, cs.State.Waiting.Reason)
				}
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}
