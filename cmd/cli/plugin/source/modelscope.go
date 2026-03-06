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

// GenerateTemplateWithRevision generates a pod template with revision support
func (m *ModelScopeSource) GenerateTemplateWithRevision(modelID string, modelPath string, revision string) (*corev1.PodTemplateSpec, error) {
	var env []corev1.EnvVar

	if m.Token != "" {
		env = append(env, corev1.EnvVar{
			Name:  "MODELSCOPE_TOKEN",
			Value: m.Token,
		})
	}

	// Build download command
	downloadCmd := "pip install modelscope -q && modelscope download --model " + modelID + " --local_dir " + modelPath
	if revision != "" && revision != "main" {
		downloadCmd += " --revision " + revision
	}

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
