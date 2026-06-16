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
	"fmt"
	"html/template"
	"os"
	"sort"
	"strings"
	"time"
)

// ReportData holds all data needed for the HTML report.
type ReportData struct {
	GeneratedAt   string
	Scenario      ScenarioSummary
	Controller    ControllerInfoReport
	Phases        []PhaseReport
	LogAnalysis   LogAnalysis
	PprofAnalysis PprofAnalysis
	Issues        []string
}

// ControllerInfoReport holds controller details for the report.
type ControllerInfoReport struct {
	Image         string
	Replicas      int
	CPULimit      string
	MemoryLimit   string
	CPURequest    string
	MemoryRequest string
	Args          []string
	NodeName      string
}

// PprofAnalysis holds profiling analysis results.
type PprofAnalysis struct {
	Available    bool
	CPUTop       string
	HeapTop      string
	AllocsTop    string
	GoroutineTop string
}

// ScenarioSummary describes the test configuration.
type ScenarioSummary struct {
	TotalRBGs     int
	RolesPerRBG   int
	LWSRoles      int
	LWSSize       int
	CreateQPS     float64
	UpdateQPS     float64
	DeleteQPS     float64
	InPlaceUpdate bool
	UseKwokNodes  bool
}

// PhaseReport holds detailed results for one phase.
type PhaseReport struct {
	Name         string
	Total        int
	Succeeded    int
	Failed       int
	P50Ms        float64
	P90Ms        float64
	P99Ms        float64
	MaxMs        float64
	MinMs        float64
	AvgMs        float64
	ActualQPS    float64
	TotalTimeSec float64
	Latencies    []LatencyPoint
	Errors       []string
}

// LatencyPoint is a single latency data point for charting.
type LatencyPoint struct {
	Index     int
	Name      string
	LatencyMs float64
	HasError  bool
}

// LogAnalysis holds classified controller log data.
type LogAnalysis struct {
	TotalLines       int
	ErrorLines       int
	ReconcileErrors  int
	WorkloadTypeErrs int
	ConflictErrors   int
	ThrottleErrors   int
	TimeoutErrors    int
	NotFoundErrors   int
	PanicErrors      int
	SampleErrors     []string
}

// GenerateHTMLReport creates a comprehensive HTML report file.
func GenerateHTMLReport(outputDir string, scenario *Scenario, recorder *TimingRecorder, logAnalysis LogAnalysis) error {
	data := buildReportData(scenario, recorder, logAnalysis)

	filename := fmt.Sprintf("%s/report.html", outputDir)
	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("create report file: %w", err)
	}
	defer func() { _ = f.Close() }()

	funcMap := template.FuncMap{
		"pct": func(val, max float64) float64 {
			if max == 0 {
				return 0
			}
			return val / max * 100
		},
		"barLeft": func(index, total int) float64 {
			if total == 0 {
				return 0
			}
			return float64(index) / float64(total) * 100
		},
		"barWidth": func(total int) float64 {
			if total == 0 {
				return 1
			}
			w := 90.0 / float64(total)
			if w < 1 {
				w = 1
			}
			return w
		},
	}

	tmpl, err := template.New("report").Funcs(funcMap).Parse(reportTemplate)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("execute template: %w", err)
	}

	return nil
}

func buildReportData(scenario *Scenario, recorder *TimingRecorder, logAnalysis LogAnalysis) ReportData {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	// Group records by operation
	byOp := map[string][]OperationRecord{}
	for _, rec := range recorder.records {
		byOp[rec.Operation] = append(byOp[rec.Operation], rec)
	}

	// Build phase reports in order
	phases := []PhaseReport{}
	for _, opName := range []string{"create", "update", "delete"} {
		records, ok := byOp[opName]
		if !ok {
			continue
		}
		phases = append(phases, buildPhaseReport(opName, records))
	}

	// Detect issues
	issues := detectIssues(phases, logAnalysis)

	// Build controller info
	controllerInfo := ControllerInfoReport{
		Image:         scenario.ControllerInfo.Image,
		Replicas:      scenario.ControllerInfo.Replicas,
		CPULimit:      scenario.ControllerInfo.CPULimit,
		MemoryLimit:   scenario.ControllerInfo.MemoryLimit,
		CPURequest:    scenario.ControllerInfo.CPURequest,
		MemoryRequest: scenario.ControllerInfo.MemoryRequest,
		Args:          scenario.ControllerInfo.Args,
		NodeName:      scenario.ControllerInfo.NodeName,
	}

	// Load pprof analysis if available
	pprofAnalysis := loadPprofAnalysis(scenario.OutputDir)

	return ReportData{
		GeneratedAt: time.Now().Format("2006-01-02 15:04:05"),
		Scenario: ScenarioSummary{
			TotalRBGs:     scenario.TotalRBGs,
			RolesPerRBG:   scenario.RolesPerRBG,
			LWSRoles:      scenario.LWSRoles,
			LWSSize:       scenario.LWSSize,
			CreateQPS:     scenario.CreateQPS,
			UpdateQPS:     scenario.UpdateQPS,
			DeleteQPS:     scenario.DeleteQPS,
			InPlaceUpdate: scenario.InPlaceUpdate,
			UseKwokNodes:  scenario.UseKwokNodes,
		},
		Controller:    controllerInfo,
		Phases:        phases,
		LogAnalysis:   logAnalysis,
		PprofAnalysis: pprofAnalysis,
		Issues:        issues,
	}
}

// loadPprofAnalysis reads pprof top text files from the output directory.
// It looks for phase-specific files (e.g. cpu-create-top.txt) and falls back to generic ones.
func loadPprofAnalysis(outputDir string) PprofAnalysis {
	analysis := PprofAnalysis{}

	// Try phases in order of importance for profiling
	phases := []string{"create", "update", "delete"}

	type profileEntry struct {
		kind string
		dest *string
	}
	entries := []profileEntry{
		{"cpu", &analysis.CPUTop},
		{"heap", &analysis.HeapTop},
		{"allocs", &analysis.AllocsTop},
		{"goroutine", &analysis.GoroutineTop},
	}

	for _, entry := range entries {
		// Try phase-specific files first, then generic
		candidates := make([]string, 0, len(phases)+1)
		for _, phase := range phases {
			candidates = append(candidates, fmt.Sprintf("%s/%s-%s-top.txt", outputDir, entry.kind, phase))
		}
		candidates = append(candidates, fmt.Sprintf("%s/%s-top.txt", outputDir, entry.kind))

		for _, candidate := range candidates {
			data, err := os.ReadFile(candidate)
			if err == nil && len(data) > 0 {
				analysis.Available = true
				*entry.dest = string(data)
				break
			}
		}
	}

	return analysis
}

func buildPhaseReport(name string, records []OperationRecord) PhaseReport {
	report := PhaseReport{
		Name:  name,
		Total: len(records),
	}

	var durations []float64
	var firstStart, lastEnd time.Time

	for i, rec := range records {
		point := LatencyPoint{
			Index: i,
			Name:  rec.Name,
		}
		if rec.Error != "" {
			report.Failed++
			point.HasError = true
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %s", rec.Name, rec.Error))
		} else {
			report.Succeeded++
			ms := float64(rec.Duration.Milliseconds())
			durations = append(durations, ms)
			point.LatencyMs = ms

			if firstStart.IsZero() || rec.StartTime.Before(firstStart) {
				firstStart = rec.StartTime
			}
			if rec.EndTime.After(lastEnd) {
				lastEnd = rec.EndTime
			}
		}
		report.Latencies = append(report.Latencies, point)
	}

	if len(durations) > 0 {
		sort.Float64s(durations)
		report.MinMs = durations[0]
		report.MaxMs = durations[len(durations)-1]
		report.P50Ms = percentile(durations, 50)
		report.P90Ms = percentile(durations, 90)
		report.P99Ms = percentile(durations, 99)

		var sum float64
		for _, d := range durations {
			sum += d
		}
		report.AvgMs = sum / float64(len(durations))
	}

	if !firstStart.IsZero() && !lastEnd.IsZero() {
		report.TotalTimeSec = lastEnd.Sub(firstStart).Seconds()
		if report.TotalTimeSec > 0 {
			report.ActualQPS = float64(report.Succeeded) / report.TotalTimeSec
		}
	}

	return report
}

func detectIssues(phases []PhaseReport, logAnalysis LogAnalysis) []string {
	var issues []string

	for _, p := range phases {
		if p.Failed > 0 {
			issues = append(issues, fmt.Sprintf("[%s] %d/%d operations failed (%.1f%% failure rate)",
				p.Name, p.Failed, p.Total, float64(p.Failed)/float64(p.Total)*100))
		}
		if p.P99Ms > 10000 {
			issues = append(issues, fmt.Sprintf("[%s] P99 latency %.0fms exceeds 10s threshold — controller may be overloaded",
				p.Name, p.P99Ms))
		}
	}

	if logAnalysis.ConflictErrors > 10 {
		issues = append(issues, fmt.Sprintf("High conflict errors (%d) — consider reducing max-concurrent-reconciles or adding retry backoff", logAnalysis.ConflictErrors))
	}
	if logAnalysis.ThrottleErrors > 0 {
		issues = append(issues, fmt.Sprintf("API throttling detected (%d occurrences) — increase kube-api-qps/burst", logAnalysis.ThrottleErrors))
	}
	if logAnalysis.WorkloadTypeErrs > 0 {
		issues = append(issues, fmt.Sprintf("Workload type errors (%d) — controller image may not support the default workload type, ensure role-workload-type annotation is set", logAnalysis.WorkloadTypeErrs))
	}
	if logAnalysis.PanicErrors > 0 {
		issues = append(issues, fmt.Sprintf("CRITICAL: %d panic(s) detected in controller logs", logAnalysis.PanicErrors))
	}
	if logAnalysis.TimeoutErrors > 0 {
		issues = append(issues, fmt.Sprintf("Context timeout errors (%d) — reconciliation taking too long", logAnalysis.TimeoutErrors))
	}

	if len(issues) == 0 {
		issues = append(issues, "No significant issues detected")
	}

	return issues
}

// ParseLogAnalysis reads the error-summary.txt and errors.log to build LogAnalysis.
func ParseLogAnalysis(outputDir string) LogAnalysis {
	analysis := LogAnalysis{}

	// Count total log lines
	if data, err := os.ReadFile(fmt.Sprintf("%s/controller-full.log", outputDir)); err == nil {
		analysis.TotalLines = len(strings.Split(string(data), "\n"))
	}

	// Read errors
	errData, err := os.ReadFile(fmt.Sprintf("%s/errors.log", outputDir))
	if err != nil {
		return analysis
	}
	errLines := strings.Split(strings.TrimSpace(string(errData)), "\n")
	if len(errLines) == 1 && errLines[0] == "" {
		errLines = nil
	}
	analysis.ErrorLines = len(errLines)

	// Classify
	for _, line := range errLines {
		lower := strings.ToLower(line)
		switch {
		case strings.Contains(lower, "unsupported workload type"):
			analysis.WorkloadTypeErrs++
		case strings.Contains(lower, "conflict") || strings.Contains(lower, "the object has been modified"):
			analysis.ConflictErrors++
		case strings.Contains(lower, "throttl") || strings.Contains(lower, "rate") && strings.Contains(lower, "limit"):
			analysis.ThrottleErrors++
		case strings.Contains(lower, "context deadline") || strings.Contains(lower, "timeout"):
			analysis.TimeoutErrors++
		case strings.Contains(lower, "not found") || strings.Contains(lower, "notfound"):
			analysis.NotFoundErrors++
		case strings.Contains(lower, "panic") || strings.Contains(lower, "runtime error"):
			analysis.PanicErrors++
		}
		if strings.Contains(lower, "reconciler error") || strings.Contains(lower, "failed to") {
			analysis.ReconcileErrors++
		}
	}

	// Sample first 5 errors
	for i, line := range errLines {
		if i >= 5 {
			break
		}
		// Truncate long lines for display
		if len(line) > 200 {
			line = line[:200] + "..."
		}
		analysis.SampleErrors = append(analysis.SampleErrors, line)
	}

	return analysis
}

const reportTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>RBG Controller Stress Test Report</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #f5f7fa; color: #333; line-height: 1.6; }
.container { max-width: 1200px; margin: 0 auto; padding: 20px; }
h1 { color: #1a1a2e; margin-bottom: 5px; }
h2 { color: #16213e; margin: 30px 0 15px; padding-bottom: 8px; border-bottom: 2px solid #e0e0e0; }
h3 { color: #0f3460; margin: 20px 0 10px; }
.header { background: linear-gradient(135deg, #1a1a2e, #16213e); color: white; padding: 30px; border-radius: 12px; margin-bottom: 30px; }
.header h1 { color: white; }
.header .meta { color: #a0aec0; font-size: 0.9em; margin-top: 8px; }
.card { background: white; border-radius: 8px; padding: 20px; margin-bottom: 20px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 20px; }
.stat-box { text-align: center; padding: 15px; background: #f8fafc; border-radius: 8px; }
.stat-box .value { font-size: 2em; font-weight: bold; color: #2563eb; }
.stat-box .label { font-size: 0.85em; color: #64748b; margin-top: 4px; }
table { width: 100%; border-collapse: collapse; margin: 10px 0; }
th, td { padding: 10px 12px; text-align: left; border-bottom: 1px solid #e2e8f0; }
th { background: #f1f5f9; font-weight: 600; color: #475569; }
tr:hover { background: #f8fafc; }
.tag { display: inline-block; padding: 2px 8px; border-radius: 4px; font-size: 0.8em; font-weight: 500; }
.tag-success { background: #dcfce7; color: #166534; }
.tag-warning { background: #fef3c7; color: #92400e; }
.tag-error { background: #fee2e2; color: #991b1b; }
.issue { padding: 10px 15px; margin: 8px 0; border-radius: 6px; border-left: 4px solid; }
.issue-info { border-color: #3b82f6; background: #eff6ff; }
.issue-warning { border-color: #f59e0b; background: #fffbeb; }
.issue-error { border-color: #ef4444; background: #fef2f2; }
.chart-container { position: relative; height: 200px; margin: 15px 0; background: #f8fafc; border-radius: 8px; padding: 10px; overflow: hidden; }
.bar { position: absolute; bottom: 30px; background: #3b82f6; border-radius: 2px 2px 0 0; min-width: 3px; transition: opacity 0.2s; }
.bar:hover { opacity: 0.8; }
.bar.error { background: #ef4444; }
.chart-label { position: absolute; bottom: 5px; left: 10px; font-size: 0.75em; color: #64748b; }
.chart-max { position: absolute; top: 5px; right: 10px; font-size: 0.75em; color: #64748b; }
.log-sample { background: #1e293b; color: #e2e8f0; padding: 15px; border-radius: 8px; font-family: 'JetBrains Mono', monospace; font-size: 0.8em; overflow-x: auto; white-space: pre-wrap; word-break: break-all; max-height: 300px; overflow-y: auto; }
.summary-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 10px; }
.summary-item { padding: 8px 12px; background: #f1f5f9; border-radius: 6px; }
.summary-item .key { font-size: 0.8em; color: #64748b; }
.summary-item .val { font-weight: 600; color: #1e293b; }
</style>
</head>
<body>
<div class="container">

<div class="header">
  <h1>RBG Controller Stress Test Report</h1>
  <div class="meta">Generated: {{.GeneratedAt}}</div>
</div>

<!-- Scenario -->
<div class="card">
  <h2>Test Scenario</h2>
  <div class="summary-grid">
    <div class="summary-item"><div class="key">Total RBGs</div><div class="val">{{.Scenario.TotalRBGs}}</div></div>
    <div class="summary-item"><div class="key">Roles/RBG</div><div class="val">{{.Scenario.RolesPerRBG}}</div></div>
    <div class="summary-item"><div class="key">LWS Roles</div><div class="val">{{.Scenario.LWSRoles}}</div></div>
    <div class="summary-item"><div class="key">LWS Size</div><div class="val">{{.Scenario.LWSSize}}</div></div>
    <div class="summary-item"><div class="key">Create QPS</div><div class="val">{{printf "%.0f" .Scenario.CreateQPS}}</div></div>
    <div class="summary-item"><div class="key">Update QPS</div><div class="val">{{printf "%.0f" .Scenario.UpdateQPS}}</div></div>
    <div class="summary-item"><div class="key">Delete QPS</div><div class="val">{{printf "%.0f" .Scenario.DeleteQPS}}</div></div>
    <div class="summary-item"><div class="key">In-Place Update</div><div class="val">{{if .Scenario.InPlaceUpdate}}Yes{{else}}No{{end}}</div></div>
    <div class="summary-item"><div class="key">KWOK Nodes</div><div class="val">{{if .Scenario.UseKwokNodes}}Yes{{else}}No{{end}}</div></div>
  </div>
</div>

<!-- Controller Configuration -->
<div class="card">
  <h2>Controller Configuration</h2>
  <table>
    <tbody>
      <tr><td><strong>Image</strong></td><td><code>{{.Controller.Image}}</code></td></tr>
      <tr><td><strong>Replicas</strong></td><td>{{.Controller.Replicas}}</td></tr>
      <tr><td><strong>CPU</strong></td><td>Request: {{.Controller.CPURequest}} / Limit: {{.Controller.CPULimit}}</td></tr>
      <tr><td><strong>Memory</strong></td><td>Request: {{.Controller.MemoryRequest}} / Limit: {{.Controller.MemoryLimit}}</td></tr>
      <tr><td><strong>Node</strong></td><td>{{.Controller.NodeName}}</td></tr>
      <tr><td><strong>Startup Args</strong></td><td><code>{{range $i, $arg := .Controller.Args}}{{if $i}} {{end}}{{$arg}}{{end}}</code></td></tr>
    </tbody>
  </table>
</div>

<!-- Issues -->
<div class="card">
  <h2>Issues & Findings</h2>
  {{range .Issues}}
  <div class="issue issue-warning">{{.}}</div>
  {{end}}
</div>

<!-- Performance Results -->
<div class="card">
  <h2>Performance Results</h2>
  <table>
    <thead>
      <tr>
        <th>Phase</th>
        <th>Total</th>
        <th>Success</th>
        <th>Failed</th>
        <th>P50 (ms)</th>
        <th>P90 (ms)</th>
        <th>P99 (ms)</th>
        <th>Max (ms)</th>
        <th>Avg (ms)</th>
        <th>Actual QPS</th>
        <th>Duration (s)</th>
      </tr>
    </thead>
    <tbody>
      {{range .Phases}}
      <tr>
        <td><strong>{{.Name}}</strong></td>
        <td>{{.Total}}</td>
        <td><span class="tag tag-success">{{.Succeeded}}</span></td>
        <td>{{if gt .Failed 0}}<span class="tag tag-error">{{.Failed}}</span>{{else}}0{{end}}</td>
        <td>{{printf "%.0f" .P50Ms}}</td>
        <td>{{printf "%.0f" .P90Ms}}</td>
        <td>{{printf "%.0f" .P99Ms}}</td>
        <td>{{printf "%.0f" .MaxMs}}</td>
        <td>{{printf "%.0f" .AvgMs}}</td>
        <td>{{printf "%.2f" .ActualQPS}}</td>
        <td>{{printf "%.2f" .TotalTimeSec}}</td>
      </tr>
      {{end}}
    </tbody>
  </table>
</div>

<!-- Per-phase latency charts -->
{{range .Phases}}
<div class="card">
  <h3>{{.Name}} — Latency Distribution</h3>
  <div class="grid">
    <div class="stat-box"><div class="value">{{printf "%.0f" .P50Ms}}ms</div><div class="label">P50</div></div>
    <div class="stat-box"><div class="value">{{printf "%.0f" .P99Ms}}ms</div><div class="label">P99</div></div>
    <div class="stat-box"><div class="value">{{printf "%.2f" .ActualQPS}}</div><div class="label">Actual QPS</div></div>
  </div>
  <div class="chart-container">
    <div class="chart-max">Max: {{printf "%.0f" .MaxMs}}ms</div>
    {{$max := .MaxMs}}{{$total := .Total}}
    {{range .Latencies}}
    {{if not .HasError}}
    <div class="bar" style="left: {{printf "%.2f" (barLeft .Index $total)}}%; height: {{printf "%.0f" (pct .LatencyMs $max)}}%; width: {{printf "%.2f" (barWidth $total)}}%;" title="{{.Name}}: {{printf "%.0f" .LatencyMs}}ms"></div>
    {{else}}
    <div class="bar error" style="left: {{printf "%.2f" (barLeft .Index $total)}}%; height: 100%; width: {{printf "%.2f" (barWidth $total)}}%;" title="{{.Name}}: ERROR"></div>
    {{end}}
    {{end}}
    <div class="chart-label">← Operations (time order) →</div>
  </div>
  {{if gt .Failed 0}}
  <h4>Errors ({{.Failed}})</h4>
  <div class="log-sample">{{range .Errors}}{{.}}
{{end}}</div>
  {{end}}
</div>
{{end}}

<!-- Controller Logs -->
<div class="card">
  <h2>Controller Log Analysis</h2>
  <div class="grid">
    <div class="stat-box"><div class="value">{{.LogAnalysis.TotalLines}}</div><div class="label">Total Log Lines</div></div>
    <div class="stat-box"><div class="value">{{.LogAnalysis.ErrorLines}}</div><div class="label">Error Lines</div></div>
    <div class="stat-box"><div class="value">{{.LogAnalysis.ReconcileErrors}}</div><div class="label">Reconcile Errors</div></div>
  </div>
  <table>
    <thead><tr><th>Error Category</th><th>Count</th><th>Severity</th></tr></thead>
    <tbody>
      <tr><td>Reconcile Errors</td><td>{{.LogAnalysis.ReconcileErrors}}</td><td>{{if gt .LogAnalysis.ReconcileErrors 0}}<span class="tag tag-warning">Warning</span>{{else}}<span class="tag tag-success">OK</span>{{end}}</td></tr>
      <tr><td>Workload Type Errors</td><td>{{.LogAnalysis.WorkloadTypeErrs}}</td><td>{{if gt .LogAnalysis.WorkloadTypeErrs 0}}<span class="tag tag-warning">Warning</span>{{else}}<span class="tag tag-success">OK</span>{{end}}</td></tr>
      <tr><td>Conflict (Optimistic Lock)</td><td>{{.LogAnalysis.ConflictErrors}}</td><td>{{if gt .LogAnalysis.ConflictErrors 10}}<span class="tag tag-error">High</span>{{else if gt .LogAnalysis.ConflictErrors 0}}<span class="tag tag-warning">Warning</span>{{else}}<span class="tag tag-success">OK</span>{{end}}</td></tr>
      <tr><td>API Throttling</td><td>{{.LogAnalysis.ThrottleErrors}}</td><td>{{if gt .LogAnalysis.ThrottleErrors 0}}<span class="tag tag-error">Critical</span>{{else}}<span class="tag tag-success">OK</span>{{end}}</td></tr>
      <tr><td>Timeout/Deadline</td><td>{{.LogAnalysis.TimeoutErrors}}</td><td>{{if gt .LogAnalysis.TimeoutErrors 0}}<span class="tag tag-warning">Warning</span>{{else}}<span class="tag tag-success">OK</span>{{end}}</td></tr>
      <tr><td>Not Found</td><td>{{.LogAnalysis.NotFoundErrors}}</td><td><span class="tag tag-success">OK</span></td></tr>
      <tr><td>Panics</td><td>{{.LogAnalysis.PanicErrors}}</td><td>{{if gt .LogAnalysis.PanicErrors 0}}<span class="tag tag-error">CRITICAL</span>{{else}}<span class="tag tag-success">OK</span>{{end}}</td></tr>
    </tbody>
  </table>
  {{if .LogAnalysis.SampleErrors}}
  <h3>Sample Errors</h3>
  <div class="log-sample">{{range .LogAnalysis.SampleErrors}}{{.}}
{{end}}</div>
  {{end}}
</div>

<!-- Profiling (pprof) -->
<div class="card">
  <h2>Profiling (pprof)</h2>
  {{if .PprofAnalysis.Available}}
  {{if .PprofAnalysis.CPUTop}}
  <h3>CPU Profile — Top Functions</h3>
  <div class="log-sample">{{.PprofAnalysis.CPUTop}}</div>
  {{end}}
  {{if .PprofAnalysis.HeapTop}}
  <h3>Heap (In-Use Memory) — Top Allocators</h3>
  <div class="log-sample">{{.PprofAnalysis.HeapTop}}</div>
  {{end}}
  {{if .PprofAnalysis.AllocsTop}}
  <h3>Cumulative Allocations — Top Functions</h3>
  <div class="log-sample">{{.PprofAnalysis.AllocsTop}}</div>
  {{end}}
  {{if .PprofAnalysis.GoroutineTop}}
  <h3>Goroutines — Top Stacks</h3>
  <div class="log-sample">{{.PprofAnalysis.GoroutineTop}}</div>
  {{end}}
  {{else}}
  <p style="color: #64748b; font-style: italic;">Pprof not available — either controller does not have pprof enabled, or <code>--pprof-addr</code> was not specified. Deploy with <code>--enable-pprof=true</code> and pass <code>--pprof-addr=localhost:6060</code> to collect profiling data.</p>
  {{end}}
</div>

<!-- Footer -->
<div style="text-align: center; color: #94a3b8; padding: 20px; font-size: 0.85em;">
  RBG Controller Stress Test • Generated by <code>/stress-test</code> skill
</div>

</div>
</body>
</html>`
