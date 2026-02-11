package benchmark

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsPVCStorageURI(t *testing.T) {
	tests := []struct {
		name     string
		uri      string
		expected bool
	}{
		{
			name:     "valid pvc URI",
			uri:      "pvc://my-pvc/",
			expected: true,
		},
		{
			name:     "valid pvc URI with sub-path",
			uri:      "pvc://my-pvc/models/tokenizer",
			expected: true,
		},
		{
			name:     "valid pvc URI with namespace",
			uri:      "pvc://my-ns:my-pvc/",
			expected: true,
		},
		{
			name:     "not a pvc URI - http",
			uri:      "http://example.com",
			expected: false,
		},
		{
			name:     "not a pvc URI - huggingface model",
			uri:      "Qwen/Qwen3-8B",
			expected: false,
		},
		{
			name:     "empty string",
			uri:      "",
			expected: false,
		},
		{
			name:     "pvc prefix only",
			uri:      "pvc://",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPVCStorageURI(tt.uri)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParsePVCStorageURI(t *testing.T) {
	tests := []struct {
		name        string
		uri         string
		expected    *PVCComponents
		expectError bool
		errContains string
	}{
		{
			name: "simple pvc with trailing slash",
			uri:  "pvc://my-pvc/",
			expected: &PVCComponents{
				PVCName: "my-pvc",
				SubPath: "",
			},
		},
		{
			name: "pvc with sub-path",
			uri:  "pvc://my-pvc/models/tokenizer",
			expected: &PVCComponents{
				PVCName: "my-pvc",
				SubPath: "models/tokenizer",
			},
		},
		{
			name: "pvc with namespace",
			uri:  "pvc://my-ns:my-pvc/",
			expected: &PVCComponents{
				Namespace: "my-ns",
				PVCName:   "my-pvc",
				SubPath:   "",
			},
		},
		{
			name: "pvc with namespace and sub-path",
			uri:  "pvc://my-ns:my-pvc/data/output",
			expected: &PVCComponents{
				Namespace: "my-ns",
				PVCName:   "my-pvc",
				SubPath:   "data/output",
			},
		},
		{
			name: "pvc with simple sub-path",
			uri:  "pvc://my-pvc/sub",
			expected: &PVCComponents{
				PVCName: "my-pvc",
				SubPath: "sub",
			},
		},
		{
			name:        "missing pvc prefix",
			uri:         "http://my-pvc/",
			expectError: true,
			errContains: "missing pvc:// prefix",
		},
		{
			name:        "empty content after prefix",
			uri:         "pvc://",
			expectError: true,
			errContains: "missing content after prefix",
		},
		{
			name:        "missing trailing slash",
			uri:         "pvc://my-pvc",
			expectError: true,
			errContains: "missing trailing slash",
		},
		{
			name:        "empty namespace before colon",
			uri:         "pvc://:my-pvc/",
			expectError: true,
			errContains: "empty namespace before colon",
		},
		{
			name:        "empty pvc name after colon",
			uri:         "pvc://my-ns:/",
			expectError: true,
			errContains: "empty PVC name after colon",
		},
		{
			name:        "empty pvc name without namespace",
			uri:         "pvc:///sub-path",
			expectError: true,
			errContains: "missing PVC name",
		},
		{
			name:        "empty string",
			uri:         "",
			expectError: true,
			errContains: "missing pvc:// prefix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parsePVCStorageURI(tt.uri)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, tt.expected.Namespace, result.Namespace)
				assert.Equal(t, tt.expected.PVCName, result.PVCName)
				assert.Equal(t, tt.expected.SubPath, result.SubPath)
			}
		})
	}
}

func TestBuildTokenizerConfig(t *testing.T) {
	tests := []struct {
		name              string
		tokenizer         string
		expectError       bool
		errContains       string
		expectedValue     string
		expectedVolumes   int
		expectedMounts    int
		expectedSubPath   string
		expectedClaimName string
	}{
		{
			name:            "huggingface model name - no PVC",
			tokenizer:       "Qwen/Qwen3-8B",
			expectedValue:   "Qwen/Qwen3-8B",
			expectedVolumes: 0,
			expectedMounts:  0,
		},
		{
			name:              "pvc URI without sub-path",
			tokenizer:         "pvc://tokenizer-pvc/",
			expectedValue:     tokenizerMountPath,
			expectedVolumes:   1,
			expectedMounts:    1,
			expectedSubPath:   "",
			expectedClaimName: "tokenizer-pvc",
		},
		{
			name:              "pvc URI with sub-path",
			tokenizer:         "pvc://tokenizer-pvc/models/qwen",
			expectedValue:     tokenizerMountPath,
			expectedVolumes:   1,
			expectedMounts:    1,
			expectedSubPath:   "models/qwen",
			expectedClaimName: "tokenizer-pvc",
		},
		{
			name:              "pvc URI with namespace",
			tokenizer:         "pvc://my-ns:tokenizer-pvc/data",
			expectedValue:     tokenizerMountPath,
			expectedVolumes:   1,
			expectedMounts:    1,
			expectedSubPath:   "data",
			expectedClaimName: "tokenizer-pvc",
		},
		{
			name:        "invalid pvc URI",
			tokenizer:   "pvc://",
			expectError: true,
			errContains: "failed to parse PVC storage URI",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, volumes, mounts, err := buildTokenizerConfig(tt.tokenizer)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedValue, value)
			assert.Len(t, volumes, tt.expectedVolumes)
			assert.Len(t, mounts, tt.expectedMounts)

			if tt.expectedVolumes > 0 {
				assert.Equal(t, tokenizerVolumeName, volumes[0].Name)
				assert.Equal(t, tt.expectedClaimName, volumes[0].VolumeSource.PersistentVolumeClaim.ClaimName)
			}

			if tt.expectedMounts > 0 {
				assert.Equal(t, tokenizerVolumeName, mounts[0].Name)
				assert.Equal(t, tokenizerMountPath, mounts[0].MountPath)
				assert.True(t, mounts[0].ReadOnly)
				assert.Equal(t, tt.expectedSubPath, mounts[0].SubPath)
			}
		})
	}
}

func TestBuildOutputConfig(t *testing.T) {
	tests := []struct {
		name              string
		outputDir         string
		expectError       bool
		errContains       string
		expectedClaimName string
		expectedSubPath   string
	}{
		{
			name:              "pvc URI without sub-path",
			outputDir:         "pvc://output-pvc/",
			expectedClaimName: "output-pvc",
			expectedSubPath:   "",
		},
		{
			name:              "pvc URI with sub-path",
			outputDir:         "pvc://output-pvc/results/exp1",
			expectedClaimName: "output-pvc",
			expectedSubPath:   "results/exp1",
		},
		{
			name:              "pvc URI with namespace",
			outputDir:         "pvc://my-ns:output-pvc/data",
			expectedClaimName: "output-pvc",
			expectedSubPath:   "data",
		},
		{
			name:        "invalid pvc URI",
			outputDir:   "pvc://",
			expectError: true,
			errContains: "failed to parse output PVC storage URI",
		},
		{
			name:        "not a pvc URI",
			outputDir:   "/local/path",
			expectError: true,
			errContains: "failed to parse output PVC storage URI",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			volume, mount, err := buildOutputConfig(tt.outputDir)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, outputVolumeName, volume.Name)
			assert.Equal(t, tt.expectedClaimName, volume.VolumeSource.PersistentVolumeClaim.ClaimName)
			assert.Equal(t, outputVolumeName, mount.Name)
			assert.Equal(t, outputMountPath, mount.MountPath)
			assert.Equal(t, tt.expectedSubPath, mount.SubPath)
		})
	}
}
