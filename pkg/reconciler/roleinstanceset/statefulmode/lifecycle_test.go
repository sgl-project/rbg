// Copyright 2026 The RBG Authors.
// SPDX-License-Identifier: Apache-2.0

package statefulmode

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instancelifecycle "sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/readiness"
)

const preDeleteHoldLabel = "consumer.example/held"

func holdPreDelete(t *testing.T, h *rollbackTestHarness, markNotReady bool, instances ...*workloadsv1alpha2.RoleInstance) client.Client {
	t.Helper()
	h.set.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceSetLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceSetLifecycleHook{
			LabelsHandler: map[string]string{preDeleteHoldLabel: "true"},
			MarkNotReady:  markNotReady,
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	builder := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&workloadsv1alpha2.RoleInstance{})
	for _, instance := range instances {
		instance.Labels[preDeleteHoldLabel] = "true"
		if markNotReady {
			instance.Spec.ReadinessGates = []workloadsv1alpha2.RoleInstanceReadinessGate{{ConditionType: workloadsv1alpha2.RoleInstanceCustomReady}}
			instance.Status.Conditions = append(instance.Status.Conditions,
				workloadsv1alpha2.RoleInstanceCondition{Type: workloadsv1alpha2.RoleInstanceCustomReady, Status: corev1.ConditionTrue})
		}
		builder.WithObjects(instance)
	}
	c := builder.Build()
	h.control.lifecycleControl = instancelifecycle.New(c)
	return c
}

func readLifecycle(t *testing.T, c client.Client, instance *workloadsv1alpha2.RoleInstance) constants.RoleInstanceLifecycleStateType {
	t.Helper()
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), instance))
	return instancelifecycle.GetRoleInstanceLifecycleState(instance)
}

func TestStatefulRollingUpdateAndScaleDownWaitForPreDeleteHook(t *testing.T) {
	for _, scaleDown := range []bool{false, true} {
		name := "rolling-update"
		if scaleDown {
			name = "scale-down"
		}
		t.Run(name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "hook-"+name, 1, 0, 0)
			h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
			if scaleDown {
				h.set.Spec.Replicas = ptr.To(int32(0))
			}
			instance := h.instance(0, h.intermediate.Name, true)
			instance.ResourceVersion = "1"
			c := holdPreDelete(t, h, false, instance)
			for pass := 0; pass < 2; pass++ {
				h.reconcile(t, instance)
				h.assertDeleted(t)
				require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, instance))
			}
			delete(instance.Labels, preDeleteHoldLabel)
			require.NoError(t, c.Update(context.Background(), instance))
			h.reconcile(t, instance)
			h.assertDeleted(t, instance.Name)
		})
	}
}

func TestStatefulScaleDownCancellationRestoresPreDeleteReadiness(t *testing.T) {
	h := newRollbackTestHarness(t, "cancel-scale-down", 1, 0, 0)
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	retained := h.instance(0, h.current.Name, true)
	held := h.instance(1, h.current.Name, true)
	c := holdPreDelete(t, h, true, held)
	check := func(state constants.RoleInstanceLifecycleStateType, gate corev1.ConditionStatus) {
		t.Helper()
		h.assertDeleted(t)
		require.Equal(t, state, readLifecycle(t, c, held))
		if got := readiness.GetReadinessCondition(held); got == nil || got.Status != gate {
			t.Fatalf("readiness gate = %+v, want %q", got, gate)
		}
	}

	h.reconcile(t, retained, held)
	check(constants.RoleInstanceLifecycleStatePreparingDelete, corev1.ConditionFalse)
	h.set.Spec.Replicas = ptr.To(int32(2))
	h.reconcile(t, retained, held)
	check(constants.RoleInstanceLifecycleStateNormal, corev1.ConditionTrue)

	h.set.Spec.Replicas = ptr.To(int32(1))
	h.reconcile(t, retained, held)
	check(constants.RoleInstanceLifecycleStatePreparingDelete, corev1.ConditionFalse)
	delete(held.Labels, preDeleteHoldLabel)
	require.NoError(t, c.Update(context.Background(), held))
	h.reconcile(t, retained, held)
	h.assertDeleted(t, held.Name)
}

func TestOrderedScaleDownWaitsForHighestPreDeleteHook(t *testing.T) {
	h := newRollbackTestHarness(t, "ordered-hooks", 1, 0, 0)
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	base := h.instance(0, h.current.Name, true)
	lower := h.instance(1, h.current.Name, true)
	higher := h.instance(2, h.current.Name, true)
	c := holdPreDelete(t, h, false, lower, higher)
	h.reconcile(t, base, lower, higher)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, higher))
	require.NotEqual(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, lower))
	delete(higher.Labels, preDeleteHoldLabel)
	require.NoError(t, c.Update(context.Background(), higher))
	h.reconcile(t, base, lower, higher)
	h.assertDeleted(t, higher.Name)
}

func TestOrderedRolloutProceedsOnceScaleDownDeletionStarts(t *testing.T) {
	for _, test := range []struct {
		name            string
		alreadyDeleting bool
	}{
		{name: "delete issued"},
		{name: "already terminating", alreadyDeleting: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "ordered-rollout", 1, 0, 0)
			h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
			h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
			target := h.instance(0, h.intermediate.Name, true)
			surplus := h.instance(1, h.current.Name, true)
			c := holdPreDelete(t, h, false, target, surplus)

			h.reconcile(t, target, surplus)
			h.assertDeleted(t)
			require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, surplus))
			require.NotEqual(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, target))

			delete(surplus.Labels, preDeleteHoldLabel)
			require.NoError(t, c.Update(context.Background(), surplus))
			if test.alreadyDeleting {
				withTerminating(surplus)
			}
			h.reconcile(t, target, surplus)
			if test.alreadyDeleting {
				h.assertDeleted(t)
			} else {
				h.assertDeleted(t, surplus.Name)
			}
			require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, target))
		})
	}
}

func TestPausedRolloutPreservesPreDeleteForSparseUpdateTarget(t *testing.T) {
	h := newRollbackTestHarness(t, "paused-rollout", 1, 2, 0)
	h.set.Annotations = map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: "1,3"}
	h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	base := h.instance(0, h.current.Name, true)
	protected := h.instance(2, h.intermediate.Name, true)
	target := h.instance(4, h.intermediate.Name, true)
	c := holdPreDelete(t, h, false, protected, target)
	h.reconcile(t, base, protected, target)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, protected))
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, target))

	h.set.Spec.Replicas = ptr.To(int32(3))
	h.set.Spec.UpdateStrategy.Paused = true
	h.reconcile(t, base, protected, target)
	h.assertDeleted(t)
	// Ordinal 2 is below partition 2 by member position; ordinal 4 is at the partition.
	require.Equal(t, constants.RoleInstanceLifecycleStateNormal, readLifecycle(t, c, protected))
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, target))

	h.set.Spec.UpdateStrategy.Paused = false
	delete(target.Labels, preDeleteHoldLabel)
	require.NoError(t, c.Update(context.Background(), target))
	h.reconcile(t, base, protected, target)
	h.assertDeleted(t, target.Name)
}

func TestRemovingPreDeleteHookUnblocksOrderedScaleDown(t *testing.T) {
	h := newRollbackTestHarness(t, "remove-pre-delete", 1, 0, 0)
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	base := h.instance(0, h.current.Name, true)
	lower := h.instance(1, h.current.Name, false)
	higher := h.instance(2, h.current.Name, true)
	c := holdPreDelete(t, h, true, higher)
	h.reconcile(t, base, lower, higher)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, higher))
	require.False(t, isHealthy(higher))

	h.set.Spec.Lifecycle = nil
	h.reconcile(t, base, lower, higher)
	require.Equal(t, []string{higher.Name, lower.Name}, h.objects.deleted)
}

func TestRollingUpdateCountsUnavailableDrainOnce(t *testing.T) {
	for _, test := range []struct {
		name   string
		stable bool
	}{
		{name: "recently NotReady"},
		{name: "stably NotReady", stable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "pre-delete-budget", 2, 0, 0)
			h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
			lower := h.instance(0, h.intermediate.Name, true)
			higher := h.instance(1, h.intermediate.Name, true)
			c := holdPreDelete(t, h, true, lower, higher)
			h.reconcile(t, lower, higher)
			h.assertDeleted(t)
			require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, higher))
			require.False(t, isHealthy(higher))
			require.NotEqual(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, lower))

			h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(2))
			if test.stable {
				setUnhealthySince(h.set, higher, time.Now().Add(-2*stableUnhealthyDuration))
			}
			h.reconcile(t, lower, higher)
			h.assertDeleted(t)
			require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, lower))
		})
	}
}
