package config

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestNewAddEngineCmd(t *testing.T) {
	cmd := newAddEngineCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "add-engine NAME", cmd.Use)
	assert.Equal(t, "Add an engine configuration", cmd.Short)
	assert.NotNil(t, cmd.Args)

	// Check flags
	flag := cmd.Flags().Lookup("type")
	assert.NotNil(t, flag)
	assert.Equal(t, "vllm", flag.DefValue)
	assert.Equal(t, "Engine type (vllm, sglang)", flag.Usage)

	configFlag := cmd.Flags().Lookup("config")
	assert.NotNil(t, configFlag)
	assert.Equal(t, "Engine configuration key=value pairs", configFlag.Usage)
}

func TestNewGetEnginesCmd(t *testing.T) {
	cmd := newGetEnginesCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "get-engines", cmd.Use)
	assert.Equal(t, "List all engine configurations", cmd.Short)
}

func TestNewUseEngineCmd(t *testing.T) {
	cmd := newUseEngineCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "use-engine NAME", cmd.Use)
	assert.Equal(t, "Set the current engine", cmd.Short)
	assert.NotNil(t, cmd.Args)
}

func TestNewSetEngineCmd(t *testing.T) {
	cmd := newSetEngineCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "set-engine NAME", cmd.Use)
	assert.Equal(t, "Update an engine configuration", cmd.Short)
	assert.NotNil(t, cmd.Args)

	// Check config flag
	configFlag := cmd.Flags().Lookup("config")
	assert.NotNil(t, configFlag)
	assert.Equal(t, "Engine configuration key=value pairs", configFlag.Usage)
}

func TestNewDeleteEngineCmd(t *testing.T) {
	cmd := newDeleteEngineCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "delete-engine NAME", cmd.Use)
	assert.Equal(t, "Delete an engine configuration", cmd.Short)
	assert.NotNil(t, cmd.Args)
}

func TestEngineCommands_ReturnCobraCommand(t *testing.T) {
	commands := []func() *cobra.Command{
		newAddEngineCmd,
		newGetEnginesCmd,
		newUseEngineCmd,
		newSetEngineCmd,
		newDeleteEngineCmd,
	}

	for _, fn := range commands {
		cmd := fn()
		assert.IsType(t, &cobra.Command{}, cmd)
	}
}
