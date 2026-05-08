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

// AutoBenchmarkConfig is the top-level configuration for auto-benchmark.
type AutoBenchmarkConfig struct {
	APIVersion string `yaml:"apiVersion" json:"apiVersion"`
	Kind       string `yaml:"kind" json:"kind"`

	// Name is the experiment name, used as the base directory on PVC for all outputs.
	Name string `yaml:"name" json:"name"`

	// Templates references complete RBG YAML files as base templates.
	Templates []TemplateRef `yaml:"templates" json:"templates"`

	// Backend is the inference engine backend (sglang | vllm).
	Backend string `yaml:"backend" json:"backend"`

	// SearchSpace defines params to overlay on base RBG container commands.
	// Keys are role names ("default" applies to all roles).
	SearchSpace map[string]map[string]SearchParam `yaml:"searchSpace" json:"searchSpace"`

	// Scenario defines the fixed workload configuration for benchmarking.
	Scenario ScenarioSpec `yaml:"scenario" json:"scenario"`

	// Objectives defines SLA constraints and the metric to optimize.
	Objectives ObjectivesSpec `yaml:"objectives" json:"objectives"`

	// Strategy configures the search algorithm.
	Strategy StrategySpec `yaml:"strategy" json:"strategy"`

	// Evaluator configures the benchmark evaluation tool.
	Evaluator EvaluatorSpec `yaml:"evaluator" json:"evaluator"`

	// Results configures result storage.
	Results ResultsSpec `yaml:"results" json:"results"`

	// Execution configures runtime behavior.
	Execution ExecutionSpec `yaml:"execution" json:"execution"`
}

// TemplateRef references a complete RBG YAML file as a base template.
type TemplateRef struct {
	// Name is a human-readable identifier for this template.
	Name string `yaml:"name" json:"name"`

	// Template is the file path to the RBG YAML template.
	Template string `yaml:"template" json:"template"`
}

// SearchParam defines a single searchable parameter.
type SearchParam struct {
	// Type is the parameter type: "float", "int", "pow2", or "categorical".
	Type string `yaml:"type" json:"type"`

	// Values is used for categorical params.
	Values []interface{} `yaml:"values,omitempty" json:"values,omitempty"`

	// Min, Max, Step are used for float and int params.
	// Min and Max are required; Step is optional (absent = continuous).
	// For pow2, Min/Max use the actual values (e.g. 512/8192), no Step.
	Min  *float64 `yaml:"min,omitempty" json:"min,omitempty"`
	Max  *float64 `yaml:"max,omitempty" json:"max,omitempty"`
	Step *float64 `yaml:"step,omitempty" json:"step,omitempty"`

	// Log indicates whether the parameter should use log-uniform distribution
	// in Optuna. Meaningful for "float" type (FloatDistribution) and "int" type
	// (IntDistribution) when step is not set.
	Log bool `yaml:"log,omitempty" json:"log,omitempty"`
}

// ScenarioSpec defines a fixed workload scenario for benchmarking.
// Fields use project-own generic syntax; each evaluator plugin translates them
// to its own CLI format (e.g. genai-bench D/N/U notation).
type ScenarioSpec struct {
	Name        string   `yaml:"name" json:"name"`
	Workloads   []string `yaml:"workloads" json:"workloads"`                         // project-own syntax: fixed(in,out), normal(μ_in,σ_in/μ_out,σ_out), uniform(min,max/min,max), dataset
	Concurrency []int    `yaml:"concurrency" json:"concurrency"`                     // list of concurrency levels to test
	Duration    string   `yaml:"duration,omitempty" json:"duration,omitempty"`       // Go duration string, e.g. "2m", "30s"
	MaxRequests int      `yaml:"maxRequests,omitempty" json:"maxRequests,omitempty"` // max requests per run
}

// ObjectivesSpec defines SLA constraints and the optimization target.
type ObjectivesSpec struct {
	SLA      SLASpec `yaml:"sla" json:"sla"`
	Optimize string  `yaml:"optimize" json:"optimize"`
}

// SLASpec defines performance SLA constraints.
type SLASpec struct {
	TTFTP99MaxMs *float64 `yaml:"ttftP99MaxMs,omitempty" json:"ttftP99MaxMs,omitempty"`
	TPOTP99MaxMs *float64 `yaml:"tpotP99MaxMs,omitempty" json:"tpotP99MaxMs,omitempty"`
	ErrorRateMax *float64 `yaml:"errorRateMax,omitempty" json:"errorRateMax,omitempty"`
}

// StrategySpec configures the search algorithm.
type StrategySpec struct {
	Algorithm            string `yaml:"algorithm" json:"algorithm"`
	MaxTrialsPerTemplate int    `yaml:"maxTrialsPerTemplate" json:"maxTrialsPerTemplate"`
	Timeout              string `yaml:"timeout,omitempty" json:"timeout,omitempty"`

	// Optuna-specific fields (used when algorithm is an Optuna-backed sampler).
	Seed            *int                   `yaml:"seed,omitempty" json:"seed,omitempty"`                       // RNG seed for reproducibility
	StoragePath     string                 `yaml:"storagePath,omitempty" json:"storagePath,omitempty"`         // SQLite DB path on PVC for persistence
	AlgorithmConfig map[string]interface{} `yaml:"algorithmConfig,omitempty" json:"algorithmConfig,omitempty"` // Extra kwargs passed to the Optuna sampler constructor
}

// EvaluatorSpec configures the benchmark evaluation tool.
// The evaluator runs in-process inside the controller; no container fields needed.
type EvaluatorSpec struct {
	Type   string                 `yaml:"type" json:"type"`
	Config map[string]interface{} `yaml:"config,omitempty" json:"config,omitempty"` // evaluator-specific config, interpreted by the plugin's Init method
}

// ResultsSpec configures result storage.
type ResultsSpec struct {
	PVC     string `yaml:"pvc" json:"pvc"`
	SubPath string `yaml:"subPath,omitempty" json:"subPath,omitempty"`
}

// ExecutionSpec configures runtime behavior.
type ExecutionSpec struct {
	RBGReadyTimeout string `yaml:"rbgReadyTimeout,omitempty" json:"rbgReadyTimeout,omitempty"`
	TrialTimeout    string `yaml:"trialTimeout,omitempty" json:"trialTimeout,omitempty"`
}

// SupportedBackends lists valid backend values.
var SupportedBackends = []string{"sglang", "vllm"}

// SupportedAlgorithms lists valid search algorithm values.
var SupportedAlgorithms = []string{"grid", "tpe", "gp", "cmaes", "random", "qmc", "nsgaii", "nsgaiii"}

// SupportedOptimizeMetrics lists valid optimization target metrics.
var SupportedOptimizeMetrics = []string{"outputThroughput", "inputThroughput", "requestsPerSecond"}

// Search parameter type constants.
const (
	ParamTypeCategorical = "categorical"
	ParamTypeFloat       = "float"
	ParamTypeInt         = "int"
	ParamTypePow2        = "pow2"
)
