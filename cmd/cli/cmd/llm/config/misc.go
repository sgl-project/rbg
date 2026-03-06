package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/rbgs/cmd/cli/config"
	engineplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/engine"
	sourceplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/source"
	storageplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/storage"
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

// readLine reads a line from stdin with prompt
func readLine(reader *bufio.Reader, prompt string, defaultValue string) string {
	if defaultValue != "" {
		fmt.Printf("%s [%s]: ", prompt, defaultValue)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	fmt.Fprint(os.Stdout) // Flush the prompt
	line, err := reader.ReadString('\n')
	if err != nil {
		// EOF or error, return default
		return defaultValue
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue
	}
	return line
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

// configureStorage interactively configures storage
func configureStorage(reader *bufio.Reader) (string, map[string]interface{}, error) {
	fmt.Println("\n=== Configure Storage ===")

	// Select storage type
	storageTypes := storageplugin.RegisteredNames()
	if len(storageTypes) == 0 {
		return "", nil, fmt.Errorf("no storage types available")
	}
	storageType := selectPlugin(reader, "storage", storageTypes)

	// Get config fields for selected type (without initializing, just to get fields)
	fields := storageplugin.GetFields(storageType)
	if fields == nil {
		return "", nil, fmt.Errorf("failed to get storage plugin fields for type: %s", storageType)
	}

	config := make(map[string]interface{})
	fmt.Printf("\nConfiguring %s storage:\n", storageType)
	for _, field := range fields {
		prompt := fmt.Sprintf("  %s", field.Key)
		if !field.Required {
			prompt += " [optional]"
		}
		prompt += fmt.Sprintf(" (%s)", field.Description)
		value := readLine(reader, prompt, "")
		for value == "" && field.Required {
			fmt.Println("  This field is required, please enter a value")
			value = readLine(reader, prompt, "")
		}
		if value != "" {
			config[field.Key] = value
		}
	}

	return storageType, config, nil
}

// configureSource interactively configures source
func configureSource(reader *bufio.Reader) (string, map[string]interface{}, error) {
	fmt.Println("\n=== Configure Model Source ===")

	// Select source type
	sourceTypes := sourceplugin.RegisteredNames()
	if len(sourceTypes) == 0 {
		return "", nil, fmt.Errorf("no source types available")
	}
	sourceType := selectPlugin(reader, "source", sourceTypes)

	// Get config fields for selected type (without initializing, just to get fields)
	fields := sourceplugin.GetFields(sourceType)
	if fields == nil {
		return "", nil, fmt.Errorf("failed to get source plugin fields for type: %s", sourceType)
	}

	config := make(map[string]interface{})
	fmt.Printf("\nConfiguring %s source:\n", sourceType)
	for _, field := range fields {
		prompt := fmt.Sprintf("  %s", field.Key)
		if !field.Required {
			prompt += " [optional]"
		}
		prompt += fmt.Sprintf(" (%s)", field.Description)
		value := readLine(reader, prompt, "")
		for value == "" && field.Required {
			fmt.Println("  This field is required, please enter a value")
			value = readLine(reader, prompt, "")
		}
		if value != "" {
			config[field.Key] = value
		}
	}

	return sourceType, config, nil
}

// configureEngine interactively configures engine
func configureEngine(reader *bufio.Reader) (string, map[string]interface{}, error) {
	fmt.Println("\n=== Configure Inference Engine ===")

	// Select engine type
	engineTypes := engineplugin.RegisteredNames()
	if len(engineTypes) == 0 {
		return "", nil, fmt.Errorf("no engine types available")
	}
	engineType := selectPlugin(reader, "engine", engineTypes)

	// Get config fields for selected type (without initializing, just to get fields)
	fields := engineplugin.GetFields(engineType)
	if fields == nil {
		return "", nil, fmt.Errorf("failed to get engine plugin fields for type: %s", engineType)
	}

	config := make(map[string]interface{})
	fmt.Printf("\nConfiguring %s engine:\n", engineType)
	for _, field := range fields {
		prompt := fmt.Sprintf("  %s", field.Key)
		if !field.Required {
			prompt += " [optional]"
		}
		prompt += fmt.Sprintf(" (%s)", field.Description)
		value := readLine(reader, prompt, "")
		for value == "" && field.Required {
			fmt.Println("  This field is required, please enter a value")
			value = readLine(reader, prompt, "")
		}
		if value != "" {
			// Convert port to int
			if field.Key == "port" {
				if port, err := strconv.Atoi(value); err == nil {
					config[field.Key] = port
				} else {
					config[field.Key] = 8000
				}
			} else {
				config[field.Key] = value
			}
		}
	}

	return engineType, config, nil
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

			// Configure Engine
			engineType, engineConfig, err := configureEngine(reader)
			if err != nil {
				return fmt.Errorf("failed to configure engine: %w", err)
			}

			// Set namespace
			namespace := readLine(reader, "\nEnter default namespace", "default")
			if namespace == "" {
				namespace = "default"
			}

			// Create and save configuration
			cfg := &config.Config{
				APIVersion:     "rbg/v1alpha1",
				Kind:           "Config",
				Namespace:      namespace,
				CurrentStorage: storageType,
				CurrentSource:  sourceType,
				CurrentEngine:  engineType,
			}

			cfg.AddStorage(storageType, storageType, storageConfig)
			cfg.AddSource(sourceType, sourceType, sourceConfig)
			cfg.AddEngine(engineType, engineType, engineConfig)

			if err := cfg.Save(); err != nil {
				return fmt.Errorf("failed to save configuration: %w", err)
			}

			fmt.Println("\n=== Configuration Initialized Successfully ===")
			fmt.Printf("  Storage: %s\n", storageType)
			fmt.Printf("  Source:  %s\n", sourceType)
			fmt.Printf("  Engine:  %s\n", engineType)
			fmt.Printf("  Namespace: %s\n", namespace)
			fmt.Println("\nUse 'kubectl rbg llm config view' to see the full configuration.")

			return nil
		},
	}

	return cmd
}
