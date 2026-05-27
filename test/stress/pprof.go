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
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"
)

// PprofCollector handles pprof data collection from the controller.
type PprofCollector struct {
	addr      string
	outputDir string
}

// NewPprofCollector creates a pprof collector targeting the given address.
func NewPprofCollector(addr, outputDir string) *PprofCollector {
	return &PprofCollector{
		addr:      addr,
		outputDir: outputDir,
	}
}

// IsAvailable checks if the pprof endpoint is reachable.
func (p *PprofCollector) IsAvailable() bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/debug/pprof/", p.addr))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// CollectCPU collects a CPU profile for the given duration.
func (p *PprofCollector) CollectCPU(phase string, durationSec int) error {
	url := fmt.Sprintf("http://%s/debug/pprof/profile?seconds=%d", p.addr, durationSec)
	outFile := fmt.Sprintf("%s/cpu-%s.prof", p.outputDir, phase)
	return p.fetchProfile(url, outFile, time.Duration(durationSec+10)*time.Second)
}

// CollectHeap collects a heap profile.
func (p *PprofCollector) CollectHeap(phase string) error {
	url := fmt.Sprintf("http://%s/debug/pprof/heap", p.addr)
	outFile := fmt.Sprintf("%s/heap-%s.prof", p.outputDir, phase)
	return p.fetchProfile(url, outFile, 10*time.Second)
}

// CollectAllocs collects an allocs profile.
func (p *PprofCollector) CollectAllocs(phase string) error {
	url := fmt.Sprintf("http://%s/debug/pprof/allocs", p.addr)
	outFile := fmt.Sprintf("%s/allocs-%s.prof", p.outputDir, phase)
	return p.fetchProfile(url, outFile, 10*time.Second)
}

// CollectGoroutine collects a goroutine profile.
func (p *PprofCollector) CollectGoroutine(phase string) error {
	url := fmt.Sprintf("http://%s/debug/pprof/goroutine", p.addr)
	outFile := fmt.Sprintf("%s/goroutine-%s.prof", p.outputDir, phase)
	return p.fetchProfile(url, outFile, 10*time.Second)
}

// CollectAll collects all profile types for a given phase (post-phase snapshot).
// CPU profile blocks for cpuDurationSec seconds.
func (p *PprofCollector) CollectAll(phase string, cpuDurationSec int) {
	log.Printf("[pprof] Collecting profiles for phase=%s (CPU %ds)...", phase, cpuDurationSec)

	if err := p.CollectHeap(phase); err != nil {
		log.Printf("[pprof] heap collection failed: %v", err)
	}
	if err := p.CollectAllocs(phase); err != nil {
		log.Printf("[pprof] allocs collection failed: %v", err)
	}
	if err := p.CollectGoroutine(phase); err != nil {
		log.Printf("[pprof] goroutine collection failed: %v", err)
	}
	if err := p.CollectCPU(phase, cpuDurationSec); err != nil {
		log.Printf("[pprof] CPU collection failed: %v", err)
	}

	// Generate top text reports
	p.generateTopReport(phase)
}

// StartCPUProfile starts a CPU profile collection that runs for the given duration.
// It returns immediately. The profile is collected in the background.
// Call WaitCPUProfile to wait for completion.
func (p *PprofCollector) StartCPUProfile(phase string, durationSec int) chan error {
	ch := make(chan error, 1)
	go func() {
		err := p.CollectCPU(phase, durationSec)
		ch <- err
	}()
	return ch
}

// CollectPostPhase collects heap, allocs, goroutine snapshots (instant) after a phase.
// Also generates top text reports for all available profiles.
func (p *PprofCollector) CollectPostPhase(phase string) {
	log.Printf("[pprof] Collecting post-phase snapshots for %s...", phase)

	if err := p.CollectHeap(phase); err != nil {
		log.Printf("[pprof] heap collection failed: %v", err)
	}
	if err := p.CollectAllocs(phase); err != nil {
		log.Printf("[pprof] allocs collection failed: %v", err)
	}
	if err := p.CollectGoroutine(phase); err != nil {
		log.Printf("[pprof] goroutine collection failed: %v", err)
	}

	// Generate top text reports
	p.generateTopReport(phase)
}

// generateTopReport generates human-readable top reports using go tool pprof.
func (p *PprofCollector) generateTopReport(phase string) {
	profiles := []struct {
		name string
		file string
	}{
		{"cpu", fmt.Sprintf("%s/cpu-%s.prof", p.outputDir, phase)},
		{"heap", fmt.Sprintf("%s/heap-%s.prof", p.outputDir, phase)},
		{"allocs", fmt.Sprintf("%s/allocs-%s.prof", p.outputDir, phase)},
		{"goroutine", fmt.Sprintf("%s/goroutine-%s.prof", p.outputDir, phase)},
	}

	for _, prof := range profiles {
		if _, err := os.Stat(prof.file); err != nil {
			continue
		}
		outFile := fmt.Sprintf("%s/%s-%s-top.txt", p.outputDir, prof.name, phase)
		cmd := exec.Command("go", "tool", "pprof", "-top", "-nodecount=30", prof.file)
		output, err := cmd.Output()
		if err != nil {
			log.Printf("[pprof] Failed to generate top report for %s: %v", prof.name, err)
			continue
		}
		if err := os.WriteFile(outFile, output, 0o644); err != nil {
			log.Printf("[pprof] Failed to write top report for %s: %v", prof.name, err)
		}
	}
}

func (p *PprofCollector) fetchProfile(url, outFile string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: status %d", url, resp.StatusCode)
	}

	f, err := os.Create(outFile)
	if err != nil {
		return fmt.Errorf("create %s: %w", outFile, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write %s: %w", outFile, err)
	}

	return nil
}
