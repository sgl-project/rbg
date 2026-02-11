package benchmark

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/yaml"

	"sigs.k8s.io/rbgs/cmd/cli/util"
)

// NewBenchmarkGetCmd creates the "llm benchmark get" command used to display
// the benchmark configuration stored on a Job's annotation.
func NewBenchmarkGetCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	var jobName string

	cmd := &cobra.Command{
		Use:   "get <rbg-name>",
		Short: "Display the benchmark configuration for a benchmark job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rbgName := args[0]
			return runBenchmarkGet(cmd.Context(), cf, rbgName, jobName)
		},
	}

	cmd.Flags().StringVar(&jobName, "job", "", "Name of the benchmark job to inspect (required)")
	_ = cmd.MarkFlagRequired("job")

	return cmd
}

// runBenchmarkGet fetches the benchmark Job, extracts the config annotation, and prints it as YAML.
func runBenchmarkGet(ctx context.Context, cf *genericclioptions.ConfigFlags, rbgName, jobName string) error {
	if cf == nil {
		return fmt.Errorf("kubeconfig flags are not initialized")
	}

	ns := util.GetNamespace(cf)
	clientset, err := util.GetK8SClientSet(cf)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	job, err := clientset.BatchV1().Jobs(ns).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get job %s: %w", jobName, err)
	}

	// Verify the job belongs to the specified RBG
	if labelValue, ok := job.Labels[benchmarkLabelKey]; !ok || labelValue != rbgName {
		return fmt.Errorf("job %s does not belong to RBG %s", jobName, rbgName)
	}

	configJSON, ok := job.Annotations[benchmarkConfigAnnotationKey]
	if !ok {
		return fmt.Errorf("job %s does not have benchmark config annotation (%s)", jobName, benchmarkConfigAnnotationKey)
	}

	// Deserialize to BenchmarkConfig so we can re-serialize as pretty YAML.
	var cfg BenchmarkConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return fmt.Errorf("failed to parse benchmark config annotation: %w", err)
	}

	yamlData, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to format benchmark config as YAML: %w", err)
	}

	fmt.Printf("Benchmark config for Job %s (RBG: %s, Namespace: %s):\n---\n%s", jobName, rbgName, ns, string(yamlData))
	return nil
}
