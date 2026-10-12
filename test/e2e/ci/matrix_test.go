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

package ci

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const legacyDefaultFilter = "!volcano && !scheduler-plugins"

type workflowFile struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Strategy workflowStrategy `yaml:"strategy"`
}

type workflowStrategy struct {
	Matrix workflowMatrix `yaml:"matrix"`
}

type workflowMatrix struct {
	Include []map[string]any `yaml:"include"`
}

type ginkgoFileReport struct {
	PreRunStats struct {
		TotalSpecs       int `json:"TotalSpecs"`
		SpecsThatWillRun int `json:"SpecsThatWillRun"`
	} `json:"PreRunStats"`
	SpecReports []ginkgoSpecReport `json:"SpecReports"`
}

type ginkgoSpecReport struct {
	ContainerHierarchyTexts []string `json:"ContainerHierarchyTexts"`
	LeafNodeType            string   `json:"LeafNodeType"`
	LeafNodeLocation        struct {
		FileName   string `json:"FileName"`
		LineNumber int    `json:"LineNumber"`
	} `json:"LeafNodeLocation"`
	LeafNodeText string `json:"LeafNodeText"`
	State        string `json:"State"`
}

// TestMatrixSlicesCoverLegacyDefaultSuite prevents a spec from silently losing
// coverage when a routing label is missing.  It uses Ginkgo's real label-filter
// engine in dry-run mode, so the check requires no cluster.
func TestMatrixSlicesCoverLegacyDefaultSuite(t *testing.T) {
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}

	filters := matrixFilters(t, repoRoot)
	workDir := t.TempDir()
	e2eBinary := buildE2ETestBinary(t, repoRoot, workDir)

	defaultReport := filepath.Join(workDir, "legacy-default-report.json")
	defaultSpecs := runE2EDryRun(t, e2eBinary, repoRoot, legacyDefaultFilter, defaultReport)
	if len(defaultSpecs) == 0 {
		t.Fatal("legacy default filter selected no specs")
	}

	union := make(map[string]struct{})
	for group, filter := range filters {
		report := filepath.Join(workDir, fmt.Sprintf("matrix-%s-report.json", group))
		specs := runE2EDryRun(t, e2eBinary, repoRoot, filter, report)
		if len(specs) == 0 {
			t.Errorf("matrix slice %q selected no specs", group)
		}
		t.Logf("matrix slice %q selected %d specs", group, len(specs))
		for id := range specs {
			union[id] = struct{}{}
		}
	}

	missing := diffKeys(defaultSpecs, union)
	extra := diffKeys(union, defaultSpecs)
	if len(missing) != 0 || len(extra) != 0 {
		sort.Strings(missing)
		sort.Strings(extra)
		t.Errorf(
			"matrix filters do not cover the legacy default suite (legacy=%d, union=%d):\nmissing specs:\n%s\nunexpected specs:\n%s",
			len(defaultSpecs), len(union), strings.Join(missing, "\n"), strings.Join(extra, "\n"),
		)
	} else {
		t.Logf("matrix union covers all %d legacy default-suite specs", len(defaultSpecs))
	}
}

func matrixFilters(t *testing.T, repoRoot string) map[string]string {
	t.Helper()

	path := filepath.Join(repoRoot, ".github", "workflows", "e2e-test.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read e2e workflow: %v", err)
	}

	var workflow workflowFile
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse e2e workflow: %v", err)
	}

	job, ok := workflow.Jobs["e2e-matrix"]
	if !ok {
		t.Fatal("e2e-matrix job not found")
	}

	filters := make(map[string]string)
	for _, entry := range job.Strategy.Matrix.Include {
		group, ok := entry["group"].(string)
		if !ok || group == "" {
			t.Fatalf("matrix entry has no group: %#v", entry)
		}
		filter, ok := entry["filter"].(string)
		if !ok || strings.TrimSpace(filter) == "" {
			t.Fatalf("matrix entry %q has no filter: %#v", group, entry)
		}
		if _, exists := filters[group]; exists {
			t.Fatalf("duplicate matrix group %q", group)
		}
		filters[group] = filter
	}
	if len(filters) == 0 {
		t.Fatal("e2e-matrix job has no entries")
	}
	return filters
}

func buildE2ETestBinary(t *testing.T, repoRoot, workDir string) string {
	t.Helper()

	binaryPath := filepath.Join(workDir, "e2e.test")
	cacheDir := filepath.Join(workDir, "go-build")
	cmd := exec.Command("go", "test", "-c", "-o", binaryPath, "./test/e2e")
	cmd.Dir = repoRoot
	cmd.Env = goCommandEnv(cacheDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build e2e test binary: %v\n%s", err, output)
	}
	return binaryPath
}

func runE2EDryRun(t *testing.T, e2eBinary, repoRoot, filter, reportPath string) map[string]struct{} {
	t.Helper()

	cmd := exec.Command(
		e2eBinary,
		"-test.run", "^TestE2E$",
		"--ginkgo.dry-run",
		"--ginkgo.fail-fast=false",
		"--ginkgo.label-filter="+filter,
		"--ginkgo.json-report="+reportPath,
	)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run e2e suite with filter %q: %v\n%s", filter, err, output)
	}

	specs := parseGinkgoReport(t, reportPath, filter)
	if len(specs) == 0 {
		t.Fatalf("dry-run report for filter %q contains no runnable specs\n%s", filter, output)
	}
	return specs
}

func parseGinkgoReport(t *testing.T, path, filter string) map[string]struct{} {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Ginkgo report for filter %q: %v", filter, err)
	}

	var reports []ginkgoFileReport
	if err := json.Unmarshal(data, &reports); err != nil {
		t.Fatalf("parse Ginkgo report for filter %q: %v", filter, err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected one suite report for filter %q, got %d", filter, len(reports))
	}

	report := reports[0]
	specs := make(map[string]struct{})
	for _, spec := range report.SpecReports {
		if spec.LeafNodeType != "It" {
			continue
		}
		if spec.State == "passed" {
			specs[specID(spec)] = struct{}{}
		}
	}
	if report.PreRunStats.SpecsThatWillRun != len(specs) {
		t.Fatalf(
			"filter %q reports %d runnable specs, but parsed %d It specs",
			filter, report.PreRunStats.SpecsThatWillRun, len(specs),
		)
	}
	return specs
}

func specID(spec ginkgoSpecReport) string {
	texts := append([]string{}, spec.ContainerHierarchyTexts...)
	texts = append(texts, spec.LeafNodeText)
	location := fmt.Sprintf("%s:%d", spec.LeafNodeLocation.FileName, spec.LeafNodeLocation.LineNumber)
	return location + " " + strings.Join(texts, " / ")
}

func diffKeys(from, to map[string]struct{}) []string {
	var keys []string
	for key := range from {
		if _, exists := to[key]; !exists {
			keys = append(keys, key)
		}
	}
	return keys
}

func goCommandEnv(cacheDir string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "GOTOOLCHAIN" || key == "GOCACHE" {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "GOTOOLCHAIN=local", "GOCACHE="+cacheDir)
}
