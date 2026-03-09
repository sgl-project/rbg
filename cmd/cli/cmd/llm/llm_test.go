package llm

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

func TestNewLLMCmd_UseAndShort(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewLLMCmd(cf)
	assert.Equal(t, "llm", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

func TestNewLLMCmd_HasExpectedSubcommands(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewLLMCmd(cf)

	expected := []string{"config", "generate", "benchmark", "pull", "models", "run"}
	names := make([]string, 0, len(cmd.Commands()))
	for _, sub := range cmd.Commands() {
		names = append(names, sub.Name())
	}

	for _, want := range expected {
		require.Contains(t, names, want, "expected subcommand %q to be registered", want)
	}
}

func TestNewLLMCmd_SubcommandCount(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewLLMCmd(cf)
	assert.Equal(t, 6, len(cmd.Commands()))
}

func TestNewLLMCmd_PullSubcommand_Flags(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewLLMCmd(cf)

	var pullCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "pull" {
			pullCmd = sub
			break
		}
	}
	require.NotNil(t, pullCmd)

	assert.NotNil(t, pullCmd.Flags().Lookup("revision"))
	assert.NotNil(t, pullCmd.Flags().Lookup("source"))
	assert.NotNil(t, pullCmd.Flags().Lookup("storage"))
	assert.NotNil(t, pullCmd.Flags().Lookup("wait"))
}

func TestNewLLMCmd_ModelsSubcommand_Flags(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewLLMCmd(cf)

	var modelsCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "models" {
			modelsCmd = sub
			break
		}
	}
	require.NotNil(t, modelsCmd)

	assert.NotNil(t, modelsCmd.Flags().Lookup("storage"))
	assert.NotNil(t, modelsCmd.Flags().Lookup("wait"))
}

func TestNewLLMCmd_RunSubcommand_Flags(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewLLMCmd(cf)

	var runCmd *cobra.Command
	for _, sub := range cmd.Commands() {
		if sub.Name() == "run" {
			runCmd = sub
			break
		}
	}
	require.NotNil(t, runCmd)

	assert.Nil(t, runCmd.Flags().Lookup("name"))
	assert.NotNil(t, runCmd.Flags().Lookup("mode"))
	assert.NotNil(t, runCmd.Flags().Lookup("replicas"))
	assert.NotNil(t, runCmd.Flags().Lookup("gpu"))
	assert.NotNil(t, runCmd.Flags().Lookup("cpu"))
	assert.NotNil(t, runCmd.Flags().Lookup("memory"))
	assert.NotNil(t, runCmd.Flags().Lookup("env"))
	assert.NotNil(t, runCmd.Flags().Lookup("arg"))
	assert.NotNil(t, runCmd.Flags().Lookup("storage"))
	assert.NotNil(t, runCmd.Flags().Lookup("engine"))
	assert.NotNil(t, runCmd.Flags().Lookup("revision"))
}
