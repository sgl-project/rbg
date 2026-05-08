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
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ParseFile reads and parses an AutoBenchmarkConfig from a YAML file.
func ParseFile(path string) (*AutoBenchmarkConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	return Parse(data)
}

// Parse parses an AutoBenchmarkConfig from YAML bytes.
func Parse(data []byte) (*AutoBenchmarkConfig, error) {
	var cfg AutoBenchmarkConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config YAML: %w", err)
	}
	if err := setDefaults(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate validates the AutoBenchmarkConfig.
// When validateFiles is true, it also checks that template file paths exist on disk.
func Validate(cfg *AutoBenchmarkConfig, validateFiles bool) error {
	var errs []string

	errs = append(errs, validateRequiredFields(cfg)...)
	errs = append(errs, validateEnumFields(cfg)...)
	errs = append(errs, validateTemplates(cfg, validateFiles)...)
	errs = append(errs, validateSearchSpace(cfg)...)
	errs = append(errs, validateScenario(cfg)...)
	errs = append(errs, validateSLA(cfg)...)
	errs = append(errs, validateStrategy(cfg)...)
	errs = append(errs, validateEvaluator(cfg)...)
	errs = append(errs, validateResults(cfg)...)
	errs = append(errs, validateExecution(cfg)...)

	if len(errs) > 0 {
		return fmt.Errorf("config validation errors:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

func validateRequiredFields(cfg *AutoBenchmarkConfig) []string {
	var errs []string
	if len(cfg.Templates) == 0 {
		errs = append(errs, "templates: at least one template is required")
	}
	if cfg.Backend == "" {
		errs = append(errs, "backend: is required")
	}
	if len(cfg.SearchSpace) == 0 {
		errs = append(errs, "searchSpace: is required")
	}
	if cfg.Scenario.Name == "" {
		errs = append(errs, "scenario.name: is required")
	}
	if cfg.Objectives.Optimize == "" {
		errs = append(errs, "objectives.optimize: is required")
	}
	return errs
}

func validateEnumFields(cfg *AutoBenchmarkConfig) []string {
	var errs []string
	if cfg.Backend != "" && !slices.Contains(SupportedBackends, cfg.Backend) {
		errs = append(errs, fmt.Sprintf("backend: unsupported value %q, must be one of %v", cfg.Backend, SupportedBackends))
	}
	if cfg.Strategy.Algorithm != "" && !slices.Contains(SupportedAlgorithms, cfg.Strategy.Algorithm) {
		errs = append(errs, fmt.Sprintf("strategy.algorithm: unsupported value %q, must be one of %v", cfg.Strategy.Algorithm, SupportedAlgorithms))
	}
	if cfg.Objectives.Optimize != "" && !slices.Contains(SupportedOptimizeMetrics, cfg.Objectives.Optimize) {
		errs = append(errs, fmt.Sprintf("objectives.optimize: unsupported value %q, must be one of %v", cfg.Objectives.Optimize, SupportedOptimizeMetrics))
	}
	return errs
}

func validateTemplates(cfg *AutoBenchmarkConfig, validateFiles bool) []string {
	var errs []string
	templateNames := make(map[string]bool)
	for i, t := range cfg.Templates {
		if t.Name == "" {
			errs = append(errs, fmt.Sprintf("templates[%d].name: is required", i))
		} else if templateNames[t.Name] {
			errs = append(errs, fmt.Sprintf("templates[%d].name: duplicate name %q", i, t.Name))
		} else {
			templateNames[t.Name] = true
		}
		if t.Template == "" {
			errs = append(errs, fmt.Sprintf("templates[%d].template: is required", i))
		} else if validateFiles {
			if _, err := os.Stat(t.Template); os.IsNotExist(err) {
				errs = append(errs, fmt.Sprintf("templates[%d].template: file not found: %s", i, t.Template))
			}
		}
	}
	return errs
}

func validateSearchSpace(cfg *AutoBenchmarkConfig) []string {
	var errs []string
	for role, params := range cfg.SearchSpace {
		for name, param := range params {
			if err := validateSearchParam(role, name, param); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	return errs
}

func validateScenario(cfg *AutoBenchmarkConfig) []string {
	var errs []string
	if len(cfg.Scenario.Workloads) == 0 {
		errs = append(errs, "scenario.workloads: at least one is required")
	}
	for i, w := range cfg.Scenario.Workloads {
		if err := ValidateWorkload(w); err != nil {
			errs = append(errs, fmt.Sprintf("scenario.workloads[%d]: %v", i, err))
		}
	}
	if len(cfg.Scenario.Concurrency) == 0 {
		errs = append(errs, "scenario.concurrency: at least one is required")
	}
	if cfg.Scenario.Duration != "" {
		if _, err := time.ParseDuration(cfg.Scenario.Duration); err != nil {
			errs = append(errs, fmt.Sprintf("scenario.duration: invalid duration: %v", err))
		}
	}
	return errs
}

func validateSLA(cfg *AutoBenchmarkConfig) []string {
	var errs []string
	sla := cfg.Objectives.SLA
	if sla.TTFTP99MaxMs != nil && *sla.TTFTP99MaxMs < 0 {
		errs = append(errs, "objectives.sla.ttftP99MaxMs: must not be negative")
	}
	if sla.TPOTP99MaxMs != nil && *sla.TPOTP99MaxMs < 0 {
		errs = append(errs, "objectives.sla.tpotP99MaxMs: must not be negative")
	}
	if sla.ErrorRateMax != nil && *sla.ErrorRateMax < 0 {
		errs = append(errs, "objectives.sla.errorRateMax: must not be negative")
	}
	return errs
}

func validateStrategy(cfg *AutoBenchmarkConfig) []string {
	var errs []string
	if cfg.Strategy.MaxTrialsPerTemplate <= 0 {
		errs = append(errs, "strategy.maxTrialsPerTemplate: must be positive")
	}
	if cfg.Strategy.Timeout != "" {
		if _, err := time.ParseDuration(cfg.Strategy.Timeout); err != nil {
			errs = append(errs, fmt.Sprintf("strategy.timeout: invalid duration: %v", err))
		}
	}
	return errs
}

func validateEvaluator(cfg *AutoBenchmarkConfig) []string {
	var errs []string
	if cfg.Evaluator.Type == "" {
		errs = append(errs, "evaluator.type: is required")
	}
	return errs
}

func validateResults(cfg *AutoBenchmarkConfig) []string {
	var errs []string
	if cfg.Results.PVC == "" {
		errs = append(errs, "results.pvc: is required")
	}
	return errs
}

func validateExecution(cfg *AutoBenchmarkConfig) []string {
	var errs []string
	if cfg.Execution.RBGReadyTimeout != "" {
		if _, err := time.ParseDuration(cfg.Execution.RBGReadyTimeout); err != nil {
			errs = append(errs, fmt.Sprintf("execution.rbgReadyTimeout: invalid duration: %v", err))
		}
	}
	if cfg.Execution.TrialTimeout != "" {
		if _, err := time.ParseDuration(cfg.Execution.TrialTimeout); err != nil {
			errs = append(errs, fmt.Sprintf("execution.trialTimeout: invalid duration: %v", err))
		}
	}
	return errs
}

func validateSearchParam(role, name string, param SearchParam) error {
	prefix := fmt.Sprintf("searchSpace.%s.%s", role, name)
	switch param.Type {
	case "float", "int":
		if param.Min == nil || param.Max == nil {
			return fmt.Errorf("%s: %s type requires min and max", prefix, param.Type)
		}
		if *param.Min > *param.Max {
			return fmt.Errorf("%s: min (%v) must be less than max (%v)", prefix, *param.Min, *param.Max)
		}
		if param.Step != nil && *param.Step <= 0 {
			return fmt.Errorf("%s: step must be positive, got %v", prefix, *param.Step)
		}
	case "pow2":
		if param.Min == nil || param.Max == nil {
			return fmt.Errorf("%s: pow2 type requires min and max", prefix)
		}
		if *param.Min >= *param.Max {
			return fmt.Errorf("%s: min (%v) must be less than max (%v)", prefix, *param.Min, *param.Max)
		}
		if param.Step != nil {
			return fmt.Errorf("%s: pow2 type does not support step", prefix)
		}
		minVal := *param.Min
		maxVal := *param.Max
		if !isPow2Int(minVal) {
			return fmt.Errorf("%s: pow2 min (%v) must be a power of 2", prefix, minVal)
		}
		if !isPow2Int(maxVal) {
			return fmt.Errorf("%s: pow2 max (%v) must be a power of 2", prefix, maxVal)
		}
	case "categorical":
		if len(param.Values) == 0 {
			return fmt.Errorf("%s: categorical type requires at least one value", prefix)
		}
	default:
		return fmt.Errorf("%s: unsupported type %q, must be \"float\", \"int\", \"pow2\", or \"categorical\"", prefix, param.Type)
	}
	return nil
}

// isPow2Int returns true if the float64 value is an integer that is a power of 2.
func isPow2Int(v float64) bool {
	if v <= 0 || v != float64(int64(v)) {
		return false
	}
	n := int64(v)
	return n > 0 && (n&(n-1)) == 0
}

func setDefaults(cfg *AutoBenchmarkConfig) error {
	if cfg.Name == "" {
		cfg.Name = fmt.Sprintf("exp-%d", time.Now().UnixMilli())
	}
	if cfg.Strategy.Algorithm == "" {
		cfg.Strategy.Algorithm = "grid"
	}
	if cfg.Strategy.MaxTrialsPerTemplate == 0 {
		cfg.Strategy.MaxTrialsPerTemplate = 20
	}
	if cfg.Strategy.Timeout == "" {
		cfg.Strategy.Timeout = "4h"
	}
	if cfg.Execution.RBGReadyTimeout == "" {
		cfg.Execution.RBGReadyTimeout = "15m"
	}
	if cfg.Execution.TrialTimeout == "" {
		cfg.Execution.TrialTimeout = "30m"
	}
	return nil
}
