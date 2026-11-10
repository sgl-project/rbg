// Copyright 2026 The RBG Authors.
// SPDX-License-Identifier: Apache-2.0

package statefulmode

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instancelifecycle "sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
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
