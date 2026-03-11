package source

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestModelScopeSource_Name(t *testing.T) {
	m := &ModelScopeSource{}
	assert.Equal(t, "modelscope", m.Name())
}

func TestModelScopeSource_ConfigFields(t *testing.T) {
	m := &ModelScopeSource{}
	fields := m.ConfigFields()
	assert.Len(t, fields, 2)
	keys := []string{fields[0].Key, fields[1].Key}
	assert.Contains(t, keys, "token")
	assert.Contains(t, keys, "tokenSecret")
}

func TestModelScopeSource_Init_Empty(t *testing.T) {
	m := &ModelScopeSource{}
	err := m.Init(map[string]interface{}{})
	require.NoError(t, err)
	assert.Empty(t, m.Token)
	assert.Empty(t, m.TokenSecret)
}

func TestModelScopeSource_Init_WithToken(t *testing.T) {
	m := &ModelScopeSource{}
	err := m.Init(map[string]interface{}{"token": "ms_secret"})
	require.NoError(t, err)
	assert.Equal(t, "ms_secret", m.Token)
}

func TestModelScopeSource_Init_WithTokenSecret(t *testing.T) {
	m := &ModelScopeSource{}
	err := m.Init(map[string]interface{}{"tokenSecret": "my-ms-secret"})
	require.NoError(t, err)
	assert.Equal(t, "my-ms-secret", m.TokenSecret)
	assert.Empty(t, m.Token)
}

func TestModelScopeSource_GenerateTemplate_NoToken(t *testing.T) {
	m := &ModelScopeSource{}
	require.NoError(t, m.Init(map[string]interface{}{}))

	tpl, err := m.GenerateTemplateWithRevision("org/model", "/models/model", "")
	require.NoError(t, err)
	require.NotNil(t, tpl)
	c := tpl.Spec.Containers[0]
	assert.Equal(t, "download", c.Name)
	assert.Equal(t, "python:3.11-slim", c.Image)
	// Check that model parameters are passed via environment variables (not string concatenation)
	require.Len(t, c.Env, 2)
	envMap := msEnvToMap(c.Env)
	assert.Equal(t, "org/model", envMap["MODEL_ID"])
	assert.Equal(t, "/models/model", envMap["MODEL_PATH"])
	// Command should use environment variable references, not direct values
	assert.Contains(t, c.Args[0], "$MODEL_ID")
	assert.Contains(t, c.Args[0], "$MODEL_PATH")
}

func TestModelScopeSource_GenerateTemplate_WithToken(t *testing.T) {
	m := &ModelScopeSource{}
	require.NoError(t, m.Init(map[string]interface{}{"token": "ms_tok"}))

	tpl, err := m.GenerateTemplateWithRevision("org/model", "/models/model", "")
	require.NoError(t, err)
	envMap := msEnvToMap(tpl.Spec.Containers[0].Env)
	assert.Equal(t, "ms_tok", envMap["MODELSCOPE_TOKEN"])
}

func TestModelScopeSource_GenerateTemplate_WithTokenSecret(t *testing.T) {
	m := &ModelScopeSource{}
	require.NoError(t, m.Init(map[string]interface{}{"tokenSecret": "my-ms-secret"}))

	tpl, err := m.GenerateTemplateWithRevision("org/model", "/models/model", "")
	require.NoError(t, err)

	c := tpl.Spec.Containers[0]
	var msTokenEnv *corev1.EnvVar
	for i := range c.Env {
		if c.Env[i].Name == "MODELSCOPE_TOKEN" {
			msTokenEnv = &c.Env[i]
			break
		}
	}
	require.NotNil(t, msTokenEnv, "MODELSCOPE_TOKEN env var should be present")
	assert.Empty(t, msTokenEnv.Value, "MODELSCOPE_TOKEN should not have a plain-text Value")
	require.NotNil(t, msTokenEnv.ValueFrom)
	require.NotNil(t, msTokenEnv.ValueFrom.SecretKeyRef)
	assert.Equal(t, "my-ms-secret", msTokenEnv.ValueFrom.SecretKeyRef.Name)
	assert.Equal(t, "MODELSCOPE_TOKEN", msTokenEnv.ValueFrom.SecretKeyRef.Key)
}

func TestModelScopeSource_GenerateTemplate_TokenSecretTakesPrecedence(t *testing.T) {
	m := &ModelScopeSource{}
	require.NoError(t, m.Init(map[string]interface{}{
		"token":       "ms_plain",
		"tokenSecret": "my-ms-secret",
	}))

	tpl, err := m.GenerateTemplateWithRevision("org/model", "/models/model", "")
	require.NoError(t, err)

	c := tpl.Spec.Containers[0]
	var msTokenEnv *corev1.EnvVar
	for i := range c.Env {
		if c.Env[i].Name == "MODELSCOPE_TOKEN" {
			msTokenEnv = &c.Env[i]
			break
		}
	}
	require.NotNil(t, msTokenEnv)
	assert.Empty(t, msTokenEnv.Value, "plain-text token must not appear when tokenSecret is set")
	require.NotNil(t, msTokenEnv.ValueFrom)
	assert.Equal(t, "my-ms-secret", msTokenEnv.ValueFrom.SecretKeyRef.Name)
}

func TestModelScopeSource_GenerateTemplate_WithRevision(t *testing.T) {
	m := &ModelScopeSource{}
	require.NoError(t, m.Init(map[string]interface{}{}))

	tpl, err := m.GenerateTemplateWithRevision("org/model", "/models/model", "v2.0")
	require.NoError(t, err)
	c := tpl.Spec.Containers[0]
	cmd := c.Args[0]
	// Check that revision is passed via environment variable
	envMap := msEnvToMap(c.Env)
	assert.Equal(t, "v2.0", envMap["REVISION"])
	// Command should use environment variable reference
	assert.Contains(t, cmd, "$REVISION")
}

func TestModelScopeSource_GenerateTemplate_MainRevisionIgnored(t *testing.T) {
	m := &ModelScopeSource{}
	require.NoError(t, m.Init(map[string]interface{}{}))

	tpl, err := m.GenerateTemplateWithRevision("org/model", "/models/model", "main")
	require.NoError(t, err)
	cmd := tpl.Spec.Containers[0].Args[0]
	assert.NotContains(t, cmd, "--revision")
}

func TestModelScopeSource_GenerateTemplate_RestartPolicy(t *testing.T) {
	m := &ModelScopeSource{}
	require.NoError(t, m.Init(map[string]interface{}{}))

	tpl, err := m.GenerateTemplateWithRevision("org/model", "/models/model", "")
	require.NoError(t, err)
	assert.Equal(t, "Never", string(tpl.Spec.RestartPolicy))
}

func TestGet_ModelScope(t *testing.T) {
	p, err := Get("modelscope", map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, "modelscope", p.Name())
}

func TestValidateConfig_ModelScope_UnknownField(t *testing.T) {
	err := ValidateConfig("modelscope", map[string]interface{}{"bad": "x"})
	assert.Error(t, err)
}

func TestGetFields_ModelScope(t *testing.T) {
	fields := GetFields("modelscope")
	require.NotNil(t, fields)
	assert.Len(t, fields, 2)
}

func msEnvToMap(envVars []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(envVars))
	for _, e := range envVars {
		m[e.Name] = e.Value
	}
	return m
}
