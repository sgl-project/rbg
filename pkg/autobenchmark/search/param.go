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
	"fmt"
	"math"
	"sort"

	"sigs.k8s.io/rbgs/pkg/autobenchmark/config"
	abtypes "sigs.k8s.io/rbgs/pkg/autobenchmark/types"
)

// ExpandSearchSpace expands a config-level SearchSpace (with range and categorical params)
// into discrete value lists per role per param.
func ExpandSearchSpace(searchSpace map[string]map[string]config.SearchParam) ExpandedSearchSpace {
	expanded := make(ExpandedSearchSpace)
	for role, params := range searchSpace {
		expanded[role] = make(map[string][]interface{})
		for name, param := range params {
			expanded[role][name] = expandParam(param)
		}
	}
	return expanded
}

func expandParam(param config.SearchParam) []interface{} {
	switch param.Type {
	case "float":
		if param.Min == nil || param.Max == nil {
			return nil
		}
		if param.Step == nil {
			return nil // continuous, only meaningful for samplers (not grid)
		}
		var values []interface{}
		for v := *param.Min; v <= *param.Max+1e-9; v += *param.Step {
			// Round to avoid floating point drift
			rounded := math.Round(v*1e6) / 1e6
			values = append(values, rounded)
		}
		return values
	case "int":
		if param.Min == nil || param.Max == nil {
			return nil
		}
		if param.Step == nil {
			return nil // continuous, only meaningful for samplers (not grid)
		}
		var values []interface{}
		step := *param.Step
		for v := *param.Min; v <= *param.Max+1e-9; v += step {
			values = append(values, int(math.Round(v)))
		}
		return values
	case "pow2":
		if param.Min == nil || param.Max == nil {
			return nil
		}
		if !isPow2Int(int64(*param.Min)) || !isPow2Int(int64(*param.Max)) {
			return nil
		}
		var values []interface{}
		start := log2Int(int(*param.Min))
		end := log2Int(int(*param.Max))
		for exp := start; exp <= end; exp++ {
			values = append(values, 1<<exp)
		}
		return values
	case "categorical":
		result := make([]interface{}, len(param.Values))
		copy(result, param.Values)
		return result
	default:
		return nil
	}
}

// isPow2 returns true if n is a positive power of 2.
func isPow2(n int) bool {
	return n > 0 && (n&(n-1)) == 0
}

// log2Int returns the base-2 logarithm of n. n must be a power of 2.
func log2Int(n int) int {
	return int(math.Log2(float64(n)))
}

// isPow2Int returns true if n is a positive power of 2 (int64 variant).
func isPow2Int(n int64) bool {
	return n > 0 && (n&(n-1)) == 0
}

// paramEntry represents a single parameter with its role, name, and values.
type paramEntry struct {
	role   string
	name   string
	values []any
}

// cartesianProductEntries returns ordered parameter entries and the total
// combination count (as uint64 to avoid int overflow).
func cartesianProductEntries(space ExpandedSearchSpace) ([]paramEntry, uint64, error) {
	var entries []paramEntry
	roles := sortedKeys(space)
	for _, role := range roles {
		params := space[role]
		names := sortedParamKeys(params)
		for _, name := range names {
			entries = append(entries, paramEntry{role: role, name: name, values: params[name]})
		}
	}
	if len(entries) == 0 {
		return nil, 1, nil
	}
	var total uint64 = 1
	for _, e := range entries {
		n := uint64(len(e.values))
		if n == 0 {
			continue
		}
		if total > math.MaxUint64/n {
			return nil, 0, fmt.Errorf("search space too large: combination count overflows")
		}
		total *= n
	}
	return entries, total, nil
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

// CartesianProduct generates all combinations of role-level param sets.
// It produces a flat list of RoleParamSet, where each entry contains
// param values for all roles.
// NOTE: This eagerly materializes all combinations and may OOM for large
// search spaces; prefer iterator-based consumption when possible.
func CartesianProduct(space ExpandedSearchSpace) []abtypes.RoleParamSet {
	entries, total, err := cartesianProductEntries(space)
	if err != nil {
		return nil
	}
	if total > uint64(math.MaxInt) {
		return nil
	}
	results := make([]abtypes.RoleParamSet, 0, int(total))
	for i := uint64(0); i < total; i++ {
		results = append(results, combinationAtIndex(int(i), entries))
	}
	return results
}

func sortedKeys(m ExpandedSearchSpace) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedParamKeys(m map[string][]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
