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

	"sigs.k8s.io/rbgs/pkg/autobenchmark/config"
	abtypes "sigs.k8s.io/rbgs/pkg/autobenchmark/types"
)

func init() {
	Register("grid", func() SearchAlgorithm { return &GridSearch{} })
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

// Init initializes the grid search with the raw and expanded search space.
// maxTrials is capped to the number of combinations so that IsDone
// correctly terminates when the space is smaller than MaxTrialsPerTemplate.
// Grid search requires all float/int parameters to have a step (discrete);
// continuous parameters are rejected because they can't be enumerated.
func (g *GridSearch) Init(_ context.Context, _ string, rawSpace RawSearchSpace, space ExpandedSearchSpace, cfg config.StrategySpec) error {
	// Validate: float/int params must have step for grid enumeration.
	for role, params := range rawSpace {
		for name, param := range params {
			switch param.Type {
			case "float", "int":
				if param.Step == nil {
					return fmt.Errorf("grid search requires step for param %q/%q (type %s)", role, name, param.Type)
				}
			}
		}
	}

	entries, total, err := cartesianProductEntries(space)
	if err != nil {
		return err
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
