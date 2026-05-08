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

package search

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/rbgs/pkg/autobenchmark/config"
	abtypes "sigs.k8s.io/rbgs/pkg/autobenchmark/types"
)

const (
	defaultBridgePath = "/tools/optuna_bridge.py"
	envBridgePath     = "OPTUNA_BRIDGE_PATH"
	envStoragePath    = "OPTUNA_STORAGE_PATH"
	bridgeTimeout     = 30 * time.Second
)

// optunaSamplers lists all algorithm names backed by OptunaSearch.
var optunaSamplers = []string{"tpe", "gp", "cmaes", "random", "qmc", "nsgaii", "nsgaiii"}

func init() {
	for _, name := range optunaSamplers {
		n := name
		Register(n, func() SearchAlgorithm { return &OptunaSearch{} })
	}
}

// OptunaSearch implements SearchAlgorithm using a Python Optuna subprocess.
// Communication happens via JSON Lines over stdin/stdout.
type OptunaSearch struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	mu      sync.Mutex
	logger  logr.Logger

	studyName   string
	algorithm   string // Optuna sampler name (tpe, gp, cmaes, etc.)
	maxTrials   int
	spaceSize   int         // Cartesian product of all param values
	toldCount   int         // number of history entries already told to Optuna
	lastTrialID int         // Optuna trial ID from last ask, -1 if none
	rawSpace    SearchSpace // raw param definitions with type metadata (for pow2 decoding)
}

// distributionDescriptor describes an Optuna distribution for the Python bridge.
type distributionDescriptor struct {
	Type    string  `json:"type"`              // "float", "int", "categorical"
	Low     float64 `json:"low,omitempty"`     // min value (for float/int)
	High    float64 `json:"high,omitempty"`    // max value (for float/int)
	Step    float64 `json:"step,omitempty"`    // step value (for discrete float/int)
	Log     bool    `json:"log,omitempty"`     // log-uniform (for float)
	Choices []any   `json:"choices,omitempty"` // for categorical
}

// bridgeRequest is the JSON message sent to the Python bridge.
type bridgeRequest struct {
	Action        string                    `json:"action"`
	StudyName     string                    `json:"study_name,omitempty"`
	SearchSpace   map[string]map[string]any `json:"search_space,omitempty"`
	Direction     string                    `json:"direction,omitempty"`
	MaxTrials     int                       `json:"max_trials,omitempty"`
	StoragePath   string                    `json:"storage_path,omitempty"`
	Seed          *int                      `json:"seed,omitempty"`
	Sampler       string                    `json:"sampler,omitempty"`
	SamplerKwargs map[string]any            `json:"sampler_kwargs,omitempty"`
	TrialID       int                       `json:"trial_id"`
	Score         float64                   `json:"score"`
	Constraints   []float64                 `json:"constraints"`
	Error         string                    `json:"error,omitempty"`
}

// bridgeResponse is the JSON message received from the Python bridge.
type bridgeResponse struct {
	Status        string                    `json:"status"`
	Message       string                    `json:"message,omitempty"`
	TrialID       int                       `json:"trial_id,omitempty"`
	Params        map[string]map[string]any `json:"params,omitempty"`
	Done          bool                      `json:"done,omitempty"`
	Score         float64                   `json:"score,omitempty"`
	ExistingTotal int                       `json:"existing_total,omitempty"`
	SpaceSize     int                       `json:"space_size,omitempty"`
	Completed     int                       `json:"completed,omitempty"`
	Skipped       bool                      `json:"skipped,omitempty"`
}

// Name returns the algorithm name.
func (o *OptunaSearch) Name() string { return o.algorithm }

// Init initializes (or reinitializes) the Optuna study for a new template.
// The Python bridge subprocess is started on the first call and reused for
// subsequent calls, since it can manage multiple studies by study_name.
func (o *OptunaSearch) Init(ctx context.Context, name string, rawSpace SearchSpace, cfg config.StrategySpec) error {
	o.logger = log.FromContext(ctx).WithValues("study", name, "algorithm", cfg.Algorithm)
	o.studyName = name
	o.algorithm = cfg.Algorithm
	o.maxTrials = cfg.MaxTrialsPerTemplate
	o.rawSpace = rawSpace
	o.lastTrialID = -1
	o.toldCount = 0

	// Start the bridge process only if not already running.
	if o.cmd == nil {
		if err := o.startProcess(); err != nil {
			return fmt.Errorf("starting optuna bridge: %w", err)
		}
	}

	// Build distribution descriptors from raw search space.
	// Falls back to ExpandedSearchSpace (all categorical) when rawSpace is empty.
	searchSpace := make(map[string]map[string]any)
	for role, params := range rawSpace {
		searchSpace[role] = make(map[string]any)
		for paramName, param := range params {
			switch param.Type {
			case config.ParamTypeCategorical:
				searchSpace[role][paramName] = distributionDescriptor{
					Type:    config.ParamTypeCategorical,
					Choices: param.Values,
				}
			case config.ParamTypeFloat:
				dd := distributionDescriptor{Type: config.ParamTypeFloat, Low: *param.Min, High: *param.Max}
				if param.Step != nil {
					dd.Step = *param.Step
				}
				if param.Log {
					dd.Log = true
				}
				searchSpace[role][paramName] = dd
			case config.ParamTypeInt:
				dd := distributionDescriptor{Type: config.ParamTypeInt, Low: *param.Min, High: *param.Max}
				if param.Step != nil {
					dd.Step = *param.Step
				}
				if param.Log {
					dd.Log = true
				}
				searchSpace[role][paramName] = dd
			case config.ParamTypePow2:
				// Encode to log2 range for the bridge; bridge sees plain IntDistribution.
				dd := distributionDescriptor{
					Type: config.ParamTypeInt,
					Low:  float64(log2Int(int(*param.Min))),
					High: float64(log2Int(int(*param.Max))),
				}
				searchSpace[role][paramName] = dd
			}
		}
	}

	resp, err := o.send(bridgeRequest{
		Action:        "init",
		StudyName:     name,
		SearchSpace:   searchSpace,
		Direction:     "maximize",
		MaxTrials:     cfg.MaxTrialsPerTemplate,
		StoragePath:   resolveStoragePath(cfg),
		Seed:          cfg.Seed,
		Sampler:       cfg.Algorithm,
		SamplerKwargs: cfg.AlgorithmConfig,
	})
	if err != nil {
		return fmt.Errorf("optuna init: %w", err)
	}
	if resp.Status != "ok" {
		return fmt.Errorf("optuna init failed: %s", resp.Message)
	}

	// Sync state from Optuna — accounts for trials from previous runs.
	// ExistingTotal counts all non-RUNNING trials (COMPLETE + FAIL), matching
	// the length of the history slice we will receive after a restart.
	o.toldCount = resp.ExistingTotal
	o.spaceSize = resp.SpaceSize
	o.logger.Info("OptunaSearch initialized", "existingTotal", resp.ExistingTotal, "spaceSize", resp.SpaceSize)
	return nil
}

// SuggestNext tells Optuna about new trial results and asks for the next suggestion.
func (o *OptunaSearch) SuggestNext(history []abtypes.TrialResult) (abtypes.RoleParamSet, error) {
	// Tell Optuna about the last trial result (if any).
	// After a restart, history contains all previous trials (COMPLETE+FAIL).
	// We always tell the most recent history entry that hasn't been told yet.
	if o.lastTrialID >= 0 && len(history) > o.toldCount {
		result := history[len(history)-1]
		req := bridgeRequest{
			Action:      "tell",
			StudyName:   o.studyName,
			TrialID:     o.lastTrialID,
			Score:       result.Score,
			Constraints: result.Constraints,
			Error:       result.Error,
		}
		resp, err := o.send(req)
		if err != nil {
			return nil, fmt.Errorf("optuna tell: %w", err)
		}
		if resp.Status != "ok" {
			return nil, fmt.Errorf("optuna tell failed: %s", resp.Message)
		}
		o.toldCount = len(history)
		o.lastTrialID = -1
	}

	// Ask for the next suggestion.
	resp, err := o.send(bridgeRequest{
		Action:    "ask",
		StudyName: o.studyName,
	})
	if err != nil {
		return nil, fmt.Errorf("optuna ask: %w", err)
	}
	if resp.Status != "ok" {
		return nil, fmt.Errorf("optuna ask failed: %s", resp.Message)
	}

	o.lastTrialID = resp.TrialID

	// Convert params to RoleParamSet.
	rps := make(abtypes.RoleParamSet)
	for role, params := range resp.Params {
		rps[role] = make(abtypes.ParamSet)
		for k, v := range params {
			rps[role][k] = v
		}
	}

	// Decode pow2 parameters: the bridge returns log2 exponents,
	// convert back to actual power-of-2 values.
	for role, roleParams := range o.rawSpace {
		for name, rawParam := range roleParams {
			if rawParam.Type == "pow2" {
				if ps, ok := rps[role]; ok {
					if v, ok := ps[name]; ok {
						var exp int
						switch val := v.(type) {
						case float64:
							exp = int(val)
						case int:
							exp = val
						case int64:
							exp = int(val)
						case json.Number:
							n, _ := val.Int64()
							exp = int(n)
						}
						ps[name] = 1 << exp
					}
				}
			}
		}
	}

	return rps, nil
}

// IsDone returns true when the search should stop. This happens when:
//   - history length reaches maxTrials, or
//   - history length reaches the search space Cartesian product size (exhausted).
func (o *OptunaSearch) IsDone(history []abtypes.TrialResult) bool {
	n := len(history)
	if n >= o.maxTrials {
		return true
	}
	if o.spaceSize > 0 && n >= o.spaceSize {
		return true
	}
	return false
}

// MarshalState serializes a minimal state marker for checkpoint.
// Actual Optuna state is persisted in SQLite; toldCount is synced from Optuna on Init.
func (o *OptunaSearch) MarshalState() ([]byte, error) {
	return json.Marshal(map[string]string{"algorithm": o.algorithm})
}

// UnmarshalState is a no-op for OptunaSearch.
// State is recovered from Optuna's SQLite storage on Init.
func (o *OptunaSearch) UnmarshalState(_ []byte) error {
	return nil
}

// Close gracefully shuts down the Python bridge subprocess.
func (o *OptunaSearch) Close() error {
	if o.cmd == nil || o.cmd.Process == nil {
		return nil
	}
	_, _ = o.send(bridgeRequest{Action: "shutdown"})
	_ = o.stdin.Close()
	return o.cmd.Wait()
}

// startProcess launches the Python bridge subprocess.
func (o *OptunaSearch) startProcess() error {
	bridgePath := os.Getenv(envBridgePath)
	if bridgePath == "" {
		bridgePath = defaultBridgePath
	}

	// Security: validate the bridge path before executing it.
	if !filepath.IsAbs(bridgePath) {
		return fmt.Errorf("optuna bridge path must be absolute: %s", bridgePath)
	}
	info, err := os.Stat(bridgePath)
	if err != nil {
		return fmt.Errorf("optuna bridge path not accessible: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("optuna bridge path is not a regular file: %s", bridgePath)
	}

	o.cmd = exec.Command("python3", bridgePath)
	o.cmd.Stderr = os.Stderr // bridge logs go to controller stderr

	stdin, err := o.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating stdin pipe: %w", err)
	}
	o.stdin = stdin

	stdout, err := o.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}
	o.scanner = bufio.NewScanner(stdout)
	o.scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer

	if err := o.cmd.Start(); err != nil {
		return fmt.Errorf("starting bridge process: %w", err)
	}

	o.logger.Info("Optuna bridge process started", "pid", o.cmd.Process.Pid)
	return nil
}

// send writes a JSON request to the bridge and reads the JSON response.
// A timeout guards against hangs if the Python process crashes or stalls.
// On timeout, the bridge process is killed to avoid leaving a goroutine
// blocked on o.scanner (bufio.Scanner is NOT safe for concurrent use).
func (o *OptunaSearch) send(req bridgeRequest) (*bridgeResponse, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if _, err := o.stdin.Write(append(data, '\n')); err != nil {
		return nil, fmt.Errorf("write to bridge: %w (process may have exited)", err)
	}

	type result struct {
		resp *bridgeResponse
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		if !o.scanner.Scan() {
			if err := o.scanner.Err(); err != nil {
				ch <- result{err: fmt.Errorf("read from bridge: %w", err)}
				return
			}
			ch <- result{err: fmt.Errorf("bridge process exited unexpectedly")}
			return
		}
		var resp bridgeResponse
		if err := json.Unmarshal(o.scanner.Bytes(), &resp); err != nil {
			ch <- result{err: fmt.Errorf("unmarshal response: %w (raw: %s)", err, o.scanner.Text())}
			return
		}
		ch <- result{resp: &resp}
	}()

	select {
	case r := <-ch:
		return r.resp, r.err
	case <-time.After(bridgeTimeout):
		// Kill the bridge process to unblock the goroutine reading from scanner.
		// This is safe because the process will be restarted on the next init.
		if o.cmd != nil && o.cmd.Process != nil {
			_ = o.cmd.Process.Kill()
		}
		return nil, fmt.Errorf("read from bridge: timeout (process killed, restart required)")
	}
}

// resolveStoragePath returns the SQLite storage path for Optuna.
// Returns empty string when unconfigured, which makes the bridge use in-memory storage.
func resolveStoragePath(cfg config.StrategySpec) string {
	if cfg.StoragePath != "" {
		return cfg.StoragePath
	}
	return os.Getenv(envStoragePath)
}

// log2Int returns the base-2 logarithm of n. n must be a power of 2.
func log2Int(n int) int {
	return int(math.Log2(float64(n)))
}
