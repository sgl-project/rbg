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

package common

import "strings"

// InheritPodGroupAnnotations copies workload annotations that match
// the scheduler-specific PodGroup annotation prefixes.
func InheritPodGroupAnnotations(annotations map[string]string, prefixes ...string) map[string]string {
	if len(annotations) == 0 || len(prefixes) == 0 {
		return nil
	}

	inherited := make(map[string]string)
	for key, value := range annotations {
		for _, prefix := range prefixes {
			if strings.HasPrefix(key, prefix) {
				inherited[key] = value
				break
			}
		}
	}

	if len(inherited) == 0 {
		return nil
	}

	return inherited
}
