package llm

import (
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// sanitizeModelID sanitizes the model ID for use in resource names
func sanitizeModelID(modelID string) string {
	result := strings.ReplaceAll(modelID, "/", "-")
	result = strings.ReplaceAll(result, ":", "-")
	result = strings.ReplaceAll(result, "_", "-")
	result = strings.ToLower(result)
	return result
}

// printRBG prints a RoleBasedGroup as YAML
func printRBG(rbg *workloadsv1alpha2.RoleBasedGroup) error {
	out, err := yaml.Marshal(rbg)
	if err != nil {
		return fmt.Errorf("failed to marshal RoleBasedGroup: %w", err)
	}
	_, err = os.Stdout.Write(out)
	return err
}
