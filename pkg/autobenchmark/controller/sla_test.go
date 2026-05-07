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
	"testing"

	"github.com/stretchr/testify/assert"

	"sigs.k8s.io/rbgs/pkg/autobenchmark/config"
	abtypes "sigs.k8s.io/rbgs/pkg/autobenchmark/types"
)

func f64(v float64) *float64 { return &v }

func TestEvaluateSLA(t *testing.T) {
	tests := []struct {
		name            string
		metrics         *abtypes.Metrics
		objectives      config.ObjectivesSpec
		wantConstraints []float64
		wantScore       float64
	}{
		{
			name: "all pass - output throughput",
			metrics: &abtypes.Metrics{
				TTFTP99:          1500,
				TPOTP99:          8,
				ErrorRate:        0.005,
				OutputThroughput: 2000,
			},
			objectives: config.ObjectivesSpec{
				SLA:      config.SLASpec{TTFTP99MaxMs: f64(2000), TPOTP99MaxMs: f64(10), ErrorRateMax: f64(0.01)},
				Optimize: "outputThroughput",
			},
			wantConstraints: []float64{0, 0, 0},
			wantScore:       2000,
		},
		{
			name: "TTFT P99 exceeds SLA",
			metrics: &abtypes.Metrics{
				TTFTP99:          2500,
				TPOTP99:          5,
				ErrorRate:        0,
				OutputThroughput: 3000,
			},
			objectives: config.ObjectivesSpec{
				SLA:      config.SLASpec{TTFTP99MaxMs: f64(2000)},
				Optimize: "outputThroughput",
			},
			wantConstraints: []float64{500},
			wantScore:       3000,
		},
		{
			name: "TPOT P99 exceeds SLA",
			metrics: &abtypes.Metrics{
				TTFTP99:          100,
				TPOTP99:          15,
				ErrorRate:        0,
				OutputThroughput: 1000,
			},
			objectives: config.ObjectivesSpec{
				SLA:      config.SLASpec{TPOTP99MaxMs: f64(10)},
				Optimize: "outputThroughput",
			},
			wantConstraints: []float64{5},
			wantScore:       1000,
		},
		{
			name: "error rate exceeds SLA",
			metrics: &abtypes.Metrics{
				TTFTP99:          100,
				TPOTP99:          5,
				ErrorRate:        0.02,
				OutputThroughput: 500,
			},
			objectives: config.ObjectivesSpec{
				SLA:      config.SLASpec{ErrorRateMax: f64(0.01)},
				Optimize: "outputThroughput",
			},
			wantConstraints: []float64{0.01},
			wantScore:       500,
		},
		{
			name: "no SLA constraints - always passes",
			metrics: &abtypes.Metrics{
				OutputThroughput: 1500,
			},
			objectives: config.ObjectivesSpec{
				SLA:      config.SLASpec{},
				Optimize: "outputThroughput",
			},
			wantConstraints: nil,
			wantScore:       1500,
		},
		{
			name:    "nil metrics",
			metrics: nil,
			objectives: config.ObjectivesSpec{
				Optimize: "outputThroughput",
			},
			wantConstraints: []float64{0},
			wantScore:       0,
		},
		{
			name: "optimize requestsPerSecond",
			metrics: &abtypes.Metrics{
				RequestsPerSecond: 50.5,
				OutputThroughput:  2000,
			},
			objectives: config.ObjectivesSpec{
				SLA:      config.SLASpec{},
				Optimize: "requestsPerSecond",
			},
			wantConstraints: nil,
			wantScore:       50.5,
		},
		{
			name: "optimize inputThroughput",
			metrics: &abtypes.Metrics{
				InputThroughput:  800,
				OutputThroughput: 2000,
			},
			objectives: config.ObjectivesSpec{
				SLA:      config.SLASpec{},
				Optimize: "inputThroughput",
			},
			wantConstraints: nil,
			wantScore:       800,
		},
		{
			name: "boundary - exactly at SLA limit passes",
			metrics: &abtypes.Metrics{
				TTFTP99:          2000,
				OutputThroughput: 1000,
			},
			objectives: config.ObjectivesSpec{
				SLA:      config.SLASpec{TTFTP99MaxMs: f64(2000)},
				Optimize: "outputThroughput",
			},
			wantConstraints: []float64{0},
			wantScore:       1000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			constraints, score := EvaluateSLA(tt.metrics, tt.objectives)
			assert.Equal(t, tt.wantConstraints, constraints)
			assert.InDelta(t, tt.wantScore, score, 0.01)
		})
	}
}
