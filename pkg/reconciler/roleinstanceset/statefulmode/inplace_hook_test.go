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

package statefulmode

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	api "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instanceinplace "sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
	instancelifecycle "sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
	instanceutils "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
	"sigs.k8s.io/rbgs/pkg/utils/revisionadapter"
)

const inplaceHookLabel = "example.com/inplace-protection"
const inplaceHookFinalizer = "example.com/inplace-protection"

type inplaceHookHarness struct {
	set         *api.RoleInstanceSet
	old, update *apps.ControllerRevision
	control     *defaultStatefulInstanceSetControl
	client      client.Client
	objects     *fakeInstanceObjectManager
	key         client.ObjectKey
}

func newInplaceHookHarness(t *testing.T, hook *api.RoleInstanceSetLifecycleHook, intercept interceptor.Funcs) *inplaceHookHarness {
	t.Helper()
	set := newRevisionTestSet("nginx:1.0")
	set.Name = "inplace-hook"
	set.Spec.Replicas = ptr.To[int32](1)
	set.Spec.PodManagementPolicy = constants.ParallelPodManagement
	set.Spec.UpdateStrategy.Type = api.InPlaceIfPossibleUpdateStrategyType
	set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	if hook != nil {
		set.Spec.Lifecycle = &api.RoleInstanceSetLifecycle{InPlaceUpdate: hook}
	}
	old, err := newRevision(set, 1, ptr.To[int32](0))
	require.NoError(t, err)
	instance := buildInst(set.Name, 0, old.Name, true, true)
	instance.Spec = *set.Spec.RoleInstanceTemplate.RoleInstanceSpec.DeepCopy()
	instance.Spec.ReadinessGates = []api.RoleInstanceReadinessGate{{ConditionType: api.RoleInstanceInPlaceUpdateReady}}
	instance.Generation = 1
	instance.Status.ObservedGeneration = 1
	instance.Status.ComponentStatuses = []api.RoleInstanceComponentStatus{{Name: "worker", UpdatedReplicas: 1}}
	instance.Status.Conditions = append(instance.Status.Conditions, api.RoleInstanceCondition{Type: api.RoleInstanceInPlaceUpdateReady, Status: corev1.ConditionTrue})
	if hook != nil {
		for k, v := range hook.LabelsHandler {
			instance.Labels[k] = v
		}
		instance.Finalizers = append(instance.Finalizers, hook.FinalizersHandler...)
	}
	set.Spec.RoleInstanceTemplate.Components[0].Template.Spec.Containers[0].Image = "nginx:2.0"
	update, err := newRevision(set, 2, ptr.To[int32](0))
	require.NoError(t, err)
	set.Status.CurrentRevision, set.Status.UpdateRevision = old.Name, update.Name
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).WithStatusSubresource(instance).WithInterceptorFuncs(intercept).Build()
	objects := &fakeInstanceObjectManager{}
	control := &defaultStatefulInstanceSetControl{
		instanceControl:  NewStatefulInstanceControlFromManager(objects, record.NewFakeRecorder(32)),
		inplaceControl:   instanceinplace.New(c, revisionadapter.NewDefaultImpl()),
		lifecycleControl: instancelifecycle.New(c),
	}
	t.Cleanup(func() {
		updateExpectations.DeleteExpectations(getInstanceSetKey(set))
		durationStore.Pop(getInstanceSetKey(set))
		instanceutils.ResourceVersionExpectations.Delete(instance)
		resetInstanceUnhealthySince()
	})
	return &inplaceHookHarness{set: set, old: old, update: update, control: control, client: c, objects: objects, key: client.ObjectKeyFromObject(instance)}
}

func (h *inplaceHookHarness) get(t *testing.T) *api.RoleInstance {
	t.Helper()
	got := &api.RoleInstance{}
	require.NoError(t, h.client.Get(context.Background(), h.key, got))
	return got
}
func (h *inplaceHookHarness) apply(t *testing.T) bool {
	t.Helper()
	instance := h.get(t)
	before := instance.DeepCopy()
	changed, err := h.control.applyTargetUpdate(context.Background(), h.set, &api.RoleInstanceSetStatus{}, instance,
		h.old, h.update, []*apps.ControllerRevision{h.old, h.update}, false, false)
	require.NoError(t, err)
	require.Equal(t, before, instance, "informer snapshots must remain immutable")
	return changed
}
func (h *inplaceHookHarness) release(t *testing.T) {
	t.Helper()
	got := h.get(t)
	delete(got.Labels, inplaceHookLabel)
	got.Finalizers = nil
	require.NoError(t, h.client.Update(context.Background(), got))
}

func TestStatefulInPlaceHookBlocksUntilAllHandlersReleased(t *testing.T) {
	for _, hook := range []*api.RoleInstanceSetLifecycleHook{
		{LabelsHandler: map[string]string{inplaceHookLabel: "hold"}},
		{FinalizersHandler: []string{inplaceHookFinalizer}},
		{LabelsHandler: map[string]string{inplaceHookLabel: "hold"}, FinalizersHandler: []string{inplaceHookFinalizer}},
	} {
		name := "labels"
		if len(hook.LabelsHandler) == 0 {
			name = "finalizers"
		} else if len(hook.FinalizersHandler) > 0 {
			name = "labels and finalizers"
		}
		t.Run(name, func(t *testing.T) {
			h := newInplaceHookHarness(t, hook, interceptor.Funcs{})
			for range 2 {
				require.False(t, h.apply(t), "waiting must not count as a revision transition")
				got := h.get(t)
				require.Equal(t, constants.RoleInstanceLifecycleStatePreparingUpdate, instancelifecycle.GetRoleInstanceLifecycleState(got))
				require.Equal(t, h.old.Name, getInstanceRevision(got))
				require.Equal(t, "nginx:1.0", got.Spec.Components[0].Template.Spec.Containers[0].Image)
				require.Empty(t, h.objects.deleted, "hook waiting must not fall back to Recreate")
				require.Nil(t, got.DeletionTimestamp)
			}
			if len(hook.LabelsHandler) > 0 && len(hook.FinalizersHandler) > 0 {
				got := h.get(t)
				delete(got.Labels, inplaceHookLabel)
				require.NoError(t, h.client.Update(context.Background(), got))
				require.False(t, h.apply(t), "a remaining finalizer must continue blocking")
			}
			h.release(t)
			require.True(t, h.apply(t))
			got := h.get(t)
			require.Equal(t, constants.RoleInstanceLifecycleStateUpdating, instancelifecycle.GetRoleInstanceLifecycleState(got))
			require.Equal(t, h.update.Name, getInstanceRevision(got))
			require.Equal(t, "nginx:2.0", got.Spec.Components[0].Template.Spec.Containers[0].Image)
			require.Empty(t, h.objects.deleted)
		})
	}
}

func TestStatefulInPlaceHookCompletesAndRearms(t *testing.T) {
	hook := &api.RoleInstanceSetLifecycleHook{LabelsHandler: map[string]string{inplaceHookLabel: "hold"}, FinalizersHandler: []string{inplaceHookFinalizer}}
	h := newInplaceHookHarness(t, hook, interceptor.Funcs{})
	got := h.get(t)
	instancelifecycle.SetRoleInstanceLifecycle(constants.RoleInstanceLifecycleStateUpdating)(got)
	require.NoError(t, h.client.Update(context.Background(), got))
	got.Status.ObservedGeneration = 0
	require.NoError(t, h.client.Status().Update(context.Background(), got))
	_, _, err := h.control.refreshInstanceState(h.set, h.get(t), h.update.Name)
	require.NoError(t, err)
	require.Equal(t, constants.RoleInstanceLifecycleStateUpdating, instancelifecycle.GetRoleInstanceLifecycleState(h.get(t)))
	h.release(t)
	got = h.get(t)
	got.Status.ObservedGeneration = got.Generation
	require.NoError(t, h.client.Status().Update(context.Background(), got))
	modified, _, err := h.control.refreshInstanceState(h.set, h.get(t), h.update.Name)
	require.NoError(t, err)
	require.True(t, modified)
	require.Equal(t, constants.RoleInstanceLifecycleStateUpdated, instancelifecycle.GetRoleInstanceLifecycleState(h.get(t)))
	got = h.get(t)
	got.Labels[inplaceHookLabel] = "hold"
	got.Finalizers = []string{inplaceHookFinalizer}
	require.NoError(t, h.client.Update(context.Background(), got))
	modified, _, err = h.control.refreshInstanceState(h.set, h.get(t), h.update.Name)
	require.NoError(t, err)
	require.True(t, modified)
	require.Equal(t, constants.RoleInstanceLifecycleStateNormal, instancelifecycle.GetRoleInstanceLifecycleState(h.get(t)))
}

func TestStatefulInPlaceHookPatchFailureDoesNotUpdateOrDelete(t *testing.T) {
	expected := errors.New("hook patch failed")
	h := newInplaceHookHarness(t, &api.RoleInstanceSetLifecycleHook{LabelsHandler: map[string]string{inplaceHookLabel: "hold"}},
		interceptor.Funcs{Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
			return expected
		}})
	_, err := h.control.applyTargetUpdate(context.Background(), h.set, &api.RoleInstanceSetStatus{}, h.get(t), h.old, h.update, []*apps.ControllerRevision{h.old, h.update}, false, false)
	require.ErrorIs(t, err, expected)
	require.Equal(t, h.old.Name, getInstanceRevision(h.get(t)))
	require.Empty(t, h.objects.deleted)
}

func TestStatefulInPlaceHookDoesNotBlockUnhookedInstances(t *testing.T) {
	for _, hook := range []*api.RoleInstanceSetLifecycleHook{nil, {LabelsHandler: map[string]string{inplaceHookLabel: "hold"}}} {
		t.Run(map[bool]string{true: "no hook", false: "unmatched hook"}[hook == nil], func(t *testing.T) {
			h := newInplaceHookHarness(t, hook, interceptor.Funcs{})
			h.release(t)
			require.True(t, h.apply(t))
			require.Equal(t, h.update.Name, getInstanceRevision(h.get(t)))
			require.Empty(t, h.objects.deleted)
		})
	}
}

func TestStatefulInPlaceHookWaitingUsesExistingBudget(t *testing.T) {
	hook := &api.RoleInstanceSetLifecycleHook{LabelsHandler: map[string]string{inplaceHookLabel: "hold"}}
	h := newInplaceHookHarness(t, hook, interceptor.Funcs{})
	h.set.Spec.Replicas = ptr.To[int32](2)
	waiting := h.get(t)
	waiting.Name = h.set.Name + "-1"
	waiting.UID = "waiting-instance"
	waiting.ResourceVersion = ""
	instancelifecycle.SetRoleInstanceLifecycle(constants.RoleInstanceLifecycleStatePreparingUpdate)(waiting)
	waiting.Status.Conditions[0].Status = corev1.ConditionFalse
	require.NoError(t, h.client.Create(context.Background(), waiting))
	require.NoError(t, h.client.Status().Update(context.Background(), waiting))
	replicas := []*api.RoleInstance{h.get(t), waiting}
	topo, err := computeTopology(h.set, replicas, h.old.Name, h.update.Name, false)
	require.NoError(t, err)
	status := &api.RoleInstanceSetStatus{CurrentReplicas: 2}
	_, err = h.control.progressUpdate(context.Background(), h.set, status, h.old, h.update, []*apps.ControllerRevision{h.old, h.update}, replicas, replicas, 0, topo)
	require.NoError(t, err)
	require.EqualValues(t, 2, status.CurrentReplicas, "waiting must not decrement the revision count")
	require.Empty(t, instancelifecycle.GetRoleInstanceLifecycleState(h.get(t)), "the next target must not start while the budget is occupied")
	delete(waiting.Labels, inplaceHookLabel)
	require.NoError(t, h.client.Update(context.Background(), waiting))
	require.NoError(t, h.client.Get(context.Background(), client.ObjectKeyFromObject(waiting), waiting))
	replicas[1] = waiting
	_, err = h.control.progressUpdate(context.Background(), h.set, status, h.old, h.update, []*apps.ControllerRevision{h.old, h.update}, replicas, replicas, 0, topo)
	require.NoError(t, err)
	require.EqualValues(t, 1, status.CurrentReplicas)
	got := &api.RoleInstance{}
	require.NoError(t, h.client.Get(context.Background(), client.ObjectKeyFromObject(waiting), got))
	require.Equal(t, h.update.Name, getInstanceRevision(got), "an admitted NotReady target must resume without the unhealthy timer")
	require.Equal(t, h.old.Name, getInstanceRevision(h.get(t)))
	require.Empty(t, h.objects.deleted)
	// OrderedReady may resume only the first blocked ordinal and must still
	// honor partition. Ordinary transient failures keep the original timer.
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	require.Same(t, waiting, orderedReadyCleanupTarget(h.set, replicas, topo, h.update.Name))
	topo.partition = 2
	require.Nil(t, orderedReadyCleanupTarget(h.set, replicas, topo, h.update.Name))
}

func TestStatefulInPlaceHookRemovalReleasesOnlyOwnReadinessKey(t *testing.T) {
	h := newInplaceHookHarness(t, nil, interceptor.Funcs{})
	got := h.get(t)
	got.Spec.ReadinessGates = append(got.Spec.ReadinessGates, api.RoleInstanceReadinessGate{ConditionType: api.RoleInstanceCustomReady})
	instancelifecycle.SetRoleInstanceLifecycle(constants.RoleInstanceLifecycleStateUpdated)(got)
	require.NoError(t, h.client.Update(context.Background(), got))
	got.Status.Conditions = append(got.Status.Conditions, api.RoleInstanceCondition{
		Type: api.RoleInstanceCustomReady, Status: corev1.ConditionFalse,
		Message: `[{"userAgent":"Lifecycle","key":"preUpdateHook"},{"userAgent":"Other","key":"hold"}]`,
	})
	require.NoError(t, h.client.Status().Update(context.Background(), got))
	modified, _, err := h.control.refreshInstanceState(h.set, h.get(t), h.update.Name)
	require.NoError(t, err)
	require.True(t, modified)
	got = h.get(t)
	require.Equal(t, constants.RoleInstanceLifecycleStateNormal, instancelifecycle.GetRoleInstanceLifecycleState(got))
	for _, condition := range got.Status.Conditions {
		if condition.Type == api.RoleInstanceCustomReady {
			require.Equal(t, corev1.ConditionFalse, condition.Status)
			require.Contains(t, condition.Message, "Other")
			require.NotContains(t, condition.Message, "preUpdateHook")
		}
	}
}

func TestStatefulInPlaceHookWaitsForPreviousHandshake(t *testing.T) {
	h := newInplaceHookHarness(t, &api.RoleInstanceSetLifecycleHook{LabelsHandler: map[string]string{inplaceHookLabel: "hold"}}, interceptor.Funcs{})
	h.release(t)
	got := h.get(t)
	instancelifecycle.SetRoleInstanceLifecycle(constants.RoleInstanceLifecycleStateUpdated)(got)
	got.Finalizers = []string{"other.example.com/protection"}
	require.NoError(t, h.client.Update(context.Background(), got))
	for range 2 {
		require.False(t, h.apply(t))
		got = h.get(t)
		require.Equal(t, constants.RoleInstanceLifecycleStateUpdated, instancelifecycle.GetRoleInstanceLifecycleState(got))
		require.Equal(t, []string{"other.example.com/protection"}, got.Finalizers)
		require.Equal(t, h.old.Name, getInstanceRevision(got))
	}
	require.Empty(t, h.objects.deleted)
}

func TestStatefulInPlaceHookWaitsForAllPostUpdateHandlers(t *testing.T) {
	h := newInplaceHookHarness(t, &api.RoleInstanceSetLifecycleHook{
		LabelsHandler: map[string]string{inplaceHookLabel: "hold"}, FinalizersHandler: []string{inplaceHookFinalizer},
	}, interceptor.Funcs{})
	h.release(t)
	got := h.get(t)
	instancelifecycle.SetRoleInstanceLifecycle(constants.RoleInstanceLifecycleStateUpdating)(got)
	got.Labels[inplaceHookLabel] = "hold"
	require.NoError(t, h.client.Update(context.Background(), got))
	modified, _, err := h.control.refreshInstanceState(h.set, h.get(t), h.update.Name)
	require.NoError(t, err)
	require.True(t, modified)
	require.Equal(t, constants.RoleInstanceLifecycleStateUpdated, instancelifecycle.GetRoleInstanceLifecycleState(h.get(t)))
	modified, _, err = h.control.refreshInstanceState(h.set, h.get(t), h.update.Name)
	require.NoError(t, err)
	require.False(t, modified, "a missing post-update finalizer must keep the handshake open")
	got = h.get(t)
	got.Finalizers = []string{inplaceHookFinalizer}
	require.NoError(t, h.client.Update(context.Background(), got))
	modified, _, err = h.control.refreshInstanceState(h.set, h.get(t), h.update.Name)
	require.NoError(t, err)
	require.True(t, modified)
	require.Equal(t, constants.RoleInstanceLifecycleStateNormal, instancelifecycle.GetRoleInstanceLifecycleState(h.get(t)))
}
