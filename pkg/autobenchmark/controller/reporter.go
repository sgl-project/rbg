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
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v2"

	"sigs.k8s.io/rbgs/pkg/autobenchmark/config"
	abtypes "sigs.k8s.io/rbgs/pkg/autobenchmark/types"
)

// Report is the final output of an auto-benchmark experiment.
type Report struct {
	ExperimentID string               `json:"experimentId" yaml:"experimentId"`
	GlobalBest   *abtypes.TrialResult `json:"globalBest,omitempty" yaml:"globalBest,omitempty"`
	Templates    []TemplateReport     `json:"templates" yaml:"templates"`
	Summary      string               `json:"summary" yaml:"summary"`
}

// TemplateReport summarizes results for a single template.
type TemplateReport struct {
	Name       string               `json:"name" yaml:"name"`
	BestTrial  *abtypes.TrialResult `json:"bestTrial,omitempty" yaml:"bestTrial,omitempty"`
	NumTrials  int                  `json:"numTrials" yaml:"numTrials"`
	NumSLAPass int                  `json:"numSLAPass" yaml:"numSLAPass"`
}

// BuildReport creates a report from the experiment state.
func BuildReport(state *abtypes.ExperimentState) *Report {
	report := &Report{
		ExperimentID: state.ExperimentID,
		GlobalBest:   state.GlobalBest,
	}

	totalTrials := 0
	totalPass := 0

	for _, ts := range state.Templates {
		tr := TemplateReport{
			Name:      ts.Name,
			BestTrial: ts.BestTrial,
			NumTrials: len(ts.Trials),
		}
		for _, trial := range ts.Trials {
			if trial.IsSLAFeasible() {
				tr.NumSLAPass++
			}
		}
		totalTrials += tr.NumTrials
		totalPass += tr.NumSLAPass
		report.Templates = append(report.Templates, tr)
	}

	if state.GlobalBest != nil {
		report.Summary = fmt.Sprintf(
			"Best result: template=%q, trial=%d, score=%.2f (%d/%d trials passed SLA)",
			state.GlobalBest.TemplateName, state.GlobalBest.TrialIndex,
			state.GlobalBest.Score, totalPass, totalTrials,
		)
	} else {
		report.Summary = fmt.Sprintf(
			"No configuration met SLA constraints (%d trials executed across %d templates)",
			totalTrials, len(state.Templates),
		)
	}

	return report
}

// SelectBest finds the best SLA-passing trial from a list by highest score.
func SelectBest(trials []abtypes.TrialResult) *abtypes.TrialResult {
	var best *abtypes.TrialResult
	for i := range trials {
		t := &trials[i]
		if !t.IsSLAFeasible() {
			continue
		}
		if best == nil || t.Score > best.Score {
			best = t
		}
	}
	return best
}

// WriteReportJSON writes the report as JSON to the given directory.
func WriteReportJSON(dir string, report *Report) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.json"), data, 0644)
}

// WriteReportYAML writes the report as YAML to the given directory.
func WriteReportYAML(dir string, report *Report) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(report)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.yaml"), data, 0644)
}

// --- ResultDetail: full experiment result for UI consumption ---

// ResultDetail is the complete experiment result including all trials,
// status metadata, and a config snapshot for UI-driven visualization.
type ResultDetail struct {
	ExperimentID string               `json:"experimentId"`
	Status       ResultStatus         `json:"status"`
	Config       ResultConfig         `json:"config"`
	Templates    []TemplateDetail     `json:"templates"`
	GlobalBest   *abtypes.TrialResult `json:"globalBest,omitempty"`
}

// ResultStatus holds timing and progress metadata for the experiment.
type ResultStatus struct {
	StartTime    time.Time        `json:"startTime"`
	EndTime      *time.Time       `json:"endTime,omitempty"`
	Duration     abtypes.Duration `json:"duration,omitempty"`
	NumTemplates int              `json:"numTemplates"`
	TotalTrials  int              `json:"totalTrials"`
	NumSLAPass   int              `json:"numSLAPass"`
	NumSLAFail   int              `json:"numSLAFail"`
}

// ResultConfig is a snapshot of the key config fields relevant for UI display.
type ResultConfig struct {
	Name                string            `json:"name"`
	Backend             string            `json:"backend"`
	Algorithm           string            `json:"algorithm"`
	Optimize            string            `json:"optimize"`
	SLA                 ResultSLA         `json:"sla"`
	ScenarioName        string            `json:"scenarioName"`
	ScenarioWorkloads   []string          `json:"scenarioWorkloads,omitempty"`
	ScenarioConcurrency []int             `json:"scenarioConcurrency,omitempty"`
	MaxTrialsPerTmpl    int               `json:"maxTrialsPerTemplate"`
	Timeout             string            `json:"timeout,omitempty"`
	Templates           []string          `json:"templates"`
	SearchSpace         ResultSearchSpace `json:"searchSpace"`
}

// ResultSLA mirrors the SLA constraints from config.
type ResultSLA struct {
	TTFTP99MaxMs *float64 `json:"ttftP99MaxMs,omitempty"`
	TPOTP99MaxMs *float64 `json:"tpotP99MaxMs,omitempty"`
	ErrorRateMax *float64 `json:"errorRateMax,omitempty"`
}

// SearchParamDetail describes a single searchable parameter with its range/categorical details.
type SearchParamDetail struct {
	Type   string        `json:"type"`
	Values []interface{} `json:"values,omitempty"`
	Min    *float64      `json:"min,omitempty"`
	Max    *float64      `json:"max,omitempty"`
	Step   *float64      `json:"step,omitempty"`
}

// ResultSearchSpace maps each role to its tunable parameter definitions.
type ResultSearchSpace map[string]map[string]SearchParamDetail

// TemplateDetail holds all trial results for a single template.
type TemplateDetail struct {
	Name      string                `json:"name"`
	BestTrial *abtypes.TrialResult  `json:"bestTrial,omitempty"`
	Trials    []abtypes.TrialResult `json:"trials"`
}

// BuildResult builds a full ResultDetail from experiment state and config.
func BuildResult(state *abtypes.ExperimentState, cfg *config.AutoBenchmarkConfig, endTime time.Time) *ResultDetail {
	result := &ResultDetail{
		ExperimentID: state.ExperimentID,
		GlobalBest:   state.GlobalBest,
	}

	// Status
	result.Status.StartTime = state.StartTime
	if !endTime.IsZero() {
		result.Status.EndTime = &endTime
		result.Status.Duration = abtypes.Duration(endTime.Sub(state.StartTime))
	}
	result.Status.NumTemplates = len(state.Templates)

	for _, ts := range state.Templates {
		result.Status.TotalTrials += len(ts.Trials)
		for _, trial := range ts.Trials {
			if trial.IsSLAFeasible() {
				result.Status.NumSLAPass++
			} else {
				result.Status.NumSLAFail++
			}
		}
	}

	// Config snapshot
	result.Config.Name = cfg.Name
	result.Config.Backend = cfg.Backend
	result.Config.Algorithm = cfg.Strategy.Algorithm
	result.Config.Optimize = cfg.Objectives.Optimize
	result.Config.SLA.TTFTP99MaxMs = cfg.Objectives.SLA.TTFTP99MaxMs
	result.Config.SLA.TPOTP99MaxMs = cfg.Objectives.SLA.TPOTP99MaxMs
	result.Config.SLA.ErrorRateMax = cfg.Objectives.SLA.ErrorRateMax
	result.Config.ScenarioName = cfg.Scenario.Name
	result.Config.ScenarioWorkloads = cfg.Scenario.Workloads
	result.Config.ScenarioConcurrency = cfg.Scenario.Concurrency
	result.Config.MaxTrialsPerTmpl = cfg.Strategy.MaxTrialsPerTemplate
	result.Config.Timeout = cfg.Strategy.Timeout

	for _, t := range cfg.Templates {
		result.Config.Templates = append(result.Config.Templates, t.Name)
	}

	result.Config.SearchSpace = make(ResultSearchSpace, len(cfg.SearchSpace))
	for role, params := range cfg.SearchSpace {
		details := make(map[string]SearchParamDetail, len(params))
		for name, sp := range params {
			details[name] = SearchParamDetail{
				Type:   sp.Type,
				Values: sp.Values,
				Min:    sp.Min,
				Max:    sp.Max,
				Step:   sp.Step,
			}
		}
		result.Config.SearchSpace[role] = details
	}

	// Templates
	for _, ts := range state.Templates {
		td := TemplateDetail{
			Name:      ts.Name,
			BestTrial: ts.BestTrial,
			Trials:    ts.Trials,
		}
		result.Templates = append(result.Templates, td)
	}

	return result
}

// WriteResultJSON writes the full result detail as JSON to the given directory.
func WriteResultJSON(dir string, result *ResultDetail) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "result.json"), data, 0644)
}
