/*
Copyright 2026.

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

package engine

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/rbgs/cmd/cli/plugin/util"
)

func init() {
	Register("vllm", func() Plugin {
		return &VLLMEngine{}
	})
}

// VLLMEngine implements the EnginePlugin interface for vLLM
type VLLMEngine struct {
	Image string
	Port  int32
}

// Name returns the plugin name
func (v *VLLMEngine) Name() string {
	return "vllm"
}

// ConfigFields returns the config fields this plugin accepts
func (v *VLLMEngine) ConfigFields() []util.ConfigField {
	return []util.ConfigField{
		{Key: "image", Description: "vLLM container image (default: vllm/vllm-openai:latest)", Required: false},
		{Key: "port", Description: "port the server listens on (default: 8000)", Required: false},
	}
}

// Init initializes the plugin with config
func (v *VLLMEngine) Init(config map[string]interface{}) error {
	if image, ok := config["image"].(string); ok {
		v.Image = image
	} else {
		v.Image = "vllm/vllm-openai:latest"
	}
	if port, ok := config["port"].(int); ok {
		v.Port = int32(port)
	} else {
		v.Port = 8000
	}
	return nil
}

// GenerateTemplate generates a pod template for running vLLM
func (v *VLLMEngine) GenerateTemplate(name string, modelID string, modelPath string) (*corev1.PodTemplateSpec, error) {
	return &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "vllm",
					Image: v.Image,
					Args: []string{
						"--model",
						modelPath,
						"--served-model-name",
						name,
					},
					Ports: []corev1.ContainerPort{
						{
							Name:          "http",
							ContainerPort: v.Port,
						},
					},
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
						},
					},
				},
			},
		},
	}, nil
}
