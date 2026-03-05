/*
Copyright 2025.

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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- newRunCmd: command metadata ---

func TestNewRunCmd_UseAndShort(t *testing.T) {
	cmd := newRunCmd()
	assert.Equal(t, "run MODEL_ID", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

func TestNewRunCmd_ExactlyOneArg(t *testing.T) {
	cmd := newRunCmd()
	// cobra.ExactArgs(1) — no args should produce an error
	err := cmd.Args(cmd, []string{})
	require.Error(t, err)

	// two args should also error
	err = cmd.Args(cmd, []string{"a", "b"})
	require.Error(t, err)

	// exactly one arg is fine
	err = cmd.Args(cmd, []string{"org/model"})
	require.NoError(t, err)
}

// --- newRunCmd: flags exist with expected defaults ---

func TestNewRunCmd_FlagDefaults(t *testing.T) {
	cmd := newRunCmd()

	// --name default is empty
	nameFlag := cmd.Flags().Lookup("name")
	require.NotNil(t, nameFlag)
	assert.Equal(t, "", nameFlag.DefValue)

	// --replicas default is 1
	replicasFlag := cmd.Flags().Lookup("replicas")
	require.NotNil(t, replicasFlag)
	assert.Equal(t, "1", replicasFlag.DefValue)

	// --gpu default is 0
	gpuFlag := cmd.Flags().Lookup("gpu")
	require.NotNil(t, gpuFlag)
	assert.Equal(t, "0", gpuFlag.DefValue)

	// --cpu default is 0
	cpuFlag := cmd.Flags().Lookup("cpu")
	require.NotNil(t, cpuFlag)
	assert.Equal(t, "0", cpuFlag.DefValue)

	// --memory default is empty
	memFlag := cmd.Flags().Lookup("memory")
	require.NotNil(t, memFlag)
	assert.Equal(t, "", memFlag.DefValue)

	// --revision default is "main"
	revFlag := cmd.Flags().Lookup("revision")
	require.NotNil(t, revFlag)
	assert.Equal(t, "main", revFlag.DefValue)

	// --storage default is empty
	storageFlag := cmd.Flags().Lookup("storage")
	require.NotNil(t, storageFlag)
	assert.Equal(t, "", storageFlag.DefValue)

	// --engine default is empty
	engineFlag := cmd.Flags().Lookup("engine")
	require.NotNil(t, engineFlag)
	assert.Equal(t, "", engineFlag.DefValue)

	// --env and --arg are StringArray, default empty
	envFlag := cmd.Flags().Lookup("env")
	require.NotNil(t, envFlag)

	argFlag := cmd.Flags().Lookup("arg")
	require.NotNil(t, argFlag)
}

// --- env-var parsing logic (mirrors run.go's inline SplitN logic) ---
// run.go: parts := strings.SplitN(env, "=", 2)
// We test the same logic directly.

func splitEnvVarTestHelper(env string) []string {
	return strings.SplitN(env, "=", 2)
}

func TestRunEnvVarParsing_ValidKeyValue(t *testing.T) {
	parts := splitEnvVarTestHelper("FOO=bar")
	require.Len(t, parts, 2)
	assert.Equal(t, "FOO", parts[0])
	assert.Equal(t, "bar", parts[1])
}

func TestRunEnvVarParsing_ValueWithEquals(t *testing.T) {
	// Value itself contains "=" — only the first "=" is the separator
	parts := splitEnvVarTestHelper("KEY=val=ue")
	require.Len(t, parts, 2)
	assert.Equal(t, "KEY", parts[0])
	assert.Equal(t, "val=ue", parts[1])
}

func TestRunEnvVarParsing_NoEqualsSign(t *testing.T) {
	// SplitN with n=2 returns single element when no separator
	parts := splitEnvVarTestHelper("NOEQUALS")
	assert.Len(t, parts, 1)
}

func TestRunEnvVarParsing_EmptyValue(t *testing.T) {
	parts := splitEnvVarTestHelper("EMPTY=")
	require.Len(t, parts, 2)
	assert.Equal(t, "EMPTY", parts[0])
	assert.Equal(t, "", parts[1])
}
