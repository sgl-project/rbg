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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/rbgs/pkg/autobenchmark/config"
	abtypes "sigs.k8s.io/rbgs/pkg/autobenchmark/types"
)

func TestExpandParamForGrid_Float(t *testing.T) {
	tests := []struct {
		name   string
		param  config.SearchParam
		expect []any
	}{
		{
			name:   "integer range",
			param:  config.SearchParam{Type: config.ParamTypeFloat, Min: ptr(1.0), Max: ptr(5.0), Step: ptr(2.0)},
			expect: []interface{}{1.0, 3.0, 5.0},
		},
		{
			name:   "single value range",
			param:  config.SearchParam{Type: config.ParamTypeFloat, Min: ptr(10.0), Max: ptr(10.0), Step: ptr(1.0)},
			expect: []interface{}{10.0},
		},
		{
			name:   "step larger than range",
			param:  config.SearchParam{Type: config.ParamTypeFloat, Min: ptr(1.0), Max: ptr(3.0), Step: ptr(5.0)},
			expect: []interface{}{1.0},
		},
		{
			name:   "float range",
			param:  config.SearchParam{Type: config.ParamTypeFloat, Min: ptr(0.8), Max: ptr(0.95), Step: ptr(0.05)},
			expect: []interface{}{0.8, 0.85, 0.9, 0.95},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := expandParamForGrid("default", tt.name, tt.param)
			require.NoError(t, err)
			assert.Equal(t, len(tt.expect), len(result))
			for i, v := range tt.expect {
				assert.InDelta(t, v, result[i], 1e-6)
			}
		})
	}
}

func TestExpandParamForGrid_Int(t *testing.T) {
	param := config.SearchParam{Type: config.ParamTypeInt, Min: ptr(1.0), Max: ptr(5.0), Step: ptr(2.0)}
	result, err := expandParamForGrid("default", "x", param)
	require.NoError(t, err)
	assert.Equal(t, []any{1, 3, 5}, result)
}

func TestExpandParamForGrid_Categorical(t *testing.T) {
	param := config.SearchParam{
		Type:   config.ParamTypeCategorical,
		Values: []interface{}{0.85, 0.9, 0.95},
	}
	result, err := expandParamForGrid("default", "x", param)
	require.NoError(t, err)
	assert.Equal(t, []interface{}{0.85, 0.9, 0.95}, result)
}

func TestExpandParamForGrid_Errors(t *testing.T) {
	tests := []struct {
		name    string
		param   config.SearchParam
		wantErr string
	}{
		{
			name:    "float without step",
			param:   config.SearchParam{Type: config.ParamTypeFloat, Min: ptr(1.0), Max: ptr(5.0)},
			wantErr: "requires step",
		},
		{
			name:    "int without step",
			param:   config.SearchParam{Type: config.ParamTypeInt, Min: ptr(1.0), Max: ptr(5.0)},
			wantErr: "requires step",
		},
		{
			name:    "pow2 type",
			param:   config.SearchParam{Type: config.ParamTypePow2, Min: ptr(512.0), Max: ptr(8192.0)},
			wantErr: "unsupported type",
		},
		{
			name:    "empty categorical",
			param:   config.SearchParam{Type: config.ParamTypeCategorical, Values: []interface{}{}},
			wantErr: "at least one value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := expandParamForGrid("default", tt.name, tt.param)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func ptr(v float64) *float64 { return &v }

func TestGridSearch_Basic(t *testing.T) {
	space := SearchSpace{
		"default": {
			"a": {Type: config.ParamTypeCategorical, Values: []interface{}{1, 2}},
			"b": {Type: config.ParamTypeCategorical, Values: []interface{}{10, 20}},
		},
	}

	g := &GridSearch{}
	require.NoError(t, g.Init(context.Background(), "test", space, config.StrategySpec{MaxTrialsPerTemplate: 100}))

	assert.Equal(t, "grid", g.Name())

	var results []map[string]any
	for !g.IsDone(nil) {
		ps, err := g.SuggestNext(nil)
		require.NoError(t, err)
		results = append(results, ps["default"])
	}

	assert.Len(t, results, 4) // 2 * 2

	// Verify exhausted
	_, err := g.SuggestNext(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exhausted")
}

func TestGridSearch_MaxTrials(t *testing.T) {
	space := SearchSpace{
		"default": {
			"a": {Type: config.ParamTypeCategorical, Values: []interface{}{1, 2, 3, 4, 5}},
		},
	}

	g := &GridSearch{}
	require.NoError(t, g.Init(context.Background(), "test", space, config.StrategySpec{MaxTrialsPerTemplate: 3}))

	var count int
	for !g.IsDone(nil) {
		_, err := g.SuggestNext(nil)
		require.NoError(t, err)
		count++
		if g.IsDone(makeHistory(count)) {
			break
		}
	}
	assert.Equal(t, 3, count)
}

func TestGridSearch_MaxTrialsExceedsSpace(t *testing.T) {
	// maxTrials (100) > space size (3): should stop after exhausting all 3 combos.
	space := SearchSpace{
		"default": {"a": {Type: config.ParamTypeCategorical, Values: []interface{}{1, 2, 3}}},
	}

	g := &GridSearch{}
	require.NoError(t, g.Init(context.Background(), "test", space, config.StrategySpec{MaxTrialsPerTemplate: 100}))
	assert.Equal(t, 3, g.maxTrials) // capped to space size

	var count int
	for !g.IsDone(makeHistory(count)) {
		_, err := g.SuggestNext(nil)
		require.NoError(t, err)
		count++
	}
	assert.Equal(t, 3, count)
}

func TestGridSearch_IsDone(t *testing.T) {
	space := SearchSpace{
		"default": {
			"a": {Type: config.ParamTypeCategorical, Values: []interface{}{1, 2}},
			"b": {Type: config.ParamTypeCategorical, Values: []interface{}{10, 20}},
		},
	}

	// maxTrials < space: stops at maxTrials.
	g1 := &GridSearch{}
	require.NoError(t, g1.Init(context.Background(), "test", space, config.StrategySpec{MaxTrialsPerTemplate: 2}))
	assert.False(t, g1.IsDone(makeHistory(1)))
	assert.True(t, g1.IsDone(makeHistory(2)))

	// maxTrials > space: capped to 4.
	g2 := &GridSearch{}
	require.NoError(t, g2.Init(context.Background(), "test", space, config.StrategySpec{MaxTrialsPerTemplate: 100}))
	assert.False(t, g2.IsDone(makeHistory(3)))
	assert.True(t, g2.IsDone(makeHistory(4)))

	// maxTrials = 0: defaults to space size (4).
	g3 := &GridSearch{}
	require.NoError(t, g3.Init(context.Background(), "test", space, config.StrategySpec{}))
	assert.False(t, g3.IsDone(makeHistory(3)))
	assert.True(t, g3.IsDone(makeHistory(4)))
}

func TestGridSearch_StateCheckpointResume(t *testing.T) {
	space := SearchSpace{
		"default": {"a": {Type: config.ParamTypeCategorical, Values: []interface{}{1, 2, 3, 4}}},
	}

	// Run first 2 trials
	g1 := &GridSearch{}
	require.NoError(t, g1.Init(context.Background(), "test", space, config.StrategySpec{MaxTrialsPerTemplate: 100}))
	ps1, _ := g1.SuggestNext(nil)
	ps2, _ := g1.SuggestNext(nil)

	// Checkpoint
	state, err := g1.MarshalState()
	require.NoError(t, err)

	// Resume in new instance
	g2 := &GridSearch{}
	require.NoError(t, g2.Init(context.Background(), "test", space, config.StrategySpec{MaxTrialsPerTemplate: 100}))
	require.NoError(t, g2.UnmarshalState(state))

	// Should continue from index 2
	ps3, err := g2.SuggestNext(nil)
	require.NoError(t, err)
	ps4, err := g2.SuggestNext(nil)
	require.NoError(t, err)

	// All 4 should be different
	assert.NotEqual(t, ps1["default"]["a"], ps2["default"]["a"])
	assert.NotEqual(t, ps2["default"]["a"], ps3["default"]["a"])
	assert.NotEqual(t, ps3["default"]["a"], ps4["default"]["a"])

	// g2 should now be done
	assert.True(t, g2.IsDone(nil))
}

func TestGridSearch_FeedbackDoesNotAffectOrder(t *testing.T) {
	space := SearchSpace{
		"default": {"a": {Type: config.ParamTypeCategorical, Values: []interface{}{1, 2, 3}}},
	}

	g := &GridSearch{}
	require.NoError(t, g.Init(context.Background(), "test", space, config.StrategySpec{MaxTrialsPerTemplate: 100}))

	// Suggest with different histories — order should be the same
	ps1, _ := g.SuggestNext(nil)
	ps2, _ := g.SuggestNext(makeHistory(5))
	ps3, _ := g.SuggestNext(makeHistory(10))

	vals := []interface{}{ps1["default"]["a"], ps2["default"]["a"], ps3["default"]["a"]}
	assert.Equal(t, []interface{}{1, 2, 3}, vals)
}

func TestFactory_GridRegistered(t *testing.T) {
	algo, err := Get("grid")
	require.NoError(t, err)
	assert.Equal(t, "grid", algo.Name())
}

func TestFactory_Unknown(t *testing.T) {
	_, err := Get("unknown")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown search algorithm")
}

func makeHistory(n int) []abtypes.TrialResult {
	h := make([]abtypes.TrialResult, n)
	for i := range h {
		h[i].TrialIndex = i
	}
	return h
}
