package config

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestNewAddSourceCmd(t *testing.T) {
	cmd := newAddSourceCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "add-source NAME", cmd.Use)
	assert.Equal(t, "Add a source configuration", cmd.Short)
	assert.NotNil(t, cmd.Args)

	// Check flags
	flag := cmd.Flags().Lookup("type")
	assert.NotNil(t, flag)
	assert.Equal(t, "huggingface", flag.DefValue)
	assert.Equal(t, "Source type (huggingface, modelscope)", flag.Usage)

	configFlag := cmd.Flags().Lookup("config")
	assert.NotNil(t, configFlag)
	assert.Equal(t, "Source configuration key=value pairs", configFlag.Usage)
}

func TestNewGetSourcesCmd(t *testing.T) {
	cmd := newGetSourcesCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "get-sources", cmd.Use)
	assert.Equal(t, "List all source configurations", cmd.Short)
}

func TestNewUseSourceCmd(t *testing.T) {
	cmd := newUseSourceCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "use-source NAME", cmd.Use)
	assert.Equal(t, "Set the current source", cmd.Short)
	assert.NotNil(t, cmd.Args)
}

func TestNewSetSourceCmd(t *testing.T) {
	cmd := newSetSourceCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "set-source NAME", cmd.Use)
	assert.Equal(t, "Update a source configuration", cmd.Short)
	assert.NotNil(t, cmd.Args)

	// Check config flag
	configFlag := cmd.Flags().Lookup("config")
	assert.NotNil(t, configFlag)
	assert.Equal(t, "Source configuration key=value pairs", configFlag.Usage)
}

func TestNewDeleteSourceCmd(t *testing.T) {
	cmd := newDeleteSourceCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "delete-source NAME", cmd.Use)
	assert.Equal(t, "Delete a source configuration", cmd.Short)
	assert.NotNil(t, cmd.Args)
}

func TestSourceCommands_ReturnCobraCommand(t *testing.T) {
	commands := []func() *cobra.Command{
		newAddSourceCmd,
		newGetSourcesCmd,
		newUseSourceCmd,
		newSetSourceCmd,
		newDeleteSourceCmd,
	}

	for _, fn := range commands {
		cmd := fn()
		assert.IsType(t, &cobra.Command{}, cmd)
	}
}
