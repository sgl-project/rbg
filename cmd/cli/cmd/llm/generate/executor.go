package generate

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
)

// ExecuteConfigurator runs the configurator command with the given configuration
func ExecuteConfigurator(config *TaskConfig) error {
	toolName := config.ConfiguratorTool
	if toolName == "" {
		toolName = AIConfigurator
	}

	// For aiconfigurator, use the specific implementation
	if toolName == AIConfigurator {
		return ExecuteAIConfigurator(config)
	}

	// For other tools, use a generic approach
	klog.Warningf("Using generic configurator execution for tool: %s", toolName)
	klog.Warning("Custom configurator tools may require different parameter formats")

	args := buildGenericConfiguratorCommand(config)
	klog.V(2).Infof("Executing %s with args: %v", toolName, args)

	cmd := exec.Command(toolName, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	klog.Infof("Running configurator tool: %s", toolName)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s execution failed: %w\nPlease check the error output above", toolName, err)
	}

	klog.Infof("✓ %s optimization completed successfully", toolName)
	return nil
}

// ExecuteAIConfigurator runs the aiconfigurator command with the given configuration
func ExecuteAIConfigurator(config *TaskConfig) error {
	args := buildAIConfiguratorCommand(config)

	klog.V(2).Infof("Executing aiconfigurator with args: %v", args)

	cmd := exec.Command(AIConfigurator, args...)

	// Set output to stdout/stderr for real-time feedback
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	klog.V(2).Info("=== Executing aiconfigurator command ===")
	klog.V(2).Infof("aiconfigurator %s", strings.Join(args, " "))
	klog.V(2).Info("========================================")

	klog.Info("Running AI Configurator optimization... This may take a few seconds.")

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("aiconfigurator execution failed: %w\nPlease check the error output above", err)
	}

	klog.Info("✓ AI Configurator optimization completed successfully")
	return nil
}

// buildAIConfiguratorCommand constructs the aiconfigurator CLI command from TaskConfig
func buildAIConfiguratorCommand(config *TaskConfig) []string {
	args := []string{"cli", "default"}

	// Required parameters
	args = append(args, "--model", config.ModelName)
	args = append(args, "--system", config.SystemName)
	args = append(args, "--total_gpus", strconv.Itoa(config.TotalGPUs))
	args = append(args, "--backend", config.BackendName)
	args = append(args, "--isl", strconv.Itoa(config.ISL))
	args = append(args, "--osl", strconv.Itoa(config.OSL))
	args = append(args, "--ttft", strconv.FormatFloat(config.TTFT, 'f', -1, 64))
	args = append(args, "--tpot", strconv.FormatFloat(config.TPOT, 'f', -1, 64))
	args = append(args, "--save_dir", config.SaveDir)
	args = append(args, "--database_mode", config.DatabaseMode)

	// Optional parameters
	if config.HuggingFaceID != "" {
		args = append(args, "--hf_id", config.HuggingFaceID)
	}

	if config.DecodeSystemName != "" && config.DecodeSystemName != config.SystemName {
		args = append(args, "--decode_system", config.DecodeSystemName)
	}

	if config.BackendVersion != "" && config.BackendVersion != "latest" {
		args = append(args, "--backend_version", config.BackendVersion)
	}

	if config.Prefix > 0 {
		args = append(args, "--prefix", strconv.Itoa(config.Prefix))
	}

	if config.RequestLatency > 0 {
		args = append(args, "--request_latency", strconv.FormatFloat(config.RequestLatency, 'f', -1, 64))
	}

	// Add extra arguments
	for key, value := range config.ExtraArgs {
		args = append(args, fmt.Sprintf("--%s", key), value)
	}

	return args
}

// buildGenericConfiguratorCommand constructs a generic configurator command from TaskConfig
// This provides a best-effort mapping of common parameters
func buildGenericConfiguratorCommand(config *TaskConfig) []string {
	args := []string{}

	// Map common parameters with standard naming
	if config.ModelName != "" {
		args = append(args, "--model", config.ModelName)
	}
	if config.SystemName != "" {
		args = append(args, "--system", config.SystemName)
	}
	if config.TotalGPUs > 0 {
		args = append(args, "--total-gpus", strconv.Itoa(config.TotalGPUs))
	}
	if config.BackendName != "" {
		args = append(args, "--backend", config.BackendName)
	}
	if config.SaveDir != "" {
		args = append(args, "--save-dir", config.SaveDir)
	}

	// Add all extra arguments
	for key, value := range config.ExtraArgs {
		args = append(args, fmt.Sprintf("--%s", key), value)
	}

	return args
}
