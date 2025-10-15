package instance

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func TestIsInstanceReady(t *testing.T) {
	testCases := []struct {
		name     string
		instance *appsv1alpha1.Instance
		expected bool
	}{
		{
			name: "Instance is ready",
			instance: &appsv1alpha1.Instance{
				Status: appsv1alpha1.InstanceStatus{
					Conditions: []appsv1alpha1.InstanceCondition{
						{
							Type:   appsv1alpha1.InstanceReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "Instance is not ready, ready condition is null",
			instance: &appsv1alpha1.Instance{
				Status: appsv1alpha1.InstanceStatus{
					Conditions: []appsv1alpha1.InstanceCondition{},
				},
			},
			expected: false,
		},
		{
			name: "Instance is not ready, ready condition is false",
			instance: &appsv1alpha1.Instance{
				Status: appsv1alpha1.InstanceStatus{
					Conditions: []appsv1alpha1.InstanceCondition{
						{
							Type:   appsv1alpha1.InstanceReady,
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
			ready := IsInstanceReady(tc.instance)
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
				appsv1alpha1.InstanceComponentName: "web",
			},
			componentName: "web",
			expected:      true,
		},
		{
			name: "Pod does not match component",
			podLabels: map[string]string{
				appsv1alpha1.InstanceComponentName: "db",
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
		component appsv1alpha1.InstanceComponent
		expected  int32
	}{
		{
			name: "Component with replicas",
			component: appsv1alpha1.InstanceComponent{
				Size: func() *int32 { i := int32(3); return &i }(),
			},
			expected: 3,
		},
		{
			name: "Component with default replicas",
			component: appsv1alpha1.InstanceComponent{
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
