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

// RunParams holds all flag values supplied to the run command.
type RunParams struct {
	Mode     string
	Engine   string
	Storage  string
	Revision string
	EnvVars  []string
	ArgsList []string
}

// runContext holds the fully resolved artifacts produced by resolveRunContext.
type runContext struct {
	PodTemplate   *corev1.PodTemplateSpec
	EngineType    string
	ModeName      string
	ResolvedPort  int32
	StoragePlugin storageplugin.Plugin
	StorageName   string
}

// resolveRunContext performs all pure data resolution for the run command:
// model/mode lookup → engine resolution → storage/model-path resolution →
// pod template construction → resource overlay → port extraction.
// It has no side effects and is independently testable.
func resolveRunContext(name, modelID string, p RunParams, userCfg *cliconfig.Config) (*runContext, error) {
	// 1. Load and find model + mode config
	models, err := runpkg.LoadBuiltinModels()
	if err != nil {
		return nil, fmt.Errorf("failed to load model configs: %w", err)
	}
	modelCfg, err := runpkg.FindModelConfig(models, modelID)
	if err != nil {
		return nil, err
	}
	modeCfg, err := runpkg.FindModeConfig(modelCfg, p.Mode)
	if err != nil {
		return nil, err
	}

	// 2. Resolve engine type (flag overrides mode config)
	engineType := modeCfg.Engine
	if p.Engine != "" {
		engineType = p.Engine
	}
	engineCfg, err := resolveEngine(engineType, userCfg)
	if err != nil {
		return nil, err
	}
	enginePlugin, err := engineplugin.Get(engineCfg.Type, engineCfg.Config)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize engine %q: %w", engineType, err)
	}

	// 3. Resolve storage plugin and model path
	var modelPath string
	var storagePlugin storageplugin.Plugin
	var storageName string
	if userCfg != nil {
		storageName = userCfg.CurrentStorage
		if p.Storage != "" {
			storageName = p.Storage
		}
		if storageName != "" {
			if storageCfg, err := userCfg.GetStorage(storageName); err == nil {
				if sp, err := storageplugin.Get(storageCfg.Type, storageCfg.Config); err == nil {
					storagePlugin = sp
					modelPath = sp.MountPath() + "/" + sanitizeModelID(modelID) + "/" + sanitizeModelID(p.Revision)
				}
			}
		}
	}
	if modelPath == "" {
		modelPath = "/model/" + sanitizeModelID(modelID) + "/" + sanitizeModelID(p.Revision)
	}

	// 4. Generate base pod template and validate
	podTemplate, err := enginePlugin.GenerateTemplate(name, modelID, modelPath)
	if err != nil {
		return nil, fmt.Errorf("failed to generate engine template: %w", err)
	}
	if len(podTemplate.Spec.Containers) == 0 {
		return nil, fmt.Errorf("engine %q generated template with no containers", engineType)
	}
	container := &podTemplate.Spec.Containers[0]

	// 5. Overlay mode config
	if modeCfg.Image != "" {
		container.Image = modeCfg.Image
	}
	container.Args = append(container.Args, modeCfg.Args...)
	container.Args = append(container.Args, p.ArgsList...)

	for _, e := range modeCfg.Env {
		container.Env = append(container.Env, corev1.EnvVar{Name: e.Name, Value: e.Value})
	}
	for _, ev := range p.EnvVars {
		parts := strings.SplitN(ev, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid environment variable format: %q, expected KEY=VALUE", ev)
		}
		container.Env = append(container.Env, corev1.EnvVar{Name: parts[0], Value: parts[1]})
	}

	// 6. Apply resource overrides
	effGPU := modeCfg.Resources.GPU
	effCPU := modeCfg.Resources.CPU
	effMemory := modeCfg.Resources.Memory
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

	if podTemplate.ObjectMeta.Labels == nil {
		podTemplate.ObjectMeta.Labels = make(map[string]string)
	}

	// 7. Extract resolved port from the generated template (ground truth for what is deployed)
	var resolvedPort int32
	for _, cp := range podTemplate.Spec.Containers[0].Ports {
		if cp.Name == "http" {
			resolvedPort = cp.ContainerPort
			break
		}
	}

	return &runContext{
		PodTemplate:   podTemplate,
		EngineType:    engineType,
		ModeName:      modeCfg.Name,
		ResolvedPort:  resolvedPort,
		StoragePlugin: storagePlugin,
		StorageName:   storageName,
	}, nil
}

// generateRBG creates a v1alpha2 RoleBasedGroup from the pod template and configuration
func generateRBG(name, namespace, modelID, revision string, replicas int32, rctx *runContext) *workloadsv1alpha2.RoleBasedGroup {
	// Build metadata annotation as JSON
	metadata := llmmeta.RunMetadata{
		ModelID:  modelID,
		Engine:   rctx.EngineType,
		Mode:     rctx.ModeName,
		Revision: revision,
		Port:     rctx.ResolvedPort,
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		klog.V(1).Infof("failed to marshal run metadata: %v", err)
	}

	// Ensure pod template has labels
	if rctx.PodTemplate.ObjectMeta.Labels == nil {
		rctx.PodTemplate.ObjectMeta.Labels = make(map[string]string)
	}

	// Set standard labels on pod template
	rctx.PodTemplate.ObjectMeta.Labels[llmmeta.RunCommandSourceLabelKey] = llmmeta.RunCommandSourceLabelValue

	// Create the RoleBasedGroup
	return &workloadsv1alpha2.RoleBasedGroup{
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
								Template: rctx.PodTemplate,
							},
						},
					},
					Workload: workloadsv1alpha2.WorkloadSpec{
						APIVersion: "workloads.x-k8s.io/v1alpha2",
						Kind:       "RoleInstanceSet",
					},
				},
			},
		},
	}
}

// createRBG creates a v1alpha2 RoleBasedGroup in Kubernetes
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
		envVars  []string
		argsList []string
		storage  string
		revision string
		dryRun   bool
	)

	cmd := &cobra.Command{
		Use:   "run <name> <model-id> [flags]",
		Short: "Run a model as an inference service",
		Long: `Deploy a model as an inference service on Kubernetes using RoleBasedGroup.

This command creates a RoleBasedGroup resource that deploys an LLM model for inference.
It supports various inference engines (vLLM, SGLang) and deployment modes optimized
for different use cases (latency, throughput, etc.).

The command will:
  1. Load the model configuration from the built-in models database
  2. Generate a pod template using the specified inference engine
  3. Create a RoleBasedGroup resource in the cluster

Prerequisites:
  - The model should be available in storage (use 'kubectl rbg llm pull' first)
  - Storage must be configured (use 'kubectl rbg llm config add-storage')

Examples:
  # Quick start with default config
  kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B

  # Use a specific mode
  kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --mode throughput

  # Override engine
  kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --mode custom --engine sglang

  # Run with multiple replicas
  kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --replicas 3

  # Dry run to preview the generated configuration
  kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --dry-run`,
		Example: `  # Quick start with default config
  kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B

  # Use a specific mode
  kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --mode throughput

  # Override engine
  kubectl rbg llm run my-qwen Qwen/Qwen3.5-0.8B --mode custom --engine sglang`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			modelID := args[1]

			// Load user config (best-effort — optional for engine and storage resolution)
			userCfg, _ := cliconfig.Load()

			// Resolve all pure data: model/mode/engine/storage/template/port
			rctx, err := resolveRunContext(name, modelID, RunParams{
				Mode:     mode,
				Engine:   engine,
				Storage:  storage,
				Revision: revision,
				EnvVars:  envVars,
				ArgsList: argsList,
			}, userCfg)
			if err != nil {
				return err
			}

			// Get namespace
			namespace := util.GetNamespace(cf)

			// Mount storage: in dry-run mode, only add volumes/mounts without provisioning K8s resources
			if rctx.StoragePlugin != nil && rctx.StorageName != "" {
				mountOpts := storageplugin.MountOptions{
					StorageName: rctx.StorageName,
					Namespace:   namespace,
					DryRun:      dryRun,
				}
				if !dryRun {
					c, err := util.GetControllerRuntimeClient(cf)
					if err != nil {
						return fmt.Errorf("failed to create controller client: %w", err)
					}
					mountOpts.Client = c
				}
				if err := rctx.StoragePlugin.MountStorage(rctx.PodTemplate, mountOpts); err != nil {
					return fmt.Errorf("failed to mount storage: %w", err)
				}
			}

			// Generate RoleBasedGroup from the (now fully configured) pod template
			rbg := generateRBG(name, namespace, modelID, revision, replicas, rctx)

			if dryRun {
				fmt.Println("# Generated RoleBasedGroup for Model Serving (DRY RUN)")
				fmt.Printf("# Name:      %s\n", name)
				fmt.Printf("# Namespace: %s\n", namespace)
				fmt.Printf("# Model:     %s\n", modelID)
				fmt.Printf("# Revision:  %s\n", revision)
				fmt.Printf("# Mode:      %s\n", rctx.ModeName)
				fmt.Printf("# Engine:    %s\n", rctx.EngineType)
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
			fmt.Printf("# Mode:      %s\n", rctx.ModeName)
			fmt.Printf("# Engine:    %s\n", rctx.EngineType)
			fmt.Printf("# Replicas:  %d\n", replicas)
			fmt.Println("#")

			// Create the v1alpha2 RoleBasedGroup workload
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
	cmd.Flags().StringArrayVar(&envVars, "env", nil, "Environment variables (KEY=VALUE)")
	cmd.Flags().StringArrayVar(&argsList, "arg", nil, "Additional arguments for the engine")
	cmd.Flags().StringVar(&storage, "storage", "", "Storage to use (overrides default)")
	cmd.Flags().StringVar(&revision, "revision", "main", "Model revision")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the generated template without creating the workload")

	return cmd
}
