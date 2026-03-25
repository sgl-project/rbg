/*
Copyright 2026 The RBG Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
