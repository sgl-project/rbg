package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"sigs.k8s.io/yaml"

	"sigs.k8s.io/rbgs/cmd/cli/util"
)

const (
	benchmarkLabelKey        = "rbg-benchmark"
	defaultBenchmarkImage    = "todo"
	defaultCPULimit          = "2"
	defaultCPURequest        = "1"
	defaultMemoryLimit       = "2Gi"
	defaultMemoryRequest     = "2Gi"
	defaultTask              = "text-to-text"
	defaultAPIKey            = "rbg"
	defaultMaxTimePerRun     = 15
	defaultMaxRequestsPerRun = 100
)

type BenchmarkOptions struct {
	cf *genericclioptions.ConfigFlags

	task              string
	maxTimePerRun     int
	maxRequestsPerRun int

	trafficScenarios []string
	numConcurrency   []int

	apiBackend   string
	apiBase      string
	apiKey       string
	apiModelName string

	modelTokenizer string

	experimentBaseDir    string
	experimentFolderName string

	extraArgs map[string]string

	image         string
	cpuRequest    string
	cpuLimit      string
	memoryRequest string
	memoryLimit   string

	wait bool
}

// BenchmarkConfig represents the YAML configuration file structure for benchmark options.
// Fields use pointers for optional scalar types so that we can distinguish between
// "not set" and "set to zero value" when merging with CLI flags.
type BenchmarkConfig struct {
	Task              string `json:"task,omitempty"`
	MaxTimePerRun     *int   `json:"maxTimePerRun,omitempty"`
	MaxRequestsPerRun *int   `json:"maxRequestsPerRun,omitempty"`

	TrafficScenarios []string `json:"trafficScenarios,omitempty"`
	NumConcurrency   []int    `json:"numConcurrency,omitempty"`

	APIBackend   string `json:"apiBackend,omitempty"`
	APIBase      string `json:"apiBase,omitempty"`
	APIKey       string `json:"apiKey,omitempty"`
	APIModelName string `json:"apiModelName,omitempty"`

	ModelTokenizer string `json:"modelTokenizer,omitempty"`

	ExperimentBaseDir    string `json:"experimentBaseDir,omitempty"`
	ExperimentFolderName string `json:"experimentFolderName,omitempty"`

	ExtraArgs map[string]string `json:"extraArgs,omitempty"`

	Image         string `json:"image,omitempty"`
	CPURequest    string `json:"cpuRequest,omitempty"`
	CPULimit      string `json:"cpuLimit,omitempty"`
	MemoryRequest string `json:"memoryRequest,omitempty"`
	MemoryLimit   string `json:"memoryLimit,omitempty"`

	Wait *bool `json:"wait,omitempty"`
}

var benchmarkOpts BenchmarkOptions

// NewBenchmarkCmd creates the "llm benchmark" parent command.
func NewBenchmarkCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "benchmark",
		Short: "Manage genai-bench benchmarks for RoleBasedGroup deployments",
	}

	cmd.AddCommand(NewBenchmarkRunCmd(cf))
	cmd.AddCommand(NewBenchmarkListCmd(cf))
	cmd.AddCommand(NewBenchmarkDeleteCmd(cf))
	cmd.AddCommand(NewBenchmarkLogsCmd(cf))
	cmd.AddCommand(NewBenchmarkDashboardCmd(cf))
	return cmd
}

// NewBenchmarkRunCmd creates the "llm benchmark run" command used to start a new benchmark Job.
func NewBenchmarkRunCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	benchmarkOpts = BenchmarkOptions{
		cf:                cf,
		task:              defaultTask,
		apiKey:            defaultAPIKey,
		maxTimePerRun:     defaultMaxTimePerRun,
		maxRequestsPerRun: defaultMaxRequestsPerRun,
		image:             defaultBenchmarkImage,
		cpuRequest:        defaultCPURequest,
		cpuLimit:          defaultCPULimit,
		memoryRequest:     defaultMemoryRequest,
		memoryLimit:       defaultMemoryLimit,
	}

	var configFile string

	cmd := &cobra.Command{
		Use:   "run <rbg-name>",
		Short: "Run a genai-bench benchmark against a RoleBasedGroup",
		Long: `Run a genai-bench benchmark against a RoleBasedGroup.

Parameters can be provided via CLI flags, a YAML config file (--config), or both.
When both are used, CLI flags take precedence over config file values.

Priority: defaults < config file < CLI flags`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rbgName := args[0]
			return runBenchmark(cmd.Context(), rbgName)
		},
	}

	cmd.Flags().StringVarP(&configFile, "config", "f", "",
		"Path to a YAML config file that provides benchmark parameters. "+
			"CLI flags override values from the config file.")

	cmd.Flags().StringVar(&benchmarkOpts.task, "task", benchmarkOpts.task, "Benchmark task type (e.g. text-to-text)")
	cmd.Flags().IntVar(&benchmarkOpts.maxTimePerRun, "max-time-per-run", benchmarkOpts.maxTimePerRun, "Maximum time per benchmark run in seconds")
	cmd.Flags().IntVar(&benchmarkOpts.maxRequestsPerRun, "max-requests-per-run", benchmarkOpts.maxRequestsPerRun, "Maximum number of requests per benchmark run")
	cmd.Flags().StringArrayVar(&benchmarkOpts.trafficScenarios, "traffic-scenario", benchmarkOpts.trafficScenarios, "Traffic scenario definitions (e.g. 'D(100,1000)'), can be specified multiple times")
	cmd.Flags().IntSliceVar(&benchmarkOpts.numConcurrency, "num-concurrency", benchmarkOpts.numConcurrency, "Concurrency levels, can be specified multiple times or comma-separated")

	cmd.Flags().StringVar(&benchmarkOpts.apiBackend, "api-backend", benchmarkOpts.apiBackend, "API backend type (overrides auto-discovered value)")
	cmd.Flags().StringVar(&benchmarkOpts.apiBase, "api-base", benchmarkOpts.apiBase, "Base URL for the model serving API (overrides auto-discovered value)")
	cmd.Flags().StringVar(&benchmarkOpts.apiKey, "api-key", benchmarkOpts.apiKey, "API key used to call the model serving API")
	cmd.Flags().StringVar(&benchmarkOpts.apiModelName, "api-model-name", benchmarkOpts.apiModelName, "Model name used by the backend (overrides auto-discovered value)")

	cmd.Flags().StringVar(&benchmarkOpts.modelTokenizer, "model-tokenizer", benchmarkOpts.modelTokenizer,
		"The tokenizer to use. Can be a Huggingface model name (e.g. Qwen/Qwen3-8B) "+
			"or a PVC path (e.g. pvc://{pvc-name}/ or pvc://{pvc-name}/{sub-path} or pvc://{namespace}:{pvc-name}/ or pvc://{namespace}:{pvc-name}/{sub-path})")

	cmd.Flags().StringVar(&benchmarkOpts.experimentBaseDir, "experiment-base-dir", benchmarkOpts.experimentBaseDir,
		"Base directory for storing experiment results. Must be a PVC path "+
			"(e.g. pvc://{pvc-name}/ or pvc://{pvc-name}/{sub-path} or pvc://{namespace}:{pvc-name}/ or pvc://{namespace}:{pvc-name}/{sub-path})")

	cmd.Flags().StringVar(&benchmarkOpts.experimentFolderName, "experiment-folder-name", benchmarkOpts.experimentFolderName,
		"The name of the folder to save the experiment results. "+
			"Defaults to the generated job name if not specified.")

	cmd.Flags().StringVar(&benchmarkOpts.image, "image", benchmarkOpts.image, "Container image used for benchmark job")
	cmd.Flags().StringVar(&benchmarkOpts.cpuRequest, "cpu-request", benchmarkOpts.cpuRequest, "CPU request for benchmark pod")
	cmd.Flags().StringVar(&benchmarkOpts.cpuLimit, "cpu-limit", benchmarkOpts.cpuLimit, "CPU limit for benchmark pod")
	cmd.Flags().StringVar(&benchmarkOpts.memoryRequest, "memory-request", benchmarkOpts.memoryRequest, "Memory request for benchmark pod")
	cmd.Flags().StringVar(&benchmarkOpts.memoryLimit, "memory-limit", benchmarkOpts.memoryLimit, "Memory limit for benchmark pod")

	cmd.Flags().BoolVar(&benchmarkOpts.wait, "wait", false, "Wait for benchmark to complete and stream logs")

	var extraArgsJSON string
	cmd.Flags().StringVar(&extraArgsJSON, "extra-args", "", `Extra arguments to pass to genai-bench in JSON format (e.g. '{"key1":"value1","key2":"value2"}')`)
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		// Step 1: Load config file if provided and apply values for flags not explicitly set via CLI.
		// Priority: defaults < config file < CLI flags.
		if configFile != "" {
			fileCfg, err := loadBenchmarkConfig(configFile)
			if err != nil {
				return err
			}
			applyConfigToOptions(fileCfg, cmd)
		}

		// Step 2: Parse extra-args JSON (CLI flag takes precedence; config file extraArgs
		// are already merged in applyConfigToOptions).
		if extraArgsJSON != "" {
			parsed := make(map[string]string)
			if err := json.Unmarshal([]byte(extraArgsJSON), &parsed); err != nil {
				return fmt.Errorf("failed to parse --extra-args JSON: %w", err)
			}
			// Merge: CLI extra-args override config file extra-args.
			if benchmarkOpts.extraArgs == nil {
				benchmarkOpts.extraArgs = make(map[string]string)
			}
			for k, v := range parsed {
				benchmarkOpts.extraArgs[k] = v
			}
		}

		// Step 3: Validate required fields (can come from either config file or CLI flags).
		if benchmarkOpts.modelTokenizer == "" {
			return fmt.Errorf("required flag \"model-tokenizer\" not set (provide via --model-tokenizer or --config file)")
		}
		if benchmarkOpts.experimentBaseDir == "" {
			return fmt.Errorf("required flag \"experiment-base-dir\" not set (provide via --experiment-base-dir or --config file)")
		}

		return nil
	}

	return cmd
}

// loadBenchmarkConfig reads and parses a YAML config file into a BenchmarkConfig.
func loadBenchmarkConfig(path string) (*BenchmarkConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var cfg BenchmarkConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", path, err)
	}
	return &cfg, nil
}

// applyConfigToOptions merges values from a BenchmarkConfig into benchmarkOpts.
// Only fields whose corresponding CLI flag was NOT explicitly set by the user are overridden.
// This ensures the priority order: defaults < config file < CLI flags.
func applyConfigToOptions(cfg *BenchmarkConfig, cmd *cobra.Command) {
	setStringOpt(&benchmarkOpts.task, cfg.Task, "task", cmd)
	setIntPtrOpt(&benchmarkOpts.maxTimePerRun, cfg.MaxTimePerRun, "max-time-per-run", cmd)
	setIntPtrOpt(&benchmarkOpts.maxRequestsPerRun, cfg.MaxRequestsPerRun, "max-requests-per-run", cmd)
	setStringSliceOpt(&benchmarkOpts.trafficScenarios, cfg.TrafficScenarios, "traffic-scenario", cmd)
	setIntSliceOpt(&benchmarkOpts.numConcurrency, cfg.NumConcurrency, "num-concurrency", cmd)
	setStringOpt(&benchmarkOpts.apiBackend, cfg.APIBackend, "api-backend", cmd)
	setStringOpt(&benchmarkOpts.apiBase, cfg.APIBase, "api-base", cmd)
	setStringOpt(&benchmarkOpts.apiKey, cfg.APIKey, "api-key", cmd)
	setStringOpt(&benchmarkOpts.apiModelName, cfg.APIModelName, "api-model-name", cmd)
	setStringOpt(&benchmarkOpts.modelTokenizer, cfg.ModelTokenizer, "model-tokenizer", cmd)
	setStringOpt(&benchmarkOpts.experimentBaseDir, cfg.ExperimentBaseDir, "experiment-base-dir", cmd)
	setStringOpt(&benchmarkOpts.experimentFolderName, cfg.ExperimentFolderName, "experiment-folder-name", cmd)
	setStringOpt(&benchmarkOpts.image, cfg.Image, "image", cmd)
	setStringOpt(&benchmarkOpts.cpuRequest, cfg.CPURequest, "cpu-request", cmd)
	setStringOpt(&benchmarkOpts.cpuLimit, cfg.CPULimit, "cpu-limit", cmd)
	setStringOpt(&benchmarkOpts.memoryRequest, cfg.MemoryRequest, "memory-request", cmd)
	setStringOpt(&benchmarkOpts.memoryLimit, cfg.MemoryLimit, "memory-limit", cmd)
	setBoolPtrOpt(&benchmarkOpts.wait, cfg.Wait, "wait", cmd)
	mergeExtraArgs(cfg.ExtraArgs)
}

// setStringOpt applies a config file string value if non-empty and the CLI flag was not explicitly set.
func setStringOpt(dst *string, val, flagName string, cmd *cobra.Command) {
	if val != "" && !cmd.Flags().Changed(flagName) {
		*dst = val
	}
}

// setIntPtrOpt applies a config file *int value if non-nil and the CLI flag was not explicitly set.
func setIntPtrOpt(dst *int, val *int, flagName string, cmd *cobra.Command) {
	if val != nil && !cmd.Flags().Changed(flagName) {
		*dst = *val
	}
}

// setBoolPtrOpt applies a config file *bool value if non-nil and the CLI flag was not explicitly set.
func setBoolPtrOpt(dst *bool, val *bool, flagName string, cmd *cobra.Command) {
	if val != nil && !cmd.Flags().Changed(flagName) {
		*dst = *val
	}
}

// setStringSliceOpt applies a config file string slice if non-empty and the CLI flag was not explicitly set.
func setStringSliceOpt(dst *[]string, val []string, flagName string, cmd *cobra.Command) {
	if len(val) > 0 && !cmd.Flags().Changed(flagName) {
		*dst = val
	}
}

// setIntSliceOpt applies a config file int slice if non-empty and the CLI flag was not explicitly set.
func setIntSliceOpt(dst *[]int, val []int, flagName string, cmd *cobra.Command) {
	if len(val) > 0 && !cmd.Flags().Changed(flagName) {
		*dst = val
	}
}

// mergeExtraArgs merges config file extra args into benchmarkOpts without overwriting existing keys.
func mergeExtraArgs(cfgExtraArgs map[string]string) {
	if len(cfgExtraArgs) == 0 {
		return
	}
	if benchmarkOpts.extraArgs == nil {
		benchmarkOpts.extraArgs = make(map[string]string)
	}
	for k, v := range cfgExtraArgs {
		if _, exists := benchmarkOpts.extraArgs[k]; !exists {
			benchmarkOpts.extraArgs[k] = v
		}
	}
}

// runBenchmark creates a benchmark Job for the given RBG and optionally waits for completion.
func runBenchmark(ctx context.Context, rbgName string) error {
	if benchmarkOpts.cf == nil {
		return fmt.Errorf("kubeconfig flags are not initialized")
	}

	ns := util.GetNamespace(benchmarkOpts.cf)

	// Best-effort: verify the RoleBasedGroup exists before creating the Job
	rbgClient, err := util.GetRBGClient(benchmarkOpts.cf)
	if err != nil {
		return fmt.Errorf("failed to create rbg client: %w", err)
	}
	if _, err := rbgClient.WorkloadsV1alpha1().RoleBasedGroups(ns).Get(ctx, rbgName, metav1.GetOptions{}); err != nil {
		return err
	}

	clientset, err := util.GetK8SClientSet(benchmarkOpts.cf)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	job, err := buildBenchmarkJob(ns, rbgName)
	if err != nil {
		return fmt.Errorf("failed to build benchmark job: %w", err)
	}

	created, err := clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create benchmark job: %w", err)
	}

	fmt.Printf("Created benchmark Job %s in namespace %s for RBG %s\n", created.Name, ns, rbgName)

	if !benchmarkOpts.wait {
		fmt.Printf("Benchmark is running asynchronously. Use \"kubectl rbg llm benchmark list %s\" to check status.\n", rbgName)
		return nil
	}

	state, finalJob, err := streamJobLogs(ctx, clientset, ns, created.Name)
	if err != nil {
		return err
	}

	printJobSummary(finalJob, state)
	return nil
}
