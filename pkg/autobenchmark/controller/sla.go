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
// For each SLA constraint, the deviation is:
//   - Max constraint (TTFT, TPOT, error rate): max(0, actual - limit)
//   - Min constraint (future): max(0, limit - actual)
//
// Deviation > 0 means constraint violated. Score is always the actual
// optimize metric value regardless of SLA violation.
func EvaluateSLA(metrics *abtypes.Metrics, objectives config.ObjectivesSpec) ([]float64, float64) {
	if metrics == nil {
		return []float64{0}, 0
	}

	sla := objectives.SLA
	var constraints []float64

	if sla.TTFTP99MaxMs != nil {
		constraints = append(constraints, max(0, metrics.TTFTP99-*sla.TTFTP99MaxMs))
	}
	if sla.TPOTP99MaxMs != nil {
		constraints = append(constraints, max(0, metrics.TPOTP99-*sla.TPOTP99MaxMs))
	}
	if sla.ErrorRateMax != nil {
		constraints = append(constraints, max(0, metrics.ErrorRate-*sla.ErrorRateMax))
	}

	score := getOptimizeMetric(metrics, objectives.Optimize)
	return constraints, score
}

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
