/*
Copyright 2026 The RBG Authors.

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

package svc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/klog/v2"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/cmd/cli/cmd/llm/shared"
	"sigs.k8s.io/rbgs/cmd/cli/cmd/llm/svc/chat"
	llmmeta "sigs.k8s.io/rbgs/cmd/cli/cmd/llm/svc/metadata"
	runpkg "sigs.k8s.io/rbgs/cmd/cli/cmd/llm/svc/run"
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
	DryRun   bool
	Replicas int32
}

// modeConfigResult holds the result of mode config resolution.
type modeConfigResult struct {
	modelCfg     *runpkg.ModelConfig
	modeCfg      *runpkg.ModeConfig
	enginePlugin engineplugin.Plugin
	engineType   string
}

// resolveModeConfig resolves model, mode, and engine configuration.
func resolveModeConfig(modelID string, p RunParams, userCfg *cliconfig.Config) (*modeConfigResult, error) {
	models, err := runpkg.LoadAllModels()
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

	return &modeConfigResult{
		modelCfg:     modelCfg,
		modeCfg:      modeCfg,
		enginePlugin: enginePlugin,
		engineType:   engineType,
	}, nil
}

// storageResult holds the result of storage resolution.
type storageResult struct {
	modelPath     string
	storagePlugin storageplugin.Plugin
	storageName   string
}

// resolveStorageAndModelPath resolves storage plugin and model path.
func resolveStorageAndModelPath(modelID string, p RunParams, userCfg *cliconfig.Config) *storageResult {
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
					modelPath = filepath.Join(sp.MountPath(), shared.SanitizeModelID(modelID), shared.SanitizeModelID(p.Revision))
				}
			}
		}
	}
	if modelPath == "" {
		modelPath = "/model/" + shared.SanitizeModelID(modelID) + "/" + shared.SanitizeModelID(p.Revision)
	}

	return &storageResult{
		modelPath:     modelPath,
		storagePlugin: storagePlugin,
		storageName:   storageName,
	}
}

// buildGenerateOptions builds GenerateOptions from mode config and run params.
func buildGenerateOptions(name, modelID, modelPath string, modeCfg *runpkg.ModeConfig, p RunParams) (engineplugin.GenerateOptions, error) {
	distributedSize := int32(0)
	if modeCfg.Distributed != nil && modeCfg.Distributed.Size > 1 {
		distributedSize = modeCfg.Distributed.Size
	}

	envVars := make([]corev1.EnvVar, len(modeCfg.Env))
	copy(envVars, modeCfg.Env)
	for _, ev := range p.EnvVars {
		parts := strings.SplitN(ev, "=", 2)
		if len(parts) != 2 {
			return engineplugin.GenerateOptions{}, fmt.Errorf("invalid environment variable format: %q, expected KEY=VALUE", ev)
		}
		envVars = append(envVars, corev1.EnvVar{Name: parts[0], Value: parts[1]})
	}

	var resources corev1.ResourceRequirements
	if len(modeCfg.Resources) > 0 {
		requests := corev1.ResourceList{}
		limits := corev1.ResourceList{}
		for k, v := range modeCfg.Resources {
			requests[k] = v
			limits[k] = v
		}
		resources.Requests = requests
		resources.Limits = limits
	}

	return engineplugin.GenerateOptions{
		Name:            name,
		ModelID:         modelID,
		ModelPath:       modelPath,
		Image:           modeCfg.Image,
		Args:            append(modeCfg.Args, p.ArgsList...),
		Env:             envVars,
		Resources:       resources,
		DistributedSize: distributedSize,
		ShmSize:         modeCfg.ShmSize,
	}, nil
}

// assembleRBG assembles a RoleBasedGroup from pattern and metadata.
func assembleRBG(name, namespace string, pattern *workloadsv1alpha2.Pattern, metadata llmmeta.RunMetadata, replicas int32) *workloadsv1alpha2.RoleBasedGroup {
	podTemplate := getPodTemplateFromPattern(pattern)
	if podTemplate.ObjectMeta.Labels == nil {
		podTemplate.ObjectMeta.Labels = make(map[string]string)
	}
	podTemplate.ObjectMeta.Labels[llmmeta.RunCommandSourceLabelKey] = llmmeta.RunCommandSourceLabelValue

	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		klog.V(1).Infof("failed to marshal run metadata: %v", err)
	}

	roleSpec := workloadsv1alpha2.RoleSpec{
		Name:     "inference",
		Replicas: &replicas,
		Pattern:  *pattern,
	}

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
			Roles: []workloadsv1alpha2.RoleSpec{roleSpec},
		},
	}
}

// generateRBG generates a RoleBasedGroup.
// It performs: model config resolution -> pattern generation -> storage mounting -> RBG assembly.
// Returns the generated RBG and metadata.
func generateRBG(name, modelID, namespace string, p RunParams, userCfg *cliconfig.Config, cf *genericclioptions.ConfigFlags) (*workloadsv1alpha2.RoleBasedGroup, llmmeta.RunMetadata, error) {
	// 1. Resolve model/mode/engine config
	modeRes, err := resolveModeConfig(modelID, p, userCfg)
	if err != nil {
		return nil, llmmeta.RunMetadata{}, err
	}

	// 2. Resolve storage and model path
	storageRes := resolveStorageAndModelPath(modelID, p, userCfg)

	// 3. Build GenerateOptions
	opts, err := buildGenerateOptions(name, modelID, storageRes.modelPath, modeRes.modeCfg, p)
	if err != nil {
		return nil, llmmeta.RunMetadata{}, err
	}

	// 4. Generate pattern
	pattern, err := modeRes.enginePlugin.GeneratePattern(opts)
	if err != nil {
		return nil, llmmeta.RunMetadata{}, fmt.Errorf("failed to generate engine pattern: %w", err)
	}
	podTemplate := getPodTemplateFromPattern(pattern)
	if podTemplate == nil || len(podTemplate.Spec.Containers) == 0 {
		return nil, llmmeta.RunMetadata{}, fmt.Errorf("engine %q generated pattern with no containers", modeRes.engineType)
	}

	// 5. Mount storage
	if storageRes.storagePlugin != nil && storageRes.storageName != "" {
		mountOpts := storageplugin.MountOptions{
			StorageName: storageRes.storageName,
			Namespace:   namespace,
			DryRun:      p.DryRun,
		}
		if !p.DryRun {
			c, err := util.GetControllerRuntimeClient(cf)
			if err != nil {
				return nil, llmmeta.RunMetadata{}, fmt.Errorf("failed to create controller client: %w", err)
			}
			mountOpts.Client = c
		}
		if err := storageRes.storagePlugin.MountStorage(podTemplate, mountOpts); err != nil {
			return nil, llmmeta.RunMetadata{}, fmt.Errorf("failed to mount storage: %w", err)
		}
	}

	// 6. Extract port and build metadata
	var resolvedPort int32
	for _, cp := range podTemplate.Spec.Containers[0].Ports {
		if cp.Name == "http" {
			resolvedPort = cp.ContainerPort
			break
		}
	}
	metadata := llmmeta.RunMetadata{
		ModelID:  modelID,
		Engine:   modeRes.engineType,
		Mode:     modeRes.modeCfg.Name,
		Revision: p.Revision,
		Port:     resolvedPort,
	}

	// 7. Assemble RBG
	rbg := assembleRBG(name, namespace, pattern, metadata, p.Replicas)

	return rbg, metadata, nil
}

// printGenerateSummary prints a human-readable summary of the generated RBG.
func printGenerateSummary(w io.Writer, rbg *workloadsv1alpha2.RoleBasedGroup, metadata llmmeta.RunMetadata) {
	_, _ = fmt.Fprintln(w, "# Generated RoleBasedGroup for Model Serving")
	_, _ = fmt.Fprintf(w, "# Name:      %s\n", rbg.Name)
	_, _ = fmt.Fprintf(w, "# Namespace: %s\n", rbg.GetNamespace())
	_, _ = fmt.Fprintf(w, "# Model:     %s\n", metadata.ModelID)
	_, _ = fmt.Fprintf(w, "# Revision:  %s\n", metadata.Revision)
	_, _ = fmt.Fprintf(w, "# Mode:      %s\n", metadata.Mode)
	_, _ = fmt.Fprintf(w, "# Engine:    %s\n", metadata.Engine)
	_, _ = fmt.Fprintln(w, "#")
}

// getPodTemplateFromPattern extracts the pod template from a Pattern
func getPodTemplateFromPattern(pattern *workloadsv1alpha2.Pattern) *corev1.PodTemplateSpec {
	if pattern == nil {
		return nil
	}
	if pattern.StandalonePattern != nil && pattern.StandalonePattern.Template != nil {
		return pattern.StandalonePattern.Template
	}
	if pattern.LeaderWorkerPattern != nil && pattern.LeaderWorkerPattern.Template != nil {
		return pattern.LeaderWorkerPattern.Template
	}
	return nil
}

// createRBG creates a v1alpha2 RoleBasedGroup in Kubernetes
func createRBG(ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup, cf *genericclioptions.ConfigFlags) error {
	client, err := util.GetRBGClient(cf)
	if err != nil {
		return fmt.Errorf("failed to create RBG client: %w", err)
	}

	_, err = client.WorkloadsV1alpha2().RoleBasedGroups(rbg.Namespace).Create(ctx, rbg, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create RoleBasedGroup: %w", err)
	}

	return nil
}

// waitForRBGReady waits for the RBG to be ready (Ready condition status is True)
func waitForRBGReady(ctx context.Context, name, namespace string, cf *genericclioptions.ConfigFlags, timeout time.Duration) error {
	client, err := util.GetRBGClient(cf)
	if err != nil {
		return fmt.Errorf("failed to create RBG client: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Waiting for RoleBasedGroup '%s' to be ready...\n", name)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastMsg string
	err = wait.PollUntilContextCancel(ctx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		rbg, err := client.WorkloadsV1alpha2().RoleBasedGroups(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		// Check if Ready condition is True
		for _, cond := range rbg.Status.Conditions {
			if cond.Type == string(workloadsv1alpha2.RoleBasedGroupReady) {
				if cond.Status == metav1.ConditionTrue {
					return true, nil
				}
				// Not ready yet, continue polling
				lastMsg = cond.Message
				return false, nil
			}
		}
		// Ready condition not found yet, continue polling
		return false, nil
	})

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timeout waiting for RoleBasedGroup '%s' to be ready: %s", name, lastMsg)
		}
		return fmt.Errorf("failed to wait for RoleBasedGroup ready: %w", err)
	}

	return nil
}

// findReadyPod finds a ready pod for the given RBG.
// For LeaderWorkerPattern (multi-node), only the leader (ComponentIndex=0) serves the API.
// For StandalonePattern (single-node), ComponentIndex is not set, so we fall back to any ready pod.
func findReadyPod(ctx context.Context, name, namespace string, cf *genericclioptions.ConfigFlags) (string, error) {
	k8sClient, err := util.GetK8SClientSet(cf)
	if err != nil {
		return "", fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// First try to find a leader pod (ComponentIndex=0) for multi-node deployments
	leaderSelector := labels.SelectorFromSet(labels.Set{
		constants.GroupNameLabelKey:      name,
		constants.RoleNameLabelKey:       "inference",
		constants.ComponentIndexLabelKey: "0",
	}).String()

	pods, err := k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: leaderSelector,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list pods: %w", err)
	}

	// If no leader pod found, fall back to any pod with the role (for single-node deployments)
	if len(pods.Items) == 0 {
		fallbackSelector := labels.SelectorFromSet(labels.Set{
			constants.GroupNameLabelKey: name,
			constants.RoleNameLabelKey:  "inference",
		}).String()
		pods, err = k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: fallbackSelector,
		})
		if err != nil {
			return "", fmt.Errorf("failed to list pods: %w", err)
		}
	}

	for i := range pods.Items {
		p := &pods.Items[i]
		if isPodReady(p) {
			return p.Name, nil
		}
	}

	return "", fmt.Errorf("no ready pods found for service %q", name)
}

// isPodReady checks if a pod is ready
func isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// testChatCompletionsWithReconnect tests the API with automatic port-forward reconstruction on disconnect
// The session is automatically stopped when the function returns (success or error)
func testChatCompletionsWithReconnect(
	baseURL, modelName string,
	timeout time.Duration,
	pfSession *chat.PortForwardSession,
	reconnectFunc func() (*chat.PortForwardSession, error),
) error {
	reqBody := map[string]interface{}{
		"model":      modelName,
		"messages":   []map[string]string{{"role": "user", "content": "Hello"}},
		"stream":     false,
		"max_tokens": 10,
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		pfSession.Stop()
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	requestTimeout := 30 * time.Second
	if timeout < requestTimeout {
		requestTimeout = timeout
	}

	client := &http.Client{Timeout: requestTimeout}
	endpoint := baseURL + "/v1/chat/completions"

	startTime := time.Now()
	attempt := 0
	reconnectAttempt := 0
	currentSession := pfSession

	for {
		attempt++
		remaining := timeout - time.Since(startTime)
		if remaining <= 0 {
			currentSession.Stop()
			return fmt.Errorf("timeout waiting for API to be ready after %s", timeout)
		}

		// Check if port-forward is still alive before making request
		if !currentSession.IsAlive() {
			reconnectAttempt++
			if reconnectAttempt%5 == 1 {
				fmt.Fprintf(os.Stderr, "  Port-forward disconnected, attempting to reconnect... (attempt %d, elapsed: %s)\n", reconnectAttempt, time.Since(startTime).Round(time.Second))
			}
			currentSession.Stop()

			newSession, err := reconnectFunc()
			if err != nil {
				return fmt.Errorf("failed to reconnect port-forward: %w", err)
			}
			currentSession = newSession
			if reconnectAttempt%5 == 1 {
				fmt.Fprintf(os.Stderr, "  Port-forward reconnected successfully\n")
			}
		}

		resp, err := client.Post(endpoint, "application/json", bytes.NewReader(data))
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			currentSession.Stop()
			return nil
		}

		var errMsg string
		if err != nil {
			errMsg = fmt.Sprintf("error: %v", err)
		} else {
			body, _ := io.ReadAll(resp.Body)
			errMsg = fmt.Sprintf("status: %d, body: %s", resp.StatusCode, string(body))
			_ = resp.Body.Close()
		}

		if attempt%5 == 0 {
			fmt.Fprintf(os.Stderr, "  API not ready yet (%s), retrying... (attempt %d, elapsed: %s)\n", errMsg, attempt, time.Since(startTime).Round(time.Second))
		}

		sleepDuration := 5 * time.Second
		if remaining < sleepDuration {
			sleepDuration = remaining
		}
		time.Sleep(sleepDuration)
	}
}

func newRunCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	var (
		replicas       int32
		mode           string
		engine         string
		envVars        []string
		argsList       []string
		storage        string
		revision       string
		dryRun         bool
		waitReady      bool
		waitTimeout    time.Duration
		testAPI        bool
		testAPITimeout time.Duration
		localPort      int32
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
  - The model should be available in storage (use 'kubectl rbg llm model pull' first)
  - Storage must be configured (use 'kubectl rbg llm config add-storage')

Examples:
  # Quick start with default config
  kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B

  # Use a specific mode
  kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --mode throughput

  # Override engine
  kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --mode custom --engine sglang

  # Run with multiple replicas
  kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --replicas 3

  # Dry run to preview the generated configuration
  kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --dry-run`,
		Example: `  # Quick start with default config
  kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B

  # Use a specific mode
  kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --mode throughput

  # Override engine
  kubectl rbg llm svc run my-qwen Qwen/Qwen3.5-0.8B --mode custom --engine sglang`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			modelID := args[1]
			namespace := util.GetNamespace(cf)

			// Load user config (best-effort — optional for engine and storage resolution)
			userCfg, cfgErr := cliconfig.Load()
			if cfgErr != nil {
				klog.V(1).Infof("Warning: failed to load user config: %v", cfgErr)
			}

			// Generate RBG (includes config resolution, pattern generation, storage mounting)
			params := RunParams{
				Mode:     mode,
				Engine:   engine,
				Storage:  storage,
				Revision: revision,
				EnvVars:  envVars,
				ArgsList: argsList,
				DryRun:   dryRun,
				Replicas: replicas,
			}
			rbg, metadata, err := generateRBG(name, modelID, namespace, params, userCfg, cf)
			if err != nil {
				return err
			}

			printGenerateSummary(os.Stdout, rbg, metadata)

			if dryRun {
				fmt.Println("# DRY RUN: No workload will be created")
				fmt.Println()
				return shared.PrintRBG(rbg)
			}

			// Create the RoleBasedGroup workload
			ctx := context.Background()
			if err := createRBG(ctx, rbg, cf); err != nil {
				klog.ErrorS(err, "Failed to create RoleBasedGroup")
				return err
			}

			fmt.Printf("✓ RoleBasedGroup '%s' created successfully in namespace '%s'\n", name, namespace)

			// Wait for RBG to be ready if requested
			if waitReady || testAPI {
				if err := waitForRBGReady(ctx, name, namespace, cf, waitTimeout); err != nil {
					return err
				}
				fmt.Printf("✓ RoleBasedGroup '%s' is ready\n", name)
			}

			// Test API if requested
			if testAPI {
				// Find a ready pod
				podName, err := findReadyPod(ctx, name, namespace, cf)
				if err != nil {
					return fmt.Errorf("failed to find ready pod: %w", err)
				}

				// Get kubeconfig path for port-forward
				kubeconfig := ""
				if cf.KubeConfig != nil {
					kubeconfig = *cf.KubeConfig
				}

				// Start port-forward
				fmt.Fprintf(os.Stderr, "Testing API endpoint...\n")
				pfSession, err := chat.StartPortForward(kubeconfig, namespace, podName, localPort, metadata.Port, 30*time.Second)
				if err != nil {
					return fmt.Errorf("failed to start port-forward: %w", err)
				}

				// Test the API with automatic port-forward reconstruction
				baseURL := fmt.Sprintf("http://localhost:%d", localPort)
				if err := testChatCompletionsWithReconnect(baseURL, name, testAPITimeout, pfSession, func() (*chat.PortForwardSession, error) {
					// Reconnect function: find new ready pod and restart port-forward
					newPodName, err := findReadyPod(ctx, name, namespace, cf)
					if err != nil {
						return nil, err
					}
					return chat.StartPortForward(kubeconfig, namespace, newPodName, localPort, metadata.Port, 30*time.Second)
				}); err != nil {
					return fmt.Errorf("API test failed: %w", err)
				}
				fmt.Printf("✓ API endpoint is ready (/v1/chat/completions)\n")
			}

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
	cmd.Flags().BoolVar(&waitReady, "wait", true, "Wait for the RoleBasedGroup to be ready before returning")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 20*time.Minute, "Timeout for waiting for RoleBasedGroup to be ready")
	cmd.Flags().BoolVar(&testAPI, "test-api", true, "Test the /v1/chat/completions API endpoint after the service is ready")
	cmd.Flags().DurationVar(&testAPITimeout, "test-api-timeout", 5*time.Minute, "Timeout for testing the API endpoint")
	cmd.Flags().Int32Var(&localPort, "local-port", 32432, "Local port for port-forward when testing API")

	return cmd
}
