package generate

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// NewGenerateCmd creates the generate command
func NewGenerateCmd() *cobra.Command {
	config := &TaskConfig{
		// Set defaults
		BackendName:      BackendSGLang,
		ISL:              4000,
		OSL:              1000,
		Prefix:           0,
		TTFT:             -1,
		TPOT:             -1,
		RequestLatency:   -1,
		DatabaseMode:     DatabaseModeSilicon,
		SaveDir:          "/tmp/rbg-llm-generate-output",
		ConfiguratorTool: AIConfigurator,
		ExtraArgs:        make(map[string]string),
	}

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate optimized RBG deployment configurations using AI Configurator",
		Long: `The generate command integrates with AI Configurator to generate optimized
deployment configurations for AI model serving. It supports both Prefill-Decode
disaggregated mode and aggregated mode deployments.

Example:
  kubectl-rbg llm generate --configurator-tool aiconfigurator --model QWEN3_32B --system h200_sxm --total-gpus 8 \
    --backend sglang --isl 4000 --osl 1000 --ttft 1000 --tpot 10 --save-dir /tmp/rbg-llm-generate-output

This will:
  1. Check if aiconfigurator is installed
  2. Run AI Configurator optimization
  3. Parse the generated configurations
  4. Generate RBG-compatible YAML files for both deployment modes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRecommender(config)
		},
	}

	cmd.Flags().StringVar(&config.ConfiguratorTool, "configurator-tool", AIConfigurator, "Configurator tool to use for generating deployment configs")

	// Core required parameters
	cmd.Flags().StringVar(&config.ModelName, "model", "", "Model name (required)")
	cmd.Flags().StringVar(&config.SystemName, "system", "", "GPU system type (required)")
	cmd.Flags().IntVar(&config.TotalGPUs, "total-gpus", 0, "Total number of GPUs for deployment (required)")
	cmd.Flags().IntVar(&config.ISL, "isl", 4000, "Input sequence length")
	cmd.Flags().IntVar(&config.OSL, "osl", 1000, "Output sequence length")

	cmd.Flags().Float64Var(&config.TTFT, "ttft", -1, "Time to first token in milliseconds (required with --tpot if --request-latency not set)")
	cmd.Flags().Float64Var(&config.TPOT, "tpot", -1, "Time per output token in milliseconds (required with --ttft if --request-latency not set)")
	cmd.Flags().Float64Var(&config.RequestLatency, "request-latency", -1, "End-to-end request latency target in milliseconds (alternative to --ttft and --tpot)")

	// Core optional parameters
	cmd.Flags().StringVar(&config.HuggingFaceID, "hf-id", "", "HuggingFace model ID (e.g., Qwen/Qwen2.5-7B)")
	cmd.Flags().StringVar(&config.DecodeSystemName, "decode-system", "", "GPU system for decode workers (defaults to --system)")
	cmd.Flags().StringVar(&config.BackendName, "backend", BackendSGLang, "Inference backend")
	cmd.Flags().StringVar(&config.BackendVersion, "backend-version", "", "Backend version")
	cmd.Flags().IntVar(&config.Prefix, "prefix", 0, "Prefix cache length")
	cmd.Flags().StringVar(&config.DatabaseMode, "database-mode", DatabaseModeSilicon, "Database mode (SILICON, HYBRID, EMPIRICAL, SOL)")
	cmd.Flags().StringVar(&config.SaveDir, "save-dir", "/tmp/rbg-llm-generate-output", "Directory to save results")

	// Mark required flags
	for _, flag := range []string{"model", "system", "total-gpus", "isl", "osl"} {
		if err := cmd.MarkFlagRequired(flag); err != nil {
			klog.Fatalf("Failed to mark flag %s as required: %v", flag, err)
		}
	}

	return cmd
}

// runRecommender executes the main recommender workflow
func runRecommender(config *TaskConfig) error {
	klog.Info("=== RBG LLM Generate ===")

	// Step 1: Validate configuration
	klog.V(2).Info("Validating configuration...")
	if err := validateConfig(config); err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}
	klog.V(2).Info("Configuration validated successfully")

	// Step 2: Check configurator tool availability
	klog.Info("Checking dependencies...")
	if config.ConfiguratorTool == AIConfigurator {
		// For aiconfigurator, perform version check
		if err := CheckAIConfiguratorAvailabilityWithVersion(); err != nil {
			return err
		}
	} else {
		// For other tools, just check if they exist
		if err := CheckConfiguratorAvailability(config.ConfiguratorTool); err != nil {
			return err
		}
	}

	// Step 3: Execute configurator
	if err := ExecuteConfigurator(config); err != nil {
		return err
	}

	// Step 4: Locate output directory
	klog.Info("Locating generated configurations...")
	outputDir, err := LocateOutputDirectory(config)
	if err != nil {
		return err
	}
	klog.V(1).Infof("Using output directory: %s", outputDir)

	// Step 5: Parse generator configurations
	klog.Info("Parsing AI Configurator output...")
	aggConfig, disaggConfig, err := ParseGeneratorConfigs(outputDir)
	if err != nil {
		return err
	}
	klog.V(2).Info("Parsing completed successfully")

	// Step 6: Generate RBG YAML files
	klog.Info("Generating RBG deployment YAMLs...")

	// Create deployment plans
	disaggPlan := &DeploymentPlan{
		Mode:          DeploymentModeDisagg,
		Config:        disaggConfig,
		OutputPath:    filepath.Join(config.SaveDir, fmt.Sprintf("%s-%s-disagg.yaml", normalizeModelName(config.ModelName), config.BackendName)),
		ModelName:     config.ModelName,
		BackendName:   config.BackendName,
		HuggingFaceID: config.HuggingFaceID,
	}

	aggPlan := &DeploymentPlan{
		Mode:          DeploymentModeAgg,
		Config:        aggConfig,
		OutputPath:    filepath.Join(config.SaveDir, fmt.Sprintf("%s-%s-agg.yaml", normalizeModelName(config.ModelName), config.BackendName)),
		ModelName:     config.ModelName,
		BackendName:   config.BackendName,
		HuggingFaceID: config.HuggingFaceID,
	}

	// Render YAML files
	if err := RenderDeploymentYAML(disaggPlan); err != nil {
		return fmt.Errorf("failed to generate disaggregated mode YAML: %w", err)
	}

	if err := RenderDeploymentYAML(aggPlan); err != nil {
		return fmt.Errorf("failed to generate aggregated mode YAML: %w", err)
	}

	// Step 7: Display results
	displayResults(config, disaggPlan, aggPlan, disaggConfig, aggConfig)

	return nil
}

// validateConfig validates the TaskConfig
func validateConfig(config *TaskConfig) error {
	if strings.TrimSpace(config.ModelName) == "" {
		return fmt.Errorf("--model is required")
	}
	if strings.TrimSpace(config.SystemName) == "" {
		return fmt.Errorf("--system is required")
	}
	if config.TotalGPUs <= 0 {
		return fmt.Errorf("--total-gpus must be greater than 0")
	}

	// Validate latency parameters: at least one of (ttft & tpot) or request-latency must be set
	hasTTFTAndTPOT := config.TTFT > 0 && config.TPOT > 0
	hasRequestLatency := config.RequestLatency > 0
	if !hasTTFTAndTPOT && !hasRequestLatency {
		return fmt.Errorf("latency parameters validation failed: either (--ttft AND --tpot) or --request-latency must be specified with positive values")
	}

	// Validate enum values
	validBackends := map[string]bool{BackendSGLang: true, BackendVLLM: true, BackendTRTLLM: true}
	if !validBackends[config.BackendName] {
		return fmt.Errorf("invalid backend %s, must be one of: sglang, vllm, trtllm", config.BackendName)
	}

	validSystems := map[string]bool{
		"h100_sxm": true, "a100_sxm": true, "b200_sxm": true,
		"gb200_sxm": true, "l40s": true, "h200_sxm": true,
	}
	if !validSystems[config.SystemName] {
		return fmt.Errorf("invalid system %s, must be one of: h100_sxm, a100_sxm, b200_sxm, gb200_sxm, l40s, h200_sxm", config.SystemName)
	}

	validDatabaseModes := map[string]bool{
		DatabaseModeSilicon:   true,
		DatabaseModeHybrid:    true,
		DatabaseModeEmpirical: true,
		DatabaseModeSOL:       true,
	}
	if !validDatabaseModes[config.DatabaseMode] {
		return fmt.Errorf("invalid database-mode %s, must be one of: SILICON, HYBRID, EMPIRICAL, SOL", config.DatabaseMode)
	}

	return nil
}

// displayResults shows the generated deployment plans to the user
func displayResults(config *TaskConfig, disaggPlan, aggPlan *DeploymentPlan, disaggConfig, aggConfig *GeneratorConfig) {
	klog.Info("✓ Successfully generated 2 deployment recommendations:")
	klog.Info("")

	// Disaggregated mode summary
	klog.Info("Plan 1: Prefill-Decode Disaggregated Mode")
	klog.Infof("  File: %s", disaggPlan.OutputPath)
	klog.Info("  Configuration:")

	prefillParams := GetWorkerParams(disaggConfig.Params.Prefill)
	decodeParams := GetWorkerParams(disaggConfig.Params.Decode)

	prefillTotalGPUs := disaggConfig.Workers.PrefillWorkers * prefillParams.TensorParallelSize
	decodeTotalGPUs := disaggConfig.Workers.DecodeWorkers * decodeParams.TensorParallelSize

	klog.Infof("    - Prefill Workers: %d (each using %d GPU)",
		disaggConfig.Workers.PrefillWorkers, prefillParams.TensorParallelSize)
	klog.Infof("    - Decode Workers: %d (each using %d GPU)",
		disaggConfig.Workers.DecodeWorkers, decodeParams.TensorParallelSize)
	klog.Infof("    - Total GPU Usage: %d", prefillTotalGPUs+decodeTotalGPUs)
	klog.Info("")

	// Aggregated mode summary
	klog.Info("Plan 2: Aggregated Mode")
	klog.Infof("  File: %s", aggPlan.OutputPath)
	klog.Info("  Configuration:")

	aggParams := GetWorkerParams(aggConfig.Params.Agg)
	aggTotalGPUs := aggConfig.Workers.AggWorkers * aggParams.TensorParallelSize

	klog.Infof("    - Workers: %d (each using %d GPU)",
		aggConfig.Workers.AggWorkers, aggParams.TensorParallelSize)
	klog.Infof("    - Total GPU Usage: %d", aggTotalGPUs)
	klog.Info("")

	// Deployment instructions
	klog.Info("To deploy, run:")
	klog.Infof("  kubectl apply -f %s", disaggPlan.OutputPath)
	klog.Info("or")
	klog.Infof("  kubectl apply -f %s", aggPlan.OutputPath)
	klog.Info("")
	klog.Infof("Note: Ensure the '%s' PVC exists in your cluster before deploying.", normalizeModelName(config.ModelName))
}

// normalizeModelName converts model name to a valid Kubernetes resource name
func normalizeModelName(name string) string {
	// Convert to lowercase and replace underscores/dots with hyphens
	var sb strings.Builder
	sb.Grow(len(name))
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			sb.WriteRune(c)
		} else if c >= 'A' && c <= 'Z' {
			sb.WriteRune(c + 32) // Convert to lowercase
		} else if c == '_' || c == '.' {
			sb.WriteRune('-')
		}
	}
	return sb.String()
}
