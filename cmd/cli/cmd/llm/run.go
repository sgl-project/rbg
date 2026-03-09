package llm

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/rbgs/cmd/cli/config"
	engineplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/engine"
	storageplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/storage"
)

// resolveEngine resolves the engine configuration.
// First tries to get from user config, then falls back to registered plugin with defaults.
func resolveEngine(engineType string, cfg *config.Config) (*config.EngineConfig, error) {
	// 1. Try to get from user config
	if engineCfg, err := cfg.GetEngine(engineType); err == nil {
		return engineCfg, nil
	}

	// 2. Check if it's a registered plugin type
	if !engineplugin.IsRegistered(engineType) {
		return nil, fmt.Errorf("unknown engine type '%s'", engineType)
	}

	// 3. Use default (empty config) - plugin will use its built-in defaults
	fmt.Printf("INFO: Using default configuration for engine '%s'. Run 'kubectl rbg llm config add-engine %s' to customize.\n", engineType, engineType)
	return &config.EngineConfig{
		Type:   engineType,
		Config: map[string]interface{}{},
	}, nil
}

func newRunCmd() *cobra.Command {
	var (
		name     string
		replicas int32
		gpu      int
		cpu      int
		memory   string
		envVars  []string
		argsList []string
		storage  string
		engine   string
		revision string
	)

	cmd := &cobra.Command{
		Use:   "run MODEL_ID",
		Short: "Run a model as a service",
		Long:  `Deploy a model as an inference service using the configured engine`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			modelID := args[0]

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			// Determine engine
			if engine == "" {
				return fmt.Errorf("--engine flag is required")
			}
			engineType := engine

			// Resolve engine configuration with fallback to defaults
			engineCfg, err := resolveEngine(engineType, cfg)
			if err != nil {
				return err
			}

			// Determine storage
			storageName := cfg.CurrentStorage
			if storage != "" {
				storageName = storage
			}
			if storageName == "" {
				return fmt.Errorf("no storage configured, please run 'kubectl rbg llm config add-storage' first")
			}

			storageCfg, err := cfg.GetStorage(storageName)
			if err != nil {
				return err
			}

			// Initialize plugins
			enginePlugin, err := engineplugin.Get(engineCfg.Type, engineCfg.Config)
			if err != nil {
				return fmt.Errorf("failed to initialize engine plugin: %w", err)
			}
			if enginePlugin == nil {
				return fmt.Errorf("unknown engine type: %s", engineCfg.Type)
			}

			storagePlugin, err := storageplugin.Get(storageCfg.Type, storageCfg.Config)
			if err != nil {
				return fmt.Errorf("failed to initialize storage plugin: %w", err)
			}
			if storagePlugin == nil {
				return fmt.Errorf("unknown storage type: %s", storageCfg.Type)
			}

			// Get mount path and construct model path
			mountPath := storagePlugin.MountPath()
			modelPath := mountPath + "/" + sanitizeModelID(modelID) + "/" + sanitizeModelID(revision)

			// Generate deployment name
			if name == "" {
				name = sanitizeModelID(modelID) + "-" + sanitizeModelID(revision)
			}

			// Generate engine template
			podTemplate, err := enginePlugin.GenerateTemplate(name, modelID, modelPath)
			if err != nil {
				return fmt.Errorf("failed to generate engine template: %w", err)
			}

			// Mount storage
			if err := storagePlugin.MountStorage(podTemplate); err != nil {
				return fmt.Errorf("failed to mount storage: %w", err)
			}

			// Apply additional configuration
			if len(podTemplate.Spec.Containers) > 0 {
				container := &podTemplate.Spec.Containers[0]

				// Set GPU resources
				if gpu > 0 {
					if container.Resources.Limits == nil {
						container.Resources.Limits = make(corev1.ResourceList)
					}
					container.Resources.Limits[corev1.ResourceName("nvidia.com/gpu")] = resource.MustParse(fmt.Sprintf("%d", gpu))
				}

				// Set CPU resources
				if cpu > 0 {
					if container.Resources.Requests == nil {
						container.Resources.Requests = make(corev1.ResourceList)
					}
					container.Resources.Requests[corev1.ResourceCPU] = resource.MustParse(fmt.Sprintf("%d", cpu))
				}

				// Set memory resources
				if memory != "" {
					if container.Resources.Requests == nil {
						container.Resources.Requests = make(corev1.ResourceList)
					}
					container.Resources.Requests[corev1.ResourceMemory] = resource.MustParse(memory)
				}

				// Add environment variables
				for _, env := range envVars {
					parts := strings.SplitN(env, "=", 2)
					if len(parts) == 2 {
						container.Env = append(container.Env, corev1.EnvVar{
							Name:  parts[0],
							Value: parts[1],
						})
					} else {
						return fmt.Errorf("invalid environment variable format: %q, expected KEY=VALUE", env)
					}
				}

				// Add extra args
				for _, arg := range argsList {
					container.Args = append(container.Args, arg)
				}
			}

			// Print the generated template
			fmt.Println("# Generated Pod Template for Model Serving")
			fmt.Printf("# Model: %s\n", modelID)
			fmt.Printf("# Revision: %s\n", revision)
			fmt.Printf("# Name: %s\n", name)
			fmt.Printf("# Engine: %s\n", engineType)
			fmt.Printf("# Storage: %s\n", storageName)
			fmt.Printf("# Replicas: %d\n", replicas)
			fmt.Println("#")
			fmt.Println("# TODO: Create RoleBasedGroup from this template to actually deploy the model")
			fmt.Println()

			return printPodTemplate(name, podTemplate)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Name for the deployment (default: model ID)")
	cmd.Flags().Int32Var(&replicas, "replicas", 1, "Number of replicas")
	cmd.Flags().IntVar(&gpu, "gpu", 0, "Number of GPUs per replica")
	cmd.Flags().IntVar(&cpu, "cpu", 0, "Number of CPUs per replica")
	cmd.Flags().StringVar(&memory, "memory", "", "Memory per replica (e.g., 256Gi)")
	cmd.Flags().StringArrayVar(&envVars, "env", nil, "Environment variables (KEY=VALUE)")
	cmd.Flags().StringArrayVar(&argsList, "arg", nil, "Additional arguments for the engine")
	cmd.Flags().StringVar(&storage, "storage", "", "Storage to use (overrides default)")
	cmd.Flags().StringVar(&engine, "engine", "", "Engine to use (overrides default)")
	cmd.Flags().StringVar(&revision, "revision", "main", "Model revision to deploy")

	return cmd
}
