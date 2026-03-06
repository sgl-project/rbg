package config

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"sigs.k8s.io/rbgs/cmd/cli/config"
	engineplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/engine"
)

func newAddEngineCmd() *cobra.Command {
	var engineType string
	var configFlags map[string]string

	cmd := &cobra.Command{
		Use:   "add-engine NAME",
		Short: "Add an engine configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			configMap := make(map[string]interface{})
			for k, v := range configFlags {
				configMap[k] = v
			}

			if err := engineplugin.ValidateConfig(engineType, configMap); err != nil {
				return err
			}

			if err := cfg.AddEngine(name, engineType, configMap); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Engine '%s' added successfully\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&engineType, "type", "vllm", "Engine type (vllm, sglang)")
	configFlags = make(map[string]string)
	cmd.Flags().StringToStringVar(&configFlags, "config", nil, "Engine configuration key=value pairs")

	return cmd
}

func newGetEnginesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get-engines",
		Short: "List all engine configurations",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTYPE\tCURRENT")
			for _, e := range cfg.Engines {
				current := ""
				if e.Name == cfg.CurrentEngine {
					current = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", e.Name, e.Type, current)
			}
			return w.Flush()
		},
	}
}

func newUseEngineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use-engine NAME",
		Short: "Set the current engine",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if err := cfg.UseEngine(name); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Now using engine '%s'\n", name)
			return nil
		},
	}
}

func newSetEngineCmd() *cobra.Command {
	var configFlags map[string]string

	cmd := &cobra.Command{
		Use:   "set-engine NAME",
		Short: "Update an engine configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			configMap := make(map[string]interface{})
			for k, v := range configFlags {
				configMap[k] = v
			}

			if err := cfg.UpdateEngine(name, configMap); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Engine '%s' updated successfully\n", name)
			return nil
		},
	}

	configFlags = make(map[string]string)
	cmd.Flags().StringToStringVar(&configFlags, "config", nil, "Engine configuration key=value pairs")

	return cmd
}

func newDeleteEngineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete-engine NAME",
		Short: "Delete an engine configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if err := cfg.DeleteEngine(name); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Engine '%s' deleted successfully\n", name)
			return nil
		},
	}
}
