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
	"fmt"
	"sync"

	"sigs.k8s.io/rbgs/pkg/autobenchmark/config"
	abtypes "sigs.k8s.io/rbgs/pkg/autobenchmark/types"
)

// RawSearchSpace maps role names to their raw (unexpanded) parameter definitions.
// This carries type metadata (float, int, pow2, categorical) that is lost after expansion.
type RawSearchSpace map[string]map[string]config.SearchParam

// ExpandedSearchSpace maps role names to their expanded parameter lists.
// Each parameter name maps to a list of discrete values to search.
type ExpandedSearchSpace map[string]map[string][]any

// SearchAlgorithm defines the interface for parameter search strategies.
type SearchAlgorithm interface {
	// Name returns the algorithm name.
	Name() string

	// Init initializes the algorithm with the given study name, raw search space,
	// expanded search space, and strategy config.
	// The name uniquely identifies this search instance (e.g., template name) and is used by
	// stateful backends (like Optuna) as the persistent study identifier.
	// rawSpace carries type metadata (float/int/pow2/categorical) needed to construct
	// proper distributions; expandedSpace is the pre-computed discrete Cartesian product.
	Init(ctx context.Context, name string, rawSpace RawSearchSpace, expandedSpace ExpandedSearchSpace, cfg config.StrategySpec) error

	// SuggestNext returns the next parameter set to try, given previous trial results.
	SuggestNext(history []abtypes.TrialResult) (abtypes.RoleParamSet, error)

	// IsDone returns true if the algorithm has exhausted its search or met stopping criteria.
	IsDone(history []abtypes.TrialResult) bool

	// MarshalState serializes the algorithm state for checkpoint.
	MarshalState() ([]byte, error)

	// UnmarshalState restores algorithm state from checkpoint.
	UnmarshalState(data []byte) error
}

// Factory is a function that creates a new SearchAlgorithm instance.
type Factory func() SearchAlgorithm

var (
	registry = make(map[string]Factory)
	mu       sync.RWMutex
)

// Register registers a SearchAlgorithm factory by name.
func Register(name string, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = factory
}

// Get creates a new SearchAlgorithm instance by name.
func Get(name string) (SearchAlgorithm, error) {
	mu.RLock()
	defer mu.RUnlock()
	factory, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown search algorithm: %q", name)
	}
	return factory(), nil
}
