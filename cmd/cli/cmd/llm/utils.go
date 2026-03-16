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

// printRBG prints a v1alpha2 RoleBasedGroup as YAML
func printRBG(rbg *workloadsv1alpha2.RoleBasedGroup) error {
	out, err := yaml.Marshal(rbg)
	if err != nil {
		return fmt.Errorf("failed to marshal RoleBasedGroup: %w", err)
	}
	_, err = os.Stdout.Write(out)
	return err
}
