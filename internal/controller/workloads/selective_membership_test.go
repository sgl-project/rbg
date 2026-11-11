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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestInvalidCommittedRecordBlocksCleanupAndReportsStatus(t *testing.T) {
	for _, previousReason := range []string{"PreviousFailure", ""} {
		for _, value := range []string{"3-1", "1,,3", "2147483648", "0-2147483647"} {
			t.Run(previousReason+"/"+value, func(t *testing.T) {
				ctx := context.Background()
				scheme := newTestScheme()
				set := &workloadsv1alpha2.RoleInstanceSet{ObjectMeta: metav1.ObjectMeta{Name: "set", Namespace: "default", UID: "set-uid", Annotations: map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: value}}, Spec: workloadsv1alpha2.RoleInstanceSetSpec{Replicas: ptr.To[int32](3), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}}}, Status: workloadsv1alpha2.RoleInstanceSetStatus{Conditions: []workloadsv1alpha2.RoleInstanceSetCondition{{Type: workloadsv1alpha2.RoleInstanceSetConditionFailedScale, Status: corev1.ConditionTrue, Reason: previousReason}}}}
				instance := &workloadsv1alpha2.RoleInstance{ObjectMeta: metav1.ObjectMeta{Name: "set-random", Namespace: set.Namespace, UID: "instance-uid", Labels: map[string]string{"app": "test"}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(set, workloadsv1alpha2.GroupVersion.WithKind("RoleInstanceSet"))}}}
				c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(set).WithObjects(set, instance).Build()
				recorder := record.NewFakeRecorder(2)
				r := &RoleInstanceSetReconciler{client: c, recorder: recorder}
				_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: set.Namespace, Name: set.Name}})
				require.Error(t, err)
				actual := &workloadsv1alpha2.RoleInstanceSet{}
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(set), actual))
				require.Equal(t, int32(3), *actual.Spec.Replicas)
				require.Len(t, actual.Status.Conditions, 2)
				require.Equal(t, previousReason, actual.Status.Conditions[0].Reason)
				require.Equal(t, invalidCommittedOrdinalsReason, actual.Status.Conditions[1].Reason)
				version := actual.ResourceVersion
				require.Error(t, r.validateCommittedMembership(ctx, actual))
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(set), actual))
				require.Equal(t, version, actual.ResourceVersion, "repeated validation must not rewrite unchanged conditions")
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
				require.Len(t, actual.Status.Conditions, 1)
				require.Equal(t, previousReason, actual.Status.Conditions[0].Reason)
				members, err := actual.CommittedOrdinals()
				require.NoError(t, err)
				require.Equal(t, []int{0, 2, 3}, members)
			})
		}
	}
}

func TestStatelessCommittedRecordBlocksCleanupAndDispatch(t *testing.T) {
	for _, withInstance := range []bool{true, false} {
		name := "without instances"
		if withInstance {
			name = "with stale Stateful instance"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			set := &workloadsv1alpha2.RoleInstanceSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "set", Namespace: "default", UID: "set-uid",
					Annotations: map[string]string{
						constants.RoleInstancePatternKey:             string(constants.StatelessPattern),
						constants.CommittedSkipOrdinalsAnnotationKey: "1",
					},
				},
				Spec: workloadsv1alpha2.RoleInstanceSetSpec{
					Replicas: ptr.To[int32](3),
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
				},
			}
			instance := &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name: "set-0", Namespace: set.Namespace, UID: "instance-uid",
					Labels: map[string]string{"app": "test"},
					OwnerReferences: []metav1.OwnerReference{
						*metav1.NewControllerRef(set, workloadsv1alpha2.GroupVersion.WithKind("RoleInstanceSet")),
					},
				},
			}
			// Without the validation guard, cleanup would delete this old Stateful
			// instance because it lacks the Stateless owner label.
			require.False(t, instanceBelongsToPattern(instance, set, constants.StatelessPattern))
			creates, deletes, deleteAlls := 0, 0, 0
			builder := fake.NewClientBuilder().WithScheme(newTestScheme()).
				WithStatusSubresource(set).WithObjects(set).
				WithInterceptorFuncs(interceptor.Funcs{
					Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
						if _, ok := obj.(*workloadsv1alpha2.RoleInstance); ok {
							creates++
						}
						return c.Create(ctx, obj, opts...)
					},
					Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
						if _, ok := obj.(*workloadsv1alpha2.RoleInstance); ok {
							deletes++
						}
						return c.Delete(ctx, obj, opts...)
					},
					DeleteAllOf: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteAllOfOption) error {
						if _, ok := obj.(*workloadsv1alpha2.RoleInstance); ok {
							deleteAlls++
						}
						return c.DeleteAllOf(ctx, obj, opts...)
					},
				})
			if withInstance {
				builder.WithObjects(instance)
			}
			c := builder.Build()
			recorder := record.NewFakeRecorder(2)
			statefulCalls, statelessCalls := 0, 0
			r := &RoleInstanceSetReconciler{
				client: c, recorder: recorder,
				statefulMode: reconcile.Func(func(context.Context, reconcile.Request) (reconcile.Result, error) {
					statefulCalls++
					return reconcile.Result{}, nil
				}),
				statelessMode: reconcile.Func(func(context.Context, reconcile.Request) (reconcile.Result, error) {
					statelessCalls++
					return reconcile.Result{}, nil
				}),
			}
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(set)})
			const message = "committed skip ordinals require Stateful mode"
			require.EqualError(t, err, message)
			require.Equal(t, reconcile.Result{}, result)
			actual := &workloadsv1alpha2.RoleInstanceSet{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(set), actual))
			require.Equal(t, set.Spec, actual.Spec)
			require.Equal(t, set.Annotations, actual.Annotations)
			require.Len(t, actual.Status.Conditions, 1)
			condition := actual.Status.Conditions[0]
			require.Equal(t, workloadsv1alpha2.RoleInstanceSetConditionFailedScale, condition.Type)
			require.Equal(t, corev1.ConditionTrue, condition.Status)
			require.Equal(t, "InvalidCommittedOrdinals", condition.Reason)
			require.Equal(t, message, condition.Message)
			select {
			case event := <-recorder.Events:
				require.Equal(t, "Warning InvalidCommittedOrdinals "+message, event)
			default:
				t.Fatal("missing warning event")
			}
			require.Zero(t, statefulCalls)
			require.Zero(t, statelessCalls)
			require.Zero(t, creates)
			require.Zero(t, deletes)
			require.Zero(t, deleteAlls)
			instances := &workloadsv1alpha2.RoleInstanceList{}
			require.NoError(t, c.List(ctx, instances, client.InNamespace(set.Namespace)))
			if withInstance {
				require.Len(t, instances.Items, 1)
				stored := instances.Items[0]
				require.Equal(t, instance.Name, stored.Name)
				require.Equal(t, instance.UID, stored.UID)
				require.Nil(t, stored.DeletionTimestamp)
			} else {
				require.Empty(t, instances.Items)
			}
		})
	}
}
