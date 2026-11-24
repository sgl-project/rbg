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

package v1alpha2

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func TestValidateRoleInstanceLifecycle(t *testing.T) {
	valid := &RoleInstanceLifecycle{
		PreUpdate: &RoleInstanceLifecycleHook{SignalLabels: map[string]string{"example.com/update": "true"}},
		PreRecover: &RoleInstanceLifecycleHook{SignalLabels: map[string]string{
			"example.com/recover": "true",
		}},
		PostReady: &RoleInstanceLifecycleHook{SignalLabels: map[string]string{"example.com/ready": "true"}},
		PreDelete: &RoleInstanceLifecycleHook{SignalLabels: map[string]string{"example.com/delete": "true"}},
	}

	tests := []struct {
		name      string
		lifecycle *RoleInstanceLifecycle
		selector  *metav1.LabelSelector
		labels    map[string]string
		wantErr   string
	}{
		{
			name:      "valid",
			lifecycle: valid,
		},
		{
			name: "empty signal labels",
			lifecycle: &RoleInstanceLifecycle{
				PreUpdate: &RoleInstanceLifecycleHook{},
			},
			wantErr: "signalLabels must not be empty",
		},
		{
			name: "shared signal key",
			lifecycle: &RoleInstanceLifecycle{
				PreUpdate: &RoleInstanceLifecycleHook{SignalLabels: map[string]string{"example.com/signal": "true"}},
				PostReady: &RoleInstanceLifecycleHook{SignalLabels: map[string]string{"example.com/signal": "true"}},
			},
			wantErr: "",
		},
		{
			name: "reserved key prefix",
			lifecycle: &RoleInstanceLifecycle{
				PreUpdate: &RoleInstanceLifecycleHook{SignalLabels: map[string]string{
					constants.RBGPrefix + "signal": "true",
				}},
			},
			wantErr: "uses reserved prefix",
		},
		{
			name: "selector collision",
			lifecycle: &RoleInstanceLifecycle{
				PreUpdate: &RoleInstanceLifecycleHook{SignalLabels: map[string]string{"example.com/signal": "true"}},
			},
			selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"example.com/signal": "member"},
			},
			wantErr: "collides with the owning selector",
		},
		{
			name: "declarative label collision",
			lifecycle: &RoleInstanceLifecycle{
				PreUpdate: &RoleInstanceLifecycleHook{SignalLabels: map[string]string{"example.com/signal": "true"}},
			},
			labels:  map[string]string{"example.com/signal": "user"},
			wantErr: "collides with a declarative label",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRoleInstanceLifecycle("spec.lifecycle", test.lifecycle, test.selector, test.labels)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateRoleInstanceLifecycle() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestValidateRoleInstanceSetLifecycleMutualExclusion(t *testing.T) {
	set := &RoleInstanceSet{
		Spec: RoleInstanceSetSpec{
			RoleInstanceTemplate: RoleInstanceTemplate{
				RoleInstanceSpec: RoleInstanceSpec{
					Lifecycle: &RoleInstanceLifecycle{
						PreUpdate: &RoleInstanceLifecycleHook{SignalLabels: map[string]string{"example.com/update": "true"}},
					},
				},
			},
			Lifecycle: &RoleInstanceSetLifecycle{
				PreDelete: &RoleInstanceSetLifecycleHook{},
			},
		},
	}
	err := ValidateRoleInstanceSetLifecycle(set)
	if err == nil || !strings.Contains(err.Error(), "cannot both be non-empty") {
		t.Fatalf("ValidateRoleInstanceSetLifecycle() error = %v, want mutual-exclusion rejection", err)
	}
}
