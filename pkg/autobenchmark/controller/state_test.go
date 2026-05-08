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

package controller

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	abtypes "sigs.k8s.io/rbgs/pkg/autobenchmark/types"
)

func TestStateManager_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	sm := NewStateManager(dir)

	state := &abtypes.ExperimentState{
		ExperimentID:       "exp-123",
		StartTime:          time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		CurrentTemplateIdx: 1,
		Templates: []abtypes.TemplateState{
			{
				Name:      "template-a",
				Completed: true,
				Trials: []abtypes.TrialResult{
					{TrialIndex: 0, TemplateName: "template-a", Constraints: []float64{}, Score: 1500},
					{TrialIndex: 1, TemplateName: "template-a", Constraints: []float64{1}, Score: 0},
				},
				BestTrial: &abtypes.TrialResult{TrialIndex: 0, Score: 1500, Constraints: []float64{}},
			},
			{
				Name:      "template-b",
				Completed: false,
				Trials:    nil,
			},
		},
		GlobalBest: &abtypes.TrialResult{TrialIndex: 0, TemplateName: "template-a", Score: 1500, Constraints: []float64{}},
	}

	// Save
	err := sm.Save(state)
	require.NoError(t, err)
	assert.True(t, sm.Exists())

	// Load
	loaded, err := sm.Load()
	require.NoError(t, err)
	require.NotNil(t, loaded)

	assert.Equal(t, "exp-123", loaded.ExperimentID)
	assert.Equal(t, 1, loaded.CurrentTemplateIdx)
	require.Len(t, loaded.Templates, 2)
	assert.True(t, loaded.Templates[0].Completed)
	assert.False(t, loaded.Templates[1].Completed)
	assert.Len(t, loaded.Templates[0].Trials, 2)
	require.NotNil(t, loaded.GlobalBest)
	assert.InDelta(t, 1500, loaded.GlobalBest.Score, 0.01)
}

func TestStateManager_LoadNonExistent(t *testing.T) {
	dir := t.TempDir()
	sm := NewStateManager(filepath.Join(dir, "nodir"))

	loaded, err := sm.Load()
	require.NoError(t, err)
	assert.Nil(t, loaded)
	assert.False(t, sm.Exists())
}

func TestStateManager_Resume(t *testing.T) {
	dir := t.TempDir()
	sm := NewStateManager(dir)

	// Simulate: template-0 completed 5 trials, template-1 has 0
	state := &abtypes.ExperimentState{
		ExperimentID:       "exp-resume",
		CurrentTemplateIdx: 1,
		Templates: []abtypes.TemplateState{
			{
				Name:      "t0",
				Completed: true,
				Trials: []abtypes.TrialResult{
					{TrialIndex: 0}, {TrialIndex: 1}, {TrialIndex: 2},
					{TrialIndex: 3}, {TrialIndex: 4},
				},
			},
			{
				Name:      "t1",
				Completed: false,
			},
		},
	}

	require.NoError(t, sm.Save(state))

	// Load and verify resume point
	loaded, err := sm.Load()
	require.NoError(t, err)
	assert.Equal(t, 1, loaded.CurrentTemplateIdx)
	assert.True(t, loaded.Templates[0].Completed)
	assert.Len(t, loaded.Templates[0].Trials, 5)
	assert.False(t, loaded.Templates[1].Completed)
}

func TestStateManager_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	sm := NewStateManager(dir)

	// Write initial state
	state1 := &abtypes.ExperimentState{ExperimentID: "v1"}
	require.NoError(t, sm.Save(state1))

	// Write updated state
	state2 := &abtypes.ExperimentState{ExperimentID: "v2"}
	require.NoError(t, sm.Save(state2))

	// Load should return v2
	loaded, err := sm.Load()
	require.NoError(t, err)
	assert.Equal(t, "v2", loaded.ExperimentID)
}

func TestStateManager_CreatesDirIfNeeded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	sm := NewStateManager(dir)

	state := &abtypes.ExperimentState{ExperimentID: "test"}
	require.NoError(t, sm.Save(state))
	assert.True(t, sm.Exists())
}
