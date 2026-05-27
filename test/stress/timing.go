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
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"time"
)

// OperationRecord records timing for a single operation.
type OperationRecord struct {
	Name      string        `json:"name"`
	Operation string        `json:"operation"`
	StartTime time.Time     `json:"start_time"`
	EndTime   time.Time     `json:"end_time"`
	Duration  time.Duration `json:"duration_ns"`
	Error     string        `json:"error,omitempty"`
}

// PhaseStats holds statistics for a test phase.
type PhaseStats struct {
	Operation    string  `json:"operation"`
	Total        int     `json:"total"`
	Succeeded    int     `json:"succeeded"`
	Failed       int     `json:"failed"`
	P50Ms        float64 `json:"p50_ms"`
	P90Ms        float64 `json:"p90_ms"`
	P99Ms        float64 `json:"p99_ms"`
	MaxMs        float64 `json:"max_ms"`
	MinMs        float64 `json:"min_ms"`
	AvgMs        float64 `json:"avg_ms"`
	TotalTimeSec float64 `json:"total_time_sec"`
	ActualQPS    float64 `json:"actual_qps"`
}

// TimingRecorder collects and analyzes operation timing data.
type TimingRecorder struct {
	mu      sync.Mutex
	records []OperationRecord
}

// NewTimingRecorder creates a new timing recorder.
func NewTimingRecorder() *TimingRecorder {
	return &TimingRecorder{
		records: make([]OperationRecord, 0, 1000),
	}
}

// Record adds an operation record.
func (r *TimingRecorder) Record(rec OperationRecord) {
	rec.Duration = rec.EndTime.Sub(rec.StartTime)
	r.mu.Lock()
	r.records = append(r.records, rec)
	r.mu.Unlock()
}

// WriteResults writes timing data to CSV files per operation.
func (r *TimingRecorder) WriteResults(outputDir string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Group by operation
	byOp := map[string][]OperationRecord{}
	for _, rec := range r.records {
		byOp[rec.Operation] = append(byOp[rec.Operation], rec)
	}

	for op, records := range byOp {
		filename := fmt.Sprintf("%s/timing-%s.csv", outputDir, op)
		if err := writeCSV(filename, records); err != nil {
			return fmt.Errorf("write %s: %w", filename, err)
		}
	}
	return nil
}

// Summary generates a JSON summary of all phases.
func (r *TimingRecorder) Summary() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	byOp := map[string][]OperationRecord{}
	for _, rec := range r.records {
		byOp[rec.Operation] = append(byOp[rec.Operation], rec)
	}

	stats := make([]PhaseStats, 0, len(byOp))
	for op, records := range byOp {
		stats = append(stats, computeStats(op, records))
	}

	// Sort by operation order
	order := map[string]int{"create": 0, "update": 1, "delete": 2}
	sort.Slice(stats, func(i, j int) bool {
		return order[stats[i].Operation] < order[stats[j].Operation]
	})

	data, _ := json.MarshalIndent(stats, "", "  ")
	return string(data)
}

func computeStats(operation string, records []OperationRecord) PhaseStats {
	stats := PhaseStats{Operation: operation, Total: len(records)}

	var durations []float64
	var firstStart, lastEnd time.Time

	for _, rec := range records {
		if rec.Error != "" {
			stats.Failed++
			continue
		}
		stats.Succeeded++
		ms := float64(rec.Duration.Milliseconds())
		durations = append(durations, ms)

		if firstStart.IsZero() || rec.StartTime.Before(firstStart) {
			firstStart = rec.StartTime
		}
		if rec.EndTime.After(lastEnd) {
			lastEnd = rec.EndTime
		}
	}

	if len(durations) == 0 {
		return stats
	}

	sort.Float64s(durations)

	stats.MinMs = durations[0]
	stats.MaxMs = durations[len(durations)-1]
	stats.P50Ms = percentile(durations, 50)
	stats.P90Ms = percentile(durations, 90)
	stats.P99Ms = percentile(durations, 99)

	var sum float64
	for _, d := range durations {
		sum += d
	}
	stats.AvgMs = sum / float64(len(durations))

	if !firstStart.IsZero() && !lastEnd.IsZero() {
		stats.TotalTimeSec = lastEnd.Sub(firstStart).Seconds()
		if stats.TotalTimeSec > 0 {
			stats.ActualQPS = float64(stats.Succeeded) / stats.TotalTimeSec
		}
	}

	return stats
}

func percentile(sorted []float64, p int) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := float64(len(sorted)-1) * float64(p) / 100.0
	lower := int(math.Floor(idx))
	upper := int(math.Ceil(idx))
	if lower == upper {
		return sorted[lower]
	}
	frac := idx - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}

func writeCSV(filename string, records []OperationRecord) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	// Header
	if err := w.Write([]string{"name", "operation", "start_time", "end_time", "duration_ms", "error"}); err != nil {
		return err
	}

	for _, rec := range records {
		row := []string{
			rec.Name,
			rec.Operation,
			rec.StartTime.Format(time.RFC3339Nano),
			rec.EndTime.Format(time.RFC3339Nano),
			fmt.Sprintf("%.2f", float64(rec.Duration.Milliseconds())),
			rec.Error,
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}
