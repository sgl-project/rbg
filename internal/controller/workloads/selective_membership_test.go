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

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestInvalidCommittedRecordBlocksCleanupAndReportsStatus(t *testing.T) {
	for _, value := range []string{"3-1", "1,,3", "2147483648", "0-2147483647"} {
		t.Run(value, func(t *testing.T) {
			ctx := context.Background()
			scheme := newTestScheme()
			set := &workloadsv1alpha2.RoleInstanceSet{ObjectMeta: metav1.ObjectMeta{Name: "set", Namespace: "default", UID: "set-uid", Annotations: map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: value}}, Spec: workloadsv1alpha2.RoleInstanceSetSpec{Replicas: ptr.To[int32](3), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}}}, Status: workloadsv1alpha2.RoleInstanceSetStatus{Conditions: []workloadsv1alpha2.RoleInstanceSetCondition{{Type: workloadsv1alpha2.RoleInstanceSetConditionFailedScale, Status: corev1.ConditionTrue, Reason: "PreviousFailure"}}}}
			instance := &workloadsv1alpha2.RoleInstance{ObjectMeta: metav1.ObjectMeta{Name: "set-random", Namespace: set.Namespace, UID: "instance-uid", Labels: map[string]string{"app": "test"}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(set, workloadsv1alpha2.GroupVersion.WithKind("RoleInstanceSet"))}}}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(set).WithObjects(set, instance).Build()
			recorder := record.NewFakeRecorder(2)
			r := &RoleInstanceSetReconciler{client: c, recorder: recorder}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: set.Namespace, Name: set.Name}})
			require.Error(t, err)
			actual := &workloadsv1alpha2.RoleInstanceSet{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(set), actual))
			require.Equal(t, int32(3), *actual.Spec.Replicas)
			require.Len(t, actual.Status.Conditions, 1)
			require.Equal(t, "InvalidCommittedOrdinals", actual.Status.Conditions[0].Reason)
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), &workloadsv1alpha2.RoleInstance{}), "invalid membership must stop before deleting a stale-pattern instance")
			select {
			case event := <-recorder.Events:
				require.Contains(t, event, "Warning InvalidCommittedOrdinals")
			default:
				t.Fatal("missing warning event")
			}
			actual.Annotations[constants.CommittedSkipOrdinalsAnnotationKey] = "1"
			require.NoError(t, c.Update(ctx, actual))
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(set), actual))
			require.NoError(t, r.validateCommittedMembership(ctx, actual))
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(set), actual))
			require.Empty(t, actual.Status.Conditions)
			members, err := actual.CommittedOrdinals()
			require.NoError(t, err)
			require.Equal(t, []int{0, 2, 3}, members)
		})
	}
}
