package config

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestNewConfigCmd(t *testing.T) {
	cmd := NewConfigCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "config", cmd.Use)
	assert.Equal(t, "Manage LLM configuration", cmd.Short)
	assert.NotEmpty(t, cmd.Long)

	// Check that all expected subcommands are registered
	expectedCommands := []string{
		"add-storage",
		"add-source",
		"add-engine",
		"get-storages",
		"get-sources",
		"get-engines",
		"use-storage",
		"use-source",
		"set-storage",
		"set-source",
		"set-engine",
		"delete-storage",
		"delete-source",
		"delete-engine",
		"view",
		"set-namespace",
		"init",
	}

	for _, name := range expectedCommands {
		subCmd, _, err := cmd.Find([]string{name})
		assert.NoError(t, err, "should find subcommand %s", name)
		assert.NotNil(t, subCmd, "subcommand %s should exist", name)
	}

	// Verify command count
	assert.Len(t, cmd.Commands(), len(expectedCommands))
}

func TestNewConfigCmd_SubcommandProperties(t *testing.T) {
	cmd := NewConfigCmd()

	testCases := []struct {
		name     string
		use      string
		expected string
	}{
		{"add-storage", "add-storage NAME", "Add a storage configuration"},
		{"add-source", "add-source NAME", "Add a source configuration"},
		{"add-engine", "add-engine NAME", "Add an engine configuration"},
		{"get-storages", "get-storages", "List all storage configurations"},
		{"get-sources", "get-sources", "List all source configurations"},
		{"get-engines", "get-engines", "List all engine configurations"},
		{"use-storage", "use-storage NAME", "Set the current storage"},
		{"use-source", "use-source NAME", "Set the current source"},
		{"set-storage", "set-storage NAME", "Update a storage configuration"},
		{"set-source", "set-source NAME", "Update a source configuration"},
		{"set-engine", "set-engine NAME", "Update an engine configuration"},
		{"delete-storage", "delete-storage NAME", "Delete a storage configuration"},
		{"delete-source", "delete-source NAME", "Delete a source configuration"},
		{"delete-engine", "delete-engine NAME", "Delete an engine configuration"},
		{"view", "view", "View current configuration"},
		{"set-namespace", "set-namespace NAMESPACE", "Set the default namespace"},
		{"init", "init", "Initialize LLM configuration"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			subCmd, _, err := cmd.Find([]string{tc.name})
			assert.NoError(t, err)
			assert.NotNil(t, subCmd)
			assert.Equal(t, tc.expected, subCmd.Short)
		})
	}
}

func TestNewConfigCmd_ReturnsCobraCommand(t *testing.T) {
	cmd := NewConfigCmd()
	assert.IsType(t, &cobra.Command{}, cmd)
}
