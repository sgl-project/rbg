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

package core

import (
	"testing"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestGetUpdateOptionsReadsGracePeriodAnnotation(t *testing.T) {
	tests := []struct {
		name                string
		annotations         map[string]string
		expectedGracePeriod int32
	}{
		{
			name: "valid grace period annotation",
			annotations: map[string]string{
				constants.RoleInplaceUpdateGracePeriodSecondsKey: "30",
			},
			expectedGracePeriod: 30,
		},
		{
			name:                "annotation absent uses default",
			annotations:         nil,
			expectedGracePeriod: 0,
		},
		{
			name: "non-numeric annotation is ignored",
			annotations: map[string]string{
				constants.RoleInplaceUpdateGracePeriodSecondsKey: "invalid",
			},
			expectedGracePeriod: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := &workloadsv1alpha2.RoleInstance{}
			instance.Annotations = tt.annotations

			opts := New(instance).GetUpdateOptions()
			if opts.GracePeriodSeconds != tt.expectedGracePeriod {
				t.Fatalf("GracePeriodSeconds = %d, want %d", opts.GracePeriodSeconds, tt.expectedGracePeriod)
			}
		})
	}
}
