// Copyright 2026 The RBG Authors.
// SPDX-License-Identifier: Apache-2.0

package statefulmode

import (
	"context"
	"testing"

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

func TestStatefulRollingUpdateAndScaleDownWaitForPreDeleteHook(t *testing.T) {
	for _, scaleDown := range []bool{false, true} {
		name := "rolling-update"
		if scaleDown {
			name = "scale-down"
		}
		t.Run(name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "hook-"+name, 1, 0, 0)
			h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
			h.set.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceSetLifecycle{
				PreDelete: &workloadsv1alpha2.RoleInstanceSetLifecycleHook{
					LabelsHandler: map[string]string{"consumer.example/held": "true"},
				},
			}
			if scaleDown {
				h.set.Spec.Replicas = ptr.To(int32(0))
			}
			instance := h.instance(0, h.intermediate.Name, true)
			instance.ResourceVersion = "1"
			instance.Labels["consumer.example/held"] = "true"
			scheme := runtime.NewScheme()
			if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()
			h.control.lifecycleControl = instancelifecycle.New(c)
			ctx := context.Background()
			for pass := 0; pass < 2; pass++ {
				h.reconcile(t, instance)
				h.assertDeleted(t)
				if err := c.Get(ctx, client.ObjectKeyFromObject(instance), instance); err != nil {
					t.Fatal(err)
				}
				if instancelifecycle.GetRoleInstanceLifecycleState(instance) != constants.RoleInstanceLifecycleStatePreparingDelete {
					t.Fatal("consumer was not notified before deletion")
				}
			}
			delete(instance.Labels, "consumer.example/held")
			if err := c.Update(ctx, instance); err != nil {
				t.Fatal(err)
			}
			h.reconcile(t, instance)
			h.assertDeleted(t, instance.Name)
		})
	}
}

func TestStatefulScaleDownCancellationRestoresPreDeleteReadiness(t *testing.T) {
	h := newRollbackTestHarness(t, "cancel-scale-down", 1, 0, 0)
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	h.set.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceSetLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceSetLifecycleHook{
			LabelsHandler: map[string]string{"consumer.example/held": "true"},
			MarkNotReady:  true,
		},
	}
	retained := h.instance(0, h.current.Name, true)
	held := h.instance(1, h.current.Name, true)
	held.Labels["consumer.example/held"] = "true"
	held.Spec.ReadinessGates = []workloadsv1alpha2.RoleInstanceReadinessGate{{ConditionType: workloadsv1alpha2.RoleInstanceCustomReady}}
	held.Status.Conditions = append(held.Status.Conditions,
		workloadsv1alpha2.RoleInstanceCondition{Type: workloadsv1alpha2.RoleInstanceCustomReady, Status: corev1.ConditionTrue})
	scheme := runtime.NewScheme()
	if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(held).WithObjects(held).Build()
	h.control.lifecycleControl = instancelifecycle.New(c)
	ctx := context.Background()
	key := client.ObjectKeyFromObject(held)
	check := func(state constants.RoleInstanceLifecycleStateType, gate corev1.ConditionStatus) {
		t.Helper()
		h.assertDeleted(t)
		if err := c.Get(ctx, key, held); err != nil {
			t.Fatal(err)
		}
		if got := instancelifecycle.GetRoleInstanceLifecycleState(held); got != state {
			t.Fatalf("lifecycle = %q, want %q", got, state)
		}
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
	delete(held.Labels, "consumer.example/held")
	if err := c.Update(ctx, held); err != nil {
		t.Fatal(err)
	}
	h.reconcile(t, retained, held)
	h.assertDeleted(t, held.Name)
}

func TestOrderedScaleDownWaitsForHighestPreDeleteHook(t *testing.T) {
	h := newRollbackTestHarness(t, "ordered-hooks", 1, 0, 0)
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	h.set.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceSetLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceSetLifecycleHook{
			LabelsHandler: map[string]string{"consumer.example/held": "true"},
		},
	}
	base := h.instance(0, h.current.Name, true)
	lower := h.instance(1, h.current.Name, true)
	higher := h.instance(2, h.current.Name, true)
	for _, instance := range []*workloadsv1alpha2.RoleInstance{lower, higher} {
		instance.Labels["consumer.example/held"] = "true"
	}
	scheme := runtime.NewScheme()
	if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(lower, higher).Build()
	h.control.lifecycleControl = instancelifecycle.New(c)
	ctx := context.Background()
	h.reconcile(t, base, lower, higher)
	h.assertDeleted(t)
	for _, instance := range []*workloadsv1alpha2.RoleInstance{lower, higher} {
		if err := c.Get(ctx, client.ObjectKeyFromObject(instance), instance); err != nil {
			t.Fatal(err)
		}
	}
	if instancelifecycle.GetRoleInstanceLifecycleState(higher) != constants.RoleInstanceLifecycleStatePreparingDelete ||
		instancelifecycle.GetRoleInstanceLifecycleState(lower) == constants.RoleInstanceLifecycleStatePreparingDelete {
		t.Fatalf("highest ordinal must drain first, got %v and %v", higher.Labels, lower.Labels)
	}
	delete(higher.Labels, "consumer.example/held")
	if err := c.Update(ctx, higher); err != nil {
		t.Fatal(err)
	}
	h.reconcile(t, base, lower, higher)
	h.assertDeleted(t, higher.Name)
}
