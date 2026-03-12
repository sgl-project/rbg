package config

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestNewSetEngineCmd(t *testing.T) {
	cmd := newSetEngineCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "set-engine ENGINE_TYPE", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.NotNil(t, cmd.Args)

	// Check config flag
	configFlag := cmd.Flags().Lookup("config")
	assert.NotNil(t, configFlag)
	assert.Equal(t, "Engine configuration key=value pairs", configFlag.Usage)
}

func TestNewGetEnginesCmd(t *testing.T) {
	cmd := newGetEnginesCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "get-engines", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

func TestNewResetEngineCmd(t *testing.T) {
	cmd := newResetEngineCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "reset-engine ENGINE_TYPE", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.NotNil(t, cmd.Args)
}

func TestEngineCommands_ReturnCobraCommand(t *testing.T) {
	commands := []func() *cobra.Command{
		newSetEngineCmd,
		newGetEnginesCmd,
		newResetEngineCmd,
	}

	for _, fn := range commands {
		cmd := fn()
		assert.IsType(t, &cobra.Command{}, cmd)
	}
}
