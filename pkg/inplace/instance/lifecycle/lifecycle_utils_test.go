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

package lifecycle

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/readiness"
)

func TestReturningToNormalRestoresLifecycleReadiness(t *testing.T) {
	for _, test := range []struct {
		name   string
		states []constants.RoleInstanceLifecycleStateType
	}{
		{
			name: "in-place update",
			states: []constants.RoleInstanceLifecycleStateType{
				constants.RoleInstanceLifecycleStatePreparingUpdate,
				constants.RoleInstanceLifecycleStateUpdating,
				constants.RoleInstanceLifecycleStateUpdated,
			},
		},
		{
			name:   "canceled pre-delete",
			states: []constants.RoleInstanceLifecycleStateType{constants.RoleInstanceLifecycleStatePreparingDelete},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			instance := &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Namespace: metav1.NamespaceDefault, Name: "instance"},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					ReadinessGates: []workloadsv1alpha2.RoleInstanceReadinessGate{{ConditionType: workloadsv1alpha2.RoleInstanceCustomReady}},
				},
				Status: workloadsv1alpha2.RoleInstanceStatus{
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{{Type: workloadsv1alpha2.RoleInstanceCustomReady, Status: corev1.ConditionTrue}},
				},
			}
			scheme := runtime.NewScheme()
			require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(instance).WithObjects(instance).Build()
			control := New(c)
			ctx := context.Background()
			key := client.ObjectKeyFromObject(instance)

			for _, state := range test.states {
				_, _, err := control.UpdateRoleInstanceLifecycle(instance, state, true)
				require.NoError(t, err)
				require.NoError(t, c.Get(ctx, key, instance))
				require.Equal(t, state, GetRoleInstanceLifecycleState(instance))
			}
			gate := readiness.GetReadinessCondition(instance)
			require.NotNil(t, gate)
			require.Equal(t, corev1.ConditionFalse, gate.Status)

			_, _, err := control.UpdateRoleInstanceLifecycle(instance, constants.RoleInstanceLifecycleStateNormal, true)
			require.NoError(t, err)
			require.NoError(t, c.Get(ctx, key, instance))
			require.Equal(t, constants.RoleInstanceLifecycleStateNormal, GetRoleInstanceLifecycleState(instance))
			gate = readiness.GetReadinessCondition(instance)
			require.NotNil(t, gate)
			require.Equal(t, corev1.ConditionTrue, gate.Status)
		})
	}
}
