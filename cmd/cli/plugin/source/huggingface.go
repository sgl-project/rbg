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
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/rbgs/cmd/cli/plugin/util"
)

func init() {
	Register("huggingface", func() Plugin {
		return &HuggingFaceSource{}
	})
}

// HuggingFaceSource implements the SourcePlugin interface for HuggingFace
type HuggingFaceSource struct {
	Token       string
	TokenSecret string // Name of a Kubernetes Secret containing the HF_TOKEN key
	Mirror      string
}

// Name returns the plugin name
func (h *HuggingFaceSource) Name() string {
	return "huggingface"
}

// ConfigFields returns the config fields this plugin accepts
func (h *HuggingFaceSource) ConfigFields() []util.ConfigField {
	return []util.ConfigField{
		{Key: "token", Description: "HuggingFace API token (required for private models)", Required: false, Masked: util.MaskAll},
		{Key: "tokenSecret", Description: "Name of Kubernetes Secret containing HF_TOKEN (takes precedence over token)", Required: false},
		{Key: "mirror", Description: "mirror URL for HuggingFace (e.g. https://hf-mirror.com)", Required: false},
	}
}

// Init initializes the plugin with config
func (h *HuggingFaceSource) Init(config map[string]interface{}) error {
	if token, ok := config["token"].(string); ok {
		h.Token = token
	}
	if tokenSecret, ok := config["tokenSecret"].(string); ok {
		h.TokenSecret = tokenSecret
	}
	if mirror, ok := config["mirror"].(string); ok {
		h.Mirror = mirror
	}
	return nil
}

// GenerateTemplateWithRevision generates a pod template with revision support.
// Uses environment variables to pass modelID, modelPath, and revision to avoid
// command injection vulnerabilities from string concatenation.
func (h *HuggingFaceSource) GenerateTemplateWithRevision(modelID string, modelPath string, revision string) (*corev1.PodTemplateSpec, error) {
	var env []corev1.EnvVar

	if h.TokenSecret != "" {
		// Prefer secret reference over plain-text token to avoid exposing credentials
		env = append(env, corev1.EnvVar{
			Name: "HF_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: h.TokenSecret},
					Key:                  "HF_TOKEN",
				},
			},
		})
	} else if h.Token != "" {
		env = append(env, corev1.EnvVar{
			Name:  "HF_TOKEN",
			Value: h.Token,
		})
	}

	if h.Mirror != "" {
		env = append(env, corev1.EnvVar{
			Name:  "HF_ENDPOINT",
			Value: h.Mirror,
		})
	}

	// Add model parameters as environment variables to prevent command injection
	env = append(env,
		corev1.EnvVar{
			Name:  "MODEL_ID",
			Value: modelID,
		},
		corev1.EnvVar{
			Name:  "MODEL_PATH",
			Value: modelPath,
		},
		corev1.EnvVar{
			Name:  "MODEL_REVISION",
			Value: revision,
		},
	)

	// Python script reads from environment variables to avoid string injection
	pythonScript := `import os
from huggingface_hub import snapshot_download

model_id = os.environ['MODEL_ID']
local_dir = os.environ['MODEL_PATH']
revision = os.environ.get('MODEL_REVISION', 'main')

snapshot_download(repo_id=model_id, local_dir=local_dir, revision=revision)`

	downloadCmd := "pip install huggingface_hub -q && python -c \"" + pythonScript + "\""

	return &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "download",
					Image:   "python:3.11-slim",
					Command: []string{"/bin/sh", "-c"},
					Args:    []string{downloadCmd},
					Env:     env,
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}, nil
}
