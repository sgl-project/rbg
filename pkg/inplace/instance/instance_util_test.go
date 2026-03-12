package instance

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/constants"
)

func TestIsRoleInstanceReady(t *testing.T) {
	testCases := []struct {
		name     string
		instance *appsv1alpha2.RoleInstance
		expected bool
	}{
		{
			name: "Instance is ready",
			instance: &appsv1alpha2.RoleInstance{
				Status: appsv1alpha2.RoleInstanceStatus{
					Conditions: []appsv1alpha2.RoleInstanceCondition{
						{
							Type:   appsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "Instance is not ready, ready condition is null",
			instance: &appsv1alpha2.RoleInstance{
				Status: appsv1alpha2.RoleInstanceStatus{
					Conditions: []appsv1alpha2.RoleInstanceCondition{},
				},
			},
			expected: false,
		},
		{
			name: "Instance is not ready, ready condition is false",
			instance: &appsv1alpha2.RoleInstance{
				Status: appsv1alpha2.RoleInstanceStatus{
					Conditions: []appsv1alpha2.RoleInstanceCondition{
						{
							Type:   appsv1alpha2.RoleInstanceReady,
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
				constants.RoleInstanceComponentNameKey: "web",
			},
			componentName: "web",
			expected:      true,
		},
		{
			name: "Pod does not match component",
			podLabels: map[string]string{
				constants.RoleInstanceComponentNameKey: "db",
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
		component appsv1alpha2.RoleInstanceComponent
		expected  int32
	}{
		{
			name: "Component with replicas",
			component: appsv1alpha2.RoleInstanceComponent{
				Size: func() *int32 { i := int32(3); return &i }(),
			},
			expected: 3,
		},
		{
			name: "Component with default replicas",
			component: appsv1alpha2.RoleInstanceComponent{
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
