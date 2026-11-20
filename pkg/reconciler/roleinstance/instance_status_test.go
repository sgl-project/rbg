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
	stderrors "errors"
	"fmt"
	"slices"
	"strings"
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
	// The KEP-502 operation-failure path emits an edge-triggered Warning Event;
	// wire a recorder so the failed delete exercises the real code path.
	r := &reconciler{Client: c, revisionControl: revisions, syncControl: control, recorder: record.NewFakeRecorder(20)}
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

func TestSameNameForeignPodConflictIsNotABlocker(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Spec.Components = instance.Spec.Components[:1]
	instance.Status.ObservedGeneration = instance.Generation
	revisions := revisioncontrol.NewRevisionControl()
	revision, err := revisions.NewRevision(instance, 1, new(int32))
	require.NoError(t, err)
	instance.Status.CurrentRevision = revision.Name
	instance.Status.UpdateRevision = revision.Name

	// A same-name Pod left behind by the previous instance UID while garbage
	// collection is still pending: this is the recreate flow racing GC, and it
	// must not flash a blocker condition at RI, RIS, or RBG.
	foreign := newReadinessTestPod(instance, "router")
	foreign.OwnerReferences[0].UID = "previous-instance-uid"
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(instance).WithObjects(instance, foreign).Build()
	control := synccontrol.New(c, c, record.NewFakeRecorder(20), nil)
	t.Cleanup(func() { control.ClearRestarting(instance) })
	r := &reconciler{Client: c, revisionControl: revisions, syncControl: control, recorder: record.NewFakeRecorder(20)}

	computed := &workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: instance.Generation}
	result := r.syncInstance(ctx, instance, computed, revision, revision, []*apps.ControllerRevision{revision}, nil, nil)
	require.ErrorContains(t, result.err, "awaiting garbage collection")
	var marker *workloadsv1alpha2.RoleInstanceCondition
	for i := range computed.Conditions {
		condition := &computed.Conditions[i]
		if condition.Type == workloadsv1alpha2.RoleInstanceProgressing && condition.Status == v1.ConditionTrue {
			marker = condition
		}
	}
	// The race is not a blocker, but a durable marker is persisted: it
	// deduplicates the identical Warning Events of repeated retries.
	require.NotNil(t, marker)
	require.Equal(t, workloadsv1alpha2.ReasonPodRecreateRace, marker.Reason)
	require.Contains(t, marker.Message, "awaiting garbage collection")
}

func TestGenuinelyForeignPodConflictPublishesBlocker(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Spec.Components = instance.Spec.Components[:1]
	instance.Status.ObservedGeneration = instance.Generation
	revisions := revisioncontrol.NewRevisionControl()
	revision, err := revisions.NewRevision(instance, 1, new(int32))
	require.NoError(t, err)
	instance.Status.CurrentRevision = revision.Name
	instance.Status.UpdateRevision = revision.Name

	// A plain Pod with the target name that is neither being deleted nor owned
	// by a previous incarnation of the instance: a genuine foreign conflict, not
	// the recreate race. It must stay visible as a current blocker.
	foreign := newReadinessTestPod(instance, "router")
	foreign.OwnerReferences = nil
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(instance).WithObjects(instance, foreign).Build()
	control := synccontrol.New(c, c, record.NewFakeRecorder(20), nil)
	t.Cleanup(func() { control.ClearRestarting(instance) })
	r := &reconciler{Client: c, revisionControl: revisions, syncControl: control, recorder: record.NewFakeRecorder(20)}

	computed := &workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: instance.Generation}
	result := r.syncInstance(ctx, instance, computed, revision, revision, []*apps.ControllerRevision{revision}, nil, nil)
	require.ErrorContains(t, result.err, "is not controlled by RoleInstance")
	var blocker *workloadsv1alpha2.RoleInstanceCondition
	for i := range computed.Conditions {
		condition := &computed.Conditions[i]
		if condition.Type == workloadsv1alpha2.RoleInstanceProgressing && condition.Status == v1.ConditionTrue {
			blocker = condition
		}
	}
	require.NotNil(t, blocker, "a genuinely foreign Pod must stay a published blocker")
	require.Equal(t, workloadsv1alpha2.ReasonPodOperationUnclassified, blocker.Reason)
}

func TestRecreateRaceMarkerDeduplicatesEvents(t *testing.T) {
	race := &synccontrol.RecreatePodConflictError{Pod: "default/ri-a-0", Instance: "default/ri-a", UID: "uid"}
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "ri-a", Namespace: "default", Generation: 2},
		Status: workloadsv1alpha2.RoleInstanceStatus{
			Conditions: []workloadsv1alpha2.RoleInstanceCondition{{
				Type: workloadsv1alpha2.RoleInstanceProgressing, Status: v1.ConditionTrue,
				Reason:  workloadsv1alpha2.ReasonPodRecreateRace,
				Message: race.Error(),
			}},
		},
	}
	recorder := record.NewFakeRecorder(1)
	r := &reconciler{recorder: recorder}

	// The persisted race marker carries the error message, so an identical
	// retry emits no second Warning Event.
	r.emitOperationFailureEvent(instance, workloadsv1alpha2.ReasonPodRecreateRace, race)
	select {
	case event := <-recorder.Events:
		t.Fatalf("identical retry must be deduplicated, got event %q", event)
	default:
	}

	// A different failure still emits.
	r.emitOperationFailureEvent(instance, workloadsv1alpha2.ReasonPodOperationUnclassified, stderrors.New("different failure"))
	select {
	case <-recorder.Events:
	default:
		t.Fatal("a different failure must emit an event")
	}
}

func TestMixedForeignConflictAndQuotaFailurePublishesBlocker(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = instance.Generation
	revisions := revisioncontrol.NewRevisionControl()
	revision, err := revisions.NewRevision(instance, 1, new(int32))
	require.NoError(t, err)
	instance.Status.CurrentRevision = revision.Name
	instance.Status.UpdateRevision = revision.Name

	// One component's target name is occupied by a Pod from the previous
	// instance UID, and creating the sibling component's Pod is rejected by
	// quota: the Scale aggregate mixes both. The quota blocker must still be
	// published, matching the classified Warning Event.
	foreign := newReadinessTestPod(instance, "router")
	foreign.OwnerReferences[0].UID = "previous-instance-uid"
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(instance).WithObjects(instance, foreign).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, cc client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if pod, ok := obj.(*v1.Pod); ok && strings.Contains(pod.Name, "sidecar") {
					return apierrors.NewForbidden(
						v1.Resource("pods"), pod.Name, apierrors.NewBadRequest("pods is forbidden: exceeded quota"))
				}
				return cc.Create(ctx, obj, opts...)
			},
		}).Build()
	control := synccontrol.New(c, c, record.NewFakeRecorder(20), nil)
	t.Cleanup(func() { control.ClearRestarting(instance) })
	r := &reconciler{Client: c, revisionControl: revisions, syncControl: control, recorder: record.NewFakeRecorder(20)}

	computed := &workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: instance.Generation}
	result := r.syncInstance(ctx, instance, computed, revision, revision, []*apps.ControllerRevision{revision}, nil, nil)
	require.ErrorContains(t, result.err, "exceeded quota")

	var blocker *workloadsv1alpha2.RoleInstanceCondition
	for i := range computed.Conditions {
		condition := &computed.Conditions[i]
		if condition.Type == workloadsv1alpha2.RoleInstanceProgressing && condition.Status == v1.ConditionTrue {
			blocker = condition
		}
	}
	require.NotNil(t, blocker, "a mixed aggregate must publish the classified blocker")
	require.Equal(t, workloadsv1alpha2.ReasonPodCreateQuotaExceeded, blocker.Reason)
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
	updater.setInstanceConditions(instance, unselected, nil, nil)
	require.Zero(t, unselected.LastReadyGeneration, "no selected target revision means no stability latch")
	ready := &workloadsv1alpha2.RoleInstanceStatus{CurrentRevision: "r1", UpdateRevision: "r1"}
	updater.setInstanceConditions(instance, ready, nil, nil)
	require.Equal(t, instance.Generation, ready.LastReadyGeneration)
	instance.Status = *ready
	require.NoError(t, c.Delete(context.Background(), pods[0]))
	notReady := &workloadsv1alpha2.RoleInstanceStatus{
		CurrentRevision: "r1", UpdateRevision: "r1", LastReadyGeneration: instance.Status.LastReadyGeneration,
	}
	updater.setInstanceConditions(instance, notReady, nil, nil)
	require.Equal(t, instance.Generation, notReady.LastReadyGeneration)
	require.True(t, slices.ContainsFunc(notReady.Conditions, func(condition workloadsv1alpha2.RoleInstanceCondition) bool {
		return condition.Type == workloadsv1alpha2.RoleInstanceReady && condition.Status == v1.ConditionFalse
	}))
	instance.Generation++
	changed := &workloadsv1alpha2.RoleInstanceStatus{
		CurrentRevision: "r1", UpdateRevision: "r1", LastReadyGeneration: notReady.LastReadyGeneration,
	}
	updater.setInstanceConditions(instance, changed, nil, nil)
	require.Less(t, changed.LastReadyGeneration, instance.Generation, "the old latch must not arm the new generation")
	require.NoError(t, c.Create(context.Background(), newReadinessTestPod(instance, "router")))
	updater.setInstanceConditions(instance, changed, nil, nil)
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
	}, pods)
	require.True(t, hasRestarting(inProgress, v1.ConditionTrue),
		"an in-progress wave must survive a Ready pod snapshot")
	instance.Status = *inProgress

	// A completed wave is dropped once the instance is Ready.
	completed := &workloadsv1alpha2.RoleInstanceStatus{
		CurrentRevision: "r1", UpdateRevision: "r1", LastReadyGeneration: instance.Status.LastReadyGeneration,
	}
	updater.setInstanceConditions(instance, completed, []workloadsv1alpha2.RoleInstanceCondition{
		{Type: workloadsv1alpha2.RoleInstanceRestarting, Status: v1.ConditionFalse, Reason: "RestartCompleted"},
	}, pods)
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
	updater.setInstanceConditions(instance, notReady, nil, pods)
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

func TestBudgetDrainStaleInformerStatusKeepsLiveGuard(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Spec.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{
		Type:             workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
		ActivateAfter:    workloadsv1alpha2.RestartPolicyActivateAfterCreated,
		MaxGroupRestarts: ptr.To[int32](2),
	}
	instance.Status.ObservedGeneration = instance.Generation
	revisionControl := revisioncontrol.NewRevisionControl()
	revision, err := revisionControl.NewRevision(instance, 1, ptr.To[int32](0))
	require.NoError(t, err)
	instance.Status.CurrentRevision = revision.Name
	instance.Status.UpdateRevision = revision.Name
	pods := []*v1.Pod{newReadinessTestPod(instance, "router"), newReadinessTestPod(instance, "sidecar")}
	for i, pod := range pods {
		pod.UID = types.UID(fmt.Sprintf("old-pod-%d", i))
		pod.Labels = map[string]string{
			constants.RoleInstanceNameLabelKey: instance.Name,
			constants.ComponentNameLabelKey:    instance.Spec.Components[i].Name,
			constants.ComponentIDLabelKey:      "0",
		}
		pod.Finalizers = []string{"test.rbg.io/hold-drain"}
	}
	pods[0].Status.Phase = v1.PodFailed
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(instance, &v1.Pod{}).WithObjects(instance, pods[0], pods[1]).Build()
	control := synccontrol.New(c, c, record.NewFakeRecorder(20), nil)
	control.ClearRestarting(instance)
	t.Cleanup(func() { control.ClearRestarting(instance) })
	stale := instance.DeepCopy()
	accepted := instance.DeepCopy()
	handled, _, err := control.Scale(ctx, accepted, revision, revision, nil, pods[1:], pods[:1])
	require.NoError(t, err)
	require.True(t, handled)

	// Run the next status computation with the old informer snapshot while
	// retained original Pods keep the accepted restart wave in progress.
	r := &reconciler{revisionControl: revisionControl, syncControl: control}
	computed := stale.Status.DeepCopy()
	result := r.syncInstance(ctx, stale, computed, revision, revision, nil, pods[1:], pods[:1])
	require.NoError(t, result.err)
	updater := &realStatusUpdater{Client: c, apiReader: c}
	updater.calculateStatus(stale, computed, slices.Clone(computed.Conditions), pods[1:])
	require.NoError(t, updater.updateStatus(ctx, stale, computed))
	live := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), live))
	require.EqualValues(t, 1, live.Status.GroupRestartCount)
	require.True(t, slices.ContainsFunc(live.Status.Conditions, func(cond workloadsv1alpha2.RoleInstanceCondition) bool {
		return cond.Type == workloadsv1alpha2.RoleInstanceRestarting && cond.Status == v1.ConditionTrue
	}), "a stale status computation must not erase the active budget wave")
}

func TestBudgetRestartCommitResponseLostKeepsGuard(t *testing.T) {
	for _, reject := range []bool{false, true} {
		for _, retryStatus := range []bool{false, true} {
			t.Run(fmt.Sprintf("reject=%t/statusRetry=%t", reject, retryStatus), func(t *testing.T) {
				ctx := context.Background()
				instance := newReadinessTestInstance()
				instance.Spec.ReadinessGates = nil
				instance.Spec.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{
					Type:             workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
					ActivateAfter:    workloadsv1alpha2.RestartPolicyActivateAfterCreated,
					MaxGroupRestarts: ptr.To[int32](1), BaseDelaySeconds: ptr.To[int32](0), MaxDelaySeconds: ptr.To[int32](0),
				}
				instance.Status.ObservedGeneration = instance.Generation
				revisions := revisioncontrol.NewRevisionControl()
				revision, err := revisions.NewRevision(instance, 1, ptr.To[int32](0))
				require.NoError(t, err)
				instance.Status.CurrentRevision, instance.Status.UpdateRevision = revision.Name, revision.Name
				pods := []*v1.Pod{newReadinessTestPod(instance, "router"), newReadinessTestPod(instance, "sidecar")}
				for i, pod := range pods {
					pod.UID = types.UID(fmt.Sprintf("original-%d", i))
					pod.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
					pod.Finalizers = []string{"test.rbg.io/hold-original"}
					pod.Labels = map[string]string{
						constants.RoleInstanceNameLabelKey: instance.Name,
						constants.ComponentNameLabelKey:    instance.Spec.Components[i].Name,
						constants.ComponentIDLabelKey:      "0",
					}
				}
				pods[0].Status.Phase = v1.PodFailed
				base := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithStatusSubresource(instance, &v1.Pod{}).
					WithObjects(instance, pods[0], pods[1]).Build()
				patches, deletes, statusWrites := 0, 0, 0
				cli := interceptor.NewClient(base, interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, inner client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						patches++
						if reject {
							return apierrors.NewConflict(schema.GroupResource{Resource: "roleinstances"}, obj.GetName(), fmt.Errorf("injected rejection"))
						}
						// Storage commits, but the caller never receives the successful response.
						require.NoError(t, inner.SubResource(sub).Patch(ctx, obj.DeepCopyObject().(client.Object), patch, opts...))
						return fmt.Errorf("injected lost commit response")
					},
					SubResourceUpdate: func(ctx context.Context, inner client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						statusWrites++
						if retryStatus && statusWrites == 1 {
							return apierrors.NewConflict(schema.GroupResource{Resource: "roleinstances"}, obj.GetName(), fmt.Errorf("injected status conflict"))
						}
						return inner.SubResource(sub).Update(ctx, obj, opts...)
					},
					Delete: func(ctx context.Context, inner client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						deletes++
						return inner.Delete(ctx, obj, opts...)
					},
				})
				control := synccontrol.New(cli, base, record.NewFakeRecorder(20), nil)
				control.ClearRestarting(instance)
				t.Cleanup(func() { control.ClearRestarting(instance) })
				r := &reconciler{revisionControl: revisions, syncControl: control, recorder: record.NewFakeRecorder(20)}
				computed := instance.Status.DeepCopy()
				result := r.syncInstance(ctx, instance, computed, revision, revision, nil, pods[1:], pods[:1])
				require.Error(t, result.err)
				require.Equal(t, 1, patches)
				require.Zero(t, deletes)
				getLive := func() *workloadsv1alpha2.RoleInstance {
					live := &workloadsv1alpha2.RoleInstance{}
					require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(instance), live))
					return live
				}
				committed := getLive()
				require.NoError(t, newStatusUpdater(cli, base).UpdateInstanceStatus(ctx, instance, computed, pods[1:]))
				live := getLive()
				require.Equal(t, committed.Status.GroupRestartCount, live.Status.GroupRestartCount)
				require.Equal(t, committed.Status.RestartCount, live.Status.RestartCount)
				require.Equal(t, committed.Status.LastRestartTime, live.Status.LastRestartTime)
				hasGuard := slices.ContainsFunc(live.Status.Conditions, func(cond workloadsv1alpha2.RoleInstanceCondition) bool {
					return cond.Type == workloadsv1alpha2.RoleInstanceRestarting && cond.Status == v1.ConditionTrue
				})
				if reject {
					require.Zero(t, live.Status.GroupRestartCount)
					require.False(t, hasGuard, "a rejected transaction must not acquire a guard")
					return
				}
				require.EqualValues(t, 1, live.Status.GroupRestartCount)
				require.True(t, hasGuard, "error-path status writing must preserve the committed restart intent")
				// With no in-memory marker, the next serialized reconcile resumes the
				// committed wave instead of exhausting the already-consumed budget.
				retryControl := synccontrol.New(base, base, record.NewFakeRecorder(20), nil)
				handled, _, err := retryControl.Scale(ctx, live, revision, revision, nil, pods[1:], pods[:1])
				require.NoError(t, err)
				require.True(t, handled)
				require.False(t, getLive().Status.RestartBudgetExhausted)
				require.EqualValues(t, 1, getLive().Status.GroupRestartCount)
				for _, original := range pods {
					pod := &v1.Pod{}
					require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(original), pod))
					require.Equal(t, original.UID, pod.UID)
					require.NotNil(t, pod.DeletionTimestamp)
				}
			})
		}
	}
}

func TestBudgetRestartConditionUsesPersistedTransaction(t *testing.T) {
	for _, tc := range []struct {
		name         string
		live, cached v1.ConditionStatus
		ready        bool
		want         v1.ConditionStatus
	}{
		{name: "accepted replaces cached completion", live: v1.ConditionTrue, cached: v1.ConditionFalse, want: v1.ConditionTrue},
		{name: "completion cannot be resurrected", live: v1.ConditionFalse, cached: v1.ConditionTrue, want: v1.ConditionFalse},
		{name: "recovery removed guard", cached: v1.ConditionTrue},
		{name: "ready drops completed guard", live: v1.ConditionFalse, cached: v1.ConditionFalse, ready: true},
		{name: "stale ready cannot erase accepted guard", live: v1.ConditionTrue, ready: true, want: v1.ConditionTrue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			instance := newReadinessTestInstance()
			instance.Status.GroupRestartRevision = "budget-revision"
			instance.Status.Conditions = nil
			if tc.live != "" {
				instance.Status.Conditions = append(instance.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{
					Type: workloadsv1alpha2.RoleInstanceRestarting, Status: tc.live,
				})
			}
			cli := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithStatusSubresource(instance).WithObjects(instance).Build()
			computed := &workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: instance.Generation}
			if tc.cached != "" {
				computed.Conditions = append(computed.Conditions, workloadsv1alpha2.RoleInstanceCondition{
					Type: workloadsv1alpha2.RoleInstanceRestarting, Status: tc.cached,
				})
			}
			if tc.ready {
				computed.Conditions = append(computed.Conditions, workloadsv1alpha2.RoleInstanceCondition{
					Type: workloadsv1alpha2.RoleInstanceReady, Status: v1.ConditionTrue,
				})
			}
			before := computed.DeepCopy()
			require.NoError(t, (&realStatusUpdater{Client: cli, apiReader: cli}).updateStatus(ctx, instance, computed))
			require.Equal(t, before, computed, "merging persisted conditions must not mutate computed status")
			live := &workloadsv1alpha2.RoleInstance{}
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(instance), live))
			var guards []workloadsv1alpha2.RoleInstanceCondition
			for _, cond := range live.Status.Conditions {
				if cond.Type == workloadsv1alpha2.RoleInstanceRestarting {
					guards = append(guards, cond)
				}
			}
			if tc.want == "" {
				require.Empty(t, guards)
			} else {
				require.Len(t, guards, 1)
				require.Equal(t, tc.want, guards[0].Status)
			}
		})
	}
}

func TestStatusWithInformerPredatingBudget(t *testing.T) {
	for _, lag := range []string{"caller-only", "reader-then-refresh", "reader-stays-stale", "generation"} {
		t.Run(lag, func(t *testing.T) {
			ctx := context.Background()
			stale := newReadinessTestInstance()
			live := stale.DeepCopy()
			live.Spec.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{
				Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart, MaxGroupRestarts: ptr.To[int32](1),
			}
			if lag == "generation" {
				live.Generation++
			}
			live.Status.GroupRestartCount = 1
			live.Status.GroupRestartRevision = "budget-revision"
			live.Status.RestartBudgetExhausted = true
			synccontrol.EnforceRestartBudgetConditions(live)
			base := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithStatusSubresource(live).WithObjects(live).Build()
			// Match a real informer snapshot's old resourceVersion so stale writes
			// conflict instead of receiving the fake client's unconditional update.
			fresh := &workloadsv1alpha2.RoleInstance{}
			require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(live), fresh))
			stale.ResourceVersion = fresh.ResourceVersion
			fresh.Status.LabelSelector = "newer-status"
			require.NoError(t, base.Status().Update(ctx, fresh))
			reads := 0
			cache := interceptor.NewClient(base, interceptor.Funcs{
				Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					reads++
					if lag == "reader-stays-stale" || (lag == "reader-then-refresh" && reads == 1) {
						stale.DeepCopyInto(obj.(*workloadsv1alpha2.RoleInstance))
						return nil
					}
					return cli.Get(ctx, key, obj, opts...)
				},
			})
			computed := stale.Status.DeepCopy()
			computed.ObservedGeneration = stale.Generation
			computed.Conditions = []workloadsv1alpha2.RoleInstanceCondition{{Type: workloadsv1alpha2.RoleInstanceReady, Status: v1.ConditionTrue}}
			err := newStatusUpdater(cache, base).(*realStatusUpdater).updateStatus(ctx, stale, computed)
			if lag == "reader-stays-stale" {
				require.True(t, apierrors.IsConflict(err), "old cache revisions must never overwrite new live state")
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, base.Get(ctx, client.ObjectKeyFromObject(live), fresh))
			require.EqualValues(t, 1, fresh.Status.GroupRestartCount)
			require.Equal(t, "budget-revision", fresh.Status.GroupRestartRevision)
			require.True(t, fresh.Status.RestartBudgetExhausted)
			require.True(t, slices.ContainsFunc(fresh.Status.Conditions, func(cond workloadsv1alpha2.RoleInstanceCondition) bool {
				return cond.Type == workloadsv1alpha2.RoleInstanceReady && cond.Status == v1.ConditionFalse
			}))
		})
	}
}

func TestStaleStatusPreservesLiveRestartBudget(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Spec.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{
		Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart, MaxGroupRestarts: ptr.To[int32](2),
	}
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
	// CustomReady is projected into computed status by main's status model, so
	// it follows that ownership rule; only budget state is merged from live here.
	require.Equal(t, "StaleExternal", conditions[workloadsv1alpha2.RoleInstanceCustomReady].Reason)
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

func TestStaleBudgetStatusDoesNotRollBackObservedGeneration(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Spec.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{
		Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart, MaxGroupRestarts: ptr.To[int32](2),
	}
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
