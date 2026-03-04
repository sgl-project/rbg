/*
Copyright 2025.

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
	"fmt"

	"github.com/spf13/cobra"
	"sigs.k8s.io/rbgs/cmd/cli/config"
)

func newViewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "view",
		Short: "View current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			fmt.Println("Current Configuration:")
			fmt.Println()

			if cfg.CurrentStorage != "" {
				if s, err := cfg.GetStorage(cfg.CurrentStorage); err == nil {
					fmt.Printf("Storage: %s (active)\n", s.Name)
					fmt.Printf("  Type: %s\n", s.Type)
					if len(s.Config) > 0 {
						fmt.Println("  Config:")
						for k, v := range s.Config {
							fmt.Printf("    %s: %v\n", k, v)
						}
					}
				}
			} else {
				fmt.Println("Storage: (not configured)")
			}
			fmt.Println()

			if cfg.CurrentSource != "" {
				if s, err := cfg.GetSource(cfg.CurrentSource); err == nil {
					fmt.Printf("Source: %s (active)\n", s.Name)
					fmt.Printf("  Type: %s\n", s.Type)
					if len(s.Config) > 0 {
						fmt.Println("  Config:")
						for k, v := range s.Config {
							fmt.Printf("    %s: %v\n", k, v)
						}
					}
				}
			} else {
				fmt.Println("Source: (not configured)")
			}
			fmt.Println()

			if cfg.CurrentEngine != "" {
				if e, err := cfg.GetEngine(cfg.CurrentEngine); err == nil {
					fmt.Printf("Engine: %s (active)\n", e.Name)
					fmt.Printf("  Type: %s\n", e.Type)
					if len(e.Config) > 0 {
						fmt.Println("  Config:")
						for k, v := range e.Config {
							fmt.Printf("    %s: %v\n", k, v)
						}
					}
				}
			} else {
				fmt.Println("Engine: (not configured)")
			}
			fmt.Println()

			fmt.Printf("Default Namespace: %s\n", cfg.Namespace)
			return nil
		},
	}
}

func newSetNamespaceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-namespace NAMESPACE",
		Short: "Set the default namespace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			namespace := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			cfg.Namespace = namespace

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Printf("Default namespace set to '%s'\n", namespace)
			return nil
		},
	}
}

func newInitCmd() *cobra.Command {
	var interactive bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize LLM configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if interactive {
				fmt.Println("Interactive initialization not yet implemented")
				fmt.Println("Please use the add-storage, add-source, and add-engine commands")
				return nil
			}

			if len(cfg.Storages) == 0 {
				cfg.AddStorage("default", "pvc", map[string]interface{}{
					"size": "100Gi",
				})
				cfg.CurrentStorage = "default"
				fmt.Println("Created default storage configuration (pvc)")
			}

			if len(cfg.Sources) == 0 {
				cfg.AddSource("huggingface", "huggingface", map[string]interface{}{})
				cfg.CurrentSource = "huggingface"
				fmt.Println("Created default source configuration (huggingface)")
			}

			if len(cfg.Engines) == 0 {
				cfg.AddEngine("vllm", "vllm", map[string]interface{}{
					"image": "vllm/vllm-openai:latest",
					"port":  8000,
				})
				cfg.CurrentEngine = "vllm"
				fmt.Println("Created default engine configuration (vllm)")
			}

			if cfg.Namespace == "" {
				cfg.Namespace = "default"
			}

			if err := cfg.Save(); err != nil {
				return err
			}

			fmt.Println("\nConfiguration initialized successfully!")
			fmt.Println("Use 'kubectl rbg llm config view' to see current configuration")
			return nil
		},
	}

	cmd.Flags().BoolVar(&interactive, "interactive", false, "Interactive mode")

	return cmd
}
