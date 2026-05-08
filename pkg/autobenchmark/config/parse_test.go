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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fullValidConfig() string {
	return `
apiVersion: rbg/v1alpha1
kind: AutoBenchmarkConfig

templates:
  - name: "agg-tp4"
    template: "./templates/agg-tp4.yaml"
  - name: "disagg-pd"
    template: "./templates/disagg-pd.yaml"

backend: "sglang"

searchSpace:
  default:
    gpuMemoryUtilization:
      type: "categorical"
      values: [0.85, 0.90, 0.95]
    maxNumSeqs:
      type: "float"
      min: 64
      max: 512
      step: 64

scenario:
  name: "test-load"
  workloads: ["fixed(512,256)"]
  concurrency: [16, 32]
  duration: "10m"

objectives:
  sla:
    ttftP99MaxMs: 2000
    tpotP99MaxMs: 50
    errorRateMax: 0.01
  optimize: "outputThroughput"

strategy:
  algorithm: "grid"
  maxTrialsPerTemplate: 10
  timeout: "2h"

evaluator:
  type: "genai-bench"

results:
  pvc: "pvc://auto-bench-results/"

execution:
  rbgReadyTimeout: "10m"
  trialTimeout: "20m"
`
}

func TestParse_FullValidConfig(t *testing.T) {
	cfg, err := Parse([]byte(fullValidConfig()))
	require.NoError(t, err)

	assert.Equal(t, "rbg/v1alpha1", cfg.APIVersion)
	assert.Equal(t, "AutoBenchmarkConfig", cfg.Kind)
	assert.Len(t, cfg.Templates, 2)
	assert.Equal(t, "agg-tp4", cfg.Templates[0].Name)
	assert.Equal(t, "./templates/agg-tp4.yaml", cfg.Templates[0].Template)
	assert.Equal(t, "sglang", cfg.Backend)
	assert.Contains(t, cfg.SearchSpace, "default")
	assert.Contains(t, cfg.SearchSpace["default"], "gpuMemoryUtilization")
	assert.Equal(t, "categorical", cfg.SearchSpace["default"]["gpuMemoryUtilization"].Type)
	assert.Equal(t, "test-load", cfg.Scenario.Name)
	assert.Equal(t, "outputThroughput", cfg.Objectives.Optimize)
	assert.Equal(t, "grid", cfg.Strategy.Algorithm)
	assert.Equal(t, 10, cfg.Strategy.MaxTrialsPerTemplate)
	assert.Equal(t, "genai-bench", cfg.Evaluator.Type)
	assert.Equal(t, "pvc://auto-bench-results/", cfg.Results.PVC)
}

func TestParse_Defaults(t *testing.T) {
	yamlData := `
templates:
  - name: "t1"
    template: "./t1.yaml"
backend: "sglang"
searchSpace:
  default:
    x:
      type: "categorical"
      values: [1, 2]
scenario:
  name: "s"
  workloads: ["fixed(100,50)"]
  concurrency: [1]
objectives:
  optimize: "outputThroughput"
evaluator:
  type: "genai-bench"
results:
  pvc: "pvc://results/"
`
	cfg, err := Parse([]byte(yamlData))
	require.NoError(t, err)

	assert.Equal(t, "grid", cfg.Strategy.Algorithm)
	assert.Equal(t, 20, cfg.Strategy.MaxTrialsPerTemplate)
	assert.Equal(t, "4h", cfg.Strategy.Timeout)
	assert.Equal(t, "15m", cfg.Execution.RBGReadyTimeout)
	assert.Equal(t, "30m", cfg.Execution.TrialTimeout)
}

func TestValidate_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(cfg *AutoBenchmarkConfig)
		wantErr string
	}{
		{
			name:    "missing templates",
			modify:  func(cfg *AutoBenchmarkConfig) { cfg.Templates = nil },
			wantErr: "templates: at least one template is required",
		},
		{
			name:    "missing backend",
			modify:  func(cfg *AutoBenchmarkConfig) { cfg.Backend = "" },
			wantErr: "backend: is required",
		},
		{
			name:    "missing searchSpace",
			modify:  func(cfg *AutoBenchmarkConfig) { cfg.SearchSpace = nil },
			wantErr: "searchSpace: is required",
		},
		{
			name:    "missing scenario name",
			modify:  func(cfg *AutoBenchmarkConfig) { cfg.Scenario.Name = "" },
			wantErr: "scenario.name: is required",
		},
		{
			name:    "missing objectives.optimize",
			modify:  func(cfg *AutoBenchmarkConfig) { cfg.Objectives.Optimize = "" },
			wantErr: "objectives.optimize: is required",
		},
		{
			name:    "missing evaluator.type",
			modify:  func(cfg *AutoBenchmarkConfig) { cfg.Evaluator.Type = "" },
			wantErr: "evaluator.type: is required",
		},
		{
			name:    "missing results.pvc",
			modify:  func(cfg *AutoBenchmarkConfig) { cfg.Results.PVC = "" },
			wantErr: "results.pvc: is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(fullValidConfig()))
			require.NoError(t, err)
			tt.modify(cfg)
			err = Validate(cfg, false)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidate_UnsupportedBackend(t *testing.T) {
	cfg, err := Parse([]byte(fullValidConfig()))
	require.NoError(t, err)
	cfg.Backend = "tensorrt"
	err = Validate(cfg, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported value \"tensorrt\"")
}

func TestValidate_UnsupportedAlgorithm(t *testing.T) {
	cfg, err := Parse([]byte(fullValidConfig()))
	require.NoError(t, err)
	cfg.Strategy.Algorithm = "unknown-algo"
	err = Validate(cfg, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported value \"unknown-algo\"")
}

func TestValidate_UnsupportedOptimizeMetric(t *testing.T) {
	cfg, err := Parse([]byte(fullValidConfig()))
	require.NoError(t, err)
	cfg.Objectives.Optimize = "latency"
	err = Validate(cfg, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported value \"latency\"")
}

func TestValidate_DuplicateTemplateName(t *testing.T) {
	cfg, err := Parse([]byte(fullValidConfig()))
	require.NoError(t, err)
	cfg.Templates[1].Name = cfg.Templates[0].Name
	err = Validate(cfg, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate name")
}

func TestValidate_SearchParamFloatIntErrors(t *testing.T) {
	tests := []struct {
		name    string
		param   SearchParam
		wantErr string
	}{
		{
			name:    "min >= max",
			param:   SearchParam{Type: "float", Min: ptr(100.0), Max: ptr(50.0), Step: ptr(10.0)},
			wantErr: "min (100) must be less than max (50)",
		},
		{
			name:    "step <= 0",
			param:   SearchParam{Type: "int", Min: ptr(1.0), Max: ptr(10.0), Step: ptr(0.0)},
			wantErr: "step must be positive",
		},
		{
			name:    "missing min/max",
			param:   SearchParam{Type: "float", Min: ptr(1.0)},
			wantErr: "float type requires min and max",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(fullValidConfig()))
			require.NoError(t, err)
			cfg.SearchSpace["default"]["testParam"] = tt.param
			err = Validate(cfg, false)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidate_SearchParamPow2Errors(t *testing.T) {
	tests := []struct {
		name    string
		param   SearchParam
		wantErr string
	}{
		{
			name:    "non-power-of-2 min",
			param:   SearchParam{Type: "pow2", Min: ptr(500.0), Max: ptr(8192.0)},
			wantErr: "pow2 min (500) must be a power of 2",
		},
		{
			name:    "non-power-of-2 max",
			param:   SearchParam{Type: "pow2", Min: ptr(512.0), Max: ptr(5000.0)},
			wantErr: "pow2 max (5000) must be a power of 2",
		},
		{
			name:    "pow2 with step",
			param:   SearchParam{Type: "pow2", Min: ptr(512.0), Max: ptr(8192.0), Step: ptr(512.0)},
			wantErr: "pow2 type does not support step",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(fullValidConfig()))
			require.NoError(t, err)
			cfg.SearchSpace["default"]["testParam"] = tt.param
			err = Validate(cfg, false)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidate_SearchParamCategoricalEmpty(t *testing.T) {
	cfg, err := Parse([]byte(fullValidConfig()))
	require.NoError(t, err)
	cfg.SearchSpace["default"]["emptyParam"] = SearchParam{Type: "categorical", Values: nil}
	err = Validate(cfg, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one value")
}

func TestValidate_SearchParamUnknownType(t *testing.T) {
	cfg, err := Parse([]byte(fullValidConfig()))
	require.NoError(t, err)
	cfg.SearchSpace["default"]["badParam"] = SearchParam{Type: "uniform"}
	err = Validate(cfg, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported type \"uniform\"")
}

func TestValidate_InvalidDurations(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(cfg *AutoBenchmarkConfig)
		wantErr string
	}{
		{
			name:    "invalid strategy.timeout",
			modify:  func(cfg *AutoBenchmarkConfig) { cfg.Strategy.Timeout = "notaduration" },
			wantErr: "strategy.timeout: invalid duration",
		},
		{
			name:    "invalid execution.rbgReadyTimeout",
			modify:  func(cfg *AutoBenchmarkConfig) { cfg.Execution.RBGReadyTimeout = "abc" },
			wantErr: "execution.rbgReadyTimeout: invalid duration",
		},
		{
			name:    "invalid execution.trialTimeout",
			modify:  func(cfg *AutoBenchmarkConfig) { cfg.Execution.TrialTimeout = "xyz" },
			wantErr: "execution.trialTimeout: invalid duration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(fullValidConfig()))
			require.NoError(t, err)
			tt.modify(cfg)
			err = Validate(cfg, false)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidate_TemplateFileNotFound(t *testing.T) {
	cfg, err := Parse([]byte(fullValidConfig()))
	require.NoError(t, err)
	cfg.Templates[0].Template = "/nonexistent/path/template.yaml"
	err = Validate(cfg, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file not found")
}

func TestValidate_TemplateFileExists(t *testing.T) {
	tmpDir := t.TempDir()
	f1 := filepath.Join(tmpDir, "t1.yaml")
	f2 := filepath.Join(tmpDir, "t2.yaml")
	require.NoError(t, os.WriteFile(f1, []byte("apiVersion: v1"), 0644))
	require.NoError(t, os.WriteFile(f2, []byte("apiVersion: v1"), 0644))

	cfg, err := Parse([]byte(fullValidConfig()))
	require.NoError(t, err)
	cfg.Templates[0].Template = f1
	cfg.Templates[1].Template = f2
	err = Validate(cfg, true)
	assert.NoError(t, err)
}

func TestValidate_StrategyMaxTrialsNonPositive(t *testing.T) {
	cfg, err := Parse([]byte(fullValidConfig()))
	require.NoError(t, err)
	cfg.Strategy.MaxTrialsPerTemplate = 0
	err = Validate(cfg, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maxTrialsPerTemplate: must be positive")
}

func TestValidate_ScenarioMissingFields(t *testing.T) {
	cfg, err := Parse([]byte(fullValidConfig()))
	require.NoError(t, err)
	cfg.Scenario.Workloads = nil
	err = Validate(cfg, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scenario.workloads: at least one is required")
}

func TestValidate_FullValidConfig(t *testing.T) {
	cfg, err := Parse([]byte(fullValidConfig()))
	require.NoError(t, err)
	err = Validate(cfg, false)
	assert.NoError(t, err)
}

func TestParseFile_NonexistentFile(t *testing.T) {
	_, err := ParseFile("/nonexistent/config.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading config file")
}

func TestParseFile_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("{{invalid yaml"), 0644))
	_, err := ParseFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing config YAML")
}

func TestValidate_SLANegativeValues(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(cfg *AutoBenchmarkConfig)
		wantErr string
	}{
		{
			name:    "negative ttftP99MaxMs",
			modify:  func(cfg *AutoBenchmarkConfig) { v := -100.0; cfg.Objectives.SLA.TTFTP99MaxMs = &v },
			wantErr: "objectives.sla.ttftP99MaxMs: must not be negative",
		},
		{
			name:    "negative tpotP99MaxMs",
			modify:  func(cfg *AutoBenchmarkConfig) { v := -50.0; cfg.Objectives.SLA.TPOTP99MaxMs = &v },
			wantErr: "objectives.sla.tpotP99MaxMs: must not be negative",
		},
		{
			name:    "negative errorRateMax",
			modify:  func(cfg *AutoBenchmarkConfig) { v := -0.01; cfg.Objectives.SLA.ErrorRateMax = &v },
			wantErr: "objectives.sla.errorRateMax: must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(fullValidConfig()))
			require.NoError(t, err)
			tt.modify(cfg)
			err = Validate(cfg, false)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidate_SLAZeroValues(t *testing.T) {
	cfg, err := Parse([]byte(fullValidConfig()))
	require.NoError(t, err)
	// Zero values are allowed (used as fallback in deviation calculation)
	zero := 0.0
	cfg.Objectives.SLA.TTFTP99MaxMs = &zero
	cfg.Objectives.SLA.TPOTP99MaxMs = &zero
	cfg.Objectives.SLA.ErrorRateMax = &zero
	err = Validate(cfg, false)
	assert.NoError(t, err)
}

func TestParse_RoleSpecificSearchSpace(t *testing.T) {
	yamlData := `
templates:
  - name: "t1"
    template: "./t1.yaml"
backend: "sglang"
searchSpace:
  default:
    maxNumSeqs:
      type: "categorical"
      values: [64, 128]
  prefill:
    chunkedPrefillSize:
      type: "categorical"
      values: [2048, 4096]
  decode:
    maxNumSeqs:
      type: "float"
      min: 128
      max: 512
      step: 128
scenario:
  name: "s"
  workloads: ["fixed(100,50)"]
  concurrency: [1]
objectives:
  optimize: "outputThroughput"
evaluator:
  type: "genai-bench"
results:
  pvc: "pvc://results/"
`
	cfg, err := Parse([]byte(yamlData))
	require.NoError(t, err)
	assert.Contains(t, cfg.SearchSpace, "default")
	assert.Contains(t, cfg.SearchSpace, "prefill")
	assert.Contains(t, cfg.SearchSpace, "decode")
	assert.Contains(t, cfg.SearchSpace["prefill"], "chunkedPrefillSize")

	err = Validate(cfg, false)
	assert.NoError(t, err)
}

func ptr(v float64) *float64 { return &v }
