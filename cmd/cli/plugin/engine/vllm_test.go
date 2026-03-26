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

package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVLLMEngine_Name(t *testing.T) {
	v := &VLLMEngine{}
	assert.Equal(t, "vllm", v.Name())
}

func TestVLLMEngine_ConfigFields(t *testing.T) {
	v := &VLLMEngine{}
	fields := v.ConfigFields()
	assert.Len(t, fields, 2)
	keys := []string{fields[0].Key, fields[1].Key}
	assert.Contains(t, keys, "image")
	assert.Contains(t, keys, "port")
}

func TestVLLMEngine_Init_Defaults(t *testing.T) {
	v := &VLLMEngine{}
	err := v.Init(map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, "vllm/vllm-openai:latest", v.Image)
	assert.Equal(t, int32(8000), v.Port)
}

func TestVLLMEngine_Init_Custom(t *testing.T) {
	v := &VLLMEngine{}
	err := v.Init(map[string]interface{}{
		"image": "my-registry/vllm:v0.4",
		"port":  9090,
	})
	require.NoError(t, err)
	assert.Equal(t, "my-registry/vllm:v0.4", v.Image)
	assert.Equal(t, int32(9090), v.Port)
}

func TestVLLMEngine_GenerateTemplate(t *testing.T) {
	v := &VLLMEngine{}
	require.NoError(t, v.Init(map[string]interface{}{}))

	tpl, err := v.GenerateTemplate("mymodel", "org/model", "/models/mymodel")
	require.NoError(t, err)
	require.NotNil(t, tpl)
	require.Len(t, tpl.Spec.Containers, 1)

	c := tpl.Spec.Containers[0]
	assert.Equal(t, "vllm", c.Name)
	assert.Equal(t, v.Image, c.Image)
	assert.Contains(t, c.Args, "--model")
	assert.Contains(t, c.Args, "/models/mymodel")
	assert.Contains(t, c.Args, "--served-model-name")
	assert.Contains(t, c.Args, "mymodel")
	require.Len(t, c.Ports, 1)
	assert.Equal(t, int32(8000), c.Ports[0].ContainerPort)
	assert.Equal(t, "http", c.Ports[0].Name)
}

func TestGet_VLLM_InitAndReturn(t *testing.T) {
	p, err := Get("vllm", map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, "vllm", p.Name())
}

func TestValidateConfig_VLLM_OK(t *testing.T) {
	err := ValidateConfig("vllm", map[string]interface{}{"image": "custom:latest"})
	assert.NoError(t, err)
}

func TestValidateConfig_VLLM_UnknownField(t *testing.T) {
	err := ValidateConfig("vllm", map[string]interface{}{"badfield": "x"})
	assert.Error(t, err)
}

func TestGetFields_VLLM(t *testing.T) {
	fields := GetFields("vllm")
	require.NotNil(t, fields)
	assert.Len(t, fields, 2)
}
