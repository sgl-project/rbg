package llm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// --- newRunCmd: command metadata ---

func TestNewRunCmd_UseAndShort(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := newRunCmd(cf)
	assert.Equal(t, "run <name> <model-id> [flags]", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

func TestNewRunCmd_ExactlyTwoArgs(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := newRunCmd(cf)
	// no args should produce an error
	err := cmd.Args(cmd, []string{})
	require.Error(t, err)

	// one arg should also error
	err = cmd.Args(cmd, []string{"my-qwen"})
	require.Error(t, err)

	// three args should also error
	err = cmd.Args(cmd, []string{"my-qwen", "Qwen/Qwen3.5-0.8B", "extra"})
	require.Error(t, err)

	// exactly two args is fine
	err = cmd.Args(cmd, []string{"my-qwen", "Qwen/Qwen3.5-0.8B"})
	require.NoError(t, err)
}

// --- newRunCmd: flags exist with expected defaults ---

func TestNewRunCmd_FlagDefaults(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := newRunCmd(cf)

	// --name flag should not exist (now positional arg)
	nameFlag := cmd.Flags().Lookup("name")
	assert.Nil(t, nameFlag)

	// --mode default is empty (first mode in model config is used)
	modeFlag := cmd.Flags().Lookup("mode")
	require.NotNil(t, modeFlag)
	assert.Equal(t, "", modeFlag.DefValue)

	// --replicas default is 1
	replicasFlag := cmd.Flags().Lookup("replicas")
	require.NotNil(t, replicasFlag)
	assert.Equal(t, "1", replicasFlag.DefValue)

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

	// --dry-run default is false
	dryRunFlag := cmd.Flags().Lookup("dry-run")
	require.NotNil(t, dryRunFlag)
	assert.Equal(t, "false", dryRunFlag.DefValue)

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

// --- resolveRunContext ---

func TestResolveRunContext_DefaultMode_VLLMEngine(t *testing.T) {
	// Qwen/Qwen3.5-0.8B standard mode uses vllm with port 8000
	rctx, err := resolveRunContext("my-svc", "Qwen/Qwen3.5-0.8B", RunParams{
		Revision: "main",
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "vllm", rctx.EngineType)
	assert.Equal(t, "standard", rctx.ModeName)
	assert.Equal(t, int32(8000), rctx.ResolvedPort)
	assert.NotNil(t, rctx.PodTemplate)
	assert.Nil(t, rctx.StoragePlugin)
}

func TestResolveRunContext_LatencyMode_SGLangEngine(t *testing.T) {
	// Qwen/Qwen3.5-0.8B latency mode uses sglang with port 30000
	rctx, err := resolveRunContext("my-svc", "Qwen/Qwen3.5-0.8B", RunParams{
		Mode:     "latency",
		Revision: "main",
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "sglang", rctx.EngineType)
	assert.Equal(t, "latency", rctx.ModeName)
	assert.Equal(t, int32(30000), rctx.ResolvedPort)
}

func TestResolveRunContext_EngineOverride(t *testing.T) {
	// Engine flag overrides the mode's default engine
	rctx, err := resolveRunContext("my-svc", "Qwen/Qwen3.5-0.8B", RunParams{
		Engine:   "sglang",
		Revision: "main",
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "sglang", rctx.EngineType)
	assert.Equal(t, int32(30000), rctx.ResolvedPort)
}

func TestResolveRunContext_EnvVarInjection(t *testing.T) {
	rctx, err := resolveRunContext("my-svc", "Qwen/Qwen3.5-0.8B", RunParams{
		Revision: "main",
		EnvVars:  []string{"MY_KEY=my_value"},
	}, nil)
	require.NoError(t, err)
	envMap := map[string]string{}
	for _, e := range rctx.PodTemplate.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}
	assert.Equal(t, "my_value", envMap["MY_KEY"])
}

func TestResolveRunContext_InvalidEnvVar(t *testing.T) {
	_, err := resolveRunContext("my-svc", "Qwen/Qwen3.5-0.8B", RunParams{
		Revision: "main",
		EnvVars:  []string{"NOEQUALSSIGN"},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid environment variable format")
}

func TestResolveRunContext_UnknownModel(t *testing.T) {
	// Unknown model falls back to wildcard "*" config
	rctx, err := resolveRunContext("my-svc", "unknown/unknown-model", RunParams{
		Revision: "main",
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "vllm", rctx.EngineType)
}

func TestResolveRunContext_UnknownEngine_Errors(t *testing.T) {
	_, err := resolveRunContext("my-svc", "Qwen/Qwen3.5-0.8B", RunParams{
		Engine:   "nonexistent-engine",
		Revision: "main",
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown engine type")
}

func TestResolveRunContext_AdditionalArgs(t *testing.T) {
	rctx, err := resolveRunContext("my-svc", "Qwen/Qwen3.5-0.8B", RunParams{
		Revision: "main",
		ArgsList: []string{"--custom-flag", "value"},
	}, nil)
	require.NoError(t, err)
	args := rctx.PodTemplate.Spec.Containers[0].Args
	assert.Contains(t, args, "--custom-flag")
	assert.Contains(t, args, "value")
}

func TestResolveRunContext_FallbackModelPath(t *testing.T) {
	// Without storage config, model path uses the /model/ fallback
	rctx, err := resolveRunContext("my-svc", "Qwen/Qwen3.5-0.8B", RunParams{
		Revision: "main",
	}, nil)
	require.NoError(t, err)
	// vllm passes model path via --model arg
	args := rctx.PodTemplate.Spec.Containers[0].Args
	var modelPathArg string
	for i, a := range args {
		if a == "--model" && i+1 < len(args) {
			modelPathArg = args[i+1]
			break
		}
	}
	assert.True(t, strings.HasPrefix(modelPathArg, "/model/"), "expected fallback model path, got: %s", modelPathArg)
}
