package config

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestNewAddStorageCmd(t *testing.T) {
	cmd := newAddStorageCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "add-storage NAME", cmd.Use)
	assert.Equal(t, "Add a storage configuration", cmd.Short)
	assert.NotNil(t, cmd.Args)

	// Check flags
	flag := cmd.Flags().Lookup("type")
	assert.NotNil(t, flag)
	assert.Equal(t, "pvc", flag.DefValue)
	assert.Equal(t, "Storage type (pvc)", flag.Usage)

	configFlag := cmd.Flags().Lookup("config")
	assert.NotNil(t, configFlag)
	assert.Equal(t, "Storage configuration key=value pairs", configFlag.Usage)
}

func TestNewGetStoragesCmd(t *testing.T) {
	cmd := newGetStoragesCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "get-storages", cmd.Use)
	assert.Equal(t, "List all storage configurations", cmd.Short)
}

func TestNewUseStorageCmd(t *testing.T) {
	cmd := newUseStorageCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "use-storage NAME", cmd.Use)
	assert.Equal(t, "Set the current storage", cmd.Short)
	assert.NotNil(t, cmd.Args)
}

func TestNewSetStorageCmd(t *testing.T) {
	cmd := newSetStorageCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "set-storage NAME", cmd.Use)
	assert.Equal(t, "Update a storage configuration", cmd.Short)
	assert.NotNil(t, cmd.Args)

	// Check config flag
	configFlag := cmd.Flags().Lookup("config")
	assert.NotNil(t, configFlag)
	assert.Equal(t, "Storage configuration key=value pairs", configFlag.Usage)
}

func TestNewDeleteStorageCmd(t *testing.T) {
	cmd := newDeleteStorageCmd()

	assert.NotNil(t, cmd)
	assert.Equal(t, "delete-storage NAME", cmd.Use)
	assert.Equal(t, "Delete a storage configuration", cmd.Short)
	assert.NotNil(t, cmd.Args)
}

func TestStorageCommands_ReturnCobraCommand(t *testing.T) {
	commands := []func() *cobra.Command{
		newAddStorageCmd,
		newGetStoragesCmd,
		newUseStorageCmd,
		newSetStorageCmd,
		newDeleteStorageCmd,
	}

	for _, fn := range commands {
		cmd := fn()
		assert.IsType(t, &cobra.Command{}, cmd)
	}
}
