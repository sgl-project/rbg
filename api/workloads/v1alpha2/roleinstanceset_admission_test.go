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
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Lifecycle reconfiguration is validated statically. The webhook must not
// cross-check live RoleInstance runtime state, so reassigning a signal key
// between hooks is accepted without any API reader.
func TestRoleInstanceSetValidatorAcceptsLifecycleReconfiguration(t *testing.T) {
	oldSet := &RoleInstanceSet{
		ObjectMeta: metav1.ObjectMeta{Name: "set", Namespace: "default", UID: "set-uid"},
		Spec: RoleInstanceSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "rbg"}},
			RoleInstanceTemplate: RoleInstanceTemplate{
				RoleInstanceSpec: RoleInstanceSpec{
					Lifecycle: &RoleInstanceLifecycle{
						PreUpdate: &RoleInstanceLifecycleHook{
							SignalLabels: map[string]string{"example.com/signal": "true"},
						},
					},
				},
			},
		},
	}
	newSet := oldSet.DeepCopy()
	newSet.Spec.RoleInstanceTemplate.Lifecycle = &RoleInstanceLifecycle{
		PreDelete: &RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"example.com/signal": "true"},
		},
	}

	validator := &RoleInstanceSetValidator{}
	if _, err := validator.ValidateUpdate(context.Background(), oldSet, newSet); err != nil {
		t.Fatalf("ValidateUpdate() error = %v, want static validation only", err)
	}
}
