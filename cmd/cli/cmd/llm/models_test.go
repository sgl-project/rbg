package llm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

// --- int32Ptr / int64Ptr ---

func TestInt32Ptr(t *testing.T) {
	v := int32Ptr(42)
	require.NotNil(t, v)
	assert.Equal(t, int32(42), *v)
}

func TestInt64Ptr(t *testing.T) {
	v := int64Ptr(100)
	require.NotNil(t, v)
	assert.Equal(t, int64(100), *v)
}

// --- buildListModelsJob ---

func TestBuildListModelsJob_NamePrefix(t *testing.T) {
	job := buildListModelsJob("/models")
	assert.True(t, strings.HasPrefix(job.Name, "list-models-"), "job name should start with list-models-")
}

func TestBuildListModelsJob_Labels(t *testing.T) {
	job := buildListModelsJob("/models")
	assert.Equal(t, "true", job.Labels["rbg-list-job"])
}

func TestBuildListModelsJob_ContainerSpec(t *testing.T) {
	job := buildListModelsJob("/data/models")
	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	c := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "scanner", c.Name)
	assert.Equal(t, "alpine:latest", c.Image)
	assert.Equal(t, []string{"/bin/sh", "-c"}, c.Command)
}

func TestBuildListModelsJob_ScriptContainsMountPath(t *testing.T) {
	job := buildListModelsJob("/custom/path")
	args := job.Spec.Template.Spec.Containers[0].Args
	require.Len(t, args, 1)
	assert.Contains(t, args[0], "/custom/path")
}

func TestBuildListModelsJob_RestartPolicy(t *testing.T) {
	job := buildListModelsJob("/models")
	assert.Equal(t, corev1.RestartPolicyNever, job.Spec.Template.Spec.RestartPolicy)
}

func TestBuildListModelsJob_Limits(t *testing.T) {
	job := buildListModelsJob("/models")
	assert.Equal(t, int32(2), *job.Spec.BackoffLimit)
	assert.Equal(t, int64(300), *job.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, int32(60), *job.Spec.TTLSecondsAfterFinished)
}

// --- printModelsList ---

func TestPrintModelsList_Empty(t *testing.T) {
	// Should not panic and should print "No models found"
	// (output goes to stdout; we just verify no panic)
	printModelsList(nil, "my-storage")
	printModelsList([]ModelInfo{}, "my-storage")
}

func TestPrintModelsList_WithModels(t *testing.T) {
	models := []ModelInfo{
		{ModelID: "org/llama", Revision: "main", DownloadedAt: "2025-01-01T00:00:00Z"},
		{ModelID: "org/qwen", Revision: "v1.0"},
	}
	// Just verify no panic; output goes to real stdout
	printModelsList(models, "test-storage")
}

func TestPrintModelsList_UnknownDownloadedAt(t *testing.T) {
	// "unknown" downloadedAt should be replaced with "-" in output (no panic)
	models := []ModelInfo{
		{ModelID: "m", Revision: "r", DownloadedAt: "unknown"},
	}
	printModelsList(models, "s")
}
