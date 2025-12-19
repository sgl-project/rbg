package llm

import (
	"github.com/spf13/cobra"
	"sigs.k8s.io/rbgs/cmd/cli/cmd/llm/generate"
)

// NewLLMCmd creates the llm command
func NewLLMCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "llm",
		Short: "LLM deployment management commands",
		Long:  `Commands for managing LLM model deployments on Kubernetes using RoleBasedGroup`,
	}

	// Add subcommands
	cmd.AddCommand(generate.NewGenerateCmd())

	return cmd
}
