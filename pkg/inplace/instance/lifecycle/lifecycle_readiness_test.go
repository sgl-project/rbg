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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	api "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/clientdapter"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/readiness"
)

func TestLifecycleMarkNotReadyInitializesGate(t *testing.T) {
	for _, state := range []constants.RoleInstanceLifecycleStateType{constants.RoleInstanceLifecycleStatePreparingDelete, constants.RoleInstanceLifecycleStatePreparingUpdate} {
		for _, withHandler := range []bool{false, true} {
			t.Run(string(state)+map[bool]string{false: "/state", true: "/handler"}[withHandler], func(t *testing.T) {
				instance := &api.RoleInstance{ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "default", UID: "original", Labels: map[string]string{"other": "keep"}}, Spec: api.RoleInstanceSpec{ReadinessGates: []api.RoleInstanceReadinessGate{{ConditionType: api.RoleInstanceInPlaceUpdateReady}}}}
				scheme := runtime.NewScheme()
				require.NoError(t, api.AddToScheme(scheme))
				c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).WithStatusSubresource(instance).Build()
				original := instance.DeepCopy()
				for range 2 {
					control := New(c)
					var err error
					if withHandler {
						_, _, err = control.UpdateRoleInstanceLifecycleWithHandler(instance, state, &api.RoleInstanceSetLifecycleHook{MarkNotReady: true})
					} else {
						_, _, err = control.UpdateRoleInstanceLifecycle(instance, state, true)
					}
					require.NoError(t, err)
					got := &api.RoleInstance{}
					require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), got))
					require.Contains(t, got.Spec.ReadinessGates, api.RoleInstanceReadinessGate{ConditionType: api.RoleInstanceCustomReady})
					require.Contains(t, got.Spec.ReadinessGates, api.RoleInstanceReadinessGate{ConditionType: api.RoleInstanceInPlaceUpdateReady})
					require.Len(t, got.Spec.ReadinessGates, 2, "gate initialization must be idempotent")
					require.Equal(t, "keep", got.Labels["other"])
					var found bool
					for _, condition := range got.Status.Conditions {
						if condition.Type == api.RoleInstanceCustomReady {
							found = true
							require.Equal(t, corev1.ConditionFalse, condition.Status)
							require.Contains(t, condition.Message, "Hook")
						}
					}
					require.True(t, found, "the first call must create a valid False condition")
					require.Equal(t, original, instance, "do not mutate informer snapshots")
					instance = got
					original = got.DeepCopy()
				}
			})
		}
	}
}

func TestLifecycleMarkNotReadyFalseDoesNotAddGate(t *testing.T) {
	instance := &api.RoleInstance{ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "default"}}
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).WithStatusSubresource(instance).Build()
	_, _, err := New(c).UpdateRoleInstanceLifecycle(instance, constants.RoleInstanceLifecycleStatePreparingDelete, false)
	require.NoError(t, err)
	got := &api.RoleInstance{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), got))
	require.Empty(t, got.Spec.ReadinessGates)
	require.Empty(t, got.Status.Conditions)
}

func TestLifecycleReadinessGateConflictPreservesConcurrentGate(t *testing.T) {
	instance := &api.RoleInstance{ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "default", UID: "original"}}
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))
	concurrentGate := api.RoleInstanceReadinessGate{ConditionType: "OtherReady"}
	patches := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).WithStatusSubresource(instance).
		WithInterceptorFuncs(interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			patches++
			if patches == 1 {
				latest := &api.RoleInstance{}
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), latest))
				latest.Spec.ReadinessGates = append(latest.Spec.ReadinessGates, concurrentGate)
				require.NoError(t, c.Update(ctx, latest))
			}
			return c.Patch(ctx, obj, patch, opts...)
		}}).Build()
	_, _, err := New(c).UpdateRoleInstanceLifecycle(instance, constants.RoleInstanceLifecycleStatePreparingDelete, true)
	require.NoError(t, err)
	require.GreaterOrEqual(t, patches, 3, "retry the conflicting gate patch before writing lifecycle metadata")
	got := &api.RoleInstance{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), got))
	require.Contains(t, got.Spec.ReadinessGates, concurrentGate)
	require.Contains(t, got.Spec.ReadinessGates, api.RoleInstanceReadinessGate{ConditionType: api.RoleInstanceCustomReady})
}

func TestLifecycleReadinessGateFailureDoesNotAdvanceState(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(map[bool]string{false: "patch failure", true: "replaced UID"}[replaced], func(t *testing.T) {
			instance := &api.RoleInstance{ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "default", UID: "original"}}
			stored := instance.DeepCopy()
			if replaced {
				stored.UID = "replacement"
			}
			scheme := runtime.NewScheme()
			require.NoError(t, api.AddToScheme(scheme))
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stored).WithStatusSubresource(stored).
				WithInterceptorFuncs(interceptor.Funcs{Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
					return errors.New("gate write failed")
				}}).Build()
			_, _, err := New(c).UpdateRoleInstanceLifecycle(instance, constants.RoleInstanceLifecycleStatePreparingDelete, true)
			require.Error(t, err)
			got := &api.RoleInstance{}
			require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), got))
			require.Empty(t, GetRoleInstanceLifecycleState(got))
			require.Empty(t, got.Spec.ReadinessGates)
			require.Empty(t, got.Status.Conditions)
		})
	}
}

func TestLifecycleReadinessReleaseKeepsOtherInstanceBlockers(t *testing.T) {
	instance := &api.RoleInstance{ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "default"}}
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).WithStatusSubresource(instance).Build()
	control := New(c)
	_, _, err := control.UpdateRoleInstanceLifecycle(instance, constants.RoleInstanceLifecycleStatePreparingUpdate, true)
	require.NoError(t, err)
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), instance))
	other := readiness.Message{UserAgent: "Other", Key: "hold"}
	require.NoError(t, readiness.New(c).AddNotReadyKey(instance, other))
	_, _, err = control.UpdateRoleInstanceLifecycle(instance, constants.RoleInstanceLifecycleStateNormal, true)
	require.NoError(t, err)
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), instance))
	condition := readiness.GetReadinessCondition(instance)
	require.NotNil(t, condition)
	require.Equal(t, corev1.ConditionFalse, condition.Status)
	require.Contains(t, condition.Message, "Other")
	require.NotContains(t, condition.Message, "preUpdateHook")
	require.NoError(t, readiness.New(c).RemoveNotReadyKey(instance, other))
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), instance))
	require.Equal(t, corev1.ConditionTrue, readiness.GetReadinessCondition(instance).Status)
}

func TestLifecycleReadinessWithUpdateOnlyAdapter(t *testing.T) {
	instance := &api.RoleInstance{ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "default"}}
	scheme := runtime.NewScheme()
	require.NoError(t, api.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).WithStatusSubresource(instance).Build()
	// Hide Patch support to exercise the Adapter's full-object Update fallback.
	adp := struct{ clientdapter.Adapter }{&clientdapter.AdapterRuntimeClient{Client: c}}
	control := &realControl{adp: adp, groupReadinessControl: newGroupReadinessControl(adp)}
	_, _, err := control.UpdateRoleInstanceLifecycle(instance, constants.RoleInstanceLifecycleStatePreparingDelete, true)
	require.NoError(t, err)
	got := &api.RoleInstance{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), got))
	require.True(t, readiness.ContainsReadinessGate(got))
	require.Equal(t, corev1.ConditionFalse, readiness.GetReadinessCondition(got).Status)
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, GetRoleInstanceLifecycleState(got))
}
