package benchmark

import (
	"fmt"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// maxJobNameLength is the maximum length of a Kubernetes resource name.
	maxJobNameLength = 63
)

type JobState string

const (
	JobStatePending   JobState = "Pending"
	JobStateRunning   JobState = "Running"
	JobStateSucceeded JobState = "Succeeded"
	JobStateFailed    JobState = "Failed"
)

// buildBenchmarkJob builds the Kubernetes Job spec for running genai-bench.
func buildBenchmarkJob(namespace, rbgName string) (*batchv1.Job, error) {
	timestamp := time.Now().Unix()
	suffix := fmt.Sprintf("-benchmark-%d", timestamp)

	// Truncate rbgName if the resulting job name would exceed the max length.
	name := rbgName
	if len(name)+len(suffix) > maxJobNameLength {
		name = name[:maxJobNameLength-len(suffix)]
	}
	jobName := name + suffix

	labels := map[string]string{
		benchmarkLabelKey: rbgName,
	}

	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(benchmarkOpts.cpuRequest),
			corev1.ResourceMemory: resource.MustParse(benchmarkOpts.memoryRequest),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(benchmarkOpts.cpuLimit),
			corev1.ResourceMemory: resource.MustParse(benchmarkOpts.memoryLimit),
		},
	}

	apiBackend := benchmarkOpts.apiBackend
	if apiBackend == "" {
		apiBackend = "sglang"
	}
	apiBase := benchmarkOpts.apiBase
	if apiBase == "" {
		apiBase = fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", rbgName, namespace)
	}
	apiModelName := benchmarkOpts.apiModelName
	if apiModelName == "" {
		apiModelName = rbgName
	}

	if benchmarkOpts.task == "" {
		benchmarkOpts.task = defaultTask
	}

	// Build tokenizer configuration (value, volumes, and mounts)
	tokenizerValue, volumes, volumeMounts, err := buildTokenizerConfig(benchmarkOpts.modelTokenizer)
	if err != nil {
		return nil, err
	}

	// Build output directory configuration (must be PVC)
	if !isPVCStorageURI(benchmarkOpts.experimentBaseDir) {
		return nil, fmt.Errorf("--experiment-base-dir must be a PVC path (e.g. pvc://{pvc-name}/{sub-path})")
	}
	outputVol, outputMount, err := buildOutputConfig(benchmarkOpts.experimentBaseDir)
	if err != nil {
		return nil, err
	}
	volumes = append(volumes, outputVol)
	volumeMounts = append(volumeMounts, outputMount)

	// Use jobName as default experiment folder name if not specified
	experimentFolderName := benchmarkOpts.experimentFolderName
	if experimentFolderName == "" {
		experimentFolderName = jobName
	}

	args := buildGenAIBenchArgs(apiBackend, apiBase, apiModelName, tokenizerValue, outputMountPath, experimentFolderName)

	container := corev1.Container{
		Name:         "genai-bench",
		Image:        benchmarkOpts.image,
		Command:      []string{"genai-bench"},
		Args:         args,
		VolumeMounts: volumeMounts,
		Env: []corev1.EnvVar{
			{Name: "ENABLE_UI", Value: "false"},
		},
		Resources: resources,
	}

	podSpec := corev1.PodSpec{
		Containers:    []corev1.Container{container},
		Volumes:       volumes,
		RestartPolicy: corev1.RestartPolicyNever,
	}

	backoffLimit := int32(0)
	ttlSecondsAfterFinished := int32(3600)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttlSecondsAfterFinished,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: podSpec,
			},
		},
	}, nil
}

// buildGenAIBenchArgs constructs the argument list for genai-bench benchmark subcommand.
func buildGenAIBenchArgs(apiBackend, apiBase, apiModelName, modelTokenizer, experimentBaseDir, experimentFolderName string) []string {
	args := []string{
		"benchmark",
		"--api-backend", apiBackend,
		"--api-base", apiBase,
		"--api-model-name", apiModelName,
		"--model-tokenizer", modelTokenizer,
		"--experiment-base-dir", experimentBaseDir,
		"--experiment-folder-name", experimentFolderName,
		"--task", benchmarkOpts.task,
		"--max-time-per-run", strconv.Itoa(benchmarkOpts.maxTimePerRun),
		"--max-requests-per-run", strconv.Itoa(benchmarkOpts.maxRequestsPerRun),
	}

	if benchmarkOpts.apiKey != "" {
		args = append(args, "--api-key", benchmarkOpts.apiKey)
	}

	for _, s := range benchmarkOpts.trafficScenarios {
		if s != "" {
			args = append(args, "--traffic-scenario", s)
		}
	}

	for _, c := range benchmarkOpts.numConcurrency {
		if c > 0 {
			args = append(args, "--num-concurrency", strconv.Itoa(c))
		}
	}

	// Append extra arguments
	for k, v := range benchmarkOpts.extraArgs {
		args = append(args, "--"+k, v)
	}

	return args
}

// deriveJobState maps a Job status to a high-level JobState.
func deriveJobState(job *batchv1.Job) JobState {
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
			return JobStateSucceeded
		}
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			return JobStateFailed
		}
	}

	if job.Status.Active > 0 {
		return JobStateRunning
	}

	return JobStatePending
}

// printJobSummary prints a concise summary of the Job state to the user.
func printJobSummary(job *batchv1.Job, state JobState) {
	fmt.Printf("Benchmark Job %s in namespace %s finished with state: %s\n", job.Name, job.Namespace, state)

	if state == JobStateFailed {
		for _, cond := range job.Status.Conditions {
			if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
				fmt.Printf("Reason: %s, Message: %s\n", cond.Reason, cond.Message)
				break
			}
		}
	}

	if job.Status.StartTime != nil && job.Status.CompletionTime != nil {
		duration := job.Status.CompletionTime.Sub(job.Status.StartTime.Time)
		fmt.Printf("Duration: %s\n", duration.Round(time.Second))
	}
}
