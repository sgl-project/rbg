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
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/rbgs/cmd/cli/config"
	sourceplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/source"
	storageplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/storage"
	"sigs.k8s.io/rbgs/cmd/cli/plugin/util"
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
						// Get field definitions to check for masked fields
						fields := storageplugin.GetFields(s.Type)
						maskedFields := make(map[string]bool)
						for _, f := range fields {
							if f.Masked == util.MaskPrevious || f.Masked == util.MaskAll {
								maskedFields[f.Key] = true
							}
						}
						for k, v := range s.Config {
							if maskedFields[k] {
								fmt.Printf("    %s: %s\n", k, util.MaskedDisplay())
							} else {
								fmt.Printf("    %s: %v\n", k, v)
							}
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
						// Get field definitions to check for masked fields
						fields := sourceplugin.GetFields(s.Type)
						maskedFields := make(map[string]bool)
						for _, f := range fields {
							if f.Masked == util.MaskPrevious || f.Masked == util.MaskAll {
								maskedFields[f.Key] = true
							}
						}
						for k, v := range s.Config {
							if maskedFields[k] {
								fmt.Printf("    %s: %s\n", k, util.MaskedDisplay())
							} else {
								fmt.Printf("    %s: %v\n", k, v)
							}
						}
					}
				}
			} else {
				fmt.Println("Source: (not configured)")
			}
			fmt.Println()

			return nil
		},
	}
}

// selectPlugin prompts user to select a plugin type from available options
func selectPlugin(reader *bufio.Reader, pluginType string, availableNames []string) string {
	fmt.Printf("\nSelect %s type:\n", pluginType)
	for i, name := range availableNames {
		fmt.Printf("  %d. %s\n", i+1, name)
	}

	maxAttempts := len(availableNames) + 5
	attempts := 0

	for attempts < maxAttempts {
		fmt.Printf("Enter choice (1-%d): ", len(availableNames))
		line, err := reader.ReadString('\n')
		// Process the line even if we got EOF (it may have content before EOF)
		line = strings.TrimSpace(line)
		if line != "" {
			idx, convErr := strconv.Atoi(line)
			if convErr == nil && idx >= 1 && idx <= len(availableNames) {
				return availableNames[idx-1]
			}
		}
		// If EOF with no valid input, return first option
		if err != nil {
			return availableNames[0]
		}
		// Empty line, count as attempt
		if line == "" {
			attempts++
			continue
		}
		fmt.Println("Invalid choice, please try again")
		attempts++
	}

	// Max attempts reached, return first option
	return availableNames[0]
}

// configurePlugin interactively configures a plugin
func configurePlugin(reader *bufio.Reader, pluginType string, getNames func() []string, getFields func(string) []util.ConfigField) (string, map[string]interface{}, error) {
	fmt.Printf("\n=== Configure %s ===\n", pluginType)

	// Select plugin type
	names := getNames()
	if len(names) == 0 {
		return "", nil, fmt.Errorf("no %s types available", pluginType)
	}
	selectedType := selectPlugin(reader, pluginType, names)

	// Get config fields for selected type
	fields := getFields(selectedType)
	if fields == nil {
		return "", nil, fmt.Errorf("failed to get %s plugin fields for type: %s", pluginType, selectedType)
	}

	config := make(map[string]interface{})
	fmt.Printf("\nConfiguring %s %s:\n", selectedType, pluginType)
	for _, field := range fields {
		prompt := fmt.Sprintf("  %s", field.Key)
		if !field.Required {
			prompt += " [optional]"
		}
		prompt += fmt.Sprintf(" (%s)", field.Description)

		var value string
		if field.Masked != util.MaskNone {
			value = util.ReadMaskedLine(prompt, "", field.Masked)
		} else {
			value = util.ReadLine(reader, prompt, "")
		}
		for value == "" && field.Required {
			fmt.Println("  This field is required, please enter a value")
			if field.Masked != util.MaskNone {
				value = util.ReadMaskedLine(prompt, "", field.Masked)
			} else {
				value = util.ReadLine(reader, prompt, "")
			}
		}
		if value != "" {
			config[field.Key] = value
		}
	}

	return selectedType, config, nil
}

// configureStorage interactively configures storage
func configureStorage(reader *bufio.Reader) (string, map[string]interface{}, error) {
	return configurePlugin(reader, "Storage", storageplugin.RegisteredNames, storageplugin.GetFields)
}

// configureSource interactively configures source
func configureSource(reader *bufio.Reader) (string, map[string]interface{}, error) {
	return configurePlugin(reader, "Model Source", sourceplugin.RegisteredNames, sourceplugin.GetFields)
}

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize LLM configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check if config file already exists
			configPath := config.GetConfigPath()
			if configPath != "" {
				if _, err := os.Stat(configPath); err == nil {
					fmt.Println("Configuration file already exists!")
					fmt.Printf("Location: %s\n", configPath)
					fmt.Println("If you want to reinitialize, please remove the existing config file first.")
					fmt.Println("Use 'kubectl rbg llm config view' to see current configuration.")
					return nil
				}
			}

			reader := bufio.NewReader(os.Stdin)

			fmt.Println("=== LLM Configuration Initialization ===")
			fmt.Println("This wizard will guide you through the initial configuration setup.")
			fmt.Println()

			// Configure Storage
			storageType, storageConfig, err := configureStorage(reader)
			if err != nil {
				return fmt.Errorf("failed to configure storage: %w", err)
			}

			// Configure Source
			sourceType, sourceConfig, err := configureSource(reader)
			if err != nil {
				return fmt.Errorf("failed to configure source: %w", err)
			}

			// Create and save configuration
			cfg := &config.Config{
				APIVersion:     "rbg/v1alpha1",
				Kind:           "Config",
				CurrentStorage: storageType,
				CurrentSource:  sourceType,
			}

			_ = cfg.AddStorage(storageType, storageType, storageConfig)
			_ = cfg.AddSource(sourceType, sourceType, sourceConfig)

			if err := cfg.Save(); err != nil {
				return fmt.Errorf("failed to save configuration: %w", err)
			}

			fmt.Println("\n=== Configuration Initialized Successfully ===")
			fmt.Printf("  Storage: %s\n", storageType)
			fmt.Printf("  Source:  %s\n", sourceType)
			fmt.Println("\nEngines work with defaults. Use 'kubectl rbg llm config set-engine' to customize.")
			fmt.Println("Use 'kubectl rbg llm config view' to see the full configuration.")

			return nil
		},
	}

	return cmd
}
