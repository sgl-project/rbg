package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestContainsString(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		str      string
		expected bool
	}{
		{
			name:     "string exists in slice",
			slice:    []string{"a", "b", "c"},
			str:      "b",
			expected: true,
		},
		{
			name:     "string does not exist in slice",
			slice:    []string{"a", "b", "c"},
			str:      "d",
			expected: false,
		},
		{
			name:     "empty slice",
			slice:    []string{},
			str:      "a",
			expected: false,
		},
		{
			name:     "nil slice",
			slice:    nil,
			str:      "a",
			expected: false,
		},
		{
			name:     "empty string in slice",
			slice:    []string{"", "b", "c"},
			str:      "",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := ContainsString(tt.slice, tt.str)
				assert.Equal(t, tt.expected, result)
			},
		)
	}
}

func TestPrettyJson(t *testing.T) {
	tests := []struct {
		name     string
		object   interface{}
		expected string
	}{
		{
			name:     "simple object",
			object:   map[string]string{"key": "value"},
			expected: "{\n    \"key\": \"value\"\n}",
		},
		{
			name:     "nil object",
			object:   nil,
			expected: "null",
		},
		{
			name: "complex object",
			object: struct {
				Name string `json:"name"`
				Age  int    `json:"age"`
			}{
				Name: "test",
				Age:  25,
			},
			expected: "{\n    \"name\": \"test\",\n    \"age\": 25\n}",
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := PrettyJson(tt.object)
				assert.Equal(t, tt.expected, result)
			},
		)
	}
}

func TestFilterSystemAnnotations(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]string
		expected map[string]string
	}{
		{
			name: "filter kubernetes system annotations",
			input: map[string]string{
				"deployment.kubernetes.io/revision":           "1",
				"rolebasedgroup.workloads.x-k8s.io/role-size": "3",
				"app.kubernetes.io/name":                      "test-app",
				"user.annotation":                             "user-value",
				"custom.annotation":                           "custom-value",
			},
			expected: map[string]string{
				"user.annotation":   "user-value",
				"custom.annotation": "custom-value",
			},
		},
		{
			name: "no system annotations",
			input: map[string]string{
				"user.annotation":   "user-value",
				"custom.annotation": "custom-value",
			},
			expected: map[string]string{
				"user.annotation":   "user-value",
				"custom.annotation": "custom-value",
			},
		},
		{
			name:     "nil annotations",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty annotations",
			input:    map[string]string{},
			expected: map[string]string{},
		},
		{
			name: "only system annotations",
			input: map[string]string{
				"deployment.kubernetes.io/revision":           "1",
				"rolebasedgroup.workloads.x-k8s.io/role-size": "3",
				"app.kubernetes.io/name":                      "test-app",
			},
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := FilterSystemAnnotations(tt.input)
				assert.Equal(t, tt.expected, result)
			},
		)
	}
}

func TestFilterSystemLabels(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]string
		expected map[string]string
	}{
		{
			name: "filter kubernetes system labels",
			input: map[string]string{
				"app.kubernetes.io/name":                 "test-app",
				"app.kubernetes.io/component":            "worker",
				"rolebasedgroup.workloads.x-k8s.io/name": "test-rbg",
				"rolebasedgroup.workloads.x-k8s.io/role": "worker",
				"user.label":                             "user-value",
				"custom.label":                           "custom-value",
			},
			expected: map[string]string{
				"user.label":   "user-value",
				"custom.label": "custom-value",
			},
		},
		{
			name: "no system labels",
			input: map[string]string{
				"user.label":   "user-value",
				"custom.label": "custom-value",
			},
			expected: map[string]string{
				"user.label":   "user-value",
				"custom.label": "custom-value",
			},
		},
		{
			name:     "nil labels",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty labels",
			input:    map[string]string{},
			expected: map[string]string{},
		},
		{
			name: "only system labels",
			input: map[string]string{
				"app.kubernetes.io/name":                 "test-app",
				"rolebasedgroup.workloads.x-k8s.io/name": "test-rbg",
			},
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := FilterSystemLabels(tt.input)
				assert.Equal(t, tt.expected, result)
			},
		)
	}
}

func TestFilterSystemEnvs(t *testing.T) {
	tests := []struct {
		name     string
		input    []corev1.EnvVar
		expected []corev1.EnvVar
	}{
		{
			name: "filter system environment variables",
			input: []corev1.EnvVar{
				{Name: "ROLE_INDEX", Value: "3"},
				{Name: "GROUP_NAME", Value: "nginx-cluster"},
				{Name: "USER_ENV", Value: "user-value"},
				{Name: "CUSTOM_ENV", Value: "custom-value"},
			},
			expected: []corev1.EnvVar{
				{Name: "USER_ENV", Value: "user-value"},
				{Name: "CUSTOM_ENV", Value: "custom-value"},
			},
		},
		{
			name: "no system environment variables",
			input: []corev1.EnvVar{
				{Name: "USER_ENV", Value: "user-value"},
				{Name: "CUSTOM_ENV", Value: "custom-value"},
			},
			expected: []corev1.EnvVar{
				{Name: "USER_ENV", Value: "user-value"},
				{Name: "CUSTOM_ENV", Value: "custom-value"},
			},
		},
		{
			name:     "nil environment variables",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty environment variables",
			input:    []corev1.EnvVar{},
			expected: nil,
		},
		{
			name: "only system environment variables",
			input: []corev1.EnvVar{
				{Name: "ROLE_INDEX", Value: "3"},
				{Name: "GROUP_NAME", Value: "nginx-cluster"},
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := FilterSystemEnvs(tt.input)
				assert.Equal(t, tt.expected, result)
			},
		)
	}
}

func TestNonZeroValue(t *testing.T) {
	tests := []struct {
		name     string
		value    int32
		expected int32
	}{
		{
			name:     "positive value",
			value:    5,
			expected: 5,
		},
		{
			name:     "zero value",
			value:    0,
			expected: 0,
		},
		{
			name:     "negative value",
			value:    -3,
			expected: 0,
		},
		{
			name:     "large positive value",
			value:    1000000,
			expected: 1000000,
		},
		{
			name:     "negative one",
			value:    -1,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := NonZeroValue(tt.value)
				assert.Equal(t, tt.expected, result)
			},
		)
	}
}

func TestCalculatePartitionReplicas(t *testing.T) {
	tests := []struct {
		name         string
		partition    *intstr.IntOrString
		replicas     *int32
		expected     int
		expectError  bool
		errorMessage string
	}{
		// Normal cases
		{
			name:      "nil partition",
			partition: nil,
			replicas:  ptrToInt32(10),
			expected:  0,
		},
		{
			name:      "absolute partition value",
			partition: ptrToIntStr(intstr.FromInt(5)),
			replicas:  ptrToInt32(10),
			expected:  5,
		},
		{
			name:      "percentage partition 50%",
			partition: ptrToIntStr(intstr.FromString("50%")),
			replicas:  ptrToInt32(10),
			expected:  5,
		},
		{
			name:      "percentage partition 10%",
			partition: ptrToIntStr(intstr.FromString("10%")),
			replicas:  ptrToInt32(10),
			expected:  1, // roundUp=true ensures at least 1
		},
		{
			name:      "percentage partition 1%",
			partition: ptrToIntStr(intstr.FromString("1%")),
			replicas:  ptrToInt32(100),
			expected:  1,
		},
		{
			name:      "percentage partition 99%",
			partition: ptrToIntStr(intstr.FromString("99%")),
			replicas:  ptrToInt32(10),
			expected:  9, // partition < 100% ensures at least 1 pod upgraded
		},
		{
			name:      "percentage partition 100%",
			partition: ptrToIntStr(intstr.FromString("100%")),
			replicas:  ptrToInt32(10),
			expected:  10,
		},
		// Edge cases
		{
			name:      "partition equals replicas with percentage < 100%",
			partition: ptrToIntStr(intstr.FromString("50%")),
			replicas:  ptrToInt32(2),
			expected:  1, // Special case: if pValue == replicas and partition < 100%, reduce by 1
		},
		{
			name:      "partition 0%",
			partition: ptrToIntStr(intstr.FromString("0%")),
			replicas:  ptrToInt32(10),
			expected:  0,
		},
		{
			name:      "partition exceeds replicas",
			partition: ptrToIntStr(intstr.FromInt(15)),
			replicas:  ptrToInt32(10),
			expected:  10, // Clamped to replicas
		},
		{
			name:      "negative partition",
			partition: ptrToIntStr(intstr.FromInt(-5)),
			replicas:  ptrToInt32(10),
			expected:  0, // Clamped to 0
		},
		{
			name:      "nil replicas with absolute partition",
			partition: ptrToIntStr(intstr.FromInt(5)),
			replicas:  nil,
			expected:  1,
		},
		{
			name:      "nil replicas with percentage partition",
			partition: ptrToIntStr(intstr.FromString("50%")),
			replicas:  nil,
			expected:  0, // replicas defaults to 1, 50% of 1 = 1 (roundUp)
		},
		{
			name:      "zero replicas",
			partition: ptrToIntStr(intstr.FromInt(5)),
			replicas:  ptrToInt32(0),
			expected:  0,
		},
		{
			name:      "small replicas with percentage",
			partition: ptrToIntStr(intstr.FromString("30%")),
			replicas:  ptrToInt32(3),
			expected:  1, // roundUp ensures at least 1
		},
		{
			name:      "large replicas with percentage",
			partition: ptrToIntStr(intstr.FromString("25%")),
			replicas:  ptrToInt32(1000),
			expected:  250,
		},
		// Error cases
		{
			name:         "invalid percentage format",
			partition:    ptrToIntStr(intstr.FromString("invalid")),
			replicas:     ptrToInt32(10),
			expectError:  true,
			errorMessage: "invalid",
		},
		{
			name:        "percentage over 100%",
			partition:   ptrToIntStr(intstr.FromString("150%")),
			replicas:    ptrToInt32(10),
			expectError: false, // GetValueFromIntOrPercent may accept it, but we clamp it
			expected:    10,    // Clamped to replicas
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result, err := CalculatePartitionReplicas(tt.partition, tt.replicas)
				if tt.expectError {
					assert.Error(t, err)
					if tt.errorMessage != "" {
						assert.Contains(t, err.Error(), tt.errorMessage)
					}
				} else {
					assert.NoError(t, err)
					assert.Equal(t, tt.expected, result)
				}
			},
		)
	}
}

func TestParseIntStrAsNonZero(t *testing.T) {
	tests := []struct {
		name        string
		input       *intstr.IntOrString
		replicas    int32
		expected    int32
		expectError bool
	}{
		// Normal cases
		{
			name:     "absolute value",
			input:    ptrToIntStr(intstr.FromInt(5)),
			replicas: 100,
			expected: 5,
		},
		{
			name:     "percentage value 10%",
			input:    ptrToIntStr(intstr.FromString("10%")),
			replicas: 100,
			expected: 10,
		},
		{
			name:     "percentage value 1%",
			input:    ptrToIntStr(intstr.FromString("1%")),
			replicas: 100,
			expected: 1,
		},
		{
			name:     "percentage value 50%",
			input:    ptrToIntStr(intstr.FromString("50%")),
			replicas: 100,
			expected: 50,
		},
		{
			name:     "percentage value with roundUp",
			input:    ptrToIntStr(intstr.FromString("1%")),
			replicas: 99,
			expected: 1, // roundUp ensures at least 1
		},
		// Edge cases - should return at least 1
		{
			name:     "zero value should become 1",
			input:    ptrToIntStr(intstr.FromInt(0)),
			replicas: 100,
			expected: 1,
		},
		{
			name:     "negative value should become 1",
			input:    ptrToIntStr(intstr.FromInt(-5)),
			replicas: 100,
			expected: 1,
		},
		{
			name:     "percentage resulting in 0 should become 1",
			input:    ptrToIntStr(intstr.FromString("0%")),
			replicas: 100,
			expected: 1,
		},
		{
			name:     "large absolute value",
			input:    ptrToIntStr(intstr.FromInt(1000)),
			replicas: 100,
			expected: 1000,
		},
		{
			name:     "percentage with small replicas",
			input:    ptrToIntStr(intstr.FromString("50%")),
			replicas: 2,
			expected: 1, // roundUp ensures at least 1
		},
		{
			name:     "percentage 100%",
			input:    ptrToIntStr(intstr.FromString("100%")),
			replicas: 100,
			expected: 100,
		},
		// Error cases
		{
			name:        "invalid percentage format",
			input:       ptrToIntStr(intstr.FromString("invalid")),
			replicas:    100,
			expectError: true,
			expected:    1, // Returns 1 on error
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result, err := ParseIntStrAsNonZero(*tt.input, tt.replicas)
				if tt.expectError {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
				assert.Equal(t, tt.expected, result)
			},
		)
	}
}

func TestABSFloat64(t *testing.T) {
	tests := []struct {
		name     string
		input    float64
		expected float64
	}{
		{
			name:     "positive value",
			input:    5.5,
			expected: 5.5,
		},
		{
			name:     "negative value",
			input:    -5.5,
			expected: 5.5,
		},
		{
			name:     "zero",
			input:    0.0,
			expected: 0.0,
		},
		{
			name:     "large positive value",
			input:    1000000.123,
			expected: 1000000.123,
		},
		{
			name:     "large negative value",
			input:    -1000000.123,
			expected: 1000000.123,
		},
		{
			name:     "small positive value",
			input:    0.000001,
			expected: 0.000001,
		},
		{
			name:     "small negative value",
			input:    -0.000001,
			expected: 0.000001,
		},
		{
			name:     "max float64",
			input:    1.7976931348623157e+308,
			expected: 1.7976931348623157e+308,
		},
		{
			name:     "min float64",
			input:    -1.7976931348623157e+308,
			expected: 1.7976931348623157e+308,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := ABSFloat64(tt.input)
				assert.Equal(t, tt.expected, result)
			},
		)
	}
}

// Helper function to create int32 pointer
func ptrToInt32(v int32) *int32 {
	return &v
}

// Helper function to create intstr.IntOrString pointer
func ptrToIntStr(v intstr.IntOrString) *intstr.IntOrString {
	return &v
}
