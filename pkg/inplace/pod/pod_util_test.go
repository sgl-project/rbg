package pod

import (
	"testing"
	"time"
)

func TestGetShortHash(t *testing.T) {
	testCases := []struct {
		name     string
		fullHash string
		expected string
	}{
		{
			name:     "Valid hash",
			fullHash: "some-prefix-name-abcdef1",
			expected: "abcdef1",
		},
		{
			name:     "Short hash",
			fullHash: "abcdef1",
			expected: "abcdef1",
		},
		{
			name:     "Empty hash",
			fullHash: "",
			expected: "",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			shortHash := GetShortHash(tc.fullHash)
			if shortHash != tc.expected {
				t.Errorf("expected %s, got %s", tc.expected, shortHash)
			}
		})
	}
}

func TestRoundupSeconds(t *testing.T) {
	testCases := []struct {
		name     string
		input    time.Duration // in nanoseconds
		expected time.Duration // in nanoseconds
	}{
		{
			name:     "Exact second",
			input:    3 * time.Second,
			expected: 3 * time.Second,
		},
		{
			name:     "Fractional second",
			input:    3*time.Second + 500*time.Millisecond,
			expected: 4 * time.Second,
		},
		{
			name:     "Just below second",
			input:    2*time.Second + 999*time.Millisecond,
			expected: 3 * time.Second,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			d := RoundupSeconds(tc.input)
			if d != tc.expected {
				t.Errorf("expected %d, got %d", tc.expected, int64(d))
			}
		})
	}
}
