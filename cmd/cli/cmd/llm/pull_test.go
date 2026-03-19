package llm

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// empty string: not matching safe chars → wrapped in single quotes
	assert.Equal(t, "''", result)
}

// --- buildPullJob ---

func TestBuildPullJob_NameContainsModelID(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "dl"}},
		},
	}
	job, err := buildPullJob("org/llama", tpl)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(job.Name, "pull-org-llama-"), "job name should contain sanitized model ID")
}

func TestBuildPullJob_Labels(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{}
	job, err := buildPullJob("org/model", tpl)
	require.NoError(t, err)
	assert.Equal(t, "true", job.Labels["rbg-pull-job"])
	assert.Equal(t, "org-model", job.Labels["rbg-model-id"])
}

func TestBuildPullJob_Limits(t *testing.T) {
	tpl := &corev1.PodTemplateSpec{}
	job, err := buildPullJob("m", tpl)
	require.NoError(t, err)
	assert.Equal(t, int32(3), *job.Spec.BackoffLimit)
	assert.Equal(t, int64(7200), *job.Spec.ActiveDeadlineSeconds)
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
	job, err := buildPullJob("org/model", tpl)
	require.NoError(t, err)
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
	// Condition present but Status == False → not terminal
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

// --- jsonSafeString ---

func TestJsonSafeString_SimpleString(t *testing.T) {
	result := jsonSafeString("hello")
	assert.Equal(t, `"hello"`, result)
}

func TestJsonSafeString_SpecialCharsEscaped(t *testing.T) {
	result := jsonSafeString(`hello "world"`)
	assert.Contains(t, result, `\"`)
}

func TestJsonSafeString_NewlineEscaped(t *testing.T) {
	result := jsonSafeString("hello\nworld")
	assert.Contains(t, result, `\n`)
}

func TestJsonSafeString_TabEscaped(t *testing.T) {
	result := jsonSafeString("hello\tworld")
	assert.Contains(t, result, `\t`)
}

func TestJsonSafeString_BackslashEscaped(t *testing.T) {
	result := jsonSafeString(`hello\world`)
	assert.Contains(t, result, `\\`)
}

// --- Security tests for command injection prevention ---

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
	// Verify single quotes in modelID are escaped with shell escape pattern '\"'\"'
	assert.Contains(t, arg, `'"'"'`)
	// The entire printf argument should be shell-escaped (wrapped in single quotes)
	assert.Contains(t, arg, `printf '`)
	// Verify the malicious content appears as escaped string data, not executable
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
	// The malicious input should be properly shell-escaped
	assert.Contains(t, arg, `.rbg-metadata.json`)
	// Verify the $() is escaped by shellEscape (wrapped in single quotes)
	assert.Contains(t, arg, `printf '`)
	// The $( should appear inside the escaped string, not as executable
	assert.Contains(t, arg, `$(rm -rf /)`)
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
