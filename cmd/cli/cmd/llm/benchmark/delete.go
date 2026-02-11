package benchmark

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"sigs.k8s.io/rbgs/cmd/cli/util"
)

// NewBenchmarkDeleteCmd creates the "llm benchmark delete" command used to delete a benchmark Job.
func NewBenchmarkDeleteCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	var jobName string

	cmd := &cobra.Command{
		Use:   "delete <rbg-name>",
		Short: "Delete a benchmark job for a RoleBasedGroup",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rbgName := args[0]
			return runBenchmarkDelete(cmd.Context(), cf, rbgName, jobName)
		},
	}

	cmd.Flags().StringVar(&jobName, "job", "", "Name of the benchmark job to delete (required)")
	_ = cmd.MarkFlagRequired("job")

	return cmd
}

// runBenchmarkDelete deletes a benchmark Job after verifying it belongs to the given RBG.
func runBenchmarkDelete(ctx context.Context, cf *genericclioptions.ConfigFlags, rbgName, jobName string) error {
	if cf == nil {
		return fmt.Errorf("kubeconfig flags are not initialized")
	}

	ns := util.GetNamespace(cf)
	clientset, err := util.GetK8SClientSet(cf)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	// Get the job first to verify it belongs to the given RBG
	job, err := clientset.BatchV1().Jobs(ns).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get job %s: %w", jobName, err)
	}

	// Verify the job belongs to the specified RBG
	if labelValue, ok := job.Labels[benchmarkLabelKey]; !ok || labelValue != rbgName {
		return fmt.Errorf("job %s does not belong to RBG %s", jobName, rbgName)
	}

	// Delete the job with background propagation policy to also clean up Pods
	propagationPolicy := metav1.DeletePropagationBackground
	err = clientset.BatchV1().Jobs(ns).Delete(ctx, jobName, metav1.DeleteOptions{
		PropagationPolicy: &propagationPolicy,
	})
	if err != nil {
		return fmt.Errorf("failed to delete job %s: %w", jobName, err)
	}

	fmt.Printf("Deleted benchmark Job %s in namespace %s\n", jobName, ns)
	return nil
}
