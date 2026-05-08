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
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"sigs.k8s.io/rbgs/pkg/autobenchmark/config"
	abtypes "sigs.k8s.io/rbgs/pkg/autobenchmark/types"
)

func init() {
	Register("grid", func() SearchAlgorithm { return &GridSearch{} })
}

// expandParamForGrid expands a single SearchParam into discrete values for grid search.
// Only categorical and int/float with step are supported; pow2 and continuous params are rejected.
func expandParamForGrid(role, name string, param config.SearchParam) ([]any, error) {
	switch param.Type {
	case config.ParamTypeCategorical:
		if len(param.Values) == 0 {
			return nil, fmt.Errorf("grid search: param %q/%q requires at least one value", role, name)
		}
		result := make([]any, len(param.Values))
		copy(result, param.Values)
		return result, nil
	case config.ParamTypeFloat:
		if param.Min == nil || param.Max == nil {
			return nil, fmt.Errorf("grid search: param %q/%q requires min and max", role, name)
		}
		if param.Step == nil {
			return nil, fmt.Errorf("grid search: param %q/%q (type float) requires step for enumeration", role, name)
		}
		var values []any
		for v := *param.Min; v <= *param.Max+1e-9; v += *param.Step {
			rounded := math.Round(v*1e6) / 1e6
			values = append(values, rounded)
		}
		return values, nil
	case config.ParamTypeInt:
		if param.Min == nil || param.Max == nil {
			return nil, fmt.Errorf("grid search: param %q/%q requires min and max", role, name)
		}
		if param.Step == nil {
			return nil, fmt.Errorf("grid search: param %q/%q (type int) requires step for enumeration", role, name)
		}
		var values []any
		step := *param.Step
		for v := *param.Min; v <= *param.Max+1e-9; v += step {
			values = append(values, int(math.Round(v)))
		}
		return values, nil
	default:
		return nil, fmt.Errorf("grid search: param %q/%q unsupported type %q, only categorical, int, or float with step are supported", role, name, param.Type)
	}
}

func sortedKeysRaw(m SearchSpace) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedParamKeysRaw(m map[string]config.SearchParam) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// GridSearch implements a simple grid (exhaustive) search over the Cartesian product of all params.
type GridSearch struct {
	entries      []paramEntry
	total        uint64
	currentIndex int
	maxTrials    int
}

type gridState struct {
	CurrentIndex int `json:"currentIndex"`
}

// Name returns the algorithm name.
func (g *GridSearch) Name() string { return "grid" }

// Init initializes the grid search with the raw search space.
// Grid search only supports categorical or discrete int/float (with step).
// Continuous float/int params (without step) and pow2 are rejected.
func (g *GridSearch) Init(_ context.Context, _ string, rawSpace SearchSpace, cfg config.StrategySpec) error {
	var entries []paramEntry
	roles := sortedKeysRaw(rawSpace)
	for _, role := range roles {
		params := rawSpace[role]
		names := sortedParamKeysRaw(params)
		for _, name := range names {
			param := params[name]
			values, err := expandParamForGrid(role, name, param)
			if err != nil {
				return err
			}
			entries = append(entries, paramEntry{role: role, name: name, values: values})
		}
	}

	var total uint64 = 1
	for _, e := range entries {
		n := uint64(len(e.values))
		if n == 0 {
			continue
		}
		if total > math.MaxUint64/n {
			return fmt.Errorf("search space too large: combination count overflows")
		}
		total *= n
	}

	g.entries = entries
	g.total = total
	g.currentIndex = 0
	g.maxTrials = cfg.MaxTrialsPerTemplate
	if g.total > 0 && (g.maxTrials <= 0 || uint64(g.maxTrials) > g.total) {
		if g.total > uint64(math.MaxInt) {
			g.maxTrials = math.MaxInt
		} else {
			g.maxTrials = int(g.total)
		}
	}
	return nil
}

// SuggestNext returns the next parameter combination in the grid.
func (g *GridSearch) SuggestNext(_ []abtypes.TrialResult) (abtypes.RoleParamSet, error) {
	if uint64(g.currentIndex) >= g.total {
		return nil, fmt.Errorf("grid search exhausted: all combinations tried")
	}
	result := combinationAtIndex(g.currentIndex, g.entries)
	g.currentIndex++
	return result, nil
}

// IsDone returns true if all combinations have been tried or maxTrials reached.
func (g *GridSearch) IsDone(history []abtypes.TrialResult) bool {
	return uint64(g.currentIndex) >= g.total || len(history) >= g.maxTrials
}

// MarshalState serializes the current grid search state for checkpoint.
func (g *GridSearch) MarshalState() ([]byte, error) {
	return json.Marshal(gridState{CurrentIndex: g.currentIndex})
}

// UnmarshalState restores grid search state from checkpoint.
func (g *GridSearch) UnmarshalState(data []byte) error {
	var state gridState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("unmarshaling grid state: %w", err)
	}
	g.currentIndex = state.CurrentIndex
	return nil
}

// paramEntry represents a single parameter with its role, name, and values.
type paramEntry struct {
	role   string
	name   string
	values []any
}

// combinationAtIndex returns the i-th combination from the given entries
// using a mixed-radix decomposition (odometer order).
func combinationAtIndex(index int, entries []paramEntry) abtypes.RoleParamSet {
	rps := make(abtypes.RoleParamSet)
	remaining := index
	for j := len(entries) - 1; j >= 0; j-- {
		n := len(entries[j].values)
		if n == 0 {
			continue
		}
		idx := remaining % n
		remaining /= n
		if _, ok := rps[entries[j].role]; !ok {
			rps[entries[j].role] = make(abtypes.ParamSet)
		}
		rps[entries[j].role][entries[j].name] = entries[j].values[idx]
	}
	return rps
}
