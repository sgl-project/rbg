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

package config

import (
	"bufio"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/rbgs/cmd/cli/plugin/util"
)

func TestNewViewCmd(t *testing.T) {
	cmd := newViewCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "view", cmd.Use)
	assert.Equal(t, "View current configuration", cmd.Short)
}

func TestNewInitCmd(t *testing.T) {
	cmd := newInitCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "init", cmd.Use)
	assert.Equal(t, "Initialize LLM configuration", cmd.Short)
}

func TestMiscCommands_ReturnCobraCommand(t *testing.T) {
	commands := []func() *cobra.Command{
		newViewCmd,
		newInitCmd,
	}

	for _, fn := range commands {
		cmd := fn()
		assert.IsType(t, &cobra.Command{}, cmd)
	}
}

func TestReadLine(t *testing.T) {
	testCases := []struct {
		name         string
		input        string
		defaultValue string
		expected     string
	}{
		{
			name:         "user provides input",
			input:        "custom-value\n",
			defaultValue: "default",
			expected:     "custom-value",
		},
		{
			name:         "empty input returns default",
			input:        "\n",
			defaultValue: "default",
			expected:     "default",
		},
		{
			name:         "whitespace trimmed",
			input:        "  value  \n",
			defaultValue: "default",
			expected:     "value",
		},
		{
			name:         "no default value, user provides input",
			input:        "value\n",
			defaultValue: "",
			expected:     "value",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tc.input))
			result := util.ReadLine(reader, "prompt", tc.defaultValue)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestReadLine_EOF(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	result := util.ReadLine(reader, "prompt", "default")
	assert.Equal(t, "default", result)
}

func TestSelectPlugin(t *testing.T) {
	testCases := []struct {
		name           string
		input          string
		availableNames []string
		expected       string
	}{
		{
			name:           "valid selection",
			input:          "2\n",
			availableNames: []string{"option1", "option2", "option3"},
			expected:       "option2",
		},
		{
			name:           "first option selected",
			input:          "1\n",
			availableNames: []string{"option1", "option2"},
			expected:       "option1",
		},
		{
			name:           "last option selected",
			input:          "3\n",
			availableNames: []string{"option1", "option2", "option3"},
			expected:       "option3",
		},
		{
			name:           "EOF returns first option",
			input:          "",
			availableNames: []string{"option1", "option2"},
			expected:       "option1",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tc.input))
			result := selectPlugin(reader, "test", tc.availableNames)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestSelectPlugin_InvalidInput(t *testing.T) {
	// Test invalid input followed by valid input
	input := "invalid\n0\n4\n2\n"
	reader := bufio.NewReader(strings.NewReader(input))
	availableNames := []string{"option1", "option2", "option3"}

	result := selectPlugin(reader, "test", availableNames)
	assert.Equal(t, "option2", result)
}

func TestSelectPlugin_MaxAttempts(t *testing.T) {
	// Test that after max attempts, first option is returned
	input := "\n\n\n\n\n\n\n\n\n\n" // Many empty lines
	reader := bufio.NewReader(strings.NewReader(input))
	availableNames := []string{"option1", "option2"}

	result := selectPlugin(reader, "test", availableNames)
	assert.Equal(t, "option1", result)
}
