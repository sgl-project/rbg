package source

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestHuggingFaceSource_Name(t *testing.T) {
	h := &HuggingFaceSource{}
	assert.Equal(t, "huggingface", h.Name())
}

func TestHuggingFaceSource_ConfigFields(t *testing.T) {
	h := &HuggingFaceSource{}
	fields := h.ConfigFields()
	assert.Len(t, fields, 2)
	keys := []string{fields[0].Key, fields[1].Key}
	assert.Contains(t, keys, "token")
	assert.Contains(t, keys, "mirror")
}

func TestHuggingFaceSource_Init_Empty(t *testing.T) {
	h := &HuggingFaceSource{}
	err := h.Init(map[string]interface{}{})
	require.NoError(t, err)
	assert.Empty(t, h.Token)
	assert.Empty(t, h.Mirror)
}

func TestHuggingFaceSource_Init_WithValues(t *testing.T) {
	h := &HuggingFaceSource{}
	err := h.Init(map[string]interface{}{
		"token":  "hf_abc123",
		"mirror": "https://hf-mirror.com",
	})
	require.NoError(t, err)
	assert.Equal(t, "hf_abc123", h.Token)
	assert.Equal(t, "https://hf-mirror.com", h.Mirror)
}

func TestHuggingFaceSource_GenerateTemplate_NoAuth(t *testing.T) {
	h := &HuggingFaceSource{}
	require.NoError(t, h.Init(map[string]interface{}{}))

	tpl, err := h.GenerateTemplateWithRevision("org/model", "/models/model", "")
	require.NoError(t, err)
	require.NotNil(t, tpl)
	require.Len(t, tpl.Spec.Containers, 1)

	c := tpl.Spec.Containers[0]
	assert.Equal(t, "download", c.Name)
	assert.Equal(t, "python:3.11-slim", c.Image)
	assert.Equal(t, []string{"/bin/sh", "-c"}, c.Command)

	// Verify environment variables are set instead of string concatenation
	envMap := hfEnvToMap(c.Env)
	assert.Equal(t, "org/model", envMap["MODEL_ID"])
	assert.Equal(t, "/models/model", envMap["MODEL_PATH"])
	assert.Equal(t, "", envMap["MODEL_REVISION"])

	// Verify the command uses os.environ instead of hardcoded values
	assert.Contains(t, c.Args[0], "os.environ['MODEL_ID']")
	assert.Contains(t, c.Args[0], "os.environ['MODEL_PATH']")
}

func TestHuggingFaceSource_GenerateTemplate_WithToken(t *testing.T) {
	h := &HuggingFaceSource{}
	require.NoError(t, h.Init(map[string]interface{}{"token": "hf_secret"}))

	tpl, err := h.GenerateTemplateWithRevision("org/model", "/models/model", "")
	require.NoError(t, err)
	envMap := hfEnvToMap(tpl.Spec.Containers[0].Env)
	assert.Equal(t, "hf_secret", envMap["HF_TOKEN"])
	// Verify model env vars are also set
	assert.Equal(t, "org/model", envMap["MODEL_ID"])
	assert.Equal(t, "/models/model", envMap["MODEL_PATH"])
}

func TestHuggingFaceSource_GenerateTemplate_WithMirror(t *testing.T) {
	h := &HuggingFaceSource{}
	require.NoError(t, h.Init(map[string]interface{}{"mirror": "https://hf-mirror.com"}))

	tpl, err := h.GenerateTemplateWithRevision("org/model", "/models/model", "")
	require.NoError(t, err)
	envMap := hfEnvToMap(tpl.Spec.Containers[0].Env)
	assert.Equal(t, "https://hf-mirror.com", envMap["HF_ENDPOINT"])
	// Verify model env vars are also set
	assert.Equal(t, "org/model", envMap["MODEL_ID"])
	assert.Equal(t, "/models/model", envMap["MODEL_PATH"])
}

func TestHuggingFaceSource_GenerateTemplate_WithRevision(t *testing.T) {
	h := &HuggingFaceSource{}
	require.NoError(t, h.Init(map[string]interface{}{}))

	tpl, err := h.GenerateTemplateWithRevision("org/model", "/models/model", "v1.0")
	require.NoError(t, err)
	envMap := hfEnvToMap(tpl.Spec.Containers[0].Env)
	assert.Equal(t, "v1.0", envMap["MODEL_REVISION"])
	// Verify Python script uses os.environ.get for revision
	assert.Contains(t, tpl.Spec.Containers[0].Args[0], "os.environ.get('MODEL_REVISION'")
}

func TestHuggingFaceSource_GenerateTemplate_MainRevisionIgnored(t *testing.T) {
	h := &HuggingFaceSource{}
	require.NoError(t, h.Init(map[string]interface{}{}))

	tpl, err := h.GenerateTemplateWithRevision("org/model", "/models/model", "main")
	require.NoError(t, err)
	// The Python script uses os.environ.get with default 'main', so 'main' is handled at runtime
	// The env var is still set, but Python will use the default if not explicitly different
	envMap := hfEnvToMap(tpl.Spec.Containers[0].Env)
	assert.Equal(t, "main", envMap["MODEL_REVISION"])
}

func TestHuggingFaceSource_GenerateTemplate_RestartPolicy(t *testing.T) {
	h := &HuggingFaceSource{}
	require.NoError(t, h.Init(map[string]interface{}{}))

	tpl, err := h.GenerateTemplateWithRevision("org/model", "/models/model", "")
	require.NoError(t, err)
	assert.Equal(t, "Never", string(tpl.Spec.RestartPolicy))
}

func TestGet_HuggingFace(t *testing.T) {
	p, err := Get("huggingface", map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, "huggingface", p.Name())
}

func TestValidateConfig_HuggingFace_UnknownField(t *testing.T) {
	err := ValidateConfig("huggingface", map[string]interface{}{"bad": "x"})
	assert.Error(t, err)
}

func TestGetFields_HuggingFace(t *testing.T) {
	fields := GetFields("huggingface")
	require.NotNil(t, fields)
	assert.Len(t, fields, 2)
}

// --- Security tests for command injection prevention ---

func TestHuggingFaceSource_GenerateTemplate_MaliciousModelID_NoInjection(t *testing.T) {
	h := &HuggingFaceSource{}
	require.NoError(t, h.Init(map[string]interface{}{}))

	// Malicious modelID with Python code injection attempt
	maliciousID := "org/model'; import os; os.system('rm -rf /'); '"
	tpl, err := h.GenerateTemplateWithRevision(maliciousID, "/models/model", "")
	require.NoError(t, err)

	// Verify the malicious input is passed as env var, not concatenated
	envMap := hfEnvToMap(tpl.Spec.Containers[0].Env)
	assert.Equal(t, maliciousID, envMap["MODEL_ID"])

	// Verify the Python script reads from environment, not string concatenation
	cmd := tpl.Spec.Containers[0].Args[0]
	assert.Contains(t, cmd, "os.environ['MODEL_ID']")
	// The malicious payload should NOT appear directly in the command
	assert.NotContains(t, cmd, "import os; os.system('rm -rf /')")
}

func TestHuggingFaceSource_GenerateTemplate_MaliciousPath_NoInjection(t *testing.T) {
	h := &HuggingFaceSource{}
	require.NoError(t, h.Init(map[string]interface{}{}))

	// Malicious path with shell injection attempt
	maliciousPath := "/models/'; os.system('cat /etc/passwd'); '"
	tpl, err := h.GenerateTemplateWithRevision("org/model", maliciousPath, "")
	require.NoError(t, err)

	envMap := hfEnvToMap(tpl.Spec.Containers[0].Env)
	assert.Equal(t, maliciousPath, envMap["MODEL_PATH"])

	// Verify safe handling via environment variables
	cmd := tpl.Spec.Containers[0].Args[0]
	assert.Contains(t, cmd, "os.environ['MODEL_PATH']")
	assert.NotContains(t, cmd, "os.system('cat /etc/passwd')")
}

func TestHuggingFaceSource_GenerateTemplate_MaliciousRevision_NoInjection(t *testing.T) {
	h := &HuggingFaceSource{}
	require.NoError(t, h.Init(map[string]interface{}{}))

	// Malicious revision with Python injection attempt
	maliciousRevision := "main'; __import__('os').system('whoami'); '"
	tpl, err := h.GenerateTemplateWithRevision("org/model", "/models/model", maliciousRevision)
	require.NoError(t, err)

	envMap := hfEnvToMap(tpl.Spec.Containers[0].Env)
	assert.Equal(t, maliciousRevision, envMap["MODEL_REVISION"])

	// Verify the Python script uses os.environ.get which treats the value as data
	cmd := tpl.Spec.Containers[0].Args[0]
	assert.Contains(t, cmd, "os.environ.get('MODEL_REVISION'")
	assert.NotContains(t, cmd, "__import__('os').system('whoami')")
}

func hfEnvToMap(envVars []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(envVars))
	for _, e := range envVars {
		m[e.Name] = e.Value
	}
	return m
}
