package config

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"sigs.k8s.io/rbgs/cmd/cli/config"
	engineplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/engine"
)

func newSetEngineCmd() *cobra.Command {
	var configFlags map[string]string

	cmd := &cobra.Command{
		Use:   "set-engine ENGINE_TYPE",
		Short: "Customize engine configuration (optional — engines work with defaults without this)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			engineType := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if !engineplugin.IsRegistered(engineType) {
				return fmt.Errorf("unknown engine type '%s'", engineType)
			}

			configMap := make(map[string]interface{})
			for k, v := range configFlags {
				configMap[k] = v
			}

			if err := engineplugin.ValidateConfig(engineType, configMap); err != nil {
				return err
			}

			cfg.SetEngine(engineType, configMap)

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Engine '%s' configured successfully\n", engineType)
			return nil
		},
	}

	configFlags = make(map[string]string)
	cmd.Flags().StringToStringVar(&configFlags, "config", nil, "Engine configuration key=value pairs")

	return cmd
}

func newGetEnginesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get-engines",
		Short: "List customized engine configurations",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "TYPE")
			for _, e := range cfg.Engines {
				fmt.Fprintf(w, "%s\n", e.Type)
			}
			return w.Flush()
		},
	}
}

func newResetEngineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset-engine ENGINE_TYPE",
		Short: "Remove custom engine configuration, reverting to defaults",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			engineType := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if err := cfg.DeleteEngine(engineType); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Engine '%s' reset to defaults\n", engineType)
			return nil
		},
	}
}
