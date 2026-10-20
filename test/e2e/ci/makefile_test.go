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
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fakeGoScript stands in for go on PATH while make runs. It records the argv of
// every `go test` call, one NUL-separated file per call, and hands any other
// subcommand (the Makefile runs `go env` and `go list` while it is parsed) to
// the real toolchain.
const fakeGoScript = `#!/bin/sh
if [ "$1" = "test" ]; then
	printf '%s\0' "$@" > "$RBG_FAKE_GO_RECORD_DIR/go-test.$$"
	exit 0
fi
exec "$RBG_REAL_GO" "$@"
`

type makeTestE2ECase struct {
	name     string
	makeVars []string
	filter   string
	timeout  string
	failFast string
}

// TestMakeTestE2EForwardsFlagsIntact runs the real `make test-e2e` recipe
// against a fake go binary and checks the argv go receives. Release Test runs
// the target with its defaults, so a quoting slip there splits the shell
// command at the `&&` inside the label filter: go then runs a truncated filter
// under its default 10m timeout, and the rest of the line fails as a command.
func TestMakeTestE2EForwardsFlagsIntact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake go binary is a POSIX shell script")
	}
	for _, tool := range []string{"make", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is required to run the Makefile recipe: %v", tool, err)
		}
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("locate go: %v", err)
	}
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}

	filters := matrixFilters(t, repoRoot)
	cases := make([]makeTestE2ECase, 0, len(filters)+1)
	// The defaults are what release-test.yml and doc/dev/how_to_develop.md use,
	// and they must select the suite the matrix coverage check compares against.
	cases = append(cases, makeTestE2ECase{
		name:     "defaults",
		filter:   legacyDefaultFilter,
		timeout:  "45m",
		failFast: "true",
	})
	// e2e-test.yml overrides every setting on the make command line.
	for _, group := range slices.Sorted(maps.Keys(filters)) {
		cases = append(cases, makeTestE2ECase{
			name: "matrix/" + group,
			makeVars: []string{
				"E2E_LABEL_FILTER=" + filters[group],
				"E2E_TIMEOUT=30m",
				"E2E_FAIL_FAST=false",
			},
			filter:   filters[group],
			timeout:  "30m",
			failFast: "false",
		})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv, output, err := runMakeTestE2E(t, repoRoot, realGo, tc.makeVars)
			if err != nil {
				t.Errorf("make test-e2e %s: %v\n%s", strings.Join(tc.makeVars, " "), err, output)
			}
			for _, want := range []string{
				"./test/e2e/",
				"--ginkgo.label-filter=" + tc.filter,
				"--ginkgo.fail-fast=" + tc.failFast,
				"--ginkgo.junit-report=artifacts/junit.xml",
				"--ginkgo.json-report=artifacts/report.json",
			} {
				if !slices.Contains(argv, want) {
					t.Errorf("go test did not receive %q as one argument\nargv: %q", want, argv)
				}
			}
			if got := flagValue(argv, "-timeout"); got != tc.timeout {
				t.Errorf("go test -timeout = %q, want %q\nargv: %q", got, tc.timeout, argv)
			}
		})
	}
}

// runMakeTestE2E runs `make test-e2e` from the repository root with the fake go
// first on PATH. It returns the argv of the single `go test` call together with
// make's output and exit error.
func runMakeTestE2E(t *testing.T, repoRoot, realGo string, makeVars []string) ([]string, []byte, error) {
	t.Helper()

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "go"), []byte(fakeGoScript), 0o755); err != nil {
		t.Fatalf("write fake go: %v", err)
	}
	recordDir := t.TempDir()

	cmd := exec.Command("make", append([]string{"test-e2e"}, makeVars...)...)
	cmd.Dir = repoRoot
	cmd.Env = append(makeTestE2EEnv(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"RBG_FAKE_GO_RECORD_DIR="+recordDir,
		"RBG_REAL_GO="+realGo,
	)
	output, runErr := cmd.CombinedOutput()

	records, err := filepath.Glob(filepath.Join(recordDir, "go-test.*"))
	if err != nil {
		t.Fatalf("list recorded go test calls: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("make test-e2e ran go test %d times, want 1 (make error: %v)\n%s", len(records), runErr, output)
	}
	data, err := os.ReadFile(records[0])
	if err != nil {
		t.Fatalf("read recorded go test call: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00"), output, runErr
}

// makeTestE2EEnv returns the current environment minus anything that would make
// the child make differ from a plain `make test-e2e`: E2E_* variables replace the
// ?= defaults under test, and MAKEFLAGS and friends carry a parent make's
// command-line variables and flags (such as -n) into the child.
func makeTestE2EEnv() []string {
	environ := os.Environ()
	env := make([]string, 0, len(environ)+3)
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		if key == "PATH" || key == "MFLAGS" || key == "GNUMAKEFLAGS" ||
			strings.HasPrefix(key, "E2E_") || strings.HasPrefix(key, "MAKE") {
			continue
		}
		env = append(env, entry)
	}
	return env
}

// flagValue returns the value passed for flag in either the `-flag value` or the
// `-flag=value` form, or "" when the flag is absent.
func flagValue(argv []string, flag string) string {
	for i, arg := range argv {
		if arg == flag && i+1 < len(argv) {
			return argv[i+1]
		}
		if value, ok := strings.CutPrefix(arg, flag+"="); ok {
			return value
		}
	}
	return ""
}
