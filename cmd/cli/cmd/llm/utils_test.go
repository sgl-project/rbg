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
