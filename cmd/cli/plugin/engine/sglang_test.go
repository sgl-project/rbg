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

package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSGLangEngine_Name(t *testing.T) {
	s := &SGLangEngine{}
	assert.Equal(t, "sglang", s.Name())
}

func TestSGLangEngine_ConfigFields(t *testing.T) {
	s := &SGLangEngine{}
	fields := s.ConfigFields()
	assert.Len(t, fields, 2)
	keys := []string{fields[0].Key, fields[1].Key}
	assert.Contains(t, keys, "image")
	assert.Contains(t, keys, "port")
}

func TestSGLangEngine_Init_Defaults(t *testing.T) {
	s := &SGLangEngine{}
	err := s.Init(map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, "lmsysorg/sglang:latest", s.Image)
	assert.Equal(t, int32(30000), s.Port)
}

func TestSGLangEngine_Init_Custom(t *testing.T) {
	s := &SGLangEngine{}
	err := s.Init(map[string]interface{}{
		"image": "my-sglang:v1",
		"port":  8888,
	})
	require.NoError(t, err)
	assert.Equal(t, "my-sglang:v1", s.Image)
	assert.Equal(t, int32(8888), s.Port)
}

func TestSGLangEngine_GenerateTemplate(t *testing.T) {
	s := &SGLangEngine{}
	require.NoError(t, s.Init(map[string]interface{}{}))

	tpl, err := s.GenerateTemplate("mymodel", "org/model", "/models/mymodel")
	require.NoError(t, err)
	require.NotNil(t, tpl)
	require.Len(t, tpl.Spec.Containers, 1)

	c := tpl.Spec.Containers[0]
	assert.Equal(t, "sglang", c.Name)
	assert.Equal(t, s.Image, c.Image)
	assert.Equal(t, []string{"python", "-m", "sglang.launch_server"}, c.Command)
	assert.Contains(t, c.Args, "--model-path")
	assert.Contains(t, c.Args, "/models/mymodel")
	assert.Contains(t, c.Args, "--model-name")
	assert.Contains(t, c.Args, "mymodel")
	require.Len(t, c.Ports, 1)
	assert.Equal(t, int32(30000), c.Ports[0].ContainerPort)
	assert.Equal(t, "http", c.Ports[0].Name)

	_, hasGPU := c.Resources.Limits["nvidia.com/gpu"]
	assert.True(t, hasGPU)
}

func TestSGLangEngine_GenerateTemplate_EnvVar(t *testing.T) {
	s := &SGLangEngine{}
	require.NoError(t, s.Init(map[string]interface{}{}))

	tpl, err := s.GenerateTemplate("m", "id", "/path/to/model")
	require.NoError(t, err)
	envMap := map[string]string{}
	for _, e := range tpl.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}
	assert.Equal(t, "/path/to/model", envMap["SGLANG_MODEL_PATH"])
}

func TestSGLangEngine_GetModelService(t *testing.T) {
	s := &SGLangEngine{}
	svc, err := s.GetModelService("my-service")
	require.NoError(t, err)
	assert.Equal(t, "http://my-service:30000", svc)
}

func TestGet_SGLang_InitAndReturn(t *testing.T) {
	p, err := Get("sglang", map[string]interface{}{})
	require.NoError(t, err)
	assert.Equal(t, "sglang", p.Name())
}

func TestValidateConfig_SGLang_OK(t *testing.T) {
	err := ValidateConfig("sglang", map[string]interface{}{"port": 12345})
	assert.NoError(t, err)
}

func TestValidateConfig_SGLang_UnknownField(t *testing.T) {
	err := ValidateConfig("sglang", map[string]interface{}{"badfield": "x"})
	assert.Error(t, err)
}

func TestGetFields_SGLang(t *testing.T) {
	fields := GetFields("sglang")
	require.NotNil(t, fields)
	assert.Len(t, fields, 2)
}
