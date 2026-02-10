package benchmark

import (
	"context"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"sigs.k8s.io/rbgs/cmd/cli/util"
)

// NewBenchmarkListCmd creates the "llm benchmark list" command used to list benchmark Jobs.
func NewBenchmarkListCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <rbg-name>",
		Short: "List benchmark jobs for a RoleBasedGroup",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rbgName := args[0]
			return runBenchmarkList(cmd.Context(), cf, rbgName)
		},
	}

	return cmd
}

// runBenchmarkList queries Job resources associated with the given RBG and prints their status.
func runBenchmarkList(ctx context.Context, cf *genericclioptions.ConfigFlags, rbgName string) error {
	if cf == nil {
		return fmt.Errorf("kubeconfig flags are not initialized")
	}

	ns := util.GetNamespace(cf)
	clientset, err := util.GetK8SClientSet(cf)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	labelSelector := fmt.Sprintf("%s=%s", benchmarkLabelKey, rbgName)
	jobList, err := clientset.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return fmt.Errorf("failed to list benchmark jobs: %w", err)
	}

	if len(jobList.Items) == 0 {
		fmt.Printf("No benchmark jobs found for RBG %s in namespace %s\n", rbgName, ns)
		return nil
	}

	jobs := jobList.Items

	// Sort by creation time from oldest to newest
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreationTimestamp.Before(&jobs[j].CreationTimestamp)
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintf(w, "NAME\tSTATUS\tCREATED\tDURATION\n")
	for _, job := range jobs {
		state := deriveJobState(&job)
		created := job.CreationTimestamp.Format(time.RFC3339)
		duration := "-"
		if job.Status.StartTime != nil && job.Status.CompletionTime != nil {
			duration = job.Status.CompletionTime.Sub(job.Status.StartTime.Time).Round(time.Second).String()
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", job.Name, state, created, duration)
	}
	w.Flush()

	return nil
}
