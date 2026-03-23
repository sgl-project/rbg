/*
Copyright 2026.

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

package llm

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"sigs.k8s.io/rbgs/cmd/cli/util"
)

func newDeleteCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete [name...] [flags]",
		Short: "Delete LLM inference services created by the CLI",
		Long: `Delete RoleBasedGroup resources created by 'kubectl rbg llm run'.

This command deletes LLM inference services that were created using the CLI.
It can delete services by name, or delete all CLI-managed services at once.

The command filters RoleBasedGroups by the CLI source label to only delete resources
managed by the kubectl-rbg CLI tool.

Examples:
  # Delete a specific service by name
  kubectl rbg llm delete my-qwen

  # Delete multiple services by name
  kubectl rbg llm delete my-qwen my-llama

  # Delete a service in a specific namespace
  kubectl rbg llm delete my-qwen -n kubeai
`,
		Example: `  # Delete a specific service by name
  kubectl rbg llm delete my-qwen

  # Delete multiple services by name
  kubectl rbg llm delete my-qwen my-llama

  # Delete a service in a specific namespace
  kubectl rbg llm delete my-qwen -n kubeai
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("at least one service name is required, or use --all to delete all services")
			}

			client, err := util.GetRBGClient(cf)
			if err != nil {
				return fmt.Errorf("unable to connect to Kubernetes cluster: %w", err)
			}

			namespace := util.GetNamespace(cf)

			ctx := context.Background()

			// Delete by name(s)
			var errCount int
			for _, name := range args {
				ns := namespace
				if ns == "" {
					ns = util.GetNamespace(cf)
				}
				if err := client.WorkloadsV1alpha2().RoleBasedGroups(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "error: failed to delete %s/%s: %v\n", ns, name, err)
					errCount++
				} else {
					fmt.Printf("rolebasedgroups.workloads.x-k8s.io \"%s\" deleted\n", name)
				}
			}
			if errCount > 0 {
				return fmt.Errorf("%d deletion(s) failed", errCount)
			}
			return nil
		},
	}

	return cmd
}
