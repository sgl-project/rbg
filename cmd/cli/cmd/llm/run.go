package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/klog/v2"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	llmmeta "sigs.k8s.io/rbgs/cmd/cli/cmd/llm/metadata"
	runpkg "sigs.k8s.io/rbgs/cmd/cli/cmd/llm/run"
	cliconfig "sigs.k8s.io/rbgs/cmd/cli/config"
	engineplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/engine"
	storageplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/storage"
	"sigs.k8s.io/rbgs/cmd/cli/util"
)

// resolveEngine resolves the engine configuration.
// First tries to get from user config, then falls back to registered plugin with defaults.
func resolveEngine(engineType string, cfg *cliconfig.Config) (*cliconfig.EngineConfig, error) {
	// 1. Try to get from user config (if available)
	if cfg != nil {
		if engineCfg, err := cfg.GetEngine(engineType); err == nil {
			return engineCfg, nil
		}
	}

	// 2. Check if it's a registered plugin type
	if !engineplugin.IsRegistered(engineType) {
		return nil, fmt.Errorf("unknown engine type '%s'", engineType)
	}

	// 3. Use default (empty config) - plugin will use its built-in defaults
	fmt.Printf("INFO: Using default configuration for engine '%s'. Run 'kubectl rbg llm config add-engine %s' to customize.\n", engineType, engineType)
	return &cliconfig.EngineConfig{
		Type:   engineType,
		Config: map[string]interface{}{},
	}, nil
}

// generateRBG creates a RoleBasedGroup from the pod template and configuration
func generateRBG(name, namespace, modelID, engineType, mode, revision string, replicas int32, podTemplate *corev1.PodTemplateSpec) *workloadsv1alpha2.RoleBasedGroup {
	// Build metadata annotation as JSON
	metadata := llmmeta.RunMetadata{
		ModelID:  modelID,
		Engine:   engineType,
		Mode:     mode,
		Revision: revision,
	}
	metadataJSON, _ := json.Marshal(metadata)

	// Ensure pod template has labels
	if podTemplate.ObjectMeta.Labels == nil {
		podTemplate.ObjectMeta.Labels = make(map[string]string)
	}

	// Set standard labels on pod template
	podTemplate.ObjectMeta.Labels[llmmeta.RunCommandSourceLabelKey] = llmmeta.RunCommandSourceLabelValue

	// Create the RoleBasedGroup
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "workloads.x-k8s.io/v1alpha2",
			Kind:       "RoleBasedGroup",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				llmmeta.RunCommandSourceLabelKey: llmmeta.RunCommandSourceLabelValue,
			},
			Annotations: map[string]string{
				llmmeta.RunCommandMetadataAnnotationKey: string(metadataJSON),
			},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{
					Name:     "inference",
					Replicas: &replicas,
					Pattern: workloadsv1alpha2.Pattern{
						StandalonePattern: &workloadsv1alpha2.StandalonePattern{
							TemplateSource: workloadsv1alpha2.TemplateSource{
								Template: podTemplate,
							},
						},
					},
					Workload: workloadsv1alpha2.WorkloadSpec{
						APIVersion: "workloads.x-k8s.io/v1alpha1",
						Kind:       "InstanceSet",
					},
				},
			},
		},
	}

	return rbg
}

// createRBG creates the RoleBasedGroup in Kubernetes
func createRBG(ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup, namespace string, cf *genericclioptions.ConfigFlags) error {
	client, err := util.GetRBGClient(cf)
	if err != nil {
		return fmt.Errorf("failed to create RBG client: %w", err)
	}

	_, err = client.WorkloadsV1alpha2().RoleBasedGroups(namespace).Create(ctx, rbg, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create RoleBasedGroup: %w", err)
	}

	return nil
}

func newRunCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	var (
		replicas int32
		mode     string
		engine   string
		gpu      int
		cpu      int
		memory   string
		envVars  []string
		argsList []string
		storage  string
		revision string
		dryRun   bool
	)

	cmd := &cobra.Command{
		Use:   "run <name> <model-id> [flags]",
		Short: "Run a model as an inference service",
		Long:  `Deploy a model as an inference service on Kubernetes using RoleBasedGroup.`,
		Example: `  # Quick start with default config
  kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B

  # Use a specific mode
  kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --mode throughput

  # Override resources
  kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --mode custom --engine sglang --gpu 2 --memory 64Gi`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			modelID := args[1]

			// Load built-in model configs
			models, err := runpkg.LoadBuiltinModels()
			if err != nil {
				return fmt.Errorf("failed to load model configs: %w", err)
			}

			// Find matching model config
			modelCfg, err := runpkg.FindModelConfig(models, modelID)
			if err != nil {
				return err
			}

			// Find mode config
			modeCfg, err := runpkg.FindModeConfig(modelCfg, mode)
			if err != nil {
				return err
			}

			// Determine engine: flag overrides mode config
			engineType := modeCfg.Engine
			if engine != "" {
				engineType = engine
			}

			// Load user config (best-effort — optional for engine and storage resolution)
			userCfg, _ := cliconfig.Load()

			// Resolve engine configuration
			engineCfg, err := resolveEngine(engineType, userCfg)
			if err != nil {
				return err
			}

			// Get engine plugin
			enginePlugin, err := engineplugin.Get(engineCfg.Type, engineCfg.Config)
			if err != nil {
				return fmt.Errorf("failed to initialize engine %q: %w", engineType, err)
			}

			// Optionally resolve storage and model path
			var modelPath string
			var storagePlugin storageplugin.Plugin
			if userCfg != nil {
				storageName := userCfg.CurrentStorage
				if storage != "" {
					storageName = storage
				}
				if storageName != "" {
					if storageCfg, err := userCfg.GetStorage(storageName); err == nil {
						if sp, err := storageplugin.Get(storageCfg.Type, storageCfg.Config); err == nil {
							storagePlugin = sp
							mountPath := sp.MountPath()
							modelPath = mountPath + "/" + sanitizeModelID(modelID) + "/" + sanitizeModelID(revision)
						}
					}
				}
			}
			if modelPath == "" {
				// Fallback: use a conventional path without a real mount
				modelPath = "/model/" + sanitizeModelID(modelID) + "/" + sanitizeModelID(revision)
			}

			// Generate base pod template from engine plugin
			podTemplate, err := enginePlugin.GenerateTemplate(name, modelID, modelPath)
			if err != nil {
				return fmt.Errorf("failed to generate engine template: %w", err)
			}
			if len(podTemplate.Spec.Containers) == 0 {
				return fmt.Errorf("engine %q generated template with no containers", engineType)
			}
			container := &podTemplate.Spec.Containers[0]

			// Overlay mode config on the base template
			if modeCfg.Image != "" {
				container.Image = modeCfg.Image
			}
			container.Args = append(container.Args, modeCfg.Args...)
			container.Args = append(container.Args, argsList...)

			// Merge env: mode config first, then user env flags
			for _, e := range modeCfg.Env {
				container.Env = append(container.Env, corev1.EnvVar{Name: e.Name, Value: e.Value})
			}
			for _, ev := range envVars {
				parts := strings.SplitN(ev, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid environment variable format: %q, expected KEY=VALUE", ev)
				}
				container.Env = append(container.Env, corev1.EnvVar{Name: parts[0], Value: parts[1]})
			}

			// Apply resource overrides
			effGPU := modeCfg.Resources.GPU
			if gpu > 0 {
				effGPU = gpu
			}
			effCPU := modeCfg.Resources.CPU
			if cpu > 0 {
				effCPU = cpu
			}
			effMemory := modeCfg.Resources.Memory
			if memory != "" {
				effMemory = memory
			}
			if effGPU > 0 {
				if container.Resources.Limits == nil {
					container.Resources.Limits = make(corev1.ResourceList)
				}
				container.Resources.Limits[corev1.ResourceName("nvidia.com/gpu")] = resource.MustParse(fmt.Sprintf("%d", effGPU))
			}
			if effCPU > 0 {
				if container.Resources.Requests == nil {
					container.Resources.Requests = make(corev1.ResourceList)
				}
				container.Resources.Requests[corev1.ResourceCPU] = resource.MustParse(fmt.Sprintf("%d", effCPU))
			}
			if effMemory != "" {
				if container.Resources.Requests == nil {
					container.Resources.Requests = make(corev1.ResourceList)
				}
				container.Resources.Requests[corev1.ResourceMemory] = resource.MustParse(effMemory)
			}

			// Ensure labels exist
			if podTemplate.ObjectMeta.Labels == nil {
				podTemplate.ObjectMeta.Labels = make(map[string]string)
			}

			// Mount storage if available
			if storagePlugin != nil {
				if err := storagePlugin.MountStorage(podTemplate); err != nil {
					return fmt.Errorf("failed to mount storage: %w", err)
				}
			}

			// Get namespace
			namespace := util.GetNamespace(cf)

			// Generate RoleBasedGroup
			rbg := generateRBG(name, namespace, modelID, engineType, modeCfg.Name, revision, replicas, podTemplate)

			if dryRun {
				fmt.Println("# Generated RoleBasedGroup for Model Serving (DRY RUN)")
				fmt.Printf("# Name:      %s\n", name)
				fmt.Printf("# Namespace: %s\n", namespace)
				fmt.Printf("# Model:     %s\n", modelID)
				fmt.Printf("# Revision:  %s\n", revision)
				fmt.Printf("# Mode:      %s\n", modeCfg.Name)
				fmt.Printf("# Engine:    %s\n", engineType)
				fmt.Printf("# Replicas:  %d\n", replicas)
				fmt.Println("#")
				fmt.Println("# DRY RUN: No workload will be created")
				fmt.Println()
				return printRBG(rbg)
			}

			// Print summary
			fmt.Println("# Generated RoleBasedGroup for Model Serving")
			fmt.Printf("# Name:      %s\n", name)
			fmt.Printf("# Namespace: %s\n", namespace)
			fmt.Printf("# Model:     %s\n", modelID)
			fmt.Printf("# Revision:  %s\n", revision)
			fmt.Printf("# Mode:      %s\n", modeCfg.Name)
			fmt.Printf("# Engine:    %s\n", engineType)
			fmt.Printf("# Replicas:  %d\n", replicas)
			fmt.Println("#")

			// TODO: v1alpha2 is not ready not, test later.
			// Create the RoleBasedGroup workload
			ctx := context.Background()
			if err := createRBG(ctx, rbg, namespace, cf); err != nil {
				klog.ErrorS(err, "Failed to create RoleBasedGroup")
				return err
			}

			fmt.Printf("✓ RoleBasedGroup '%s' created successfully in namespace '%s'\n", name, namespace)
			return nil
		},
	}

	cmd.Flags().Int32Var(&replicas, "replicas", 1, "Number of replicas")
	cmd.Flags().StringVar(&mode, "mode", "", "Run mode (default: first mode in model config)")
	cmd.Flags().StringVar(&engine, "engine", "", "Inference engine override: vllm, sglang (default: from mode config)")
	cmd.Flags().IntVar(&gpu, "gpu", 0, "GPU count override")
	cmd.Flags().IntVar(&cpu, "cpu", 0, "CPU count override")
	cmd.Flags().StringVar(&memory, "memory", "", "Memory override (e.g., 32Gi)")
	cmd.Flags().StringArrayVar(&envVars, "env", nil, "Environment variables (KEY=VALUE)")
	cmd.Flags().StringArrayVar(&argsList, "arg", nil, "Additional arguments for the engine")
	cmd.Flags().StringVar(&storage, "storage", "", "Storage to use (overrides default)")
	cmd.Flags().StringVar(&revision, "revision", "main", "Model revision")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the generated template without creating the workload")

	return cmd
}
