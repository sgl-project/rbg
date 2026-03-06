package source

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGet_UnknownType(t *testing.T) {
	_, err := Get("nonexistent", map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown source type")
}

func TestValidateConfig_UnknownType(t *testing.T) {
	err := ValidateConfig("nonexistent", map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown source type")
}

func TestGetFields_UnknownType(t *testing.T) {
	fields := GetFields("nonexistent")
	assert.Nil(t, fields)
}

func TestRegisteredNames_ContainsBuiltins(t *testing.T) {
	names := RegisteredNames()
	assert.Contains(t, names, "huggingface")
	assert.Contains(t, names, "modelscope")
}
