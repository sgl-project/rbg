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

// svc_reconciler_test.go
package reconciler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func TestServiceReconciler_reconcileHeadlessService(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))

	leaderOnlyRole := wrappersv2.BuildLeaderWorkerRole("test-role-leaderonly").Obj()
	leaderOnlyRole.Workload = workloadsv1alpha2.WorkloadSpec{
		APIVersion: "workloads.x-k8s.io/v1alpha2",
		Kind:       "RoleInstanceSet",
	}
	leaderOnlyRole.LeaderWorkerPattern.SharedServiceSelection = ptr.To(
		workloadsv1alpha2.SharedServiceSelectionLeaderOnly,
	)

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").WithRoles(
		[]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("test-role-statefulset").WithWorkload("apps/v1", "StatefulSet").Obj(),
			wrappersv2.BuildStandaloneRole("test-role-roleinstanceset").WithWorkload("workloads.x-k8s.io/v1alpha2", "RoleInstanceSet").Obj(),
			leaderOnlyRole,
		},
	).Obj()

	statefulset := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "StatefulSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
			Namespace: rbg.Namespace,
			UID:       "test-sts",
		},
	}

	roleInstanceSet := &workloadsv1alpha2.RoleInstanceSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RoleInstanceSet",
			APIVersion: "workloads.x-k8s.io/v1alpha2",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[1]),
			Namespace: rbg.Namespace,
			UID:       "test-roleinstanceset",
		},
	}

	leaderOnlyRoleInstanceSet := &workloadsv1alpha2.RoleInstanceSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RoleInstanceSet",
			APIVersion: "workloads.x-k8s.io/v1alpha2",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[2]),
			Namespace: rbg.Namespace,
			UID:       "test-leaderonly-roleinstanceset",
		},
	}

	rbg.ObjectMeta.Labels = map[string]string{"app": "test"}

	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(
		rbg, statefulset, roleInstanceSet, leaderOnlyRoleInstanceSet,
	).Build()

	reconciler := NewServiceReconciler(cl)

	t.Run("successful reconciliation", func(t *testing.T) {
		for i := 0; i < len(rbg.Spec.Roles); i++ {
			err := reconciler.reconcileHeadlessService(context.TODO(), rbg, &rbg.Spec.Roles[i])
			assert.NoError(t, err)

			// Check that service was created
			svcName, _ := utils.GetCompatibleHeadlessServiceName(context.TODO(), cl, rbg, &rbg.Spec.Roles[i])
			svc := &corev1.Service{}
			err = cl.Get(context.TODO(), types.NamespacedName{Name: svcName, Namespace: rbg.Namespace}, svc)
			assert.NoError(t, err)
			assert.Equal(t, "None", svc.Spec.ClusterIP)
			assert.True(t, svc.Spec.PublishNotReadyAddresses)

			expectedSelector := map[string]string{
				constants.GroupNameLabelKey: rbg.Name,
				constants.RoleNameLabelKey:  rbg.Spec.Roles[i].Name,
			}
			if rbg.Spec.Roles[i].IsLeaderWorkerPattern() &&
				rbg.Spec.Roles[i].LeaderWorkerPattern.SharedServiceSelection != nil &&
				*rbg.Spec.Roles[i].LeaderWorkerPattern.SharedServiceSelection == workloadsv1alpha2.SharedServiceSelectionLeaderOnly {
				expectedSelector[constants.ComponentNameLabelKey] = "leader"
			}
			assert.Equal(t, expectedSelector, svc.Spec.Selector)

			// Verify owner reference
			assert.Len(t, svc.OwnerReferences, 1)
			assert.Equal(t, rbg.GetWorkloadName(&rbg.Spec.Roles[i]), svc.OwnerReferences[0].Name)
		}
	})
}

func TestServiceReconciler_reconcileHeadlessService_UpdatesSelectorInPlace(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))

	role := wrappersv2.BuildLeaderWorkerRole("test-role").Obj()
	role.Workload = workloadsv1alpha2.WorkloadSpec{
		APIVersion: "workloads.x-k8s.io/v1alpha2",
		Kind:       "RoleInstanceSet",
	}
	role.LeaderWorkerPattern.SharedServiceSelection = nil

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").WithRoles(
		[]workloadsv1alpha2.RoleSpec{role},
	).Obj()

	roleInstanceSet := &workloadsv1alpha2.RoleInstanceSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RoleInstanceSet",
			APIVersion: "workloads.x-k8s.io/v1alpha2",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
			Namespace: rbg.Namespace,
			UID:       "test-roleinstanceset",
		},
	}

	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(rbg, roleInstanceSet).Build()
	reconciler := NewServiceReconciler(cl)

	roleRef := &rbg.Spec.Roles[0]
	require.NoError(t, reconciler.reconcileHeadlessService(context.Background(), rbg, roleRef))

	svcName, err := utils.GetCompatibleHeadlessServiceName(context.Background(), cl, rbg, roleRef)
	require.NoError(t, err)

	svc := &corev1.Service{}
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: svcName, Namespace: rbg.Namespace}, svc))
	assert.NotContains(t, svc.Spec.Selector, constants.ComponentNameLabelKey)

	roleRef.LeaderWorkerPattern.SharedServiceSelection = ptr.To(
		workloadsv1alpha2.SharedServiceSelectionLeaderOnly,
	)
	require.NoError(t, reconciler.reconcileHeadlessService(context.Background(), rbg, roleRef))

	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: svcName, Namespace: rbg.Namespace}, svc))
	assert.Equal(t, "leader", svc.Spec.Selector[constants.ComponentNameLabelKey])
}

func TestSemanticallyEqualService(t *testing.T) {
	baseSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-svc",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": "test",
			},
		},
	}

	t.Run("services are equal", func(t *testing.T) {
		svc1 := baseSvc.DeepCopy()
		svc2 := baseSvc.DeepCopy()

		equal, err := semanticallyEqualService(svc1, svc2)
		assert.True(t, equal)
		assert.NoError(t, err)
	})

	t.Run("services differ in selector", func(t *testing.T) {
		svc1 := baseSvc.DeepCopy()
		svc2 := baseSvc.DeepCopy()
		svc2.Spec.Selector["version"] = "v2"

		equal, err := semanticallyEqualService(svc1, svc2)
		assert.False(t, equal)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "selector not equal")
	})

	t.Run("one service is nil", func(t *testing.T) {
		equal, err := semanticallyEqualService(nil, baseSvc)
		assert.False(t, equal)
		assert.Error(t, err)
	})
}
