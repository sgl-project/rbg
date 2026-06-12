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
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	scenario := &Scenario{}

	flag.StringVar(&scenario.Kubeconfig, "kubeconfig", "", "Path to kubeconfig (default: in-cluster or ~/.kube/config)")
	flag.StringVar(&scenario.Namespace, "namespace", "stress-test", "Namespace to run stress test in")
	flag.IntVar(&scenario.TotalRBGs, "total-rbgs", 10, "Total number of RBG instances to create")
	flag.IntVar(&scenario.RolesPerRBG, "roles-per-rbg", 2, "Number of roles per RBG")
	flag.IntVar(&scenario.LWSRoles, "lws-roles", 0, "Number of roles using leaderWorkerPattern")
	flag.IntVar(&scenario.LWSSize, "lws-size", 4, "Size (total pods) for each LWS role instance")
	flag.Float64Var(&scenario.CreateQPS, "create-qps", 5, "RBG creation rate (per second)")
	flag.Float64Var(&scenario.UpdateQPS, "update-qps", 5, "RBG update rate (per second)")
	flag.Float64Var(&scenario.DeleteQPS, "delete-qps", 5, "RBG deletion rate (per second)")
	flag.BoolVar(&scenario.InPlaceUpdate, "in-place-update", false, "Use InPlaceIfPossible strategy for updates")
	flag.BoolVar(&scenario.UseKwokNodes, "use-kwok-nodes", true, "Schedule pods to kwok fake nodes (adds toleration + nodeSelector)")
	flag.StringVar(&scenario.PprofAddr, "pprof-addr", "", "Controller pprof address (e.g. localhost:6060). Empty to skip profiling.")
	flag.StringVar(&scenario.OutputDir, "output-dir", "/tmp/rbg-stress-results", "Directory for output files")
	flag.DurationVar(&scenario.Timeout, "timeout", 30*time.Minute, "Overall timeout for the stress test")

	flag.Parse()

	if err := scenario.Validate(); err != nil {
		log.Fatalf("Invalid scenario: %v", err)
	}

	// Setup output directory
	if err := os.MkdirAll(scenario.OutputDir, 0o755); err != nil {
		log.Fatalf("Failed to create output dir: %v", err)
	}

	// Setup context with timeout and signal handling
	ctx, cancel := context.WithTimeout(context.Background(), scenario.Timeout)
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Received shutdown signal, canceling...")
		cancel()
	}()

	// Create kubernetes client
	client, err := NewStressClient(scenario.Kubeconfig)
	if err != nil {
		log.Fatalf("Failed to create kubernetes client: %v", err)
	}

	// Ensure namespace exists
	if err := client.EnsureNamespace(ctx, scenario.Namespace); err != nil {
		log.Fatalf("Failed to ensure namespace: %v", err)
	}

	// Initialize timing recorder
	recorder := NewTimingRecorder()

	// Collect controller info
	scenario.ControllerInfo = client.GetControllerInfo(ctx)

	// Record test start time for log collection
	testStartTime := time.Now().UTC()

	fmt.Println("=== RBG Controller Stress Test ===")
	fmt.Printf("Controller: %s (replicas=%d)\n", scenario.ControllerInfo.Image, scenario.ControllerInfo.Replicas)
	fmt.Printf("Resources: CPU=%s/%s, Memory=%s/%s\n",
		scenario.ControllerInfo.CPURequest, scenario.ControllerInfo.CPULimit,
		scenario.ControllerInfo.MemoryRequest, scenario.ControllerInfo.MemoryLimit)
	fmt.Printf("Scenario: %d RBGs, %d roles/RBG (%d LWS), QPS: create=%.0f update=%.0f delete=%.0f\n",
		scenario.TotalRBGs, scenario.RolesPerRBG, scenario.LWSRoles,
		scenario.CreateQPS, scenario.UpdateQPS, scenario.DeleteQPS)
	fmt.Printf("Output: %s\n\n", scenario.OutputDir)

	// Setup pprof collector if address provided
	var pprofCollector *PprofCollector
	if scenario.PprofAddr != "" {
		pprofCollector = NewPprofCollector(scenario.PprofAddr, scenario.OutputDir)
		if pprofCollector.IsAvailable() {
			fmt.Printf("Pprof: connected to %s\n\n", scenario.PprofAddr)
		} else {
			fmt.Printf("Pprof: endpoint %s not reachable, profiling disabled\n\n", scenario.PprofAddr)
			pprofCollector = nil
		}
	}

	// Phase 1: Create
	fmt.Println("--- Phase 1: Create ---")
	var cpuCh chan error
	if pprofCollector != nil {
		// Start CPU profile during the phase (30s sample)
		cpuCh = pprofCollector.StartCPUProfile("create", 30)
	}
	startTime := time.Now()
	if err := RunCreatePhase(ctx, client, scenario, recorder); err != nil {
		log.Printf("Create phase error: %v", err)
	}
	fmt.Printf("Create phase completed in %v\n", time.Since(startTime))
	if pprofCollector != nil {
		if cpuCh != nil {
			<-cpuCh // wait for CPU profile to finish
		}
		pprofCollector.CollectPostPhase("create")
	}
	fmt.Println()

	// Phase 2: Update
	fmt.Println("--- Phase 2: Update ---")
	if pprofCollector != nil {
		cpuCh = pprofCollector.StartCPUProfile("update", 30)
	}
	startTime = time.Now()
	if err := RunUpdatePhase(ctx, client, scenario, recorder); err != nil {
		log.Printf("Update phase error: %v", err)
	}
	fmt.Printf("Update phase completed in %v\n", time.Since(startTime))
	if pprofCollector != nil {
		if cpuCh != nil {
			<-cpuCh
		}
		pprofCollector.CollectPostPhase("update")
	}
	fmt.Println()

	// Phase 3: Delete
	fmt.Println("--- Phase 3: Delete ---")
	if pprofCollector != nil {
		cpuCh = pprofCollector.StartCPUProfile("delete", 30)
	}
	startTime = time.Now()
	if err := RunDeletePhase(ctx, client, scenario, recorder); err != nil {
		log.Printf("Delete phase error: %v", err)
	}
	fmt.Printf("Delete phase completed in %v\n", time.Since(startTime))
	if pprofCollector != nil {
		if cpuCh != nil {
			<-cpuCh
		}
		pprofCollector.CollectPostPhase("delete")
	}
	fmt.Println()

	// Generate timing report
	fmt.Println("--- Generating Report ---")
	if err := recorder.WriteResults(scenario.OutputDir); err != nil {
		log.Printf("Failed to write results: %v", err)
	}

	summary := recorder.Summary()
	fmt.Println(summary)

	// Write summary to file
	if err := os.WriteFile(fmt.Sprintf("%s/summary.json", scenario.OutputDir), []byte(summary), 0o644); err != nil {
		log.Printf("Failed to write summary: %v", err)
	}

	// Collect controller logs (only from test period)
	fmt.Println("--- Collecting Controller Logs ---")
	collectControllerLogs(scenario.OutputDir, testStartTime)

	// Parse controller logs for analysis
	logAnalysis := ParseLogAnalysis(scenario.OutputDir)

	// Generate HTML report
	fmt.Println("--- Generating HTML Report ---")
	if err := GenerateHTMLReport(scenario.OutputDir, scenario, recorder, logAnalysis); err != nil {
		log.Printf("Failed to generate HTML report: %v", err)
	} else {
		fmt.Printf("HTML report: %s/report.html\n", scenario.OutputDir)
	}

	fmt.Println("\n=== Stress test complete ===")
}

// collectControllerLogs fetches controller logs via kubectl.
func collectControllerLogs(outputDir string, sinceTime time.Time) {
	// Fetch logs from test start time
	cmd := exec.Command("kubectl", "logs", "-n", "rbgs-system",
		"-l", "control-plane=rbgs-controller",
		"--since-time="+sinceTime.Format(time.RFC3339), "--tail=-1")
	output, err := cmd.Output()
	if err != nil {
		log.Printf("Failed to collect controller logs: %v", err)
		return
	}

	logFile := fmt.Sprintf("%s/controller-full.log", outputDir)
	if err := os.WriteFile(logFile, output, 0o644); err != nil {
		log.Printf("Failed to write controller logs: %v", err)
		return
	}

	// Extract error lines
	lines := strings.Split(string(output), "\n")
	var errorLines []string
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "panic") {
			errorLines = append(errorLines, line)
		}
	}

	errFile := fmt.Sprintf("%s/errors.log", outputDir)
	if err := os.WriteFile(errFile, []byte(strings.Join(errorLines, "\n")), 0o644); err != nil {
		log.Printf("Failed to write error logs: %v", err)
		return
	}

	log.Printf("Collected %d total log lines, %d error lines", len(lines), len(errorLines))
}
