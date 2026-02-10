package benchmark

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

func TestNewBenchmarkCmd(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewBenchmarkCmd(cf)

	assert.Equal(t, "benchmark", cmd.Use)
	assert.NotEmpty(t, cmd.Short)

	// Check that all subcommands are registered
	subCmds := cmd.Commands()
	subCmdNames := make(map[string]bool)
	for _, sub := range subCmds {
		subCmdNames[sub.Name()] = true
	}

	assert.True(t, subCmdNames["run"], "should have 'run' subcommand")
	assert.True(t, subCmdNames["list"], "should have 'list' subcommand")
	assert.True(t, subCmdNames["delete"], "should have 'delete' subcommand")
	assert.True(t, subCmdNames["logs"], "should have 'logs' subcommand")
	assert.True(t, subCmdNames["dashboard"], "should have 'dashboard' subcommand")
}

func TestNewBenchmarkRunCmd(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewBenchmarkRunCmd(cf)

	assert.Equal(t, "run <rbg-name>", cmd.Use)

	// Check important flags exist
	expectedFlags := []string{
		"config",
		"task", "max-time-per-run", "max-requests-per-run",
		"traffic-scenario", "num-concurrency",
		"api-backend", "api-base", "api-key", "api-model-name",
		"model-tokenizer", "experiment-base-dir", "experiment-folder-name",
		"image", "cpu-request", "cpu-limit", "memory-request", "memory-limit",
		"wait", "extra-args",
	}

	for _, flagName := range expectedFlags {
		f := cmd.Flags().Lookup(flagName)
		assert.NotNil(t, f, "flag %q should exist", flagName)
	}

	// Check required flags
	f := cmd.Flags().Lookup("model-tokenizer")
	require.NotNil(t, f)

	f = cmd.Flags().Lookup("experiment-base-dir")
	require.NotNil(t, f)

	// Verify default values
	assert.Equal(t, defaultTask, cmd.Flags().Lookup("task").DefValue)
	assert.Equal(t, "15", cmd.Flags().Lookup("max-time-per-run").DefValue)
	assert.Equal(t, "100", cmd.Flags().Lookup("max-requests-per-run").DefValue)
	assert.Equal(t, defaultAPIKey, cmd.Flags().Lookup("api-key").DefValue)

	// Verify config flag has short form
	f = cmd.Flags().ShorthandLookup("f")
	require.NotNil(t, f)
	assert.Equal(t, "config", f.Name)
}

func TestLoadBenchmarkConfig(t *testing.T) {
	t.Run("full config", func(t *testing.T) {
		content := `
task: text-to-text
maxTimePerRun: 30
maxRequestsPerRun: 200
trafficScenarios:
  - "D(100,1000)"
  - "D(200,2000)"
numConcurrency:
  - 1
  - 5
  - 10
apiBackend: sglang
apiBase: http://localhost:8080
apiKey: my-key
apiModelName: my-model
modelTokenizer: Qwen/Qwen3-8B
experimentBaseDir: pvc://output-pvc/results
experimentFolderName: my-experiment
image: my-image:latest
cpuRequest: "2"
cpuLimit: "4"
memoryRequest: 4Gi
memoryLimit: 8Gi
wait: true
extraArgs:
  custom-key: custom-value
`
		tmpFile := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(tmpFile, []byte(content), 0644))

		cfg, err := loadBenchmarkConfig(tmpFile)
		require.NoError(t, err)

		assert.Equal(t, "text-to-text", cfg.Task)
		assert.Equal(t, 30, *cfg.MaxTimePerRun)
		assert.Equal(t, 200, *cfg.MaxRequestsPerRun)
		assert.Equal(t, []string{"D(100,1000)", "D(200,2000)"}, cfg.TrafficScenarios)
		assert.Equal(t, []int{1, 5, 10}, cfg.NumConcurrency)
		assert.Equal(t, "sglang", cfg.APIBackend)
		assert.Equal(t, "http://localhost:8080", cfg.APIBase)
		assert.Equal(t, "my-key", cfg.APIKey)
		assert.Equal(t, "my-model", cfg.APIModelName)
		assert.Equal(t, "Qwen/Qwen3-8B", cfg.ModelTokenizer)
		assert.Equal(t, "pvc://output-pvc/results", cfg.ExperimentBaseDir)
		assert.Equal(t, "my-experiment", cfg.ExperimentFolderName)
		assert.Equal(t, "my-image:latest", cfg.Image)
		assert.Equal(t, "2", cfg.CPURequest)
		assert.Equal(t, "4", cfg.CPULimit)
		assert.Equal(t, "4Gi", cfg.MemoryRequest)
		assert.Equal(t, "8Gi", cfg.MemoryLimit)
		require.NotNil(t, cfg.Wait)
		assert.True(t, *cfg.Wait)
		assert.Equal(t, map[string]string{"custom-key": "custom-value"}, cfg.ExtraArgs)
	})

	t.Run("partial config", func(t *testing.T) {
		content := `
modelTokenizer: Qwen/Qwen3-8B
experimentBaseDir: pvc://output-pvc/results
`
		tmpFile := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(tmpFile, []byte(content), 0644))

		cfg, err := loadBenchmarkConfig(tmpFile)
		require.NoError(t, err)

		assert.Equal(t, "Qwen/Qwen3-8B", cfg.ModelTokenizer)
		assert.Equal(t, "pvc://output-pvc/results", cfg.ExperimentBaseDir)
		// Unset fields should remain zero-value
		assert.Empty(t, cfg.Task)
		assert.Nil(t, cfg.MaxTimePerRun)
		assert.Nil(t, cfg.Wait)
	})

	t.Run("nonexistent file returns error", func(t *testing.T) {
		_, err := loadBenchmarkConfig("/nonexistent/path.yaml")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read config file")
	})

	t.Run("invalid yaml returns error", func(t *testing.T) {
		tmpFile := filepath.Join(t.TempDir(), "bad.yaml")
		require.NoError(t, os.WriteFile(tmpFile, []byte("{{invalid"), 0644))

		_, err := loadBenchmarkConfig(tmpFile)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse config file")
	})
}

func TestApplyConfigToOptions(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)

	t.Run("config values applied when no CLI flags set", func(t *testing.T) {
		cmd := NewBenchmarkRunCmd(cf)
		// Simulate: no flags explicitly set (just defaults)

		maxTime := 60
		maxReqs := 500
		waitVal := true
		cfg := &BenchmarkConfig{
			Task:              "image-to-text",
			MaxTimePerRun:     &maxTime,
			MaxRequestsPerRun: &maxReqs,
			APIBackend:        "vllm",
			APIBase:           "http://my-api:9090",
			APIKey:            "file-key",
			APIModelName:      "file-model",
			ModelTokenizer:    "Qwen/Qwen3-8B",
			ExperimentBaseDir: "pvc://exp-pvc/",
			Image:             "file-image:v1",
			CPURequest:        "4",
			CPULimit:          "8",
			MemoryRequest:     "8Gi",
			MemoryLimit:       "16Gi",
			Wait:              &waitVal,
		}

		applyConfigToOptions(cfg, cmd)

		assert.Equal(t, "image-to-text", benchmarkOpts.task)
		assert.Equal(t, 60, benchmarkOpts.maxTimePerRun)
		assert.Equal(t, 500, benchmarkOpts.maxRequestsPerRun)
		assert.Equal(t, "vllm", benchmarkOpts.apiBackend)
		assert.Equal(t, "http://my-api:9090", benchmarkOpts.apiBase)
		assert.Equal(t, "file-key", benchmarkOpts.apiKey)
		assert.Equal(t, "file-model", benchmarkOpts.apiModelName)
		assert.Equal(t, "Qwen/Qwen3-8B", benchmarkOpts.modelTokenizer)
		assert.Equal(t, "pvc://exp-pvc/", benchmarkOpts.experimentBaseDir)
		assert.Equal(t, "file-image:v1", benchmarkOpts.image)
		assert.Equal(t, "4", benchmarkOpts.cpuRequest)
		assert.Equal(t, "8", benchmarkOpts.cpuLimit)
		assert.Equal(t, "8Gi", benchmarkOpts.memoryRequest)
		assert.Equal(t, "16Gi", benchmarkOpts.memoryLimit)
		assert.True(t, benchmarkOpts.wait)
	})

	t.Run("CLI flags override config values", func(t *testing.T) {
		cmd := NewBenchmarkRunCmd(cf)
		// Simulate: user explicitly sets --task via CLI
		require.NoError(t, cmd.Flags().Set("task", "cli-task"))
		require.NoError(t, cmd.Flags().Set("api-key", "cli-key"))

		cfg := &BenchmarkConfig{
			Task:   "file-task",
			APIKey: "file-key",
			Image:  "file-image:v2",
		}

		applyConfigToOptions(cfg, cmd)

		// CLI flags should win
		assert.Equal(t, "cli-task", benchmarkOpts.task)
		assert.Equal(t, "cli-key", benchmarkOpts.apiKey)
		// Config file should apply for unchanged flags
		assert.Equal(t, "file-image:v2", benchmarkOpts.image)
	})

	t.Run("extraArgs merge from config", func(t *testing.T) {
		_ = NewBenchmarkRunCmd(cf)

		cfg := &BenchmarkConfig{
			ExtraArgs: map[string]string{
				"from-file": "file-val",
			},
		}

		benchmarkOpts.extraArgs = nil
		applyConfigToOptions(cfg, nil)

		assert.Equal(t, "file-val", benchmarkOpts.extraArgs["from-file"])
	})
}

func TestNewBenchmarkListCmd(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewBenchmarkListCmd(cf)

	assert.Equal(t, "list <rbg-name>", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

func TestNewBenchmarkDeleteCmd(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewBenchmarkDeleteCmd(cf)

	assert.Equal(t, "delete <rbg-name>", cmd.Use)

	// Check job flag exists
	f := cmd.Flags().Lookup("job")
	assert.NotNil(t, f)
}

func TestNewBenchmarkLogsCmd(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewBenchmarkLogsCmd(cf)

	assert.Equal(t, "logs <rbg-name>", cmd.Use)

	// Check flags
	f := cmd.Flags().Lookup("job")
	assert.NotNil(t, f)

	f = cmd.Flags().Lookup("follow")
	assert.NotNil(t, f)
	assert.Equal(t, "true", f.DefValue)
}

func TestNewBenchmarkDashboardCmd(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewBenchmarkDashboardCmd(cf)

	assert.Equal(t, "dashboard", cmd.Use)
	assert.NotEmpty(t, cmd.Long)
	assert.NotEmpty(t, cmd.Example)

	// Check flags
	expectedFlags := []string{
		"experiment-base-dir", "port", "open-browser", "image",
	}

	for _, flagName := range expectedFlags {
		f := cmd.Flags().Lookup(flagName)
		assert.NotNil(t, f, "flag %q should exist", flagName)
	}

	// Check default values
	assert.Equal(t, "18888", cmd.Flags().Lookup("port").DefValue)
	assert.Equal(t, "true", cmd.Flags().Lookup("open-browser").DefValue)
}
