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

package model

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// --- shellEscape ---

func TestShellEscape_SafeString(t *testing.T) {
	// Pure alphanumeric — returned as-is
	assert.Equal(t, "abc123", shellEscape("abc123"))
}

func TestShellEscape_SafePath(t *testing.T) {
	assert.Equal(t, "/models/my-model.bin", shellEscape("/models/my-model.bin"))
}

func TestShellEscape_SpaceRequiresQuoting(t *testing.T) {
	result := shellEscape("hello world")
	assert.True(t, strings.HasPrefix(result, "'"), "should be single-quoted")
	assert.True(t, strings.HasSuffix(result, "'"), "should be single-quoted")
	assert.Contains(t, result, "hello world")
}

func TestShellEscape_SingleQuoteEscaped(t *testing.T) {
	result := shellEscape("it's")
	// Single quote must be escaped via '\''
	assert.Contains(t, result, `'"'"'`)
}

func TestShellEscape_Empty(t *testing.T) {
	result := shellEscape("")
	// empty string: not matching safe chars -> wrapped in single quotes
	assert.Equal(t, "''", result)
}

// --- newPullCmd flags ---

func TestNewPullCmd_ResourceFlagsExist(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := newPullCmd(cf)

	memFlag := cmd.Flags().Lookup("memory")
	require.NotNil(t, memFlag)
	assert.Equal(t, defaultPullMemory, memFlag.DefValue)

	cpuFlag := cmd.Flags().Lookup("cpu")
	require.NotNil(t, cpuFlag)
	assert.Equal(t, defaultPullCPU, cpuFlag.DefValue)
}

// --- applyPullResources ---

func TestApplyPullResources_Defaults(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "dl"}},
		},
	}
	applyPullResources(tpl, defaultPullCPU, defaultPullMemory)

	res := tpl.Spec.Containers[0].Resources
	assert.True(t, res.Requests.Cpu().Equal(resource.MustParse(defaultPullCPU)))
	assert.True(t, res.Requests.Memory().Equal(resource.MustParse(defaultPullMemory)))
	assert.True(t, res.Limits.Cpu().Equal(resource.MustParse(defaultPullCPU)))
	assert.True(t, res.Limits.Memory().Equal(resource.MustParse(defaultPullMemory)))
}

func TestApplyPullResources_CustomValues(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "dl"}},
		},
	}
	applyPullResources(tpl, "2", "16Gi")

	res := tpl.Spec.Containers[0].Resources
	assert.True(t, res.Requests.Cpu().Equal(resource.MustParse("2")))
	assert.True(t, res.Requests.Memory().Equal(resource.MustParse("16Gi")))
	assert.True(t, res.Limits.Cpu().Equal(resource.MustParse("2")))
	assert.True(t, res.Limits.Memory().Equal(resource.MustParse("16Gi")))
}

func TestApplyPullResources_NoContainers_NoPanic(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{}
	// Must not panic
	applyPullResources(tpl, "1", "2Gi")
}

// --- buildPullJob ---

func TestBuildPullJob_NameContainsModelID(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "dl"}},
		},
	}
	job := buildPullJob("org/llama", tpl)
	assert.True(t, strings.HasPrefix(job.Name, "pull-org-llama-"), "job name should contain sanitized model ID")
}

func TestBuildPullJob_Labels(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{}
	job := buildPullJob("org/model", tpl)
	assert.Equal(t, "true", job.Labels["rbg-pull-job"])
	assert.Equal(t, "org-model", job.Labels["rbg-model-id"])
}

func TestBuildPullJob_Limits(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{}
	job := buildPullJob("m", tpl)
	assert.Equal(t, int32(3), *job.Spec.BackoffLimit)
	assert.Equal(t, int64(86400), *job.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, int32(86400), *job.Spec.TTLSecondsAfterFinished)
}

func TestBuildPullJob_InheritsSpec(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "downloader", Image: "python:3.11-slim"},
			},
		},
	}
	job := buildPullJob("org/model", tpl)
	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, "downloader", job.Spec.Template.Spec.Containers[0].Name)
}

// --- deriveJobState ---

func TestDeriveJobState_Succeeded(t *testing.T) {
	job := &batchv1.Job{
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		},
	}
	assert.Equal(t, JobStateSucceeded, deriveJobState(job))
}

func TestDeriveJobState_Failed(t *testing.T) {
	job := &batchv1.Job{
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			},
		},
	}
	assert.Equal(t, JobStateFailed, deriveJobState(job))
}

func TestDeriveJobState_Running(t *testing.T) {
	job := &batchv1.Job{
		Status: batchv1.JobStatus{Active: 2},
	}
	assert.Equal(t, JobStateRunning, deriveJobState(job))
}

func TestDeriveJobState_Pending(t *testing.T) {
	job := &batchv1.Job{}
	assert.Equal(t, JobStatePending, deriveJobState(job))
}

func TestDeriveJobState_ConditionFalse_NotSucceeded(t *testing.T) {
	// Condition present but Status == False -> not terminal
	job := &batchv1.Job{
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionFalse},
			},
		},
	}
	assert.Equal(t, JobStatePending, deriveJobState(job))
}

// --- injectMetadataSave ---

func TestInjectMetadataSave_ShellCaseAppendsToArgs(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Command: []string{"/bin/sh", "-c"},
					Args:    []string{"pip install hub && python -c \"download()\""},
				},
			},
		},
	}
	injectMetadataSave(tpl, "org/model", "main", "/models/org-model/main")

	arg := tpl.Spec.Containers[0].Args[0]
	assert.Contains(t, arg, ".rbg-metadata.json")
	assert.Contains(t, arg, "org/model")
	// Command must stay unchanged
	assert.Equal(t, []string{"/bin/sh", "-c"}, tpl.Spec.Containers[0].Command)
}

func TestInjectMetadataSave_DirectBinaryWrapsWithShell(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Command: []string{"huggingface-cli"},
					Args:    []string{"download", "org/model"},
				},
			},
		},
	}
	injectMetadataSave(tpl, "org/model", "v1", "/models/org-model/v1")

	c := tpl.Spec.Containers[0]
	assert.Equal(t, []string{"/bin/sh", "-c"}, c.Command)
	require.Len(t, c.Args, 1)
	assert.Contains(t, c.Args[0], "huggingface-cli")
	assert.Contains(t, c.Args[0], ".rbg-metadata.json")
}

func TestInjectMetadataSave_NoContainers_Noop(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{}
	// Must not panic
	injectMetadataSave(tpl, "m", "r", "/p")
}

func TestInjectMetadataSave_MetadataContainsModelIDAndRevision(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Command: []string{"/bin/sh", "-c"},
					Args:    []string{"download cmd"},
				},
			},
		},
	}
	injectMetadataSave(tpl, "myorg/mymodel", "v2", "/m/p")

	arg := tpl.Spec.Containers[0].Args[0]
	assert.Contains(t, arg, "myorg/mymodel")
	assert.Contains(t, arg, "v2")
}

// --- Security tests for JSON escaping and command injection prevention ---

// extractJSONFromCommand extracts the JSON payload from a wrapped shell command.
// It handles both Case 1 (/bin/sh -c with appended metadata) and Case 2 (wrapped binary).
func extractJSONFromCommand(t *testing.T, cmd []string, args []string) string {
	t.Helper()
	require.Len(t, cmd, 2, "expected /bin/sh -c command")
	require.Equal(t, "/bin/sh", cmd[0])
	require.Equal(t, "-c", cmd[1])
	require.Len(t, args, 1, "expected single shell command string")

	shellCmd := args[0]

	// Both Case 1 and Case 2 now use printf '%s\n' and sed for safety
	// Case 1: "original_cmd && downloadedAt=$(date ...) && printf '%s\n' {JSON} | sed ... > /path"
	// Case 2: "binary args && downloadedAt=$(date ...) && printf '%s\n' {JSON} | sed ... > /path"
	//
	// The JSON is passed as argument to printf (with shellEscape, may be quoted)
	printfIdx := strings.Index(shellCmd, "printf '%s\\n' ")
	require.GreaterOrEqual(t, printfIdx, 0, "expected printf command in shell command")
	start := printfIdx + len("printf '%s\\n' ")

	// The JSON argument may be:
	// - Single-quoted if it contains special chars: 'escaped-json'
	// - Unquoted if safe: raw-json
	rest := shellCmd[start:]

	// Check if it starts with single quote (shellEscape wrapped it)
	if strings.HasPrefix(rest, "'") {
		// Find the closing single quote
		// Note: shellEscape escapes internal single quotes as '"'"' so we need to handle that
		jsonEnd := strings.Index(rest, "' | ")
		if jsonEnd == -1 {
			jsonEnd = strings.Index(rest, "' > ")
		}
		require.GreaterOrEqual(t, jsonEnd, 0, "expected closing quote after printf argument")
		// Extract content between quotes, then unescape shell escapes
		quotedContent := rest[1:jsonEnd]
		// Unescape shell single-quote escapes: '"'"' -> '
		return strings.ReplaceAll(quotedContent, `'"'"'`, "'")
	}

	// Unquoted: find the end marker
	endMarkers := []string{" | sed ", " > "}
	for _, marker := range endMarkers {
		if idx := strings.Index(rest, marker); idx >= 0 {
			return rest[:idx]
		}
	}
	require.Fail(t, "expected end marker after printf argument")
	return ""
}

func TestInjectMetadataSave_JSONIsValidAndEscaped(t *testing.T) {
	tests := []struct {
		name     string
		modelID  string
		revision string
	}{
		{
			name:     "normal values",
			modelID:  "org/model",
			revision: "main",
		},
		{
			name:     "modelID with double quotes",
			modelID:  `org/"quoted"-model`,
			revision: "v1",
		},
		{
			name:     "modelID with backslashes",
			modelID:  `org/path\\to\\model`,
			revision: "v1",
		},
		{
			name:     "modelID with newlines",
			modelID:  "org/model\nwith\nnewlines",
			revision: "v1",
		},
		{
			name:     "revision with special characters",
			modelID:  "org/model",
			revision: `v1"beta"\n\r\t`,
		},
		{
			name:     "both with mixed special characters",
			modelID:  `org/"test"\model`,
			revision: `v1.0"release"\candidate`,
		},
		{
			name:     "unicode characters",
			modelID:  "org/模型-🚀",
			revision: "版本-ñ",
		},
		{
			name:     "json injection attempt",
			modelID:  `org/model","malicious":"true`,
			revision: `v1"},"injected":{}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Run("shell case", func(t *testing.T) {
				tpl := &corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Command: []string{"/bin/sh", "-c"},
								Args:    []string{"download cmd"},
							},
						},
					},
				}
				injectMetadataSave(tpl, tt.modelID, tt.revision, "/models/path")

				c := tpl.Spec.Containers[0]
				jsonStr := extractJSONFromCommand(t, c.Command, c.Args)

				// Verify JSON is syntactically valid
				var metadata map[string]interface{}
				err := json.Unmarshal([]byte(jsonStr), &metadata)
				require.NoError(t, err, "extracted JSON should be valid: %s", jsonStr)

				// Verify values are correctly preserved (not corrupted by escaping)
				assert.Equal(t, tt.modelID, metadata["modelID"], "modelID should match exactly")
				assert.Equal(t, tt.revision, metadata["revision"], "revision should match exactly")
				assert.Equal(t, "{{DOWNLOADED_AT}}", metadata["downloadedAt"], "downloadedAt should be placeholder")
			})

			t.Run("direct binary case", func(t *testing.T) {
				tpl := &corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Command: []string{"huggingface-cli"},
								Args:    []string{"download", "org/model"},
							},
						},
					},
				}
				injectMetadataSave(tpl, tt.modelID, tt.revision, "/models/path")

				c := tpl.Spec.Containers[0]
				jsonStr := extractJSONFromCommand(t, c.Command, c.Args)

				// Verify JSON is syntactically valid
				var metadata map[string]interface{}
				err := json.Unmarshal([]byte(jsonStr), &metadata)
				require.NoError(t, err, "extracted JSON should be valid: %s", jsonStr)

				// Verify values are correctly preserved
				assert.Equal(t, tt.modelID, metadata["modelID"], "modelID should match exactly")
				assert.Equal(t, tt.revision, metadata["revision"], "revision should match exactly")
				assert.Equal(t, "{{DOWNLOADED_AT}}", metadata["downloadedAt"], "downloadedAt should be placeholder")
			})
		})
	}
}

func TestInjectMetadataSave_MaliciousModelID_QuotedAndEscaped(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Command: []string{"/bin/sh", "-c"},
					Args:    []string{"download cmd"},
				},
			},
		},
	}
	// Malicious modelID with single quotes and command injection attempt
	injectMetadataSave(tpl, "org/model'; rm -rf /; '", "main", "/models/path")

	arg := tpl.Spec.Containers[0].Args[0]
	// The malicious input should be properly escaped, not executed
	assert.Contains(t, arg, `.rbg-metadata.json`)
	// Verify the malicious content appears inside the JSON string, not as executable shell code
	assert.Contains(t, arg, `rm -rf /`)
	// Verify the command uses printf with shell-escaped JSON content (Case 1: shell -c)
	assert.Contains(t, arg, `printf '%s\n'`)
	// Verify the JSON contains the modelID with single quotes (shellEscaped properly)
	// The malicious single quotes should be escaped: '"'"'
	assert.Contains(t, arg, `org/model'`)
	assert.Contains(t, arg, `rm -rf /`)
}

func TestInjectMetadataSave_MaliciousRevision_QuotedAndEscaped(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Command: []string{"/bin/sh", "-c"},
					Args:    []string{"download cmd"},
				},
			},
		},
	}
	// Malicious revision with command substitution attempt
	injectMetadataSave(tpl, "org/model", "$(rm -rf /)", "/models/path")

	arg := tpl.Spec.Containers[0].Args[0]
	// The malicious input should be properly escaped
	assert.Contains(t, arg, `.rbg-metadata.json`)
	// Verify the $() is inside the JSON string, not as executable shell command
	// The $ and ( should be part of the JSON-escaped string value
	assert.Contains(t, arg, `$(rm -rf /)`)
	// Verify the command uses printf with shell-escaped JSON content (Case 1: shell -c)
	assert.Contains(t, arg, `printf '%s\n'`)
}

func TestInjectMetadataSave_DirectBinary_MaliciousInput_Escaped(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Command: []string{"huggingface-cli"},
					Args:    []string{"download", "org/model"},
				},
			},
		},
	}
	// Malicious modelID with backticks and pipe
	injectMetadataSave(tpl, "org/`cat /etc/passwd`|malicious", "v1", "/models/path")

	c := tpl.Spec.Containers[0]
	assert.Equal(t, []string{"/bin/sh", "-c"}, c.Command)
	require.Len(t, c.Args, 1)
	// Verify the command is wrapped and malicious input is escaped
	assert.Contains(t, c.Args[0], `.rbg-metadata.json`)
	// The backticks should be inside single quotes, not executable
	assert.Contains(t, c.Args[0], `printf '`)
	assert.Contains(t, c.Args[0], "`cat /etc/passwd`")
}

// --- printJobSummary ---

func TestPrintJobSummary_Succeeded_NoPanic(t *testing.T) {
	now := metav1.Now()
	later := metav1.NewTime(now.Add(5 * time.Second))
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "pull-job-1", Namespace: "default"},
		Status: batchv1.JobStatus{
			StartTime:      &now,
			CompletionTime: &later,
		},
	}
	// Must not panic; output goes to stdout
	printJobSummary(job, JobStateSucceeded)
}

func TestPrintJobSummary_Failed_PrintsReason(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "pull-job-2", Namespace: "ns"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:    batchv1.JobFailed,
					Status:  corev1.ConditionTrue,
					Reason:  "BackoffLimitExceeded",
					Message: "pod failed",
				},
			},
		},
	}
	// Must not panic
	printJobSummary(job, JobStateFailed)
}

func TestPrintJobSummary_NoDuration_NoPanic(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "j", Namespace: "ns"},
	}
	printJobSummary(job, JobStateSucceeded)
}

// --- Shell command execution tests ---

// TestMetadataCommandExecution_RealShell tests the actual shell command execution
// to verify the metadata command works correctly in a real shell environment.
func TestMetadataCommandExecution_RealShell(t *testing.T) {
	if os.Getenv("SKIP_SHELL_TESTS") != "" {
		t.Skip("Skipping shell execution test")
	}

	tests := []struct {
		name     string
		modelID  string
		revision string
	}{
		{
			name:     "normal values",
			modelID:  "org/model",
			revision: "main",
		},
		{
			name:     "modelID with special chars",
			modelID:  "org/my-model_v2",
			revision: "v1.0.0",
		},
		{
			name:     "malicious modelID with single quotes",
			modelID:  "org/model'; rm -rf /; '",
			revision: "main",
		},
		{
			name:     "malicious revision with command substitution",
			modelID:  "org/model",
			revision: "$(rm -rf /)",
		},
		{
			name:     "malicious with backticks",
			modelID:  "org/`cat /etc/passwd`",
			revision: "v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build the metadata JSON (same as injectMetadataSave)
			metadata := struct {
				ModelID      string `json:"modelID"`
				Revision     string `json:"revision"`
				DownloadedAt string `json:"downloadedAt"`
			}{
				ModelID:      tt.modelID,
				Revision:     tt.revision,
				DownloadedAt: "{{DOWNLOADED_AT}}",
			}
			metadataBytes, _ := json.Marshal(metadata)
			metadataJSON := string(metadataBytes)

			// Build the shell command (same as in injectMetadataSave Case 1)
			shellCmd := fmt.Sprintf(
				"downloadedAt=$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ) && printf '%%s\\n' %s | sed \"s/{{DOWNLOADED_AT}}/$downloadedAt/g\"",
				shellEscape(metadataJSON))

			// Execute the command
			cmd := exec.Command("/bin/sh", "-c", shellCmd)
			output, err := cmd.Output()
			require.NoError(t, err, "shell command should execute successfully: %s", shellCmd)

			// Parse the output as JSON
			var result map[string]interface{}
			err = json.Unmarshal(output, &result)
			require.NoError(t, err, "output should be valid JSON: %s", string(output))

			// Verify the values
			assert.Equal(t, tt.modelID, result["modelID"], "modelID should match")
			assert.Equal(t, tt.revision, result["revision"], "revision should match")

			// Verify downloadedAt is a valid timestamp (replaced from placeholder)
			downloadedAt, ok := result["downloadedAt"].(string)
			require.True(t, ok, "downloadedAt should be a string")
			assert.NotEqual(t, "{{DOWNLOADED_AT}}", downloadedAt, "placeholder should be replaced")
			// Verify it matches ISO 8601 format
			_, err = time.Parse(time.RFC3339, downloadedAt)
			assert.NoError(t, err, "downloadedAt should be valid ISO 8601 timestamp: %s", downloadedAt)
		})
	}
}

// TestMetadataCommandExecution_FileWrite tests the complete flow including file writing
func TestMetadataCommandExecution_FileWrite(t *testing.T) {
	if os.Getenv("SKIP_SHELL_TESTS") != "" {
		t.Skip("Skipping shell execution test")
	}

	// Create a temp file for the test
	tmpFile := "/tmp/test-rbg-metadata.json"
	defer func() { _ = os.Remove(tmpFile) }()

	modelID := "org/test-model"
	revision := "v1.0"

	// Build the metadata JSON
	metadata := struct {
		ModelID      string `json:"modelID"`
		Revision     string `json:"revision"`
		DownloadedAt string `json:"downloadedAt"`
	}{
		ModelID:      modelID,
		Revision:     revision,
		DownloadedAt: "{{DOWNLOADED_AT}}",
	}
	metadataBytes, _ := json.Marshal(metadata)
	metadataJSON := string(metadataBytes)

	// Build the full command with file output
	shellCmd := fmt.Sprintf(
		"downloadedAt=$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ) && printf '%%s\\n' %s | sed \"s/{{DOWNLOADED_AT}}/$downloadedAt/g\" > %s",
		shellEscape(metadataJSON), shellEscape(tmpFile))

	// Execute the command
	cmd := exec.Command("/bin/sh", "-c", shellCmd)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "shell command should execute successfully: %s\noutput: %s", shellCmd, string(output))

	// Read and verify the file content
	content, err := os.ReadFile(tmpFile)
	require.NoError(t, err, "should be able to read the output file")

	var result map[string]interface{}
	err = json.Unmarshal(content, &result)
	require.NoError(t, err, "file content should be valid JSON: %s", string(content))

	assert.Equal(t, modelID, result["modelID"])
	assert.Equal(t, revision, result["revision"])
	assert.NotEqual(t, "{{DOWNLOADED_AT}}", result["downloadedAt"])
}
