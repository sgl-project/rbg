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
	"sigs.k8s.io/rbgs/pkg/autobenchmark/config"
	abtypes "sigs.k8s.io/rbgs/pkg/autobenchmark/types"
)

// EvaluateSLA computes per-SLA constraint deviations and the optimize metric.
//
// For each SLA constraint, the deviation is computed as a relative ratio:
//   - Max constraint (field name ends with "Max", e.g. TTFTP99MaxMs):
//     actual should be <= limit, deviation = max(0, (actual - limit) / limit)
//   - Min constraint (field name ends with "Min"):
//     actual should be >= limit, deviation = max(0, (limit - actual) / limit)
//   - When limit is 0, falls back to absolute deviation.
//
// Deviation > 0 means constraint violated. The ratio allows Optuna's sampler
// to perceive the severity of violation relative to the constraint threshold.
// Score is always the actual optimize metric value regardless of SLA violation.
func EvaluateSLA(metrics *abtypes.Metrics, objectives config.ObjectivesSpec) ([]float64, float64) {
	if metrics == nil {
		return []float64{0}, 0
	}

	sla := objectives.SLA
	var constraints []float64

	// Max constraints: actual should be <= limit (lower is better)
	if sla.TTFTP99MaxMs != nil {
		constraints = append(constraints, maxConstraintDeviation(metrics.TTFTP99, *sla.TTFTP99MaxMs))
	}
	if sla.TPOTP99MaxMs != nil {
		constraints = append(constraints, maxConstraintDeviation(metrics.TPOTP99, *sla.TPOTP99MaxMs))
	}
	if sla.ErrorRateMax != nil {
		constraints = append(constraints, maxConstraintDeviation(metrics.ErrorRate, *sla.ErrorRateMax))
	}

	score := getOptimizeMetric(metrics, objectives.Optimize)
	return constraints, score
}

// maxConstraintDeviation computes the relative deviation from a max constraint.
// The actual value should be <= limit. Returns (actual - limit) / limit when limit != 0,
// otherwise (actual - limit). Result is clamped to >= 0.
func maxConstraintDeviation(actual, limit float64) float64 {
	var dev float64
	if limit != 0 {
		dev = (actual - limit) / limit
	} else {
		dev = actual - limit
	}
	return max(0, dev)
}

// minConstraintDeviation computes the relative deviation from a min constraint.
// The actual value should be >= limit. Returns (limit - actual) / limit when limit != 0,
// otherwise (limit - actual). Result is clamped to >= 0.
// func minConstraintDeviation(actual, limit float64) float64 {
// 	var dev float64
// 	if limit != 0 {
// 		dev = (limit - actual) / limit
// 	} else {
// 		dev = limit - actual
// 	}
// 	return max(0, dev)
// }

// getOptimizeMetric extracts the metric value to maximize based on the optimize target.
func getOptimizeMetric(metrics *abtypes.Metrics, optimize string) float64 {
	switch optimize {
	case "outputThroughput":
		return metrics.OutputThroughput
	case "inputThroughput":
		return metrics.InputThroughput
	case "requestsPerSecond":
		return metrics.RequestsPerSecond
	default:
		return metrics.OutputThroughput // fallback
	}
}
