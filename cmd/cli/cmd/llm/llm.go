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

package llm

import (
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/rbgs/cmd/cli/cmd/llm/benchmark"
	"sigs.k8s.io/rbgs/cmd/cli/cmd/llm/generate"
)

// NewLLMCmd creates the llm command
func NewLLMCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "llm",
		Short: "LLM deployment management commands",
		Long:  `Commands for managing LLM model deployments on Kubernetes using RoleBasedGroup`,
	}

	// Add subcommands
	cmd.AddCommand(generate.NewGenerateCmd())
	cmd.AddCommand(benchmark.NewBenchmarkCmd(cf))

	return cmd
}
