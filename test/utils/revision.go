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

package utils

import "encoding/json"

// WithLegacyCreationTimestamp rewrites every "metadata" object in a
// ControllerRevision patch to carry an explicit "creationTimestamp": null,
// reproducing how older client-go versions serialized revisions. It edits the
// parsed tree rather than the raw text, so the result stays valid JSON
// regardless of the fixture's shape.
func WithLegacyCreationTimestamp(patch []byte) ([]byte, error) {
	var tree interface{}
	if err := json.Unmarshal(patch, &tree); err != nil {
		return nil, err
	}

	var inject func(node interface{})
	inject = func(node interface{}) {
		switch n := node.(type) {
		case map[string]interface{}:
			for key, child := range n {
				if meta, ok := child.(map[string]interface{}); ok && key == "metadata" {
					meta["creationTimestamp"] = nil
				}
				inject(child)
			}
		case []interface{}:
			for _, child := range n {
				inject(child)
			}
		}
	}
	inject(tree)

	return json.Marshal(tree)
}
