package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterAndGet_UnknownType(t *testing.T) {
	_, err := Get("nonexistent", map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown engine type")
}

func TestValidateConfig_UnknownType(t *testing.T) {
	err := ValidateConfig("nonexistent", map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown engine type")
}

func TestGetFields_UnknownType(t *testing.T) {
	fields := GetFields("nonexistent")
	assert.Nil(t, fields)
}

func TestRegisteredNames_ContainsBuiltins(t *testing.T) {
	names := RegisteredNames()
	assert.Contains(t, names, "vllm")
	assert.Contains(t, names, "sglang")
}
