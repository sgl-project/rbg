package generate

import (
	"os"
	"testing"
)

// TestCheckAIConfiguratorAvailability tests if aiconfigurator is installed and meets version requirements
// This test requires external tool installation and is skipped by default
// To run this test, set environment variable: RUN_INTEGRATION_TESTS=1
// Example: RUN_INTEGRATION_TESTS=1 go test -v
func TestCheckAIConfiguratorAvailability(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") == "" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION_TESTS=1 to run this test")
	}

	if err := CheckAIConfiguratorAvailability(); err != nil {
		t.Error(err)
	}
}

func TestCheckMinVersion(t *testing.T) {
	tests := []struct {
		name        string
		actual      string
		required    string
		expectError bool
		description string
	}{
		{
			name:        "exact same version",
			actual:      "0.5.0",
			required:    "0.5.0",
			expectError: false,
			description: "0.5.0 == 0.5.0 should pass",
		},
		{
			name:        "higher patch version",
			actual:      "0.5.1",
			required:    "0.5.0",
			expectError: false,
			description: "0.5.1 >= 0.5.0 should pass",
		},
		{
			name:        "higher minor version",
			actual:      "0.6.0",
			required:    "0.5.0",
			expectError: false,
			description: "0.6.0 >= 0.5.0 should pass",
		},
		{
			name:        "higher major version",
			actual:      "1.0.0",
			required:    "0.5.0",
			expectError: false,
			description: "1.0.0 >= 0.5.0 should pass",
		},
		{
			name:        "much higher version",
			actual:      "2.3.4",
			required:    "0.5.0",
			expectError: false,
			description: "2.3.4 >= 0.5.0 should pass",
		},
		{
			name:        "lower patch version",
			actual:      "0.4.9",
			required:    "0.5.0",
			expectError: true,
			description: "0.4.9 < 0.5.0 should fail",
		},
		{
			name:        "lower minor version",
			actual:      "0.3.0",
			required:    "0.5.0",
			expectError: true,
			description: "0.3.0 < 0.5.0 should fail",
		},
		{
			name:        "much lower version",
			actual:      "0.1.0",
			required:    "0.5.0",
			expectError: true,
			description: "0.1.0 < 0.5.0 should fail",
		},
		{
			name:        "same major and minor but lower patch",
			actual:      "0.5.0",
			required:    "0.5.1",
			expectError: true,
			description: "0.5.0 < 0.5.1 should fail",
		},
		{
			name:        "invalid actual version format - missing part",
			actual:      "0.5",
			required:    "0.5.0",
			expectError: true,
			description: "invalid format should fail",
		},
		{
			name:        "invalid actual version format - not a number",
			actual:      "0.5.x",
			required:    "0.5.0",
			expectError: true,
			description: "non-numeric version should fail",
		},
		{
			name:        "invalid required version format",
			actual:      "0.5.0",
			required:    "0.5",
			expectError: true,
			description: "invalid required format should fail",
		},
		{
			name:        "complex higher version",
			actual:      "1.2.3",
			required:    "1.2.2",
			expectError: false,
			description: "1.2.3 >= 1.2.2 should pass",
		},
		{
			name:        "zero versions",
			actual:      "0.0.0",
			required:    "0.0.0",
			expectError: false,
			description: "0.0.0 == 0.0.0 should pass",
		},
		{
			name:        "large version numbers",
			actual:      "10.20.30",
			required:    "10.20.29",
			expectError: false,
			description: "10.20.30 >= 10.20.29 should pass",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkMinVersion(tt.actual, tt.required)

			if tt.expectError {
				if err == nil {
					t.Errorf("checkMinVersion(%q, %q) expected error but got nil - %s",
						tt.actual, tt.required, tt.description)
				}
			} else {
				if err != nil {
					t.Errorf("checkMinVersion(%q, %q) unexpected error: %v - %s",
						tt.actual, tt.required, err, tt.description)
				}
			}
		})
	}
}
