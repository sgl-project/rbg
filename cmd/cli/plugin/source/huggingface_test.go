/*
Copyright 2025.

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
	assert.Empty(t, c.Env)
	assert.Contains(t, c.Args[0], "org/model")
	assert.Contains(t, c.Args[0], "/models/model")
}

func TestHuggingFaceSource_GenerateTemplate_WithToken(t *testing.T) {
	h := &HuggingFaceSource{}
	require.NoError(t, h.Init(map[string]interface{}{"token": "hf_secret"}))

	tpl, err := h.GenerateTemplateWithRevision("org/model", "/models/model", "")
	require.NoError(t, err)
	envMap := hfEnvToMap(tpl.Spec.Containers[0].Env)
	assert.Equal(t, "hf_secret", envMap["HF_TOKEN"])
}

func TestHuggingFaceSource_GenerateTemplate_WithMirror(t *testing.T) {
	h := &HuggingFaceSource{}
	require.NoError(t, h.Init(map[string]interface{}{"mirror": "https://hf-mirror.com"}))

	tpl, err := h.GenerateTemplateWithRevision("org/model", "/models/model", "")
	require.NoError(t, err)
	envMap := hfEnvToMap(tpl.Spec.Containers[0].Env)
	assert.Equal(t, "https://hf-mirror.com", envMap["HF_ENDPOINT"])
}

func TestHuggingFaceSource_GenerateTemplate_WithRevision(t *testing.T) {
	h := &HuggingFaceSource{}
	require.NoError(t, h.Init(map[string]interface{}{}))

	tpl, err := h.GenerateTemplateWithRevision("org/model", "/models/model", "v1.0")
	require.NoError(t, err)
	cmd := tpl.Spec.Containers[0].Args[0]
	assert.Contains(t, cmd, "revision='v1.0'")
}

func TestHuggingFaceSource_GenerateTemplate_MainRevisionIgnored(t *testing.T) {
	h := &HuggingFaceSource{}
	require.NoError(t, h.Init(map[string]interface{}{}))

	tpl, err := h.GenerateTemplateWithRevision("org/model", "/models/model", "main")
	require.NoError(t, err)
	cmd := tpl.Spec.Containers[0].Args[0]
	assert.NotContains(t, cmd, "revision=")
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

func hfEnvToMap(envVars []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(envVars))
	for _, e := range envVars {
		m[e.Name] = e.Value
	}
	return m
}
