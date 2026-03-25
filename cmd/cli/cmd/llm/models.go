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
	"time"

	"github.com/spf13/cobra"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/rbgs/cmd/cli/config"
	storageplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/storage"
	"sigs.k8s.io/rbgs/cmd/cli/util"
)

func newModelsCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	var (
		storage string
	)

	cmd := &cobra.Command{
		Use:   "models",
		Short: "List downloaded models in storage",
		Long: `List all downloaded models from the configured storage.

This command creates a Kubernetes Job that scans the configured storage and lists
all models that have been downloaded using 'kubectl rbg llm pull'. It displays
the model ID, revision, and download timestamp for each model found.

The command requires:
  - A configured storage (use 'kubectl rbg llm config add-storage' to configure)

Examples:
  # List models in the default storage
  kubectl rbg llm models

  # List models in a specific storage
  kubectl rbg llm models --storage my-pvc
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
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

			storagePlugin, err := storageplugin.Get(storageCfg.Type, storageCfg.Config)
			if err != nil {
				return fmt.Errorf("failed to initialize storage plugin: %w", err)
			}
			if storagePlugin == nil {
				return fmt.Errorf("unknown storage type: %s", storageCfg.Type)
			}

			// Get namespace and create client
			ns := util.GetNamespace(cf)
			clientset, err := util.GetK8SClientSet(cf)
			if err != nil {
				return fmt.Errorf("failed to create kubernetes clientset: %w", err)
			}

			// Create a job to scan storage and list models
			job := buildListModelsJob(storagePlugin.MountPath())

			// Mount storage (provisions resources for OSS and adds volumes/mounts)
			ctrlClient, err := util.GetControllerRuntimeClient(cf)
			if err != nil {
				return fmt.Errorf("failed to create controller client: %w", err)
			}
			podTemplate := &job.Spec.Template
			if err := storagePlugin.MountStorage(podTemplate, storageplugin.MountOptions{
				Client:      ctrlClient,
				StorageName: storageName,
				Namespace:   ns,
			}); err != nil {
				return fmt.Errorf("failed to mount storage: %w", err)
			}

			created, err := clientset.BatchV1().Jobs(ns).Create(context.Background(), job, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create job: %w", err)
			}

			// Wait for job completion
			fmt.Printf("Scanning storage for models")
			state, _, err := waitForJobCompletionWithProgress(context.Background(), clientset, ns, created.Name, "model listing")
			fmt.Println()
			if err != nil {
				return err
			}

			if state == JobStateFailed {
				return fmt.Errorf("model listing job failed")
			}

			// Get output from the job's pod
			models, err := getJobOutput(context.Background(), clientset, ns, created.Name)
			if err != nil {
				return fmt.Errorf("failed to get job output: %w", err)
			}

			// Print results
			printModelsList(models, storageName)
			return nil
		},
	}

	cmd.Flags().StringVar(&storage, "storage", "", "Storage to use (overrides default)")

	return cmd
}

// buildListModelsJob creates a Job to scan storage and list models
func buildListModelsJob(mountPath string) *batchv1.Job {
	timestamp := time.Now().Unix()
	jobName := fmt.Sprintf("list-models-%d", timestamp)

	labels := map[string]string{
		"rbg-list-job": "true",
	}

	// Script to scan storage and extract metadata
	// Note: mountPath is directly embedded in the script to avoid Kubernetes $(VAR) substitution
	scanScript := fmt.Sprintf(`#!/bin/sh
set -e

MODELS_DIR="%s"
OUTPUT_FILE="/tmp/models.json"

echo "[" > "$OUTPUT_FILE"
first=true

# Scan each model directory
if [ -d "$MODELS_DIR" ]; then
  for model_dir in "$MODELS_DIR"/*/; do
    if [ -d "$model_dir" ]; then
      model_name=$(basename "$model_dir")

      # Scan each revision
      for rev_dir in "$model_dir"/*/; do
        if [ -d "$rev_dir" ]; then
          revision=$(basename "$rev_dir")
          metadata_file="$rev_dir/.rbg-metadata.json"

          if [ -f "$metadata_file" ]; then
            # Read metadata and add model info
            if [ "$first" = true ]; then
              first=false
            else
              echo "," >> "$OUTPUT_FILE"
            fi

            # Add path info to metadata (use | as sed delimiter to avoid issues with / in paths)
            cat "$metadata_file" >> "$OUTPUT_FILE"
          else
            # No metadata file, create basic entry
            if [ "$first" = true ]; then
              first=false
            else
              echo "," >> "$OUTPUT_FILE"
            fi
            echo "{\"modelID\":\"$model_name\",\"revision\":\"$revision\",\"downloadedAt\":\"unknown\"}" >> "$OUTPUT_FILE"
          fi
        fi
      done
    fi
  done
fi

echo "]" >> "$OUTPUT_FILE"
cat "$OUTPUT_FILE"
`, mountPath)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:   jobName,
			Labels: labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            int32Ptr(2),
			ActiveDeadlineSeconds:   int64Ptr(300),
			TTLSecondsAfterFinished: int32Ptr(60),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "scanner",
							Image:   "alpine:latest",
							Command: []string{"/bin/sh", "-c"},
							Args:    []string{scanScript},
						},
					},
					RestartPolicy: corev1.RestartPolicyNever,
				},
			},
		},
	}
}

// getJobOutput retrieves the output from the job's pod
func getJobOutput(ctx context.Context, clientset *kubernetes.Clientset, namespace, jobName string) ([]ModelInfo, error) {
	// Find the pod for this job
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("job-name=%s", jobName),
	})
	if err != nil {
		return nil, err
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no pods found for job %s", jobName)
	}

	podName := pods.Items[0].Name

	// Get pod logs
	req := clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
	logs, err := req.DoRaw(ctx)
	if err != nil {
		return nil, err
	}

	// Parse JSON output
	var models []ModelInfo
	if err := json.Unmarshal(logs, &models); err != nil {
		return nil, fmt.Errorf("failed to parse job output: %w", err)
	}

	return models, nil
}

// ModelInfo contains information about a downloaded model
type ModelInfo struct {
	ModelID      string `json:"modelID"`
	Revision     string `json:"revision"`
	DownloadedAt string `json:"downloadedAt,omitempty"`
}

// printModelsList prints the list of models in a formatted table
func printModelsList(models []ModelInfo, storageName string) {
	if len(models) == 0 {
		fmt.Println("No models found in storage")
		return
	}

	fmt.Printf("Models in storage '%s':\n\n", storageName)
	fmt.Printf("%-40s %-15s %s\n", "MODEL ID", "REVISION", "DOWNLOADED AT")
	fmt.Println(strings.Repeat("-", 90))

	for _, model := range models {
		downloadedAt := model.DownloadedAt
		if downloadedAt == "" || downloadedAt == "unknown" {
			downloadedAt = "-"
		}
		fmt.Printf("%-40s %-15s %s\n", model.ModelID, model.Revision, downloadedAt)
	}

	fmt.Printf("\nTotal: %d model(s)\n", len(models))
}

// int32Ptr returns a pointer to an int32 value
func int32Ptr(i int32) *int32 {
	return &i
}

// int64Ptr returns a pointer to an int64 value
func int64Ptr(i int64) *int64 {
	return &i
}
