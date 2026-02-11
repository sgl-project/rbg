package main

import "time"

// ExperimentMetadata represents the metadata of a benchmark experiment.
type ExperimentMetadata struct {
	Cmd                     string                 `json:"cmd,omitempty"`
	BenchmarkVersion        string                 `json:"benchmark_version,omitempty"`
	APIBackend              string                 `json:"api_backend,omitempty"`
	AuthConfig              map[string]interface{} `json:"auth_config,omitempty"`
	APIModelName            string                 `json:"api_model_name,omitempty"`
	ServerModelTokenizer    string                 `json:"server_model_tokenizer,omitempty"`
	Model                   string                 `json:"model,omitempty"`
	Task                    string                 `json:"task,omitempty"`
	NumConcurrency          []int                  `json:"num_concurrency,omitempty"`
	BatchSize               []int                  `json:"batch_size,omitempty"`
	IterationType           string                 `json:"iteration_type,omitempty"`
	TrafficScenario         []string               `json:"traffic_scenario,omitempty"`
	AdditionalRequestParams map[string]interface{} `json:"additional_request_params,omitempty"`
	ServerEngine            *string                `json:"server_engine,omitempty"`
	ServerVersion           *string                `json:"server_version,omitempty"`
	ServerGPUType           *string                `json:"server_gpu_type,omitempty"`
	ServerGPUCount          *int                   `json:"server_gpu_count,omitempty"`
	MaxTimePerRunS          int                    `json:"max_time_per_run_s,omitempty"`
	MaxRequestsPerRun       int                    `json:"max_requests_per_run,omitempty"`
	ExperimentFolderName    string                 `json:"experiment_folder_name,omitempty"`
	MetricsTimeUnit         string                 `json:"metrics_time_unit,omitempty"`
	DatasetPath             string                 `json:"dataset_path,omitempty"`
}

// Experiment represents a benchmark experiment with its metadata and files.
type Experiment struct {
	Name        string              `json:"name"`
	Metadata    *ExperimentMetadata `json:"metadata,omitempty"`
	ResultFiles []string            `json:"result_files,omitempty"`
	ImageFiles  []string            `json:"image_files,omitempty"`
	ExcelFiles  []string            `json:"excel_files,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
}

// ExperimentListResponse is the response for listing experiments.
type ExperimentListResponse struct {
	Experiments []Experiment `json:"experiments"`
	Total       int          `json:"total"`
}

// AggregatedMetrics represents the aggregated metrics from a benchmark run.
type AggregatedMetrics struct {
	Scenario                  string           `json:"scenario"`
	NumConcurrency            int              `json:"num_concurrency"`
	BatchSize                 int              `json:"batch_size"`
	IterationType             string           `json:"iteration_type"`
	RunDuration               float64          `json:"run_duration"`
	MeanOutputThroughput      float64          `json:"mean_output_throughput_tokens_per_s"`
	MeanInputThroughput       float64          `json:"mean_input_throughput_tokens_per_s"`
	MeanTotalTokensThroughput float64          `json:"mean_total_tokens_throughput_tokens_per_s"`
	RequestsPerSecond         float64          `json:"requests_per_second"`
	ErrorCodesFrequency       map[string]int   `json:"error_codes_frequency"`
	ErrorRate                 float64          `json:"error_rate"`
	NumErrorRequests          int              `json:"num_error_requests"`
	NumCompletedRequests      int              `json:"num_completed_requests"`
	NumRequests               int              `json:"num_requests"`
	Stats                     map[string]Stats `json:"stats"`
}

// Stats represents statistical metrics.
type Stats struct {
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	Mean   float64 `json:"mean"`
	Stddev float64 `json:"stddev"`
	Sum    float64 `json:"sum"`
	P25    float64 `json:"p25"`
	P50    float64 `json:"p50"`
	P75    float64 `json:"p75"`
	P90    float64 `json:"p90"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
}

// BenchmarkResult represents a single benchmark result file.
type BenchmarkResult struct {
	AggregatedMetrics        AggregatedMetrics         `json:"aggregated_metrics"`
	IndividualRequestMetrics []IndividualRequestMetric `json:"individual_request_metrics,omitempty"`
	TimeUnit                 string                    `json:"_time_unit,omitempty"`
}

// IndividualRequestMetric represents metrics for a single request.
type IndividualRequestMetric struct {
	TTFT                 float64 `json:"ttft"`
	TPOT                 float64 `json:"tpot"`
	E2ELatency           float64 `json:"e2e_latency"`
	OutputLatency        float64 `json:"output_latency"`
	OutputInferenceSpeed float64 `json:"output_inference_speed"`
	NumInputTokens       int     `json:"num_input_tokens"`
	NumOutputTokens      int     `json:"num_output_tokens"`
	TotalTokens          int     `json:"total_tokens"`
	InputThroughput      float64 `json:"input_throughput"`
	OutputThroughput     float64 `json:"output_throughput"`
	ErrorCode            *string `json:"error_code,omitempty"`
	ErrorMessage         *string `json:"error_message,omitempty"`
}

// ResultSummary is a simplified view of a benchmark result for listing.
type ResultSummary struct {
	FileName          string  `json:"file_name"`
	Scenario          string  `json:"scenario"`
	NumConcurrency    int     `json:"num_concurrency"`
	RunDuration       float64 `json:"run_duration"`
	RequestsPerSecond float64 `json:"requests_per_second"`
	MeanTTFT          float64 `json:"mean_ttft"`
	P99TTFT           float64 `json:"p99_ttft"`
	MeanE2ELatency    float64 `json:"mean_e2e_latency"`
	P99E2ELatency     float64 `json:"p99_e2e_latency"`
	OutputThroughput  float64 `json:"output_throughput"`
	ErrorRate         float64 `json:"error_rate"`
	NumRequests       int     `json:"num_requests"`
}

// CompareRequest is the request for comparing experiments.
type CompareRequest struct {
	ExperimentNames []string `json:"experiment_names"`
}

// CompareResponse is the response for comparing experiments.
type CompareResponse struct {
	Experiments []ExperimentCompareData `json:"experiments"`
}

// ExperimentCompareData contains comparison data for a single experiment.
type ExperimentCompareData struct {
	Name     string              `json:"name"`
	Metadata *ExperimentMetadata `json:"metadata,omitempty"`
	Results  []ResultSummary     `json:"results"`
}

// ErrorResponse represents an API error response.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}
