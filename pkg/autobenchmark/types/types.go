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

package types

import "time"

// ParamSet represents a set of parameter values for a single trial.
// Keys are abstract param names (e.g., "gpuMemoryUtilization"),
// values can be float64, int, string, or bool.
type ParamSet map[string]interface{}

// RoleParamSet maps role names to their parameter sets.
// "default" key applies to all roles.
type RoleParamSet map[string]ParamSet

// TrialResult holds the outcome of a single trial execution.
type TrialResult struct {
	TrialIndex   int          `json:"trialIndex"`
	TemplateName string       `json:"templateName"`
	Params       RoleParamSet `json:"params"`
	Metrics      *Metrics     `json:"metrics,omitempty"`
	Constraints  []float64    `json:"constraints"` // per-SLA deviation, ≤0 = feasible
	Score        float64      `json:"score"`
	Error        string       `json:"error,omitempty"`
	Duration     Duration     `json:"duration"`
	StartTime    time.Time    `json:"startTime"`
	EndTime      time.Time    `json:"endTime"`
}

// IsSLAFeasible returns true when all SLA constraints are satisfied (all ≤ 0).
func (t *TrialResult) IsSLAFeasible() bool {
	for _, c := range t.Constraints {
		if c > 0 {
			return false
		}
	}
	return true
}

// Metrics contains the benchmark performance metrics extracted from evaluator output.
type Metrics struct {
	// Latency metrics (milliseconds)
	TTFTP50 float64 `json:"ttftP50"`
	TTFTP99 float64 `json:"ttftP99"`
	TPOTP50 float64 `json:"tpotP50"`
	TPOTP99 float64 `json:"tpotP99"`

	// Throughput metrics (tokens per second)
	OutputThroughput float64 `json:"outputThroughput"`
	InputThroughput  float64 `json:"inputThroughput"`
	TotalThroughput  float64 `json:"totalThroughput"`

	// Request metrics
	RequestsPerSecond    float64 `json:"requestsPerSecond"`
	ErrorRate            float64 `json:"errorRate"`
	NumCompletedRequests int     `json:"numCompletedRequests"`
	NumErrorRequests     int     `json:"numErrorRequests"`
	NumRequests          int     `json:"numRequests"`
}

// TemplateState tracks the progress of trials for a single template.
type TemplateState struct {
	Name           string        `json:"name"`
	Completed      bool          `json:"completed"`
	Trials         []TrialResult `json:"trials"`
	BestTrial      *TrialResult  `json:"bestTrial,omitempty"`
	AlgorithmState []byte        `json:"algorithmState,omitempty"`
}

// ExperimentState is the full checkpoint state persisted to PVC.
type ExperimentState struct {
	ExperimentID       string          `json:"experimentId"`
	StartTime          time.Time       `json:"startTime"`
	CurrentTemplateIdx int             `json:"currentTemplateIdx"`
	Templates          []TemplateState `json:"templates"`
	GlobalBest         *TrialResult    `json:"globalBest,omitempty"`
}

// Duration is a JSON-serializable wrapper around time.Duration.
type Duration time.Duration

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(dur)
	return nil
}
