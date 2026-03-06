package config

import (
	"github.com/spf13/cobra"
)

// NewConfigCmd creates the config command
func NewConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage LLM configuration",
		Long:  `Configure storage, source, and engine settings for LLM deployment`,
	}

	// Add subcommands
	cmd.AddCommand(newAddStorageCmd())
	cmd.AddCommand(newAddSourceCmd())
	cmd.AddCommand(newAddEngineCmd())
	cmd.AddCommand(newGetStoragesCmd())
	cmd.AddCommand(newGetSourcesCmd())
	cmd.AddCommand(newGetEnginesCmd())
	cmd.AddCommand(newUseStorageCmd())
	cmd.AddCommand(newUseSourceCmd())
	cmd.AddCommand(newUseEngineCmd())
	cmd.AddCommand(newSetStorageCmd())
	cmd.AddCommand(newSetSourceCmd())
	cmd.AddCommand(newSetEngineCmd())
	cmd.AddCommand(newDeleteStorageCmd())
	cmd.AddCommand(newDeleteSourceCmd())
	cmd.AddCommand(newDeleteEngineCmd())
	cmd.AddCommand(newViewCmd())
	cmd.AddCommand(newSetNamespaceCmd())
	cmd.AddCommand(newInitCmd())

	return cmd
}
