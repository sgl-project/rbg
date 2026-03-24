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
	assert.Len(t, fields, 3)
	keys := []string{fields[0].Key, fields[1].Key, fields[2].Key}
	assert.Contains(t, keys, "token")
	assert.Contains(t, keys, "tokenSecret")
	assert.Contains(t, keys, "mirror")
}

func TestHuggingFaceSource_Init_Empty(t *testing.T) {
	h := &HuggingFaceSource{}
	err := h.Init(map[string]interface{}{})
	require.NoError(t, err)
	assert.Empty(t, h.Token)
	assert.Empty(t, h.TokenSecret)
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

func TestHuggingFaceSource_Init_WithTokenSecret(t *testing.T) {
	h := &HuggingFaceSource{}
	err := h.Init(map[string]interface{}{
		"tokenSecret": "my-hf-secret",
	})
	require.NoError(t, err)
	assert.Equal(t, "my-hf-secret", h.TokenSecret)
	assert.Empty(t, h.Token)
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

func TestHuggingFaceSource_GenerateTemplate_WithTokenSecret(t *testing.T) {
	h := &HuggingFaceSource{}
	require.NoError(t, h.Init(map[string]interface{}{"tokenSecret": "my-hf-secret"}))

	tpl, err := h.GenerateTemplateWithRevision("org/model", "/models/model", "")
	require.NoError(t, err)

	c := tpl.Spec.Containers[0]
	// Find the HF_TOKEN env var
	var hfTokenEnv *corev1.EnvVar
	for i := range c.Env {
		if c.Env[i].Name == "HF_TOKEN" {
			hfTokenEnv = &c.Env[i]
			break
		}
	}
	require.NotNil(t, hfTokenEnv, "HF_TOKEN env var should be present")
	// Must use ValueFrom/SecretKeyRef, NOT a plain Value
	assert.Empty(t, hfTokenEnv.Value, "HF_TOKEN should not have a plain-text Value")
	require.NotNil(t, hfTokenEnv.ValueFrom)
	require.NotNil(t, hfTokenEnv.ValueFrom.SecretKeyRef)
	assert.Equal(t, "my-hf-secret", hfTokenEnv.ValueFrom.SecretKeyRef.Name)
	assert.Equal(t, "HF_TOKEN", hfTokenEnv.ValueFrom.SecretKeyRef.Key)
}

func TestHuggingFaceSource_GenerateTemplate_TokenSecretTakesPrecedence(t *testing.T) {
	h := &HuggingFaceSource{}
	// Both token and tokenSecret provided: tokenSecret wins
	require.NoError(t, h.Init(map[string]interface{}{
		"token":       "hf_plain",
		"tokenSecret": "my-hf-secret",
	}))

	tpl, err := h.GenerateTemplateWithRevision("org/model", "/models/model", "")
	require.NoError(t, err)

	c := tpl.Spec.Containers[0]
	var hfTokenEnv *corev1.EnvVar
	for i := range c.Env {
		if c.Env[i].Name == "HF_TOKEN" {
			hfTokenEnv = &c.Env[i]
			break
		}
	}
	require.NotNil(t, hfTokenEnv)
	assert.Empty(t, hfTokenEnv.Value, "plain-text token must not appear when tokenSecret is set")
	require.NotNil(t, hfTokenEnv.ValueFrom)
	assert.Equal(t, "my-hf-secret", hfTokenEnv.ValueFrom.SecretKeyRef.Name)
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
	assert.Len(t, fields, 3)
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
