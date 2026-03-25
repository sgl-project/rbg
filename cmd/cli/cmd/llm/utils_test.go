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

package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- sanitizeModelID ---

func TestSanitizeModelID_Slash(t *testing.T) {
	assert.Equal(t, "org-model", sanitizeModelID("org/model"))
}

func TestSanitizeModelID_Colon(t *testing.T) {
	assert.Equal(t, "model-v1.0", sanitizeModelID("model:v1.0"))
}

func TestSanitizeModelID_Underscore(t *testing.T) {
	assert.Equal(t, "my-model", sanitizeModelID("my_model"))
}

func TestSanitizeModelID_UpperCase(t *testing.T) {
	assert.Equal(t, "qwen-qwen2.5-7b", sanitizeModelID("Qwen/Qwen2.5-7B"))
}

func TestSanitizeModelID_AllSpecialChars(t *testing.T) {
	assert.Equal(t, "a-b-c-d", sanitizeModelID("a/b:c_d"))
}

func TestSanitizeModelID_NoChange(t *testing.T) {
	assert.Equal(t, "simple", sanitizeModelID("simple"))
}

func TestSanitizeModelID_Empty(t *testing.T) {
	assert.Equal(t, "", sanitizeModelID(""))
}
