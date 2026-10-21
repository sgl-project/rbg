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

package migrate

import (
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// NewMigrateCmd returns the migration command tree. Invoking it without a
// subcommand deliberately prints the recommended order instead of doing work.
func NewMigrateCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate deprecated v1alpha1 resources and workload types",
		Long: "Migrate resources away from deprecated v1alpha1 APIs and workload types.\n\n" +
			"Recommended order:\n" +
			"  1. kubectl rbg migrate plan\n" +
			"  2. kubectl rbg migrate workload-type --rbg RBG --role ROLE\n" +
			"  3. kubectl rbg migrate storage-version\n",
		SilenceUsage:      true,
		DisableAutoGenTag: true,
		Args:              cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newPlanCmd(cf))
	cmd.AddCommand(newWorkloadTypeCmd(cf))
	cmd.AddCommand(newStorageVersionCmd(cf))

	return cmd
}
