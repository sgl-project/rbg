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

package config

import (
	"bufio"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"sigs.k8s.io/rbgs/cmd/cli/config"
	sourceplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/source"
)

func newAddSourceCmd() *cobra.Command {
	var sourceType string
	var configFlags map[string]string
	var interactive bool

	cmd := &cobra.Command{
		Use:   "add-source NAME",
		Short: "Add a source configuration",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("'add-source' requires exactly 1 argument\n\nUsage:\n  kubectl rbg llm config add-source NAME [-i]\n\nSee 'kubectl rbg llm config add-source -h' for examples.")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			var configMap map[string]interface{}

			if interactive {
				// Interactive mode
				reader := bufio.NewReader(os.Stdin)
				sourceType, configMap, err = configureSource(reader)
				if err != nil {
					return err
				}
			} else {
				// Command-line mode
				configMap = make(map[string]interface{})
				for k, v := range configFlags {
					configMap[k] = v
				}
			}

			if err := sourceplugin.ValidateConfig(sourceType, configMap); err != nil {
				return err
			}

			if err := cfg.AddSource(name, sourceType, configMap); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Source '%s' added successfully\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&sourceType, "type", "huggingface", "Source type (huggingface, modelscope)")
	configFlags = make(map[string]string)
	cmd.Flags().StringToStringVar(&configFlags, "config", nil, "Source configuration key=value pairs")
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "Interactive configuration mode")

	return cmd
}

func newGetSourcesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get-sources",
		Short: "List all source configurations",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTYPE\tCURRENT")
			for _, s := range cfg.Sources {
				current := ""
				if s.Name == cfg.CurrentSource {
					current = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, s.Type, current)
			}
			return w.Flush()
		},
	}
}

func newUseSourceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use-source NAME",
		Short: "Set the current source",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("'use-source' requires exactly 1 argument\n\nUsage:\n  kubectl rbg llm config use-source NAME\n\nSee 'kubectl rbg llm config use-source -h' for examples.")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if err := cfg.UseSource(name); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Now using source '%s'\n", name)
			return nil
		},
	}
}

func newSetSourceCmd() *cobra.Command {
	var configFlags map[string]string

	cmd := &cobra.Command{
		Use:   "set-source NAME",
		Short: "Update a source configuration",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("'set-source' requires exactly 1 argument\n\nUsage:\n  kubectl rbg llm config set-source NAME [--config key=value]\n\nSee 'kubectl rbg llm config set-source -h' for examples.")
			}
			return nil
		},
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

			if err := cfg.UpdateSource(name, configMap); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Source '%s' updated successfully\n", name)
			return nil
		},
	}

	configFlags = make(map[string]string)
	cmd.Flags().StringToStringVar(&configFlags, "config", nil, "Source configuration key=value pairs")

	return cmd
}

func newDeleteSourceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete-source NAME",
		Short: "Delete a source configuration",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("'delete-source' requires exactly 1 argument\n\nUsage:\n  kubectl rbg llm config delete-source NAME\n\nSee 'kubectl rbg llm config delete-source -h' for examples.")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if err := cfg.DeleteSource(name); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Source '%s' deleted successfully\n", name)
			return nil
		},
	}
}
