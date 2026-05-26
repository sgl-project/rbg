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

package roleinstance

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInconsistentBaselines(t *testing.T) {
	tests := []struct {
		name     string
		old      map[string]map[string]int32
		new      map[string]map[string]int32
		expected bool
	}{
		{
			name:     "both nil",
			old:      nil,
			new:      nil,
			expected: false,
		},
		{
			name:     "old nil, new non-nil",
			old:      nil,
			new:      map[string]map[string]int32{"pod-0": {"main": 0}},
			expected: true,
		},
		{
			name:     "old non-nil, new nil",
			old:      map[string]map[string]int32{"pod-0": {"main": 0}},
			new:      nil,
			expected: true,
		},
		{
			name:     "same content",
			old:      map[string]map[string]int32{"pod-0": {"main": 0, "sidecar": 2}},
			new:      map[string]map[string]int32{"pod-0": {"main": 0, "sidecar": 2}},
			expected: false,
		},
		{
			name:     "different pod names",
			old:      map[string]map[string]int32{"pod-0": {"main": 0}},
			new:      map[string]map[string]int32{"pod-1": {"main": 0}},
			expected: true,
		},
		{
			name:     "different container names",
			old:      map[string]map[string]int32{"pod-0": {"main": 0}},
			new:      map[string]map[string]int32{"pod-0": {"sidecar": 0}},
			expected: true,
		},
		{
			name:     "different restart counts",
			old:      map[string]map[string]int32{"pod-0": {"main": 0}},
			new:      map[string]map[string]int32{"pod-0": {"main": 1}},
			expected: true,
		},
		{
			name:     "extra pod in new",
			old:      map[string]map[string]int32{"pod-0": {"main": 0}},
			new:      map[string]map[string]int32{"pod-0": {"main": 0}, "pod-1": {"main": 0}},
			expected: true,
		},
		{
			name:     "both empty maps",
			old:      map[string]map[string]int32{},
			new:      map[string]map[string]int32{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := inconsistentBaselines(tt.old, tt.new)
			assert.Equal(t, tt.expected, result)
		})
	}
}
