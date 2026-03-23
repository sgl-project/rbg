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
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/rbgs/cmd/cli/config"
	sourceplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/source"
	storageplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/storage"
	"sigs.k8s.io/rbgs/cmd/cli/util"
)

func newPullCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	var (
		revision string
		source   string
		storage  string
		wait     bool
	)

	cmd := &cobra.Command{
		Use:   "pull MODEL_ID",
		Short: "Pull a model from source to storage",
		Long: `Download a model from the configured source to the configured storage.

This command creates a Kubernetes Job that downloads the specified model from a source
(e.g., HuggingFace, ModelScope) to the configured storage (e.g., PVC). The model can
then be used by inference services created with 'kubectl rbg llm run'.

The command requires:
  - A configured source (use 'kubectl rbg llm config add-source' to configure)
  - A configured storage (use 'kubectl rbg llm config add-storage' to configure)

Examples:
  # Pull a model with default settings
  kubectl rbg llm pull Qwen/Qwen3.5-0.8B

  # Pull a specific revision of a model
  kubectl rbg llm pull Qwen/Qwen3.5-0.8B --revision v1.0

  # Pull using a specific source and storage
  kubectl rbg llm pull Qwen/Qwen3.5-0.8B --source huggingface --storage model-pvc

  # Pull without waiting for completion
  kubectl rbg llm pull Qwen/Qwen3.5-0.8B --wait=false`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			modelID := args[0]

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			// Determine source
			sourceName := cfg.CurrentSource
			if source != "" {
				sourceName = source
			}
			if sourceName == "" {
				return fmt.Errorf("no source configured, please run 'kubectl rbg llm config add-source' first")
			}

			sourceCfg, err := cfg.GetSource(sourceName)
			if err != nil {
				return err
			}

			// Determine storage
			storageName := cfg.CurrentStorage
			if storage != "" {
				storageName = storage
			}
			if storageName == "" {
				return fmt.Errorf("no storage configured, please run 'kubectl rbg llm config add-storage' first")
			}

			storageCfg, err := cfg.GetStorage(storageName)
			if err != nil {
				return err
			}

			// Initialize plugins
			sourcePlugin, err := sourceplugin.Get(sourceCfg.Type, sourceCfg.Config)
			if err != nil {
				return fmt.Errorf("failed to initialize source plugin: %w", err)
			}
			if sourcePlugin == nil {
				return fmt.Errorf("unknown source type: %s", sourceCfg.Type)
			}

			storagePlugin, err := storageplugin.Get(storageCfg.Type, storageCfg.Config)
			if err != nil {
				return fmt.Errorf("failed to initialize storage plugin: %w", err)
			}
			if storagePlugin == nil {
				return fmt.Errorf("unknown storage type: %s", storageCfg.Type)
			}

			// Get mount path and construct model path
			mountPath := storagePlugin.MountPath()
			modelPath := mountPath + "/" + sanitizeModelID(modelID) + "/" + sanitizeModelID(revision)

			// Get namespace early for PreMount
			ns := util.GetNamespace(cf)

			// PreMount: create any required resources (e.g., PV, PVC, Secret for OSS)
			ctrlClient, err := util.GetControllerRuntimeClient(cf)
			if err != nil {
				return fmt.Errorf("failed to create controller client: %w", err)
			}
			if err := storagePlugin.PreMount(ctrlClient, storageplugin.PreMountOptions{
				StorageName: storageName,
				Namespace:   ns,
			}); err != nil {
				return fmt.Errorf("failed to prepare storage: %w", err)
			}

			// Generate download template with revision support
			podTemplate, err := sourcePlugin.GenerateTemplateWithRevision(modelID, modelPath, revision)
			if err != nil {
				return fmt.Errorf("failed to generate download template: %w", err)
			}

			// Inject metadata saving logic - wraps the original command to save model info after download
			injectMetadataSave(podTemplate, modelID, revision, modelPath)

			// Mount storage
			if err := storagePlugin.MountStorage(podTemplate); err != nil {
				return fmt.Errorf("failed to mount storage: %w", err)
			}

			// Apply resource limits to the download container
			if len(podTemplate.Spec.Containers) > 0 {
				container := &podTemplate.Spec.Containers[0]
				container.Resources = corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
				}
			}

			// Create the Job
			job := buildPullJob(modelID, podTemplate)

			// Create k8s clientset
			clientset, err := util.GetK8SClientSet(cf)
			if err != nil {
				return fmt.Errorf("failed to create kubernetes clientset: %w", err)
			}

			created, err := clientset.BatchV1().Jobs(ns).Create(context.Background(), job, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create job: %w", err)
			}

			fmt.Printf("Created pull Job %s in namespace %s\n", created.Name, ns)
			fmt.Printf("Model: %s\n", modelID)
			fmt.Printf("Source: %s\n", sourceName)
			fmt.Printf("Storage: %s\n", storageName)
			fmt.Printf("Revision: %s\n", revision)
			fmt.Println()

			if !wait {
				fmt.Printf("Model pull is running asynchronously. Check job status with: kubectl logs job/%s -n %s\n", created.Name, ns)
				return nil
			}

			// Wait for job completion
			state, finalJob, err := waitForJobCompletionWithProgress(context.Background(), clientset, ns, created.Name, "model download")
			fmt.Println() // New line after progress indicator
			if err != nil {
				return err
			}

			printJobSummary(finalJob, state)
			return nil
		},
	}

	cmd.Flags().StringVar(&revision, "revision", "main", "Model revision to download")
	cmd.Flags().StringVar(&source, "source", "", "Source to use (overrides default)")
	cmd.Flags().StringVar(&storage, "storage", "", "Storage to use (overrides default)")
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait for the pull job to complete and stream logs")

	return cmd
}

// buildPullJob creates a Job from the pod template
func buildPullJob(modelID string, podTemplate *corev1.PodTemplateSpec) *batchv1.Job {
	timestamp := time.Now().Unix()
	sanitizedID := sanitizeModelID(modelID)
	jobName := fmt.Sprintf("pull-%s-%d", sanitizedID, timestamp)

	labels := map[string]string{
		"rbg-pull-job": "true",
		"rbg-model-id": sanitizedID,
	}

	// Model download can take a long time; set appropriate limits.
	backoffLimit := DefaultPullBackoffLimit
	activeDeadlineSeconds := DefaultPullActiveDeadlineSeconds
	ttlSecondsAfterFinished := DefaultPullTTLSecondsAfterFinished

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:   jobName,
			Labels: labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			ActiveDeadlineSeconds:   &activeDeadlineSeconds,
			TTLSecondsAfterFinished: &ttlSecondsAfterFinished,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: podTemplate.Spec,
			},
		},
	}
}

// waitForJobCompletionWithProgress waits for job completion with animated progress indicator
func waitForJobCompletionWithProgress(ctx context.Context, clientset *kubernetes.Clientset, namespace, jobName string, jobDescription string) (JobState, *batchv1.Job, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	dots := 0
	animate := func() {
		dots = (dots % 3) + 1
		// Use \r to return to start of line, then clear to end of line with \033[K
		fmt.Printf("\rWaiting for %s to complete %s\033[K", jobDescription, strings.Repeat(".", dots))
	}

	for {
		select {
		case <-ctx.Done():
			fmt.Println()
			return JobStatePending, nil, ctx.Err()
		case <-ticker.C:
			animate()
			job, err := clientset.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
			if err != nil {
				fmt.Println()
				return JobStatePending, nil, fmt.Errorf("failed to get job: %w", err)
			}

			state := deriveJobState(job)
			switch state {
			case JobStateSucceeded, JobStateFailed:
				return state, job, nil
			}
		}
	}
}

// JobState represents the state of a job
type JobState string

const (
	JobStatePending   JobState = "Pending"
	JobStateRunning   JobState = "Running"
	JobStateSucceeded JobState = "Succeeded"
	JobStateFailed    JobState = "Failed"

	// DefaultPull* constants control Job behavior for model download jobs.
	DefaultPullBackoffLimit            = int32(3)     // retry up to 3 times on failure
	DefaultPullActiveDeadlineSeconds   = int64(7200)  // 2 hours max runtime for large models
	DefaultPullTTLSecondsAfterFinished = int32(86400) // keep job 24 hours after completion
)

// deriveJobState maps a Job status to a high-level JobState
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

// printJobSummary prints a concise summary of the Job state
func printJobSummary(job *batchv1.Job, state JobState) {
	fmt.Printf("Pull Job %s in namespace %s finished with state: %s\n", job.Name, job.Namespace, state)

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

// injectMetadataSave wraps the container command to save model metadata after successful download.
// This is done generically for all source plugins by intercepting the container's command/args.
//
// It handles two cases:
// 1. Container already uses /bin/sh -c: directly append metadata save to the shell command
// 2. Container uses direct binary execution: wrap with shell and properly escape all arguments
func injectMetadataSave(podTemplate *corev1.PodTemplateSpec, modelID, revision, modelPath string) {
	if len(podTemplate.Spec.Containers) == 0 {
		return
	}

	container := &podTemplate.Spec.Containers[0]

	// Generate timestamp inside the container using shell command
	timestampCmd := "$(date -u +%Y-%m-%dT%H:%M:%SZ)"

	// Build the metadata JSON using proper JSON encoding to prevent injection
	// Note: downloadedAt is generated inside the container after download completes
	// to ensure accurate timing, not at CLI execution time
	// modelID and revision are JSON-escaped to prevent injection
	modelIDEscaped := jsonSafeString(modelID)
	revisionEscaped := jsonSafeString(revision)
	metadataJSON := fmt.Sprintf(`{"modelID":%s,"revision":%s,"downloadedAt":"%s"}`,
		modelIDEscaped, revisionEscaped, timestampCmd)

	// Case 1: Container already uses /bin/sh -c, directly append to the command
	if len(container.Command) >= 2 && container.Command[0] == "/bin/sh" && container.Command[1] == "-c" {
		if len(container.Args) > 0 {
			originalCmd := container.Args[0]
			// Use printf instead of echo to prevent flag injection (e.g. echo -e, echo -n)
			container.Args[0] = fmt.Sprintf(`%s && printf '%%s\n' %s > %s`,
				originalCmd, shellEscape(metadataJSON), shellEscape(modelPath+"/.rbg-metadata.json"))
		}
		return
	}

	// Case 2: Container uses direct binary execution (e.g., huggingface-cli)
	// Need to wrap with shell and properly escape all arguments
	var fullCmd strings.Builder

	// Add the main command (properly escaped)
	if len(container.Command) > 0 {
		fullCmd.WriteString(shellEscape(container.Command[0]))
	}

	// Add all arguments (properly escaped)
	for _, arg := range container.Args {
		fullCmd.WriteString(" ")
		fullCmd.WriteString(shellEscape(arg))
	}

	// Build the wrapped command with metadata save
	// Use printf instead of echo to prevent flag injection (e.g. echo -e, echo -n)
	wrappedCmd := fmt.Sprintf(`%s && printf '%%s\n' %s > %s`,
		fullCmd.String(), shellEscape(metadataJSON), shellEscape(modelPath+"/.rbg-metadata.json"))

	container.Command = []string{"/bin/sh", "-c"}
	container.Args = []string{wrappedCmd}
}

// jsonSafeString escapes a string for safe inclusion in JSON.
// It returns the string wrapped in quotes with all special characters properly escaped.
func jsonSafeString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// shellEscape properly escapes a string for safe use in shell commands.
// If the string contains only safe characters (alphanumeric, _, ., /, -, :, =), it's returned as-is.
// Otherwise, it's wrapped in single quotes with internal single quotes escaped.
var safeShellChars = regexp.MustCompile(`^[a-zA-Z0-9_./:@=-]+$`)

func shellEscape(s string) string {
	if safeShellChars.MatchString(s) {
		return s
	}
	// Escape single quotes by replacing ' with '\''
	escaped := strings.ReplaceAll(s, "'", "'\"'\"'")
	return "'" + escaped + "'"
}
