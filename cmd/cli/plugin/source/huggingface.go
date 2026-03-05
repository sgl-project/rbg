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
	Token  string
	Mirror string
}

// Name returns the plugin name
func (h *HuggingFaceSource) Name() string {
	return "huggingface"
}

// ConfigFields returns the config fields this plugin accepts
func (h *HuggingFaceSource) ConfigFields() []util.ConfigField {
	return []util.ConfigField{
		{Key: "token", Description: "HuggingFace API token (required for private models)", Required: false},
		{Key: "mirror", Description: "mirror URL for HuggingFace (e.g. https://hf-mirror.com)", Required: false},
	}
}

// Init initializes the plugin with config
func (h *HuggingFaceSource) Init(config map[string]interface{}) error {
	if token, ok := config["token"].(string); ok {
		h.Token = token
	}
	if mirror, ok := config["mirror"].(string); ok {
		h.Mirror = mirror
	}
	return nil
}

// GenerateTemplateWithRevision generates a pod template with revision support
func (h *HuggingFaceSource) GenerateTemplateWithRevision(modelID string, modelPath string, revision string) (*corev1.PodTemplateSpec, error) {
	var env []corev1.EnvVar

	if h.Token != "" {
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

	// Build download command using Python API directly to avoid PATH issues with huggingface-cli
	pythonScript := "from huggingface_hub import snapshot_download; snapshot_download('" + modelID + "', local_dir='" + modelPath + "'"
	if revision != "" && revision != "main" {
		pythonScript += ", revision='" + revision + "'"
	}
	pythonScript += ")"
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
