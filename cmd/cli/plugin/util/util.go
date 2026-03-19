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

package util

import "fmt"

// ConfigField describes a single configuration key for a plugin.
type ConfigField struct {
	Key         string
	Description string
	Required    bool
}

// ValidateConfig checks that all required fields are present and that no unknown fields are provided.
func ValidateConfig(fields []ConfigField, config map[string]interface{}) error {
	// Build a set of valid keys
	validKeys := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		validKeys[f.Key] = struct{}{}
	}

	// Check for unknown keys
	for key := range config {
		if _, ok := validKeys[key]; !ok {
			return fmt.Errorf("unknown config field %q", key)
		}
	}

	// Check required fields
	for _, f := range fields {
		if !f.Required {
			continue
		}
		v, ok := config[f.Key]
		if !ok || fmt.Sprintf("%v", v) == "" {
			return fmt.Errorf("required config field %q is missing (hint: %s)", f.Key, f.Description)
		}
	}
	return nil
}
