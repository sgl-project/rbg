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

package roleinstance

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	synccontrol "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/sync"
)

func TestInconsistentBaselines(t *testing.T) {
	tests := []struct {
		name     string
		old      map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline
		new      map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline
		expected bool
	}{
		{
			name:     "both nil",
			old:      nil,
			new:      nil,
			expected: false,
		},
		{
			name: "old nil, new non-nil",
			old:  nil,
			new: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			expected: true,
		},
		{
			name: "old non-nil, new nil",
			old: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			new:      nil,
			expected: true,
		},
		{
			name: "same content",
			old: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}, "sidecar": {RestartCount: 2}},
			},
			new: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}, "sidecar": {RestartCount: 2}},
			},
			expected: false,
		},
		{
			name: "different pod names",
			old: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			new: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-1": {"main": {RestartCount: 0}},
			},
			expected: true,
		},
		{
			name: "different container names",
			old: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			new: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"sidecar": {RestartCount: 0}},
			},
			expected: true,
		},
		{
			name: "different restart counts",
			old: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			new: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 1}},
			},
			expected: true,
		},
		{
			name: "different ImageIDs",
			old: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0, ImageID: "img-v1"}},
			},
			new: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0, ImageID: "img-v2"}},
			},
			expected: true,
		},
		{
			name: "extra pod in new",
			old: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			new: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
				"pod-1": {"main": {RestartCount: 0}},
			},
			expected: true,
		},
		{
			name:     "both empty maps",
			old:      map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{},
			new:      map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := inconsistentBaselines(tt.old, tt.new)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRestartTrackingChanged(t *testing.T) {
	now := metav1.NewTime(time.Now())
	earlier := metav1.NewTime(now.Add(-1 * time.Minute))
	later := metav1.NewTime(now.Add(1 * time.Minute))

	tests := []struct {
		name                string
		newLastRestartTime  *metav1.Time
		instanceLastRestart *metav1.Time
		liveLastRestart     *metav1.Time
		expectedChanged     bool
	}{
		{
			name:                "new is nil: not changed",
			newLastRestartTime:  nil,
			instanceLastRestart: &now,
			liveLastRestart:     &now,
			expectedChanged:     false,
		},
		{
			name:                "new differs from both informer and live: changed (updateRestartTracking ran)",
			newLastRestartTime:  &later,
			instanceLastRestart: &now,
			liveLastRestart:     &now,
			expectedChanged:     true,
		},
		{
			name:                "new equals informer but differs from live: not changed (syncRestartTrackingFromAPI pass-through)",
			newLastRestartTime:  &now,
			instanceLastRestart: &now,
			liveLastRestart:     &earlier,
			expectedChanged:     false,
		},
		{
			name:                "new equals live but differs from informer: not changed (informer stale, live matches)",
			newLastRestartTime:  &now,
			instanceLastRestart: &earlier,
			liveLastRestart:     &now,
			expectedChanged:     false,
		},
		{
			name:                "new equals both: not changed",
			newLastRestartTime:  &now,
			instanceLastRestart: &now,
			liveLastRestart:     &now,
			expectedChanged:     false,
		},
		{
			name:                "informer nil, live nil, new set: changed (first restart)",
			newLastRestartTime:  &now,
			instanceLastRestart: nil,
			liveLastRestart:     nil,
			expectedChanged:     true,
		},
		{
			name:                "informer nil, new equals live: not changed",
			newLastRestartTime:  &now,
			instanceLastRestart: nil,
			liveLastRestart:     &now,
			expectedChanged:     false,
		},
		{
			name:                "live nil, new differs from informer: changed",
			newLastRestartTime:  &later,
			instanceLastRestart: &now,
			liveLastRestart:     nil,
			expectedChanged:     true,
		},
		{
			name:                "live nil, new equals informer: not changed",
			newLastRestartTime:  &now,
			instanceLastRestart: &now,
			liveLastRestart:     nil,
			expectedChanged:     false,
		},
		{
			name:                "informer nil, live set, new differs from live: changed",
			newLastRestartTime:  &later,
			instanceLastRestart: nil,
			liveLastRestart:     &now,
			expectedChanged:     true,
		},
		{
			name:                "informer nil, live set, new equals live: not changed",
			newLastRestartTime:  &now,
			instanceLastRestart: nil,
			liveLastRestart:     &now,
			expectedChanged:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := restartTrackingChanged(tt.newLastRestartTime, tt.instanceLastRestart, tt.liveLastRestart)
			assert.Equal(t, tt.expectedChanged, result)
		})
	}
}

func TestStaleStatusPreservesLiveRestartBudget(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	stale := instance.DeepCopy()
	instance.Status.GroupRestartCount = 2
	instance.Status.GroupRestartRevision = "template-hash"
	instance.Status.RestartBudgetExhausted = true
	instance.Status.Conditions = append(instance.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{Type: workloadsv1alpha2.RoleInstanceCustomReady, Status: v1.ConditionFalse, Reason: "LiveExternal"})
	synccontrol.EnforceRestartBudgetConditions(instance)
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithStatusSubresource(instance).WithObjects(instance).Build()
	updater := &realStatusUpdater{Client: c, apiReader: c}
	status := &workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: instance.Generation, Conditions: []workloadsv1alpha2.RoleInstanceCondition{
		{Type: workloadsv1alpha2.RoleInstanceReady, Status: v1.ConditionTrue},
		{Type: workloadsv1alpha2.RoleInstanceCustomReady, Status: v1.ConditionTrue, Reason: "StaleExternal"},
	}}
	require.NoError(t, updater.updateStatus(ctx, stale, status))
	live := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), live))
	require.EqualValues(t, 2, live.Status.GroupRestartCount)
	require.Equal(t, "template-hash", live.Status.GroupRestartRevision)
	require.True(t, live.Status.RestartBudgetExhausted)
	conditions := map[workloadsv1alpha2.RoleInstanceConditionType]workloadsv1alpha2.RoleInstanceCondition{}
	for _, condition := range live.Status.Conditions {
		require.NotContains(t, conditions, condition.Type)
		conditions[condition.Type] = condition
	}
	require.Equal(t, v1.ConditionFalse, conditions[workloadsv1alpha2.RoleInstanceReady].Status)
	require.Equal(t, synccontrol.GroupRestartBudgetExceeded, conditions[workloadsv1alpha2.RoleInstanceDegraded].Reason)
	require.Equal(t, "LiveExternal", conditions[workloadsv1alpha2.RoleInstanceCustomReady].Reason)
	// Nor can a stale exhausted snapshot resurrect state after explicit recovery.
	stale = live.DeepCopy()
	live.Status.RestartBudgetExhausted = false
	live.Status.GroupRestartCount = 0
	live.Status.Conditions = nil
	require.NoError(t, c.Status().Update(ctx, live))
	staleStatus := stale.Status.DeepCopy()
	require.NoError(t, updater.updateStatus(ctx, stale, staleStatus))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), live))
	require.False(t, live.Status.RestartBudgetExhausted)
	require.Zero(t, live.Status.GroupRestartCount)
	for _, cond := range live.Status.Conditions {
		require.NotEqual(t, workloadsv1alpha2.RoleInstanceDegraded, cond.Type)
	}
}

type budgetStatusWriteCounter struct {
	client.Client
	writes int
}
type budgetCountingStatusWriter struct {
	client.SubResourceWriter
	owner *budgetStatusWriteCounter
}

func (c *budgetStatusWriteCounter) Status() client.SubResourceWriter {
	return &budgetCountingStatusWriter{SubResourceWriter: c.Client.Status(), owner: c}
}
func (c *budgetCountingStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	c.owner.writes++
	return c.SubResourceWriter.Update(ctx, obj, opts...)
}

func TestExhaustedRestartBudgetStatusConverges(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Spec.ReadyPolicy = workloadsv1alpha2.RoleInstanceReadyPolicyTypeNone
	instance.Status.RestartBudgetExhausted = true
	instance.Status.GroupRestartRevision = "template"
	synccontrol.EnforceRestartBudgetConditions(instance)
	cli := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithStatusSubresource(instance).WithObjects(instance).Build()
	counter := &budgetStatusWriteCounter{Client: cli}
	updater := newStatusUpdater(counter, cli)
	update := func() {
		live := &workloadsv1alpha2.RoleInstance{}
		require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(instance), live))
		status := &workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: live.Generation}
		require.NoError(t, updater.UpdateInstanceStatus(ctx, live, status, nil))
	}
	update()
	require.Positive(t, counter.writes, "initial component-status projection should be written")
	writes := counter.writes
	update()
	require.Equal(t, writes, counter.writes, "unchanged exhausted conditions must not rewrite status every reconcile")
}

func TestStaleStatusDoesNotRollBackObservedGeneration(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	stale := instance.DeepCopy()
	instance.Status.ObservedGeneration = 5
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithStatusSubresource(instance).WithObjects(instance).Build()
	updater := &realStatusUpdater{Client: c, apiReader: c}
	require.NoError(t, updater.updateStatus(ctx, stale, &workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: 1}))
	live := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), live))
	require.EqualValues(t, 5, live.Status.ObservedGeneration)
	// Different RI UID at the same name must never receive the old object's status.
	stale.UID = "old-uid"
	require.NoError(t, updater.updateStatus(ctx, stale, &workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: 9, LastRestartTime: &metav1.Time{}}))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), live))
	require.EqualValues(t, 5, live.Status.ObservedGeneration)
}
