/*
Copyright 2025 The RBG Authors.

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

package main

import (
	"math"
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	tests := []struct {
		name   string
		sorted []float64
		p      int
		want   float64
	}{
		{"empty", nil, 50, 0},
		{"single", []float64{100}, 50, 100},
		{"single p99", []float64{100}, 99, 100},
		{"two elements p50", []float64{100, 200}, 50, 150},
		{"two elements p99", []float64{100, 200}, 99, 199},
		{"three elements p50", []float64{10, 20, 30}, 50, 20},
		{"three elements p90", []float64{10, 20, 30}, 90, 28},
		{"ten elements p50", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 50, 5.5},
		{"ten elements p99", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 99, 9.91},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := percentile(tt.sorted, tt.p)
			if math.Abs(got-tt.want) > 0.01 {
				t.Errorf("percentile(%v, %d) = %f, want %f", tt.sorted, tt.p, got, tt.want)
			}
		})
	}
}

func TestComputeStats(t *testing.T) {
	base := time.Now()

	tests := []struct {
		name       string
		records    []OperationRecord
		wantTotal  int
		wantSucc   int
		wantFailed int
		wantP50Ms  float64
	}{
		{
			name:       "empty",
			records:    nil,
			wantTotal:  0,
			wantSucc:   0,
			wantFailed: 0,
		},
		{
			name: "all succeeded",
			records: []OperationRecord{
				{Name: "a", StartTime: base, EndTime: base.Add(100 * time.Millisecond), Duration: 100 * time.Millisecond},
				{Name: "b", StartTime: base, EndTime: base.Add(200 * time.Millisecond), Duration: 200 * time.Millisecond},
				{Name: "c", StartTime: base, EndTime: base.Add(300 * time.Millisecond), Duration: 300 * time.Millisecond},
			},
			wantTotal:  3,
			wantSucc:   3,
			wantFailed: 0,
			wantP50Ms:  200,
		},
		{
			name: "mixed results",
			records: []OperationRecord{
				{Name: "a", StartTime: base, EndTime: base.Add(100 * time.Millisecond), Duration: 100 * time.Millisecond},
				{Name: "b", StartTime: base, EndTime: base.Add(200 * time.Millisecond), Duration: 200 * time.Millisecond, Error: "timeout"},
				{Name: "c", StartTime: base, EndTime: base.Add(300 * time.Millisecond), Duration: 300 * time.Millisecond},
			},
			wantTotal:  3,
			wantSucc:   2,
			wantFailed: 1,
			wantP50Ms:  200,
		},
		{
			name: "all failed",
			records: []OperationRecord{
				{Name: "a", StartTime: base, EndTime: base, Error: "err1"},
				{Name: "b", StartTime: base, EndTime: base, Error: "err2"},
			},
			wantTotal:  2,
			wantSucc:   0,
			wantFailed: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := computeStats("test", tt.records)
			if stats.Total != tt.wantTotal {
				t.Errorf("Total = %d, want %d", stats.Total, tt.wantTotal)
			}
			if stats.Succeeded != tt.wantSucc {
				t.Errorf("Succeeded = %d, want %d", stats.Succeeded, tt.wantSucc)
			}
			if stats.Failed != tt.wantFailed {
				t.Errorf("Failed = %d, want %d", stats.Failed, tt.wantFailed)
			}
			if tt.wantP50Ms > 0 && math.Abs(stats.P50Ms-tt.wantP50Ms) > 1 {
				t.Errorf("P50Ms = %f, want %f", stats.P50Ms, tt.wantP50Ms)
			}
		})
	}
}
