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
