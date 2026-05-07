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

package controller

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/rbgs/pkg/autobenchmark/config"
	abtypes "sigs.k8s.io/rbgs/pkg/autobenchmark/types"
)

func TestSelectBest(t *testing.T) {
	tests := []struct {
		name   string
		trials []abtypes.TrialResult
		want   *abtypes.TrialResult
	}{
		{
			name:   "empty",
			trials: nil,
			want:   nil,
		},
		{
			name: "all fail SLA",
			trials: []abtypes.TrialResult{
				{TrialIndex: 0, Constraints: []float64{1}, Score: 0},
				{TrialIndex: 1, Constraints: []float64{1}, Score: 0},
			},
			want: nil,
		},
		{
			name: "single pass",
			trials: []abtypes.TrialResult{
				{TrialIndex: 0, Constraints: []float64{1}, Score: 0},
				{TrialIndex: 1, Constraints: []float64{}, Score: 1500},
			},
			want: &abtypes.TrialResult{TrialIndex: 1, Constraints: []float64{}, Score: 1500},
		},
		{
			name: "multiple pass - highest score wins",
			trials: []abtypes.TrialResult{
				{TrialIndex: 0, Constraints: []float64{}, Score: 1000},
				{TrialIndex: 1, Constraints: []float64{}, Score: 2000},
				{TrialIndex: 2, Constraints: []float64{}, Score: 1500},
				{TrialIndex: 3, Constraints: []float64{1}, Score: 0},
			},
			want: &abtypes.TrialResult{TrialIndex: 1, Constraints: []float64{}, Score: 2000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			best := SelectBest(tt.trials)
			if tt.want == nil {
				assert.Nil(t, best)
			} else {
				require.NotNil(t, best)
				assert.Equal(t, tt.want.TrialIndex, best.TrialIndex)
				assert.InDelta(t, tt.want.Score, best.Score, 0.01)
			}
		})
	}
}

func TestBuildReport(t *testing.T) {
	t.Run("with global best", func(t *testing.T) {
		state := &abtypes.ExperimentState{
			ExperimentID: "exp-1",
			Templates: []abtypes.TemplateState{
				{
					Name: "t1",
					Trials: []abtypes.TrialResult{
						{TrialIndex: 0, Constraints: []float64{}, Score: 1000},
						{TrialIndex: 1, Constraints: []float64{1}},
					},
					BestTrial: &abtypes.TrialResult{TrialIndex: 0, Score: 1000},
				},
				{
					Name: "t2",
					Trials: []abtypes.TrialResult{
						{TrialIndex: 0, Constraints: []float64{}, Score: 2000},
					},
					BestTrial: &abtypes.TrialResult{TrialIndex: 0, Score: 2000},
				},
			},
			GlobalBest: &abtypes.TrialResult{TrialIndex: 0, TemplateName: "t2", Score: 2000},
		}

		report := BuildReport(state)
		assert.Equal(t, "exp-1", report.ExperimentID)
		require.NotNil(t, report.GlobalBest)
		assert.InDelta(t, 2000, report.GlobalBest.Score, 0.01)
		require.Len(t, report.Templates, 2)
		assert.Equal(t, 2, report.Templates[0].NumTrials)
		assert.Equal(t, 1, report.Templates[0].NumSLAPass)
		assert.Equal(t, 1, report.Templates[1].NumTrials)
		assert.Equal(t, 1, report.Templates[1].NumSLAPass)
		assert.Contains(t, report.Summary, "Best result")
		assert.Contains(t, report.Summary, "2/3 trials passed SLA")
	})

	t.Run("no SLA pass", func(t *testing.T) {
		state := &abtypes.ExperimentState{
			ExperimentID: "exp-2",
			Templates: []abtypes.TemplateState{
				{
					Name: "t1",
					Trials: []abtypes.TrialResult{
						{TrialIndex: 0, Constraints: []float64{1}},
						{TrialIndex: 1, Constraints: []float64{1}},
					},
				},
			},
		}

		report := BuildReport(state)
		assert.Nil(t, report.GlobalBest)
		assert.Contains(t, report.Summary, "No configuration met SLA")
		assert.Contains(t, report.Summary, "2 trials executed")
	})
}

func TestWriteReportJSON(t *testing.T) {
	dir := t.TempDir()
	report := &Report{
		ExperimentID: "exp-test",
		Summary:      "test summary",
	}

	err := WriteReportJSON(dir, report)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "report.json"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "exp-test")
	assert.Contains(t, string(data), "test summary")
}

func TestWriteReportYAML(t *testing.T) {
	dir := t.TempDir()
	report := &Report{
		ExperimentID: "exp-test",
		Summary:      "test summary",
	}

	err := WriteReportYAML(dir, report)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "report.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "exp-test")
	assert.Contains(t, string(data), "test summary")
}

func TestBuildResult(t *testing.T) {
	startTime := time.Now()
	endTime := startTime.Add(30 * time.Minute)

	cfg := &config.AutoBenchmarkConfig{
		Name:    "exp-1",
		Backend: "vllm",
		Strategy: config.StrategySpec{
			Algorithm:            "tpe",
			MaxTrialsPerTemplate: 10,
			Timeout:              "1h",
		},
		Objectives: config.ObjectivesSpec{
			Optimize: "outputThroughput",
			SLA: config.SLASpec{
				TTFTP99MaxMs: float64Ptr(500),
				TPOTP99MaxMs: float64Ptr(50),
			},
		},
		Scenario: config.ScenarioSpec{
			Name:        "smoke",
			Workloads:   []string{"fixed(128,10)"},
			Concurrency: []int{4},
		},
		Templates: []config.TemplateRef{
			{Name: "tp"},
			{Name: "pp"},
		},
		SearchSpace: map[string]map[string]config.SearchParam{
			"default": {
				"gpuMemoryUtilization": {Type: "float", Min: float64Ptr(0.7), Max: float64Ptr(0.99), Step: float64Ptr(0.05)},
				"maxNumSeqs":           {Type: "categorical", Values: []interface{}{float64(64), float64(128), float64(256)}},
			},
		},
	}

	t.Run("with trials", func(t *testing.T) {
		state := &abtypes.ExperimentState{
			ExperimentID: "exp-1",
			StartTime:    startTime,
			Templates: []abtypes.TemplateState{
				{
					Name: "tp",
					Trials: []abtypes.TrialResult{
						{TrialIndex: 0, Constraints: []float64{}, Score: 1000},
						{TrialIndex: 1, Constraints: []float64{1}, Score: 0},
						{TrialIndex: 2, Constraints: []float64{}, Score: 2000},
					},
					BestTrial: &abtypes.TrialResult{TrialIndex: 2, Score: 2000},
				},
				{
					Name: "pp",
					Trials: []abtypes.TrialResult{
						{TrialIndex: 0, Constraints: []float64{}, Score: 1500},
					},
					BestTrial: &abtypes.TrialResult{TrialIndex: 0, Score: 1500},
				},
			},
			GlobalBest: &abtypes.TrialResult{TrialIndex: 2, TemplateName: "tp", Score: 2000},
		}

		result := BuildResult(state, cfg, endTime)

		// ExperimentID
		assert.Equal(t, "exp-1", result.ExperimentID)

		// Status — completed (endTime is set)
		assert.Equal(t, startTime, result.Status.StartTime)
		assert.Equal(t, endTime, result.Status.EndTime)
		assert.InDelta(t, 30*time.Minute.Seconds(), time.Duration(result.Status.Duration).Seconds(), 1)
		assert.Equal(t, 2, result.Status.NumTemplates)
		assert.Equal(t, 4, result.Status.TotalTrials)
		assert.Equal(t, 3, result.Status.NumSLAPass)
		assert.Equal(t, 1, result.Status.NumSLAFail)

		// Config
		assert.Equal(t, "exp-1", result.Config.Name)
		assert.Equal(t, "vllm", result.Config.Backend)
		assert.Equal(t, "tpe", result.Config.Algorithm)
		assert.Equal(t, "outputThroughput", result.Config.Optimize)
		require.NotNil(t, result.Config.SLA.TTFTP99MaxMs)
		assert.InDelta(t, 500, *result.Config.SLA.TTFTP99MaxMs, 0.01)
		require.NotNil(t, result.Config.SLA.TPOTP99MaxMs)
		assert.InDelta(t, 50, *result.Config.SLA.TPOTP99MaxMs, 0.01)
		assert.Nil(t, result.Config.SLA.ErrorRateMax)
		assert.Equal(t, "smoke", result.Config.ScenarioName)
		assert.Equal(t, []string{"fixed(128,10)"}, result.Config.ScenarioWorkloads)
		assert.Equal(t, []int{4}, result.Config.ScenarioConcurrency)
		assert.Equal(t, 10, result.Config.MaxTrialsPerTmpl)
		assert.Equal(t, "1h", result.Config.Timeout)
		assert.Equal(t, []string{"tp", "pp"}, result.Config.Templates)
		assert.Len(t, result.Config.SearchSpace, 1)
		assert.Len(t, result.Config.SearchSpace["default"], 2)
		gpuMem := result.Config.SearchSpace["default"]["gpuMemoryUtilization"]
		assert.Equal(t, "float", gpuMem.Type)
		require.NotNil(t, gpuMem.Min)
		assert.InDelta(t, 0.7, *gpuMem.Min, 0.01)
		require.NotNil(t, gpuMem.Max)
		assert.InDelta(t, 0.99, *gpuMem.Max, 0.01)
		require.NotNil(t, gpuMem.Step)
		assert.InDelta(t, 0.05, *gpuMem.Step, 0.01)
		maxSeqs := result.Config.SearchSpace["default"]["maxNumSeqs"]
		assert.Equal(t, "categorical", maxSeqs.Type)
		require.Len(t, maxSeqs.Values, 3)

		// Templates
		require.Len(t, result.Templates, 2)
		assert.Equal(t, "tp", result.Templates[0].Name)
		require.NotNil(t, result.Templates[0].BestTrial)
		assert.InDelta(t, 2000, result.Templates[0].BestTrial.Score, 0.01)
		assert.Len(t, result.Templates[0].Trials, 3)

		assert.Equal(t, "pp", result.Templates[1].Name)
		assert.Len(t, result.Templates[1].Trials, 1)

		// GlobalBest
		require.NotNil(t, result.GlobalBest)
		assert.InDelta(t, 2000, result.GlobalBest.Score, 0.01)
	})

	t.Run("in progress - no endTime", func(t *testing.T) {
		state := &abtypes.ExperimentState{
			ExperimentID: "exp-1",
			StartTime:    startTime,
			Templates: []abtypes.TemplateState{
				{
					Name: "tp",
					Trials: []abtypes.TrialResult{
						{TrialIndex: 0, Constraints: []float64{}, Score: 1000},
					},
				},
			},
		}

		result := BuildResult(state, cfg, time.Time{})

		assert.True(t, result.Status.EndTime.IsZero())
		assert.Equal(t, abtypes.Duration(0), result.Status.Duration)
	})

	t.Run("empty state", func(t *testing.T) {
		state := &abtypes.ExperimentState{
			ExperimentID: "exp-empty",
			StartTime:    startTime,
		}

		result := BuildResult(state, cfg, endTime)

		assert.Equal(t, "exp-empty", result.ExperimentID)
		assert.Equal(t, 0, result.Status.NumTemplates)
		assert.Equal(t, 0, result.Status.TotalTrials)
		assert.Equal(t, 0, result.Status.NumSLAPass)
		assert.Equal(t, 0, result.Status.NumSLAFail)
		assert.Len(t, result.Templates, 0)
		assert.Nil(t, result.GlobalBest)
	})
}

func TestWriteResultJSON(t *testing.T) {
	dir := t.TempDir()
	result := &ResultDetail{
		ExperimentID: "exp-test",
		Status: ResultStatus{
			TotalTrials: 5,
			NumSLAPass:  4,
			NumSLAFail:  1,
		},
		Config: ResultConfig{
			Name:    "exp-test",
			Backend: "vllm",
		},
	}

	err := WriteResultJSON(dir, result)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	require.NoError(t, err)

	// Verify file exists and contains expected content
	assert.Contains(t, string(data), "exp-test")

	// Verify JSON is valid and parseable
	var parsed ResultDetail
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, "exp-test", parsed.ExperimentID)
	assert.Equal(t, 5, parsed.Status.TotalTrials)
	assert.Equal(t, 4, parsed.Status.NumSLAPass)
	assert.Equal(t, 1, parsed.Status.NumSLAFail)
}

func float64Ptr(v float64) *float64 { return &v }
