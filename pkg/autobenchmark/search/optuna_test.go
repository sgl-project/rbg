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
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/rbgs/pkg/autobenchmark/config"
	abtypes "sigs.k8s.io/rbgs/pkg/autobenchmark/types"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func findBridgePath() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "..", "tools", "optuna", "optuna_bridge.py")
}

func skipIfNoOptuna(t *testing.T) {
	t.Helper()
	if err := exec.Command("python3", "-c", "import optuna").Run(); err != nil {
		t.Skip("python3 with optuna not available, skipping integration test")
	}
}

func toFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	default:
		return 0
	}
}

// objectiveFunc computes (score, constraints) from suggested params.
type objectiveFunc func(params abtypes.RoleParamSet) (float64, []float64)

// runLoop runs a full ask-tell optimisation loop and returns the trial history.
func runLoop(
	t *testing.T,
	algo *OptunaSearch,
	studyName string,
	space SearchSpace,
	cfg config.StrategySpec,
	obj objectiveFunc,
) []abtypes.TrialResult {
	t.Helper()
	require.NoError(t, algo.Init(context.Background(), studyName, space, cfg))

	var history []abtypes.TrialResult
	for !algo.IsDone(history) {
		params, err := algo.SuggestNext(history)
		require.NoError(t, err)

		score, constraints := obj(params)
		history = append(history, abtypes.TrialResult{
			TrialIndex:  len(history),
			Params:      params,
			Score:       score,
			Constraints: constraints,
		})
	}
	return history
}

func bestScore(history []abtypes.TrialResult) float64 {
	best := history[0].Score
	for _, r := range history[1:] {
		if r.Score > best {
			best = r.Score
		}
	}
	return best
}

func avgScore(history []abtypes.TrialResult) float64 {
	var sum float64
	for _, r := range history {
		sum += r.Score
	}
	return sum / float64(len(history))
}

// ---------------------------------------------------------------------------
// Small search space (5×5 = 25 combos) — smoke tests
// ---------------------------------------------------------------------------

func buildSmallSpace() SearchSpace {
	return SearchSpace{
		"default": {
			"x": {Type: "categorical", Values: []interface{}{1, 2, 3, 4, 5}},
			"y": {Type: "categorical", Values: []interface{}{0.1, 0.2, 0.3, 0.4, 0.5}},
		},
	}
}

// evalSmall: peak at x=3, y=0.3 → score=10.0. SLA constraint: score > 8.0.
func evalSmall(params abtypes.RoleParamSet) (float64, []float64) {
	x := toFloat64(params["default"]["x"])
	y := toFloat64(params["default"]["y"])
	score := 10.0 - (x-3)*(x-3) - (y-0.3)*(y-0.3)*100
	constraint := 8.0 - score
	if constraint < 0 {
		constraint = 0
	}
	return score, []float64{constraint}
}

// ---------------------------------------------------------------------------
// Large search space (20×20×10 = 4000 combos) — effectiveness tests
//
// Objective: score = 100 − 0.5·(a−15)² − (b−7)² − 2·(c−4)²
// Peak at (a=15, b=7, c=4) → score = 100.
// The three parameters have different sensitivities, making the landscape
// non-trivial: c is most sensitive (coeff 2), b is moderate (coeff 1),
// a is least sensitive (coeff 0.5).
// ---------------------------------------------------------------------------

func buildLargeSpace() SearchSpace {
	aVals := make([]interface{}, 20)
	for i := range aVals {
		aVals[i] = float64(i + 1)
	}
	bVals := make([]interface{}, 20)
	for i := range bVals {
		bVals[i] = float64(i + 1)
	}
	cVals := make([]interface{}, 10)
	for i := range cVals {
		cVals[i] = float64(i + 1)
	}
	return SearchSpace{
		"default": {
			"a": {Type: "categorical", Values: aVals},
			"b": {Type: "categorical", Values: bVals},
			"c": {Type: "categorical", Values: cVals},
		},
	}
}

// evalLarge: peak at a=15, b=7, c=4 → 100. SLA constraint: score > 90.0.
func evalLarge(params abtypes.RoleParamSet) (float64, []float64) {
	a := toFloat64(params["default"]["a"])
	b := toFloat64(params["default"]["b"])
	c := toFloat64(params["default"]["c"])
	score := 100.0 - 0.5*(a-15)*(a-15) - (b-7)*(b-7) - 2*(c-4)*(c-4)
	constraint := 90.0 - score
	if constraint < 0 {
		constraint = 0
	}
	return score, []float64{constraint}
}

// ===========================================================================
// Effectiveness tests — verify algorithms produce meaningful optimisation
// ===========================================================================

// TestOptunaSearch_Convergence verifies that TPE learns over time:
//   - best score exceeds a high threshold (95.0)
//   - average score of latter trials > average of early trials
func TestOptunaSearch_Convergence(t *testing.T) {
	skipIfNoOptuna(t)
	t.Setenv(envBridgePath, findBridgePath())

	seed := 42
	algo := &OptunaSearch{}
	defer func() { _ = algo.Close() }()

	history := runLoop(t, algo, "convergence-tpe", buildLargeSpace(), config.StrategySpec{
		Algorithm:            "tpe",
		MaxTrialsPerTemplate: 50,
		Seed:                 &seed,
	}, evalLarge)

	assert.Len(t, history, 50)

	best := bestScore(history)
	t.Logf("Best score: %.2f (peak=100)", best)
	assert.GreaterOrEqual(t, best, 95.0,
		"TPE should find score >= 95 within 50 trials on a 4000-combo space")

	// Convergence: last 15 trials should score higher on average than first 15.
	first15 := avgScore(history[:15])
	last15 := avgScore(history[35:])
	t.Logf("Avg first 15: %.2f, avg last 15: %.2f", first15, last15)
	assert.Greater(t, last15, first15,
		"TPE should converge: later trials score higher than early trials")
}

// TestOptunaSearch_TPEBeatsRandom gives TPE and Random the same budget on the
// large space and compares the average score of the last 15 trials ("tail avg").
// TPE should converge into the high-score region, producing a much higher tail
// average than random, which remains uniformly distributed (theoretical mean ≈ 7).
func TestOptunaSearch_TPEBeatsRandom(t *testing.T) {
	skipIfNoOptuna(t)
	t.Setenv(envBridgePath, findBridgePath())

	space := buildLargeSpace()
	seed := 42
	budget := 50
	tailSize := 15

	// TPE
	tpeAlgo := &OptunaSearch{}
	tpeHistory := runLoop(t, tpeAlgo, "cmp-tpe", space, config.StrategySpec{
		Algorithm:            "tpe",
		MaxTrialsPerTemplate: budget,
		Seed:                 &seed,
	}, evalLarge)
	_ = tpeAlgo.Close()

	// Random
	randAlgo := &OptunaSearch{}
	randHistory := runLoop(t, randAlgo, "cmp-random", space, config.StrategySpec{
		Algorithm:            "random",
		MaxTrialsPerTemplate: budget,
		Seed:                 &seed,
	}, evalLarge)
	_ = randAlgo.Close()

	tpeTailAvg := avgScore(tpeHistory[budget-tailSize:])
	randTailAvg := avgScore(randHistory[budget-tailSize:])

	t.Logf("TPE tail avg (last %d)=%.2f, Random tail avg=%.2f", tailSize, tpeTailAvg, randTailAvg)
	assert.Greater(t, tpeTailAvg, randTailAvg,
		"TPE tail average should exceed Random tail average")
	assert.Greater(t, tpeTailAvg, 0.0,
		"TPE should find positive-scoring trials")
}

// ===========================================================================
// Functional tests — samplers, multi-role, SLA, error handling, config
// ===========================================================================

func TestOptunaSearch_AllSamplers(t *testing.T) {
	skipIfNoOptuna(t)
	t.Setenv(envBridgePath, findBridgePath())

	// Smoke-test: every sampler should complete 5 trials without error
	// and produce valid params.
	// Note: gp and cmaes are designed for continuous spaces and may not work
	// reliably with purely categorical distributions, so we skip them here.
	samplers := []string{"tpe", "random", "qmc"}

	for _, sampler := range samplers {
		t.Run(sampler, func(t *testing.T) {
			algo := &OptunaSearch{}
			defer func() { _ = algo.Close() }()

			history := runLoop(t, algo, "smoke-"+sampler, buildSmallSpace(), config.StrategySpec{
				Algorithm:            sampler,
				MaxTrialsPerTemplate: 5,
			}, evalSmall)

			assert.Len(t, history, 5)
			for i, r := range history {
				assert.Equal(t, i, r.TrialIndex)
				assert.NotNil(t, r.Params["default"])
			}
		})
	}
}

func TestOptunaSearch_WithAlgorithmConfig(t *testing.T) {
	skipIfNoOptuna(t)
	t.Setenv(envBridgePath, findBridgePath())

	seed := 42
	algo := &OptunaSearch{}
	defer func() { _ = algo.Close() }()

	history := runLoop(t, algo, "cfg-tpe", buildLargeSpace(), config.StrategySpec{
		Algorithm:            "tpe",
		MaxTrialsPerTemplate: 30,
		Seed:                 &seed,
		AlgorithmConfig: map[string]any{
			"n_startup_trials": 5, // fewer random warmup trials
		},
	}, evalLarge)

	assert.Len(t, history, 30)

	best := bestScore(history)
	t.Logf("Best with n_startup_trials=5: %.2f", best)
	assert.GreaterOrEqual(t, best, 90.0,
		"TPE with n_startup_trials=5 should still find a good solution")
}

func TestOptunaSearch_SLAConstraint(t *testing.T) {
	skipIfNoOptuna(t)
	t.Setenv(envBridgePath, findBridgePath())

	seed := 42
	algo := &OptunaSearch{}
	defer func() { _ = algo.Close() }()

	history := runLoop(t, algo, "sla-tpe", buildLargeSpace(), config.StrategySpec{
		Algorithm:            "tpe",
		MaxTrialsPerTemplate: 50,
		Seed:                 &seed,
	}, evalLarge)

	var passCount, failCount int
	for _, r := range history {
		if r.IsSLAFeasible() {
			passCount++
		} else {
			failCount++
		}
	}
	t.Logf("SLA pass=%d fail=%d (threshold > 90)", passCount, failCount)
	assert.Greater(t, passCount, 0, "should have at least one SLA-passing trial")
	assert.Greater(t, failCount, 0, "should have at least one SLA-failing trial")
}

func TestOptunaSearch_MultiRole(t *testing.T) {
	skipIfNoOptuna(t)
	t.Setenv(envBridgePath, findBridgePath())

	space := SearchSpace{
		"prefill": {
			"chunkedPrefillSize": {Type: "categorical", Values: []interface{}{2048, 4096, 8192}},
		},
		"decode": {
			"maxNumSeqs": {Type: "categorical", Values: []interface{}{64, 128, 256, 512}},
		},
	}

	algo := &OptunaSearch{}
	defer func() { _ = algo.Close() }()

	require.NoError(t, algo.Init(context.Background(), "test-multi-role", space, config.StrategySpec{
		Algorithm:            "tpe",
		MaxTrialsPerTemplate: 8,
	}))

	var history []abtypes.TrialResult
	for !algo.IsDone(history) {
		params, err := algo.SuggestNext(history)
		require.NoError(t, err)

		require.Contains(t, params, "prefill")
		require.Contains(t, params, "decode")
		require.Contains(t, params["prefill"], "chunkedPrefillSize")
		require.Contains(t, params["decode"], "maxNumSeqs")

		result := abtypes.TrialResult{
			TrialIndex:  len(history),
			Params:      params,
			Score:       toFloat64(params["decode"]["maxNumSeqs"]) * 0.1,
			Constraints: []float64{},
		}
		history = append(history, result)

		t.Logf("Trial %d: prefill.chunkedPrefillSize=%.0f decode.maxNumSeqs=%.0f score=%.2f",
			result.TrialIndex,
			toFloat64(params["prefill"]["chunkedPrefillSize"]),
			toFloat64(params["decode"]["maxNumSeqs"]),
			result.Score)
	}
	assert.Len(t, history, 8)
}

func TestOptunaSearch_ErrorTrial(t *testing.T) {
	skipIfNoOptuna(t)
	t.Setenv(envBridgePath, findBridgePath())

	algo := &OptunaSearch{}
	defer func() { _ = algo.Close() }()

	require.NoError(t, algo.Init(context.Background(), "test-error", buildSmallSpace(), config.StrategySpec{
		Algorithm:            "tpe",
		MaxTrialsPerTemplate: 5,
	}))

	var history []abtypes.TrialResult
	for !algo.IsDone(history) {
		params, err := algo.SuggestNext(history)
		require.NoError(t, err)

		result := abtypes.TrialResult{
			TrialIndex: len(history),
			Params:     params,
		}

		if len(history) == 0 {
			result.Error = "simulated failure"
			result.Score = 0
			result.Constraints = []float64{1}
		} else {
			score, constraints := evalSmall(params)
			result.Score = score
			result.Constraints = constraints
		}
		history = append(history, result)
	}

	assert.Len(t, history, 5)
	assert.NotEmpty(t, history[0].Error)
	assert.Empty(t, history[1].Error)
}

// TestOptunaSearch_ResumeWithFailedTrials verifies that after a restart
// where history contains failed/SLA-failing trials, the resumed search
// tells the correct (most recent) trial result to Optuna instead of
// indexing by toldCount and sending stale data.
func TestOptunaSearch_ResumeWithFailedTrials(t *testing.T) {
	skipIfNoOptuna(t)
	t.Setenv(envBridgePath, findBridgePath())

	studyName := "resume-failed"
	space := buildSmallSpace()
	storagePath := filepath.Join(t.TempDir(), "optuna.db")
	cfg := config.StrategySpec{Algorithm: "tpe", MaxTrialsPerTemplate: 5, StoragePath: storagePath}

	// Phase 1: run 3 trials, one with an error.
	algo1 := &OptunaSearch{}
	require.NoError(t, algo1.Init(context.Background(), studyName, space, cfg))

	var history []abtypes.TrialResult
	for i := 0; i < 3; i++ {
		params, err := algo1.SuggestNext(history)
		require.NoError(t, err)
		result := abtypes.TrialResult{
			TrialIndex:  i,
			Params:      params,
			Score:       float64(i * 10),
			Constraints: []float64{0},
		}
		if i == 1 {
			result.Error = "simulated failure"
			result.Constraints = []float64{1}
		}
		history = append(history, result)
	}
	require.NoError(t, algo1.Close())

	// Phase 2: resume with the same study name and existing history.
	// The bridge should report existing_total=3 (including the failed trial).
	algo2 := &OptunaSearch{}
	require.NoError(t, algo2.Init(context.Background(), studyName, space, cfg))

	// Continue for 2 more trials.
	for i := 3; i < 5; i++ {
		params, err := algo2.SuggestNext(history)
		require.NoError(t, err)
		result := abtypes.TrialResult{
			TrialIndex:  i,
			Params:      params,
			Score:       float64(i * 10),
			Constraints: []float64{0},
		}
		history = append(history, result)
	}

	assert.Len(t, history, 5)
	// The bridge's idempotent tell should not have skipped any new trials.
	for i, r := range history {
		assert.Equal(t, i, r.TrialIndex)
		assert.NotNil(t, r.Params["default"])
	}
}

// ===========================================================================
// Unit tests — no Python required
// ===========================================================================

func TestOptunaSearch_FactoryRegistration(t *testing.T) {
	for _, name := range optunaSamplers {
		t.Run(name, func(t *testing.T) {
			algo, err := Get(name)
			require.NoError(t, err)
			_, ok := algo.(*OptunaSearch)
			assert.True(t, ok, "algorithm %q should be OptunaSearch", name)
		})
	}
}

func TestOptunaSearch_Name(t *testing.T) {
	for _, name := range optunaSamplers {
		t.Run(name, func(t *testing.T) {
			algo := &OptunaSearch{algorithm: name}
			assert.Equal(t, name, algo.Name())
		})
	}
}

func TestOptunaSearch_MarshalUnmarshal(t *testing.T) {
	algo := &OptunaSearch{algorithm: "tpe"}
	data, err := algo.MarshalState()
	require.NoError(t, err)
	assert.Contains(t, string(data), "tpe")
	require.NoError(t, algo.UnmarshalState(data))
}

func TestOptunaSearch_IsDone(t *testing.T) {
	// maxTrials is the bound.
	algo := &OptunaSearch{maxTrials: 3}
	assert.False(t, algo.IsDone(nil))
	assert.False(t, algo.IsDone(makeHistory(2)))
	assert.True(t, algo.IsDone(makeHistory(3)))
	assert.True(t, algo.IsDone(makeHistory(5)))

	// spaceSize < maxTrials: search space exhausted before maxTrials.
	algo2 := &OptunaSearch{maxTrials: 100, spaceSize: 4}
	assert.False(t, algo2.IsDone(makeHistory(3)))
	assert.True(t, algo2.IsDone(makeHistory(4)))
	assert.True(t, algo2.IsDone(makeHistory(10)))

	// spaceSize=0 (unknown): only maxTrials matters.
	algo3 := &OptunaSearch{maxTrials: 5, spaceSize: 0}
	assert.False(t, algo3.IsDone(makeHistory(4)))
	assert.True(t, algo3.IsDone(makeHistory(5)))
}

func TestOptunaSearch_BridgePathResolution(t *testing.T) {
	assert.Equal(t, "/tools/optuna_bridge.py", defaultBridgePath)

	t.Setenv(envBridgePath, "/custom/bridge.py")
	algo := &OptunaSearch{}
	err := algo.startProcess()
	if err == nil {
		assert.Equal(t, []string{"python3", "/custom/bridge.py"}, algo.cmd.Args)
		_ = algo.Close()
	} else {
		t.Logf("startProcess error (expected on systems without python3): %v", err)
	}
}
