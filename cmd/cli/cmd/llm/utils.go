package llm

import (
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// sanitizeModelID sanitizes the model ID for use in resource names
func sanitizeModelID(modelID string) string {
	result := strings.ReplaceAll(modelID, "/", "-")
	result = strings.ReplaceAll(result, ":", "-")
	result = strings.ReplaceAll(result, "_", "-")
	result = strings.ToLower(result)
	return result
}

// printPodTemplate prints a corev1.PodTemplateSpec as a YAML Pod object
func printPodTemplate(name string, podTemplate *corev1.PodTemplateSpec) error {
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: podTemplate.Spec,
	}
	out, err := yaml.Marshal(pod)
	if err != nil {
		return fmt.Errorf("failed to marshal pod template: %w", err)
	}
	_, err = os.Stdout.Write(out)
	return err
}
