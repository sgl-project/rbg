package utils

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

// TestGetPodComponentID tests the GetPodComponentID function after the fix
func TestGetPodComponentID(t *testing.T) {
	// Test cases: covers scenarios with labels present/absent, valid values, boundary values, out-of-range, and non-numeric cases
	tests := []struct {
		name     string      // Test case name
		pod      *corev1.Pod // Input Pod object
		expected int32       // Expected returned ComponentID
	}{
		// Scenario 1: Get ID from label (normal case)
		{
			name: "valid id from label (positive)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1alpha1.InstanceComponentIDKey: "123",
					},
				},
			},
			expected: 123,
		},
		{
			name: "valid id from label (negative)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1alpha1.InstanceComponentIDKey: "-456",
					},
				},
			},
			expected: -456,
		},

		// Scenario 2: Label exists but value is invalid (non-numeric, out of range)
		{
			name: "invalid non-numeric id in label",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1alpha1.InstanceComponentIDKey: "abc",
					},
				},
			},
			expected: 0, // Return default value 0 on error
		},
		{
			name: "id in label exceeds int32 max",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1alpha1.InstanceComponentIDKey: "2147483648", // 比MaxInt32大1
					},
				},
			},
			expected: 0,
		},
		{
			name: "id in label less than int32 min",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1alpha1.InstanceComponentIDKey: "-2147483649", // 1 less than MinInt32
					},
				},
			},
			expected: 0,
		},

		// Scenario 3: Label doesn't exist, parse ID from Pod name (name format: prefix-componentName-ID)
		{
			name: "valid id from pod name (positive)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "instance-web-789",  // last segment is the ID
					Labels: map[string]string{}, // no ComponentID label
				},
			},
			expected: 789,
		},
		{
			name: "valid id from pod name (negative)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "instance-db--987", // Last segment is negative ID
					Labels: map[string]string{},
				},
			},
			expected: -987,
		},

		// Scenario 4: Label doesn't exist, name parsing is invalid
		{
			name: "invalid non-numeric id in pod name",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "instance-cache-xyz", // Last segment is non-numeric
					Labels: map[string]string{},
				},
			},
			expected: 0,
		},
		{
			name: "id in pod name exceeds int32 max",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "instance-store-2147483648",
					Labels: map[string]string{},
				},
			},
			expected: 0,
		},
		{
			name: "id in pod name less than int32 min",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "instance-queue--2147483649",
					Labels: map[string]string{},
				},
			},
			expected: 0,
		},

		// Scenario 5: Boundary value tests (int32 max/min values)
		{
			name: "int32 max value in label",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1alpha1.InstanceComponentIDKey: "2147483647", // math.MaxInt32
					},
				},
			},
			expected: math.MaxInt32,
		},
		{
			name: "int32 min value in label",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1alpha1.InstanceComponentIDKey: "-2147483648", // math.MinInt32
					},
				},
			},
			expected: math.MinInt32,
		},
		{
			name: "int32 max value in pod name",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "instance-max-2147483647",
					Labels: map[string]string{},
				},
			},
			expected: math.MaxInt32,
		},
	}

	// Execute test cases
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetPodComponentID(tt.pod)
			assert.Equal(t, tt.expected, result, "Test case [%s] failed", tt.name)
		})
	}
}
