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
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	revisioncontrol "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/revision"
	synccontrol "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/sync"
	"sigs.k8s.io/rbgs/pkg/utils/fieldindex"
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

// TestLastReadyGenerationIsWrittenAndMonotonic pins the two write-path rules the
// readiness latch depends on: a latch-only change must trigger a status write, and a
// stale informer snapshot must never move an already persisted latch backwards.
func TestLastReadyGenerationIsWrittenAndMonotonic(t *testing.T) {
	instance := newReadinessTestInstance()
	instance.Status.LastReadyGeneration = 3
	instance.Generation = 4
	updater := &realStatusUpdater{}

	// A latch advance with nothing else changed still counts as a status change.
	assert.True(t, updater.inconsistentStatus(instance, &workloadsv1alpha2.RoleInstanceStatus{
		ObservedGeneration:  instance.Status.ObservedGeneration,
		LabelSelector:       instance.Status.LabelSelector,
		CurrentRevision:     instance.Status.CurrentRevision,
		UpdateRevision:      instance.Status.UpdateRevision,
		RestartCount:        instance.Status.RestartCount,
		LastRestartTime:     instance.Status.LastRestartTime,
		LastReadyGeneration: 4,
		ComponentStatuses:   instance.Status.ComponentStatuses,
		Conditions:          instance.Status.Conditions,
	}))

	// The write path keeps the live latch when the computed one is older.
	live := instance.DeepCopy()
	live.Status.LastReadyGeneration = 4
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).WithObjects(live).Build()
	updater = &realStatusUpdater{Client: c}
	cached := instance.DeepCopy()
	cached.Status.LastReadyGeneration = 3
	computed := cached.Status.DeepCopy()
	computed.ObservedGeneration = cached.Generation
	computed.LastReadyGeneration = 3
	require.NoError(t, updater.updateStatus(context.Background(), cached, computed))

	current := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(live), current))
	assert.EqualValues(t, 4, current.Status.LastReadyGeneration,
		"a stale snapshot must not roll the latch back")
}

func TestRestartBookkeepingForAlreadyDeletingMembers(t *testing.T) {
	for _, state := range []string{"terminating", "not found"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			instance := newReadinessTestInstance()
			instance.Spec.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{
				Type:          workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
				ActivateAfter: workloadsv1alpha2.RestartPolicyActivateAfterCreated,
			}
			instance.Status.ObservedGeneration = instance.Generation
			revisions := revisioncontrol.NewRevisionControl()
			revision, err := revisions.NewRevision(instance, 1, new(int32))
			require.NoError(t, err)
			instance.Status.CurrentRevision = revision.Name
			instance.Status.UpdateRevision = revision.Name
			pods := []*v1.Pod{newReadinessTestPod(instance, "router"), newReadinessTestPod(instance, "sidecar")}
			objects := []client.Object{instance}
			for i, pod := range pods {
				pod.UID = types.UID(fmt.Sprintf("old-%d", i))
				if state == "terminating" {
					pod.DeletionTimestamp = ptr.To(metav1.Now())
					pod.Finalizers = []string{"test.rbg.io/hold"}
					objects = append(objects, pod)
				} else {
					pod.Status.Phase = v1.PodFailed
				}
			}
			c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithStatusSubresource(instance).WithObjects(objects...).Build()
			control := synccontrol.New(c, c, record.NewFakeRecorder(20), nil)
			t.Cleanup(func() { control.ClearRestarting(instance) })
			r := &reconciler{Client: c, revisionControl: revisions, syncControl: control}
			computed := &workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: instance.Generation}
			result := r.syncInstance(ctx, instance, computed, revision, revision, []*apps.ControllerRevision{revision}, nil, pods)
			require.NoError(t, result.err)
			require.EqualValues(t, 1, computed.RestartCount, "already absent/deleting Pods must not lose the accepted restart")
			require.NotNil(t, computed.LastRestartTime)
			require.True(t, slices.ContainsFunc(computed.Conditions, func(condition workloadsv1alpha2.RoleInstanceCondition) bool {
				return condition.Type == workloadsv1alpha2.RoleInstanceRestarting && condition.Status == v1.ConditionTrue
			}))
			require.NoError(t, (&realStatusUpdater{Client: c}).updateStatus(ctx, instance, computed))
			live := &workloadsv1alpha2.RoleInstance{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), live))
			require.EqualValues(t, 1, live.Status.RestartCount)
			require.NotNil(t, live.Status.LastRestartTime)
			require.True(t, slices.ContainsFunc(live.Status.Conditions, func(condition workloadsv1alpha2.RoleInstanceCondition) bool {
				return condition.Type == workloadsv1alpha2.RoleInstanceRestarting && condition.Status == v1.ConditionTrue
			}))
		})
	}
}

// TestRestartBookkeepingWhenFirstDeleteFails covers the wave-start accounting
// when the first DELETE of a new wave fails: the reconcile must still propagate
// RestartCount/LastRestartTime together with Restarting=True, otherwise backoff
// and the marker-less fallback key on stale tracking values.
func TestRestartBookkeepingWhenFirstDeleteFails(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Spec.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{
		Type:          workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
		ActivateAfter: workloadsv1alpha2.RestartPolicyActivateAfterCreated,
	}
	instance.Status.ObservedGeneration = instance.Generation
	revisions := revisioncontrol.NewRevisionControl()
	revision, err := revisions.NewRevision(instance, 1, new(int32))
	require.NoError(t, err)
	instance.Status.CurrentRevision = revision.Name
	instance.Status.UpdateRevision = revision.Name
	pods := []*v1.Pod{newReadinessTestPod(instance, "router"), newReadinessTestPod(instance, "sidecar")}
	objects := make([]client.Object, 0, 1+len(pods))
	objects = append(objects, instance)
	for i, pod := range pods {
		pod.UID = types.UID(fmt.Sprintf("old-%d", i))
		// Container-restart trigger while the Pods are still Running/Ready.
		pod.Status.ContainerStatuses = []v1.ContainerStatus{{Name: "main", RestartCount: 1}}
		objects = append(objects, pod)
	}
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(instance).WithObjects(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, cc client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				return apierrors.NewConflict(
					schema.GroupResource{Resource: "pods"}, obj.GetName(), fmt.Errorf("transient conflict"))
			},
		}).Build()
	control := synccontrol.New(c, c, record.NewFakeRecorder(20), nil)
	t.Cleanup(func() { control.ClearRestarting(instance) })
	r := &reconciler{Client: c, revisionControl: revisions, syncControl: control}
	computed := &workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: instance.Generation}
	result := r.syncInstance(ctx, instance, computed, revision, revision, []*apps.ControllerRevision{revision}, nil, pods)
	require.ErrorContains(t, result.err, "transient conflict")
	require.EqualValues(t, 1, computed.RestartCount,
		"the wave must be counted even when its first DELETE fails")
	require.NotNil(t, computed.LastRestartTime)
	require.True(t, slices.ContainsFunc(computed.Conditions, func(condition workloadsv1alpha2.RoleInstanceCondition) bool {
		return condition.Type == workloadsv1alpha2.RoleInstanceRestarting && condition.Status == v1.ConditionTrue
	}))
	require.NoError(t, (&realStatusUpdater{Client: c}).updateStatus(ctx, instance, computed))
	live := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), live))
	require.EqualValues(t, 1, live.Status.RestartCount,
		"the API must persist the wave's restart accounting")
	require.NotNil(t, live.Status.LastRestartTime)
	require.True(t, slices.ContainsFunc(live.Status.Conditions, func(condition workloadsv1alpha2.RoleInstanceCondition) bool {
		return condition.Type == workloadsv1alpha2.RoleInstanceRestarting && condition.Status == v1.ConditionTrue
	}))
}

func TestAuxiliaryOwnerReferencesDoNotBlockMemberRepair(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Spec.RestartPolicy = workloadsv1alpha2.RestartPolicyNone //nolint:staticcheck // legacy policy still supported
	for i := range instance.Spec.Components {
		instance.Spec.Components[i].Template.Spec.Containers = []v1.Container{{Name: "main", Image: "nginx:1.27"}}
	}
	member := newReadinessTestPod(instance, "router")
	member.Labels = map[string]string{constants.ComponentNameLabelKey: "router", constants.ComponentIDLabelKey: "0"}
	activeAux := newReadinessTestPod(instance, "router")
	activeAux.Name = "active-auxiliary"
	activeAux.OwnerReferences[0].Controller = ptr.To(false)
	failedAux := activeAux.DeepCopy()
	failedAux.Name = "failed-auxiliary"
	failedAux.Status.Phase = v1.PodFailed
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithObjects(instance, member, activeAux, failedAux).
		WithIndex(&v1.Pod{}, fieldindex.IndexNameForOwnerRefUID, func(obj client.Object) []string {
			keys := make([]string, 0, len(obj.GetOwnerReferences()))
			for _, ref := range obj.GetOwnerReferences() {
				keys = append(keys, string(ref.UID))
			}
			return keys
		}).Build()
	r := &reconciler{Client: c}
	active, inactive, err := r.getOwnedPods(ctx, instance)
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, member.Name, active[0].Name)
	require.Empty(t, inactive)
	control := synccontrol.New(c, c, record.NewFakeRecorder(20), nil)
	revision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "revision-1"}}
	handled, _, err := control.Scale(ctx, instance, revision, revision, nil, active, inactive)
	require.NoError(t, err)
	require.True(t, handled)
	replacement := &v1.Pod{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(newReadinessTestPod(instance, "sidecar")), replacement))
	require.True(t, metav1.IsControlledBy(replacement, instance))
	for _, auxiliary := range []*v1.Pod{activeAux, failedAux} {
		live := &v1.Pod{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(auxiliary), live))
		require.Nil(t, live.DeletionTimestamp)
	}
}

func TestReadinessLatchAcquiredAndCarriedAcrossStatusCalculations(t *testing.T) {
	instance := newReadinessTestInstance()
	pods := []*v1.Pod{newReadinessTestPod(instance, "router"), newReadinessTestPod(instance, "sidecar")}
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithObjects(pods[0], pods[1]).Build()
	updater := &realStatusUpdater{Client: c}
	unselected := &workloadsv1alpha2.RoleInstanceStatus{}
	updater.setInstanceConditions(instance, unselected, nil)
	require.Zero(t, unselected.LastReadyGeneration, "no selected target revision means no stability latch")
	ready := &workloadsv1alpha2.RoleInstanceStatus{CurrentRevision: "r1", UpdateRevision: "r1"}
	updater.setInstanceConditions(instance, ready, nil)
	require.Equal(t, instance.Generation, ready.LastReadyGeneration)
	instance.Status = *ready
	require.NoError(t, c.Delete(context.Background(), pods[0]))
	notReady := &workloadsv1alpha2.RoleInstanceStatus{
		CurrentRevision: "r1", UpdateRevision: "r1", LastReadyGeneration: instance.Status.LastReadyGeneration,
	}
	updater.setInstanceConditions(instance, notReady, nil)
	require.Equal(t, instance.Generation, notReady.LastReadyGeneration)
	require.True(t, slices.ContainsFunc(notReady.Conditions, func(condition workloadsv1alpha2.RoleInstanceCondition) bool {
		return condition.Type == workloadsv1alpha2.RoleInstanceReady && condition.Status == v1.ConditionFalse
	}))
	instance.Generation++
	changed := &workloadsv1alpha2.RoleInstanceStatus{
		CurrentRevision: "r1", UpdateRevision: "r1", LastReadyGeneration: notReady.LastReadyGeneration,
	}
	updater.setInstanceConditions(instance, changed, nil)
	require.Less(t, changed.LastReadyGeneration, instance.Generation, "the old latch must not arm the new generation")
	require.NoError(t, c.Create(context.Background(), newReadinessTestPod(instance, "router")))
	updater.setInstanceConditions(instance, changed, nil)
	require.Equal(t, instance.Generation, changed.LastReadyGeneration, "advance only after the new spec is ready")
}

func TestReadinessLatchRequiresReadyTargetRevision(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Generation = 2
	instance.Spec.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart}
	instance.Status.LastReadyGeneration = 1
	pods := []*v1.Pod{newReadinessTestPod(instance, "router"), newReadinessTestPod(instance, "sidecar")}
	for i, pod := range pods {
		pod.Labels = map[string]string{
			apps.ControllerRevisionHashLabelKey: "old",
			constants.ComponentNameLabelKey:     instance.Spec.Components[i].Name,
			constants.ComponentIDLabelKey:       "0",
		}
	}
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(instance, &v1.Pod{}).WithObjects(instance, pods[0], pods[1]).Build()
	updater := &realStatusUpdater{Client: c}
	status := &workloadsv1alpha2.RoleInstanceStatus{
		ObservedGeneration: instance.Generation, CurrentRevision: "old", UpdateRevision: "new", LastReadyGeneration: 1,
	}
	updater.calculateStatus(instance, status, nil, pods)
	require.Equal(t, "old", status.CurrentRevision)
	require.EqualValues(t, 1, status.LastReadyGeneration, "old-revision readiness must not arm the new generation")
	instance.Status = *status.DeepCopy()

	// Target-revision labels can converge before application readiness recovers.
	for _, pod := range pods {
		pod.Labels[apps.ControllerRevisionHashLabelKey] = "new"
		require.NoError(t, c.Update(ctx, pod))
		pod.Status.Conditions[0].Status = v1.ConditionFalse
		require.NoError(t, c.Status().Update(ctx, pod))
	}
	updater.calculateStatus(instance, status, nil, pods)
	require.Equal(t, "new", status.CurrentRevision)
	require.EqualValues(t, 1, status.LastReadyGeneration, "revision convergence alone is not readiness")
	instance.Status = *status.DeepCopy()
	require.NoError(t, c.Status().Update(ctx, instance))
	pods[0].Status.ContainerStatuses = []v1.ContainerStatus{{Name: "main", RestartCount: 1}}
	control := synccontrol.New(c, c, record.NewFakeRecorder(20), nil)
	t.Cleanup(func() { control.ClearRestarting(instance) })
	revision := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "new"}}
	handled, _, err := control.Scale(ctx, instance, revision, revision, nil, pods, nil)
	require.NoError(t, err)
	require.False(t, handled, "the target generation has never been Ready, so its restart must not rebuild the group")
	require.Zero(t, instance.Status.RestartCount)

	for _, pod := range pods {
		pod.Status.ContainerStatuses = nil
		pod.Status.Conditions[0].Status = v1.ConditionTrue
		require.NoError(t, c.Status().Update(ctx, pod))
	}
	updater.calculateStatus(instance, status, nil, pods)
	require.Equal(t, instance.Generation, status.LastReadyGeneration, "latch once the target revision actually becomes Ready")
}

func TestRestartingConditionLifecycleWithReadiness(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	pods := []*v1.Pod{newReadinessTestPod(instance, "router"), newReadinessTestPod(instance, "sidecar")}
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithObjects(pods[0], pods[1]).Build()
	updater := &realStatusUpdater{Client: c}
	hasRestarting := func(status *workloadsv1alpha2.RoleInstanceStatus, condStatus v1.ConditionStatus) bool {
		return slices.ContainsFunc(status.Conditions, func(condition workloadsv1alpha2.RoleInstanceCondition) bool {
			return condition.Type == workloadsv1alpha2.RoleInstanceRestarting && condition.Status == condStatus
		})
	}

	// A wave that starts while the pod snapshot is still Ready (for example a
	// container-restart trigger) must keep its in-progress condition.
	inProgress := &workloadsv1alpha2.RoleInstanceStatus{CurrentRevision: "r1", UpdateRevision: "r1"}
	updater.setInstanceConditions(instance, inProgress, []workloadsv1alpha2.RoleInstanceCondition{
		{Type: workloadsv1alpha2.RoleInstanceRestarting, Status: v1.ConditionTrue, Reason: "RestartPolicyTriggered"},
	})
	require.True(t, hasRestarting(inProgress, v1.ConditionTrue),
		"an in-progress wave must survive a Ready pod snapshot")
	instance.Status = *inProgress

	// A completed wave is dropped once the instance is Ready.
	completed := &workloadsv1alpha2.RoleInstanceStatus{
		CurrentRevision: "r1", UpdateRevision: "r1", LastReadyGeneration: instance.Status.LastReadyGeneration,
	}
	updater.setInstanceConditions(instance, completed, []workloadsv1alpha2.RoleInstanceCondition{
		{Type: workloadsv1alpha2.RoleInstanceRestarting, Status: v1.ConditionFalse, Reason: "RestartCompleted"},
	})
	require.False(t, hasRestarting(completed, v1.ConditionFalse),
		"a completed wave must be dropped when the instance is Ready")

	// A completed wave persists while the instance is not Ready.
	instance.Status = *completed
	instance.Status.Conditions = append(instance.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{
		Type: workloadsv1alpha2.RoleInstanceRestarting, Status: v1.ConditionFalse, Reason: "RestartCompleted",
	})
	require.NoError(t, c.Delete(ctx, pods[0]))
	notReady := &workloadsv1alpha2.RoleInstanceStatus{
		CurrentRevision: "r1", UpdateRevision: "r1", LastReadyGeneration: instance.Status.LastReadyGeneration,
	}
	updater.setInstanceConditions(instance, notReady, nil)
	require.True(t, hasRestarting(notReady, v1.ConditionFalse),
		"a completed wave must persist while the instance is not Ready")
}

// TestUpdateStatusPersistsRestartingCompletion verifies that a condition-only
// wave completion (Restarting True → False/RestartCompleted, with no other
// status change) still triggers an API status write.
func TestUpdateStatusPersistsRestartingCompletion(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.Conditions = append(instance.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{
		Type:   workloadsv1alpha2.RoleInstanceRestarting,
		Status: v1.ConditionTrue,
		Reason: "RestartPolicyTriggered",
	})
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(instance).WithObjects(instance).Build()
	updater := &realStatusUpdater{Client: c}

	completed := instance.Status.DeepCopy()
	completed.Conditions[len(completed.Conditions)-1] = workloadsv1alpha2.RoleInstanceCondition{
		Type:   workloadsv1alpha2.RoleInstanceRestarting,
		Status: v1.ConditionFalse,
		Reason: "RestartCompleted",
	}
	require.NoError(t, updater.updateStatus(ctx, instance, completed))

	live := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), live))
	require.True(t, slices.ContainsFunc(live.Status.Conditions, func(cond workloadsv1alpha2.RoleInstanceCondition) bool {
		return cond.Type == workloadsv1alpha2.RoleInstanceRestarting &&
			cond.Status == v1.ConditionFalse &&
			cond.Reason == "RestartCompleted"
	}), "a condition-only wave completion must be persisted")
}
