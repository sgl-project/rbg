package config

import (
	"bufio"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestNewViewCmd(t *testing.T) {
	cmd := newViewCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "view", cmd.Use)
	assert.Equal(t, "View current configuration", cmd.Short)
}

func TestNewSetNamespaceCmd(t *testing.T) {
	cmd := newSetNamespaceCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "set-namespace NAMESPACE", cmd.Use)
	assert.Equal(t, "Set the default namespace", cmd.Short)
	assert.NotNil(t, cmd.Args)
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
		newSetNamespaceCmd,
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
			result := readLine(reader, "prompt", tc.defaultValue)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestReadLine_EOF(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	result := readLine(reader, "prompt", "default")
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
