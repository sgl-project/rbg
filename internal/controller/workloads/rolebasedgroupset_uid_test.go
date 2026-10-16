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

package workloads

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func TestRoleBasedGroupSetReconciler_ForeignChildUID(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))

	role := wrappersv2.BuildStandaloneRole("worker").Obj()
	set := &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default", UID: "new-set-uid"},
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			Replicas:      ptr.To[int32](1),
			GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{role}}},
		},
	}

	t.Run("foreign child is not counted as ready", func(t *testing.T) {
		oldSet := set.DeepCopy()
		oldSet.UID = "old-set-uid"
		child := newRBGForSet(set, 0)
		child.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(oldSet, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroupSet"))}
		child.Status.Conditions = []metav1.Condition{{Type: string(workloadsv1alpha2.RoleBasedGroupReady), Status: metav1.ConditionTrue, Reason: "Ready"}}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(set, child).WithObjects(set, child).Build()
		r := &RoleBasedGroupSetReconciler{client: c, scheme: scheme}

		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(set)})
		// The reconcile succeeds — it simply skips the foreign child during listing and scaleUp.
		require.NoError(t, err)

		after := &workloadsv1alpha2.RoleBasedGroupSet{}
		require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(set), after))
		assert.Equal(t, int32(0), after.Status.ReadyReplicas, "foreign child should not be counted as ready")
	})

	t.Run("foreign child is not deleted on scale-down", func(t *testing.T) {
		oldSet := set.DeepCopy()
		oldSet.UID = "old-set-uid"
		scaledDown := set.DeepCopy()
		scaledDown.Spec.Replicas = ptr.To[int32](0)
		child := newRBGForSet(set, 0)
		child.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(oldSet, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroupSet"))}

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(scaledDown, child).WithObjects(scaledDown, child).Build()
		r := &RoleBasedGroupSetReconciler{client: c, scheme: scheme}

		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(scaledDown)})
		require.NoError(t, err)

		err = c.Get(context.Background(), client.ObjectKeyFromObject(child), &workloadsv1alpha2.RoleBasedGroup{})
		assert.NoError(t, err, "foreign child should not be deleted")
	})

	t.Run("orphan with correct label is adopted", func(t *testing.T) {
		child := newRBGForSet(set, 0)
		child.OwnerReferences = nil

		c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(set, child).WithObjects(set, child).Build()
		r := &RoleBasedGroupSetReconciler{client: c, scheme: scheme}

		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(set)})
		require.NoError(t, err)

		after := &workloadsv1alpha2.RoleBasedGroup{}
		require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(child), after))
		assert.True(t, metav1.IsControlledBy(after, set), "orphan child should be adopted by the Set")
	})

	t.Run("own child is managed normally", func(t *testing.T) {
		child := newRBGForSet(set, 0)
		child.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(set, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroupSet"))}

		assert.True(t, (&RoleBasedGroupSetReconciler{}).isOwnedOrAdoptable(set, child), "own child should be managed")
	})
}
