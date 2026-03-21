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

package instance

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestIsRoleInstanceReady(t *testing.T) {
	testCases := []struct {
		name     string
		instance *workloadsv1alpha2.RoleInstance
		expected bool
	}{
		{
			name: "Instance is ready",
			instance: &workloadsv1alpha2.RoleInstance{
				Status: workloadsv1alpha2.RoleInstanceStatus{
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "Instance is not ready, ready condition is null",
			instance: &workloadsv1alpha2.RoleInstance{
				Status: workloadsv1alpha2.RoleInstanceStatus{
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{},
				},
			},
			expected: false,
		},
		{
			name: "Instance is not ready, ready condition is false",
			instance: &workloadsv1alpha2.RoleInstance{
				Status: workloadsv1alpha2.RoleInstanceStatus{
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ready := IsRoleInstanceReady(tc.instance)
			if ready != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, ready)
			}
		})
	}
}

func TestIsMatchComponent(t *testing.T) {
	testCases := []struct {
		name          string
		podLabels     map[string]string
		componentName string
		expected      bool
	}{
		{
			name: "Pod matches component",
			podLabels: map[string]string{
				constants.ComponentNameLabelKey: "web",
			},
			componentName: "web",
			expected:      true,
		},
		{
			name: "Pod does not match component",
			podLabels: map[string]string{
				constants.ComponentNameLabelKey: "db",
			},
			componentName: "web",
			expected:      false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: tc.podLabels,
				},
			}
			match := MatchComponent(pod, tc.componentName)
			if match != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, match)
			}
		})
	}
}

func TestGetComponentSize(t *testing.T) {
	testCases := []struct {
		name      string
		component workloadsv1alpha2.RoleInstanceComponent
		expected  int32
	}{
		{
			name: "Component with replicas",
			component: workloadsv1alpha2.RoleInstanceComponent{
				Size: func() *int32 { i := int32(3); return &i }(),
			},
			expected: 3,
		},
		{
			name: "Component with default replicas",
			component: workloadsv1alpha2.RoleInstanceComponent{
				Size: nil,
			},
			expected: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			size := GetComponentSize(&tc.component)
			if size != tc.expected {
				t.Errorf("expected %d, got %d", tc.expected, size)
			}
		})
	}
}
