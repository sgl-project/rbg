package benchmark

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildGenAIBenchArgs(t *testing.T) {
	tests := []struct {
		name                 string
		apiBackend           string
		apiBase              string
		apiModelName         string
		modelTokenizer       string
		experimentBaseDir    string
		experimentFolderName string
		opts                 BenchmarkOptions
		expectedContains     []string
		expectedNotContains  []string
	}{
		{
			name:                 "basic args",
			apiBackend:           "sglang",
			apiBase:              "http://my-rbg.default.svc.cluster.local:8080",
			apiModelName:         "my-model",
			modelTokenizer:       "/tokenizer",
			experimentBaseDir:    "/output",
			experimentFolderName: "exp-1",
			opts: BenchmarkOptions{
				task:              "text-to-text",
				maxTimePerRun:     15,
				maxRequestsPerRun: 100,
			},
			expectedContains: []string{
				"benchmark",
				"--api-backend", "sglang",
				"--api-base", "http://my-rbg.default.svc.cluster.local:8080",
				"--api-model-name", "my-model",
				"--model-tokenizer", "/tokenizer",
				"--experiment-base-dir", "/output",
				"--experiment-folder-name", "exp-1",
				"--task", "text-to-text",
				"--max-time-per-run", "15",
				"--max-requests-per-run", "100",
			},
			expectedNotContains: []string{"--api-key", "--traffic-scenario", "--num-concurrency"},
		},
		{
			name:                 "with api key",
			apiBackend:           "vllm",
			apiBase:              "http://localhost:8080",
			apiModelName:         "model",
			modelTokenizer:       "Qwen/Qwen3-8B",
			experimentBaseDir:    "/output",
			experimentFolderName: "exp-2",
			opts: BenchmarkOptions{
				task:              "text-to-text",
				maxTimePerRun:     30,
				maxRequestsPerRun: 200,
				apiKey:            "my-secret-key",
			},
			expectedContains: []string{
				"--api-key", "my-secret-key",
			},
		},
		{
			name:                 "with traffic scenarios",
			apiBackend:           "sglang",
			apiBase:              "http://localhost:8080",
			apiModelName:         "model",
			modelTokenizer:       "/tokenizer",
			experimentBaseDir:    "/output",
			experimentFolderName: "exp-3",
			opts: BenchmarkOptions{
				task:              "text-to-text",
				maxTimePerRun:     15,
				maxRequestsPerRun: 100,
				trafficScenarios:  []string{"D(100,1000)", "D(200,2000)"},
			},
			expectedContains: []string{
				"--traffic-scenario", "D(100,1000)",
				"--traffic-scenario", "D(200,2000)",
			},
		},
		{
			name:                 "with concurrency levels",
			apiBackend:           "sglang",
			apiBase:              "http://localhost:8080",
			apiModelName:         "model",
			modelTokenizer:       "/tokenizer",
			experimentBaseDir:    "/output",
			experimentFolderName: "exp-4",
			opts: BenchmarkOptions{
				task:              "text-to-text",
				maxTimePerRun:     15,
				maxRequestsPerRun: 100,
				numConcurrency:    []int{1, 5, 10},
			},
			expectedContains: []string{
				"--num-concurrency", "1",
				"--num-concurrency", "5",
				"--num-concurrency", "10",
			},
		},
		{
			name:                 "with extra args",
			apiBackend:           "sglang",
			apiBase:              "http://localhost:8080",
			apiModelName:         "model",
			modelTokenizer:       "/tokenizer",
			experimentBaseDir:    "/output",
			experimentFolderName: "exp-5",
			opts: BenchmarkOptions{
				task:              "text-to-text",
				maxTimePerRun:     15,
				maxRequestsPerRun: 100,
				extraArgs:         map[string]string{"custom-key": "custom-value"},
			},
			expectedContains: []string{
				"--custom-key", "custom-value",
			},
		},
		{
			name:                 "empty traffic scenario and zero concurrency are skipped",
			apiBackend:           "sglang",
			apiBase:              "http://localhost:8080",
			apiModelName:         "model",
			modelTokenizer:       "/tokenizer",
			experimentBaseDir:    "/output",
			experimentFolderName: "exp-6",
			opts: BenchmarkOptions{
				task:              "text-to-text",
				maxTimePerRun:     15,
				maxRequestsPerRun: 100,
				trafficScenarios:  []string{"", "D(100,1000)"},
				numConcurrency:    []int{0, 5},
			},
			expectedContains: []string{"--traffic-scenario", "D(100,1000)", "--num-concurrency", "5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore global benchmarkOpts
			old := benchmarkOpts
			benchmarkOpts = tt.opts
			defer func() { benchmarkOpts = old }()

			args := buildGenAIBenchArgs(
				tt.apiBackend, tt.apiBase, tt.apiModelName,
				tt.modelTokenizer, tt.experimentBaseDir, tt.experimentFolderName,
			)

			// Check the first arg is always "benchmark"
			require.NotEmpty(t, args)
			assert.Equal(t, "benchmark", args[0])

			argsStr := joinArgs(args)
			for _, expected := range tt.expectedContains {
				assert.Contains(t, argsStr, expected, "expected args to contain %q", expected)
			}
			for _, notExpected := range tt.expectedNotContains {
				assert.NotContains(t, argsStr, notExpected, "expected args to not contain %q", notExpected)
			}
		})
	}
}

// joinArgs is a helper for readable assertion messages.
func joinArgs(args []string) string {
	result := ""
	for _, a := range args {
		result += a + " "
	}
	return result
}

func TestDeriveJobState(t *testing.T) {
	tests := []struct {
		name     string
		job      *batchv1.Job
		expected JobState
	}{
		{
			name: "succeeded job",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobComplete,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: JobStateSucceeded,
		},
		{
			name: "failed job",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobFailed,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: JobStateFailed,
		},
		{
			name: "running job with active pods",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Active: 1,
				},
			},
			expected: JobStateRunning,
		},
		{
			name: "pending job - no conditions, no active pods",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{},
			},
			expected: JobStatePending,
		},
		{
			name: "complete condition false is not succeeded",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobComplete,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expected: JobStatePending,
		},
		{
			name: "failed condition false with active pods is running",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Active: 2,
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobFailed,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expected: JobStateRunning,
		},
		{
			name: "both complete and failed - complete wins (first in list)",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{
						{
							Type:   batchv1.JobComplete,
							Status: corev1.ConditionTrue,
						},
						{
							Type:   batchv1.JobFailed,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: JobStateSucceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := deriveJobState(tt.job)
			assert.Equal(t, tt.expected, state)
		})
	}
}

func TestBuildBenchmarkJob(t *testing.T) {
	// Save and restore global benchmarkOpts
	old := benchmarkOpts
	defer func() { benchmarkOpts = old }()

	benchmarkOpts = BenchmarkOptions{
		task:              "text-to-text",
		apiKey:            "test-key",
		maxTimePerRun:     15,
		maxRequestsPerRun: 100,
		image:             "my-image:latest",
		cpuRequest:        "1",
		cpuLimit:          "2",
		memoryRequest:     "2Gi",
		memoryLimit:       "4Gi",
		modelTokenizer:    "pvc://tokenizer-pvc/models/qwen",
		experimentBaseDir: "pvc://output-pvc/results",
	}

	t.Run("basic job creation", func(t *testing.T) {
		job, err := buildBenchmarkJob("test-ns", "my-rbg")

		require.NoError(t, err)
		require.NotNil(t, job)

		// Check namespace and labels
		assert.Equal(t, "test-ns", job.Namespace)
		assert.Equal(t, "my-rbg", job.Labels[benchmarkLabelKey])

		// Check job name prefix
		assert.Contains(t, job.Name, "my-rbg-benchmark-")
		assert.LessOrEqual(t, len(job.Name), maxJobNameLength)

		// Check spec
		assert.Equal(t, int32(0), *job.Spec.BackoffLimit)
		assert.Equal(t, int32(3600), *job.Spec.TTLSecondsAfterFinished)
		assert.Equal(t, corev1.RestartPolicyNever, job.Spec.Template.Spec.RestartPolicy)

		// Check container
		require.Len(t, job.Spec.Template.Spec.Containers, 1)
		container := job.Spec.Template.Spec.Containers[0]
		assert.Equal(t, "genai-bench", container.Name)
		assert.Equal(t, "my-image:latest", container.Image)
		assert.Equal(t, []string{"genai-bench"}, container.Command)

		// Check resources
		assert.Equal(t, "1", container.Resources.Requests.Cpu().String())
		assert.Equal(t, "2", container.Resources.Limits.Cpu().String())
		assert.Equal(t, "2Gi", container.Resources.Requests.Memory().String())
		assert.Equal(t, "4Gi", container.Resources.Limits.Memory().String())

		// Check env
		require.Len(t, container.Env, 1)
		assert.Equal(t, "ENABLE_UI", container.Env[0].Name)
		assert.Equal(t, "false", container.Env[0].Value)

		// Check volumes exist (tokenizer + output)
		assert.GreaterOrEqual(t, len(job.Spec.Template.Spec.Volumes), 2)

		// Check template labels
		assert.Equal(t, "my-rbg", job.Spec.Template.Labels[benchmarkLabelKey])
	})

	t.Run("long rbg name is truncated", func(t *testing.T) {
		longName := "this-is-a-very-long-rbg-name-that-exceeds-the-kubernetes-name-limit"
		job, err := buildBenchmarkJob("test-ns", longName)

		require.NoError(t, err)
		require.NotNil(t, job)
		assert.LessOrEqual(t, len(job.Name), maxJobNameLength)
		// The label still holds the full name
		assert.Equal(t, longName, job.Labels[benchmarkLabelKey])
	})

	t.Run("experiment-base-dir must be PVC", func(t *testing.T) {
		benchmarkOpts.experimentBaseDir = "/local/path"
		_, err := buildBenchmarkJob("test-ns", "my-rbg")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--experiment-base-dir must be a PVC path")
	})

	t.Run("invalid tokenizer PVC URI", func(t *testing.T) {
		benchmarkOpts.experimentBaseDir = "pvc://output-pvc/results"
		benchmarkOpts.modelTokenizer = "pvc://"
		_, err := buildBenchmarkJob("test-ns", "my-rbg")
		require.Error(t, err)
	})

	t.Run("default api values", func(t *testing.T) {
		benchmarkOpts.modelTokenizer = "pvc://tokenizer-pvc/models/qwen"
		benchmarkOpts.experimentBaseDir = "pvc://output-pvc/results"
		benchmarkOpts.apiBackend = ""
		benchmarkOpts.apiBase = ""
		benchmarkOpts.apiModelName = ""

		job, err := buildBenchmarkJob("my-ns", "my-rbg")
		require.NoError(t, err)

		// Check args contain default values
		container := job.Spec.Template.Spec.Containers[0]
		args := container.Args
		argsStr := joinArgs(args)
		assert.Contains(t, argsStr, "--api-backend sglang")
		assert.Contains(t, argsStr, "--api-base http://my-rbg.my-ns.svc.cluster.local:8080")
		assert.Contains(t, argsStr, "--api-model-name my-rbg")
	})

	t.Run("experiment folder name defaults to job name", func(t *testing.T) {
		benchmarkOpts.modelTokenizer = "pvc://tokenizer-pvc/"
		benchmarkOpts.experimentBaseDir = "pvc://output-pvc/"
		benchmarkOpts.experimentFolderName = ""

		job, err := buildBenchmarkJob("test-ns", "my-rbg")
		require.NoError(t, err)

		container := job.Spec.Template.Spec.Containers[0]
		argsStr := joinArgs(container.Args)
		// The folder name should contain the job name
		assert.Contains(t, argsStr, "--experiment-folder-name "+job.Name)
	})

	t.Run("custom experiment folder name", func(t *testing.T) {
		benchmarkOpts.modelTokenizer = "pvc://tokenizer-pvc/"
		benchmarkOpts.experimentBaseDir = "pvc://output-pvc/"
		benchmarkOpts.experimentFolderName = "my-custom-exp"

		job, err := buildBenchmarkJob("test-ns", "my-rbg")
		require.NoError(t, err)

		container := job.Spec.Template.Spec.Containers[0]
		argsStr := joinArgs(container.Args)
		assert.Contains(t, argsStr, "--experiment-folder-name my-custom-exp")
	})
}

func TestPrintJobSummary(t *testing.T) {
	t.Run("succeeded job with duration", func(t *testing.T) {
		startTime := metav1.NewTime(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		completionTime := metav1.NewTime(time.Date(2025, 1, 1, 0, 5, 30, 0, time.UTC))

		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-job",
				Namespace: "default",
			},
			Status: batchv1.JobStatus{
				StartTime:      &startTime,
				CompletionTime: &completionTime,
			},
		}

		// Just verify it doesn't panic
		printJobSummary(job, JobStateSucceeded)
	})

	t.Run("failed job with conditions", func(t *testing.T) {
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-job",
				Namespace: "default",
			},
			Status: batchv1.JobStatus{
				Conditions: []batchv1.JobCondition{
					{
						Type:    batchv1.JobFailed,
						Status:  corev1.ConditionTrue,
						Reason:  "BackoffLimitExceeded",
						Message: "Job has reached backoff limit",
					},
				},
			},
		}

		// Just verify it doesn't panic
		printJobSummary(job, JobStateFailed)
	})

	t.Run("pending job without times", func(t *testing.T) {
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-job",
				Namespace: "default",
			},
			Status: batchv1.JobStatus{},
		}

		// Just verify it doesn't panic
		printJobSummary(job, JobStatePending)
	})
}
