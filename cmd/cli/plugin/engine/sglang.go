package engine

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/rbgs/cmd/cli/plugin/util"
)

func init() {
	Register("sglang", func() Plugin {
		return &SGLangEngine{}
	})
}

// SGLangEngine implements the EnginePlugin interface for SGLang
type SGLangEngine struct {
	Image string
	Port  int32
}

// Name returns the plugin name
func (s *SGLangEngine) Name() string {
	return "sglang"
}

// ConfigFields returns the config fields this plugin accepts
func (s *SGLangEngine) ConfigFields() []util.ConfigField {
	return []util.ConfigField{
		{Key: "image", Description: "SGLang container image (default: lmsysorg/sglang:latest)", Required: false},
		{Key: "port", Description: "port the server listens on (default: 30000)", Required: false},
	}
}

// Init initializes the plugin with config
func (s *SGLangEngine) Init(config map[string]interface{}) error {
	if image, ok := config["image"].(string); ok {
		s.Image = image
	} else {
		s.Image = "lmsysorg/sglang:latest"
	}
	if port, ok := config["port"].(int); ok {
		s.Port = int32(port)
	} else {
		s.Port = 30000
	}
	return nil
}

// GenerateTemplate generates a pod template for running SGLang
func (s *SGLangEngine) GenerateTemplate(name string, modelID string, modelPath string) (*corev1.PodTemplateSpec, error) {
	return &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:    "sglang",
					Image:   s.Image,
					Command: []string{"python", "-m", "sglang.launch_server"},
					Args: []string{
						"--model-path",
						modelPath,
						"--model-name",
						name,
					},
					Ports: []corev1.ContainerPort{
						{
							Name:          "http",
							ContainerPort: s.Port,
						},
					},
					Env: []corev1.EnvVar{
						{
							Name:  "SGLANG_MODEL_PATH",
							Value: modelPath,
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
