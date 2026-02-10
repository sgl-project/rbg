package benchmark

import (
	"bufio"
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"

	"sigs.k8s.io/rbgs/cmd/cli/util"
)

// NewBenchmarkLogsCmd creates the "llm benchmark logs" command used to view logs of a benchmark Job.
func NewBenchmarkLogsCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	var (
		jobName string
		follow  bool
	)

	cmd := &cobra.Command{
		Use:   "logs <rbg-name>",
		Short: "View logs of a benchmark job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rbgName := args[0]
			return runBenchmarkLogs(cmd.Context(), cf, rbgName, jobName, follow)
		},
	}

	cmd.Flags().StringVar(&jobName, "job", "", "Name of the benchmark job to view logs for (required)")
	_ = cmd.MarkFlagRequired("job")
	cmd.Flags().BoolVarP(&follow, "follow", "f", true, "Follow log output in real time")

	return cmd
}

// runBenchmarkLogs streams the logs of a benchmark Job's Pod.
func runBenchmarkLogs(ctx context.Context, cf *genericclioptions.ConfigFlags, rbgName, jobName string, follow bool) error {
	if cf == nil {
		return fmt.Errorf("kubeconfig flags are not initialized")
	}

	ns := util.GetNamespace(cf)
	clientset, err := util.GetK8SClientSet(cf)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	// Verify the job belongs to the specified RBG
	job, err := clientset.BatchV1().Jobs(ns).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get job %s: %w", jobName, err)
	}
	if labelValue, ok := job.Labels[benchmarkLabelKey]; !ok || labelValue != rbgName {
		return fmt.Errorf("job %s does not belong to RBG %s", jobName, rbgName)
	}

	// Find the pod associated with the job
	podName, err := findJobPod(ctx, clientset, ns, jobName)
	if err != nil {
		return err
	}

	fmt.Printf("Streaming logs from pod %s (job: %s)...\n", podName, jobName)

	return streamPodLogsWithFollow(ctx, clientset, ns, podName, follow)
}

// findJobPod finds a pod associated with the given job.
func findJobPod(ctx context.Context, clientset *kubernetes.Clientset, namespace, jobName string) (string, error) {
	labelSelector := fmt.Sprintf("job-name=%s", jobName)
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list pods for job %s: %w", jobName, err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods found for job %s", jobName)
	}

	return pods.Items[0].Name, nil
}

// streamPodLogsWithFollow streams logs from a pod with the given follow flag.
func streamPodLogsWithFollow(ctx context.Context, clientset *kubernetes.Clientset, namespace, podName string, follow bool) error {
	req := clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Follow: follow,
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
