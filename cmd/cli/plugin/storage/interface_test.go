package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGet_UnknownType(t *testing.T) {
	_, err := Get("nonexistent", map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown storage type")
}

func TestValidateConfig_UnknownType(t *testing.T) {
	err := ValidateConfig("nonexistent", map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown storage type")
}

func TestGetFields_UnknownType(t *testing.T) {
	fields := GetFields("nonexistent")
	assert.Nil(t, fields)
}

func TestRegisteredNames_ContainsPVC(t *testing.T) {
	names := RegisteredNames()
	assert.Contains(t, names, "pvc")
}
