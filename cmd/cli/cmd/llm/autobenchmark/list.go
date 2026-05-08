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

package autobenchmark

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"sigs.k8s.io/rbgs/cmd/cli/util"
	"sigs.k8s.io/rbgs/pkg/autobenchmark/constant"
)

func newListCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List auto-benchmark experiments",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd.Context(), cf)
		},
	}
	return cmd
}

func runList(ctx context.Context, cf *genericclioptions.ConfigFlags) error {
	clientset, err := util.GetK8SClientSet(cf)
	if err != nil {
		return err
	}

	namespace := util.GetNamespace(cf)

	jobs, err := clientset.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: constant.AutoBenchmarkLabelKey,
	})
	if err != nil {
		return fmt.Errorf("listing jobs: %w", err)
	}

	if len(jobs.Items) == 0 {
		fmt.Printf("No auto-benchmark experiments found in namespace %q\n", namespace)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tSTATUS\tAGE")

	for _, job := range jobs.Items {
		status := "Running"
		for _, c := range job.Status.Conditions {
			if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
				status = "Completed"
			} else if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
				status = "Failed"
			}
		}

		age := metav1.Now().Sub(job.CreationTimestamp.Time)
		jn := job.Labels[constant.AutoBenchmarkLabelKey]
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", jn, status, formatDuration(age))
	}

	return w.Flush()
}

func formatDuration(d interface {
	Hours() float64
	Minutes() float64
}) string {
	h := int(d.Hours())
	if h >= 24 {
		return fmt.Sprintf("%dd", h/24)
	}
	if h > 0 {
		return fmt.Sprintf("%dh", h)
	}
	m := int(d.Minutes())
	if m > 0 {
		return fmt.Sprintf("%dm", m)
	}
	return "<1m"
}
