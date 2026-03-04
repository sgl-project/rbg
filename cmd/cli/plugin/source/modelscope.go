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
	Register("modelscope", func() Plugin {
		return &ModelScopeSource{}
	})
}

// ModelScopeSource implements the SourcePlugin interface for ModelScope
type ModelScopeSource struct {
	Token string
}

// Name returns the plugin name
func (m *ModelScopeSource) Name() string {
	return "modelscope"
}

// ConfigFields returns the config fields this plugin accepts
func (m *ModelScopeSource) ConfigFields() []util.ConfigField {
	return []util.ConfigField{
		{Key: "token", Description: "ModelScope API token (required for private models)", Required: false},
	}
}

// Init initializes the plugin with config
func (m *ModelScopeSource) Init(config map[string]interface{}) error {
	if token, ok := config["token"].(string); ok {
		m.Token = token
	}
	return nil
}

// GenerateTemplate generates a pod template for downloading models from ModelScope
func (m *ModelScopeSource) GenerateTemplate(modelID string, modelPath string) (*corev1.PodTemplateSpec, error) {
	var env []corev1.EnvVar

	if m.Token != "" {
		env = append(env, corev1.EnvVar{
			Name:  "MODELSCOPE_TOKEN",
			Value: m.Token,
		})
	}

	return &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "download",
					Image:   "modelscope/modelscope:latest",
					Command: []string{"modelscope"},
					Args: []string{
						"download",
						"--model",
						modelID,
						"--local_dir",
						modelPath,
					},
					Env: env,
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}, nil
}
