/*
Copyright 2025.

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
