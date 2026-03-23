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
	storageplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/storage"
)

func newAddStorageCmd() *cobra.Command {
	var storageType string
	var configFlags map[string]string
	var interactive bool

	cmd := &cobra.Command{
		Use:   "add-storage NAME",
		Short: "Add a storage configuration",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("'add-storage' requires exactly 1 argument\n\nUsage:\n  kubectl rbg llm config add-storage NAME [-i]\n\nSee 'kubectl rbg llm config add-storage -h' for examples.")
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
				storageType, configMap, err = configureStorage(reader)
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

			if err := storageplugin.ValidateConfig(storageType, configMap); err != nil {
				return err
			}

			if err := cfg.AddStorage(name, storageType, configMap); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Storage '%s' added successfully\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&storageType, "type", "pvc", "Storage type (pvc)")
	configFlags = make(map[string]string)
	cmd.Flags().StringToStringVar(&configFlags, "config", nil, "Storage configuration key=value pairs")
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "Interactive configuration mode")

	return cmd
}

func newGetStoragesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get-storages",
		Short: "List all storage configurations",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTYPE\tCURRENT")
			for _, s := range cfg.Storages {
				current := ""
				if s.Name == cfg.CurrentStorage {
					current = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, s.Type, current)
			}
			return w.Flush()
		},
	}
}

func newUseStorageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use-storage NAME",
		Short: "Set the current storage",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("'use-storage' requires exactly 1 argument\n\nUsage:\n  kubectl rbg llm config use-storage NAME\n\nSee 'kubectl rbg llm config use-storage -h' for examples.")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if err := cfg.UseStorage(name); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Now using storage '%s'\n", name)
			return nil
		},
	}
}

func newSetStorageCmd() *cobra.Command {
	var configFlags map[string]string

	cmd := &cobra.Command{
		Use:   "set-storage NAME",
		Short: "Update a storage configuration",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("'set-storage' requires exactly 1 argument\n\nUsage:\n  kubectl rbg llm config set-storage NAME [--config key=value]\n\nSee 'kubectl rbg llm config set-storage -h' for examples.")
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

			if err := cfg.UpdateStorage(name, configMap); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Storage '%s' updated successfully\n", name)
			return nil
		},
	}

	configFlags = make(map[string]string)
	cmd.Flags().StringToStringVar(&configFlags, "config", nil, "Storage configuration key=value pairs")

	return cmd
}

func newDeleteStorageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete-storage NAME",
		Short: "Delete a storage configuration",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("'delete-storage' requires exactly 1 argument\n\nUsage:\n  kubectl rbg llm config delete-storage NAME\n\nSee 'kubectl rbg llm config delete-storage -h' for examples.")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if err := cfg.DeleteStorage(name); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Storage '%s' deleted successfully\n", name)
			return nil
		},
	}
}
