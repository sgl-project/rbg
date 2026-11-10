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
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	synccontrol "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/sync"
)

// Count optimistic patches as well as Updates in the existing budget convergence
// fixture; the status writer now uses RV-checked patches.
func (c *budgetCountingStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	c.owner.writes++
	return c.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

func TestStatusConflictRereadsLiveGateAndBudget(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithStatusSubresource(instance).WithObjects(instance).Build()
	cached := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), cached))
	live := cached.DeepCopy()
	for i := range live.Status.Conditions {
		live.Status.Conditions[i].Status = v1.ConditionFalse
	}
	live.Status.RetiringPods = []workloadsv1alpha2.RoleInstancePodReference{{Name: "old", UID: "old-uid"}}
	live.Status.RestartCount = 7
	live.Status.GroupRestartCount = 3
	live.Status.RestartBudgetExhausted = true
	require.NoError(t, c.Status().Update(ctx, live))
	cacheGets, apiGets, attempts := 0, 0, 0
	cachedClient := interceptor.NewClient(c, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if ri, ok := obj.(*workloadsv1alpha2.RoleInstance); ok {
				cacheGets++
				*ri = *cached.DeepCopy()
				return nil
			}
			return c.Get(ctx, key, obj, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			attempts++
			data, err := patch.Data(obj)
			require.NoError(t, err)
			var body struct {
				Metadata struct {
					ResourceVersion string `json:"resourceVersion"`
				} `json:"metadata"`
			}
			require.NoError(t, json.Unmarshal(data, &body))
			if attempts == 1 {
				assert.Equal(t, cached.ResourceVersion, body.Metadata.ResourceVersion, "CAS must snapshot the cached RV, not the reconciler's later live read")
			}
			err = c.SubResource(sub).Patch(ctx, obj, patch, opts...)
			if attempts == 1 {
				assert.True(t, apierrors.IsConflict(err), "stale status must conflict, not roll back live status")
			}
			return err
		},
	})
	reader := interceptor.NewClient(c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		apiGets++
		return c.Get(ctx, key, obj, opts...)
	}})
	computed := cached.Status.DeepCopy()
	computed.ObservedGeneration = cached.Generation
	computed.Conditions = []workloadsv1alpha2.RoleInstanceCondition{{Type: workloadsv1alpha2.RoleInstanceReady, Status: v1.ConditionTrue}}
	updater := &realStatusUpdater{Client: cachedClient, apiReader: reader}
	require.NoError(t, updater.updateStatus(ctx, cached, computed))
	assert.Equal(t, 1, cacheGets)
	assert.Equal(t, 1, apiGets)
	assert.Equal(t, 2, attempts)
	actual := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), actual))
	assert.Equal(t, v1.ConditionFalse, findInstanceCondition(actual.Status.Conditions, workloadsv1alpha2.RoleInstanceInPlaceUpdateReady).Status)
	assert.Equal(t, "GroupRestartBudgetExceeded", findInstanceCondition(actual.Status.Conditions, workloadsv1alpha2.RoleInstanceReady).Reason)
	assert.Equal(t, live.Status.RetiringPods, actual.Status.RetiringPods)
	assert.Equal(t, live.Status.GroupRestartCount, actual.Status.GroupRestartCount)
	assert.Equal(t, live.Status.RestartCount, actual.Status.RestartCount)
}

func TestExhaustedBudgetOverridesComputedDrainReady(t *testing.T) {
	instance := newReadinessTestInstance()
	instance.Status.RestartBudgetExhausted = true
	instance.Status.RetiringPods = []workloadsv1alpha2.RoleInstancePodReference{{Name: "old", UID: "old-uid"}}
	instance.Spec.ReadyPolicy = workloadsv1alpha2.RoleInstanceReadyPolicyTypeNone
	updater := &realStatusUpdater{}
	computed := &workloadsv1alpha2.RoleInstanceStatus{}
	updater.setInstanceConditions(instance, computed, nil)
	ready := findInstanceCondition(computed.Conditions, workloadsv1alpha2.RoleInstanceReady)
	require.NotNil(t, ready)
	assert.Equal(t, v1.ConditionFalse, ready.Status)
	assert.Equal(t, "GroupRestartBudgetExceeded", ready.Reason)
	require.NotNil(t, findInstanceCondition(computed.Conditions, workloadsv1alpha2.RoleInstanceRestarting))
	instance.Status.Conditions = computed.Conditions
	again := &workloadsv1alpha2.RoleInstanceStatus{}
	updater.setInstanceConditions(instance, again, nil)
	assert.False(t, inconsistentCondition(computed.Conditions, again.Conditions), "budget and drain projections must converge")
}

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

func TestStatusPreservesPersistedRestartList(t *testing.T) {
	for _, state := range []string{"deleting", "completed", "count reset"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			cached := newReadinessTestInstance()
			oldTime := metav1.NewTime(time.Now().Add(-time.Minute))
			cached.Status.RestartCount = 5
			cached.Status.LastRestartTime = &oldTime
			cached.Status.RetiringPods = []workloadsv1alpha2.RoleInstancePodReference{{Name: "old-pod", UID: "old"}}
			cached.Status.InPlaceUpdateContainerBaselines = map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"old-pod": {"main": {RestartCount: 0, ImageID: "old-image"}},
			}
			cached.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
				{Type: workloadsv1alpha2.RoleInstanceReady, Status: v1.ConditionTrue},
				{Type: workloadsv1alpha2.RoleInstanceRestarting, Status: v1.ConditionTrue},
				{Type: workloadsv1alpha2.RoleInstanceCustomReady, Status: v1.ConditionTrue},
			}
			live := cached.DeepCopy()
			live.Status.RestartCount = 6
			live.Status.LastRestartTime = nil
			live.Status.InPlaceUpdateContainerBaselines = nil
			live.Status.Conditions[2].Status = v1.ConditionFalse
			if state != "deleting" {
				live.Status.RetiringPods = nil
				live.Status.Conditions = append(live.Status.Conditions[:1], live.Status.Conditions[2:]...)
			}
			if state == "count reset" {
				live.Status.RestartCount = 1
			}
			c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
				WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).WithObjects(live).Build()
			updater := &realStatusUpdater{Client: c, apiReader: c}
			computed := cached.Status.DeepCopy()
			computed.ObservedGeneration = cached.Generation
			require.NoError(t, updater.updateStatus(ctx, cached, computed))
			actual := &workloadsv1alpha2.RoleInstance{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(live), actual))
			assert.Equal(t, live.Status.RetiringPods, actual.Status.RetiringPods)
			assert.Equal(t, live.Status.RestartCount, actual.Status.RestartCount)
			assert.Nil(t, actual.Status.LastRestartTime, "preserve an explicit fresh clear")
			assert.NotContains(t, actual.Status.InPlaceUpdateContainerBaselines, "old-pod")
			counts := map[workloadsv1alpha2.RoleInstanceConditionType]int{}
			for _, condition := range actual.Status.Conditions {
				counts[condition.Type]++
				if condition.Type == workloadsv1alpha2.RoleInstanceCustomReady {
					assert.Equal(t, v1.ConditionFalse, condition.Status, "preserve the external writer")
				}
				if state == "deleting" && condition.Type == workloadsv1alpha2.RoleInstanceReady {
					assert.Equal(t, v1.ConditionFalse, condition.Status)
				}
			}
			assert.Equal(t, 1, counts[workloadsv1alpha2.RoleInstanceCustomReady])
			if state == "deleting" {
				assert.Equal(t, 1, counts[workloadsv1alpha2.RoleInstanceRestarting])
			} else {
				assert.Zero(t, counts[workloadsv1alpha2.RoleInstanceRestarting])
			}
		})
	}
}

func TestStatusPreservesAppliedBaselineAcrossGenerationChange(t *testing.T) {
	for _, state := range []string{"same pod", "newer observed generation", "replacement pod", "newer update", "retiring", "newer baseline"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			cached := newReadinessTestInstance()
			cached.Status.ObservedGeneration = cached.Generation
			cached.Status.CurrentRevision = "revision-1"
			cached.Status.UpdateRevision = "revision-2"
			pod := newReadinessTestPod(cached, "router")
			pod.UID = "updated-pod"
			pod.Labels = map[string]string{apps.ControllerRevisionHashLabelKey: "revision-2"}
			livePod := pod.DeepCopy()
			live := cached.DeepCopy()
			live.Generation++
			*live.Spec.Components[0].Size = 2 // size-only changes do not change the Pod revision
			computed := cached.Status.DeepCopy()
			computed.CurrentRevision = "revision-2"
			computed.InPlaceUpdateContainerBaselines = map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				pod.Name: {"main": {RestartCount: 0, ImageID: "old-image"}},
			}
			switch state {
			case "newer observed generation":
				live.Status.ObservedGeneration = live.Generation
			case "replacement pod":
				livePod.UID = "replacement-uid"
			case "newer update":
				livePod.Labels[apps.ControllerRevisionHashLabelKey] = "revision-3"
			case "retiring":
				live.Status.RetiringPods = []workloadsv1alpha2.RoleInstancePodReference{{Name: pod.Name, UID: pod.UID}}
			case "newer baseline":
				live.Status.InPlaceUpdateContainerBaselines = map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
					pod.Name: {"main": {RestartCount: 2, ImageID: "newer-image"}},
				}
			}
			c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
				WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).WithObjects(live, livePod).Build()
			updater := &realStatusUpdater{Client: c, apiReader: c}
			require.NoError(t, updater.updateStatus(ctx, cached, computed, pod))
			actual := &workloadsv1alpha2.RoleInstance{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(live), actual))
			assert.Equal(t, live.Status.ObservedGeneration, actual.Status.ObservedGeneration,
				"do not acknowledge a spec that this reconcile did not process")
			assert.Equal(t, live.Status.CurrentRevision, actual.Status.CurrentRevision,
				"do not write stale aggregate status")
			if state == "same pod" || state == "newer observed generation" {
				assert.Equal(t, computed.InPlaceUpdateContainerBaselines, actual.Status.InPlaceUpdateContainerBaselines,
					"preserve bookkeeping for an already-applied image update")
			} else {
				assert.Equal(t, live.Status.InPlaceUpdateContainerBaselines, actual.Status.InPlaceUpdateContainerBaselines,
					"do not restore allowances for another incarnation or newer operation")
			}
		})
	}
}

func findInstanceCondition(conditions []workloadsv1alpha2.RoleInstanceCondition, conditionType workloadsv1alpha2.RoleInstanceConditionType) *workloadsv1alpha2.RoleInstanceCondition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// TestDrainStatusConvergesWithoutRewrites pins cheyang's review note: while a
// drain lasts, the computed status and the persisted status have to match,
// otherwise inconsistentStatus stays true and every reconcile issues another
// status write (and the reconcile that its own watch event enqueues).
func TestDrainStatusConvergesWithoutRewrites(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.RetiringPods = []workloadsv1alpha2.RoleInstancePodReference{{Name: "router-0", UID: "old-uid"}}
	instance.Status.Conditions = append(instance.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{
		Type:               workloadsv1alpha2.RoleInstanceReady,
		Status:             v1.ConditionFalse,
		Reason:             "Restarting",
		Message:            "Removing the previous member Pods",
		LastTransitionTime: metav1.NewTime(time.Now()),
	})

	var writes int32
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(instance.DeepCopy()).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, subResource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				atomic.AddInt32(&writes, 1)
				return c.SubResource(subResource).Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	var apiGets int
	reader := interceptor.NewClient(c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		apiGets++
		return c.Get(ctx, key, obj, opts...)
	}})
	updater := &realStatusUpdater{Client: c, apiReader: reader}

	current := &workloadsv1alpha2.RoleInstance{}
	for i := 0; i < 3; i++ {
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), current))
		require.NoError(t, updater.UpdateInstanceStatus(ctx, current,
			&workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: current.Generation}, nil))
	}

	assert.Zero(t, apiGets, "a steady drain's status projection requires no API reads")
	assert.Equal(t, int32(1), atomic.LoadInt32(&writes),
		"an unchanged drain must be written once, not once per reconcile")
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), current))
	ready := findInstanceCondition(current.Status.Conditions, workloadsv1alpha2.RoleInstanceReady)
	require.NotNil(t, ready)
	assert.Equal(t, v1.ConditionFalse, ready.Status)
	assert.Equal(t, "Restarting", ready.Reason, "the drain keeps owning the Ready reason")
}

// TestStatusDoesNotPublishReadyAgainstLiveGate pins diw-zw's review note: Ready
// is computed from the informer snapshot, so a readiness gate that another
// controller flipped to False between the snapshot and the live read must not be
// published next to Ready=True.
func TestStatusDoesNotPublishReadyAgainstLiveGate(t *testing.T) {
	ctx := context.Background()
	cached := newReadinessTestInstance()
	live := cached.DeepCopy()
	for i := range live.Status.Conditions {
		if live.Status.Conditions[i].Type == workloadsv1alpha2.RoleInstanceInPlaceUpdateReady {
			live.Status.Conditions[i].Status = v1.ConditionFalse
			live.Status.Conditions[i].Message = "in-place update in progress"
		}
	}
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(live,
			newReadinessTestPod(cached, "router"),
			newReadinessTestPod(cached, "sidecar"),
		).Build()
	updater := &realStatusUpdater{Client: c, apiReader: c}

	// The cached snapshot says the gate is satisfied and every Pod is ready, so an
	// unguarded compute would publish Ready=True.
	require.NoError(t, updater.UpdateInstanceStatus(ctx, cached,
		&workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: cached.Generation}, nil))

	persisted := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(live), persisted))
	ready := findInstanceCondition(persisted.Status.Conditions, workloadsv1alpha2.RoleInstanceReady)
	require.NotNil(t, ready)
	assert.Equal(t, v1.ConditionFalse, ready.Status)
	assert.Equal(t, "InplaceUpdateNotReady", ready.Reason)
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
