package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
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
