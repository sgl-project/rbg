package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateConfig_UnknownKey(t *testing.T) {
	fields := []ConfigField{
		{Key: "token", Required: false},
	}
	err := ValidateConfig(fields, map[string]interface{}{
		"token":   "abc",
		"unknown": "bad",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config field")
}

func TestValidateConfig_MissingRequired(t *testing.T) {
	fields := []ConfigField{
		{Key: "pvcName", Description: "PVC name", Required: true},
	}
	err := ValidateConfig(fields, map[string]interface{}{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required config field")
	assert.Contains(t, err.Error(), "pvcName")
}

func TestValidateConfig_EmptyRequiredValue(t *testing.T) {
	fields := []ConfigField{
		{Key: "pvcName", Description: "PVC name", Required: true},
	}
	err := ValidateConfig(fields, map[string]interface{}{"pvcName": ""})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required config field")
}

func TestValidateConfig_OK(t *testing.T) {
	fields := []ConfigField{
		{Key: "token", Required: false},
		{Key: "mirror", Required: false},
	}
	err := ValidateConfig(fields, map[string]interface{}{"token": "abc"})
	assert.NoError(t, err)
}

func TestValidateConfig_EmptyConfig(t *testing.T) {
	fields := []ConfigField{
		{Key: "token", Required: false},
	}
	err := ValidateConfig(fields, map[string]interface{}{})
	assert.NoError(t, err)
}

func TestValidateConfig_AllRequiredPresent(t *testing.T) {
	fields := []ConfigField{
		{Key: "pvcName", Description: "PVC name", Required: true},
		{Key: "extra", Required: false},
	}
	err := ValidateConfig(fields, map[string]interface{}{"pvcName": "my-pvc"})
	assert.NoError(t, err)
}
