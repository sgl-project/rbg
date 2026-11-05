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

package reconciler

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func TestValidateRolloutStrategyAllowsZeroReplicas(t *testing.T) {
	tests := []struct {
		name           string
		maxUnavailable intstr.IntOrString
	}{
		{name: "percentage", maxUnavailable: intstr.FromString("30%")},
		{name: "integer", maxUnavailable: intstr.FromInt32(0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			strategy := &workloadsv1alpha2.RolloutStrategy{
				Type: workloadsv1alpha2.RollingUpdateStrategyType,
				RollingUpdate: &workloadsv1alpha2.RollingUpdate{
					MaxUnavailable: ptr.To(tt.maxUnavailable),
					MaxSurge:       ptr.To(intstr.FromInt32(0)),
					Partition:      ptr.To(intstr.FromInt32(0)),
				},
			}

			_, err := validateRolloutStrategy(strategy, 0)

			assert.NoError(t, err)
		})
	}
}

func TestValidateRolloutStrategyRejectsZeroBudgetWhenReplicasGreaterThanZero(t *testing.T) {
	strategy := &workloadsv1alpha2.RolloutStrategy{
		Type: workloadsv1alpha2.RollingUpdateStrategyType,
		RollingUpdate: &workloadsv1alpha2.RollingUpdate{
			MaxUnavailable: ptr.To(intstr.FromInt32(0)),
			MaxSurge:       ptr.To(intstr.FromInt32(0)),
			Partition:      ptr.To(intstr.FromInt32(0)),
		},
	}

	_, err := validateRolloutStrategy(strategy, 1)

	assert.ErrorContains(t, err, "maxUnavailable may not be 0 when maxSurge is 0")
}

func TestStatefulSetReconciler_Reconciler(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	role := wrappersv2.BuildStandaloneRole("test-role").WithWorkload("apps/v1", "StatefulSet").Obj()
	rollingRole := wrappersv2.BuildStandaloneRole("test-role").WithReplicas(4).WithWorkload("apps/v1", "StatefulSet").
		WithRollingUpdate(
			workloadsv1alpha2.RollingUpdate{
				MaxUnavailable: ptr.To(intstr.FromInt32(2)),
				MaxSurge:       ptr.To(intstr.FromInt32(2)),
				Partition:      ptr.To(intstr.FromInt32(1)),
			},
		).Obj()

	tests := []struct {
		name      string
		rbg       *workloadsv1alpha2.RoleBasedGroup
		role      *workloadsv1alpha2.RoleSpec
		expectErr bool
	}{
		{
			name:      "normal",
			rbg:       rbg,
			role:      &role,
			expectErr: false,
		},
		{
			name:      "role with rollingUpdate",
			rbg:       rbg,
			role:      &rollingRole,
			expectErr: false,
		},
		{
			name: "rbg name start with numeric",
			rbg:  wrappersv2.BuildBasicRoleBasedGroup("123-rbg", "default").Obj(),
			role: func() *workloadsv1alpha2.RoleSpec {
				r := wrappersv2.BuildStandaloneRole("test-role").WithWorkload("apps/v1", "StatefulSet").Obj()
				return &r
			}(),
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				client := fake.NewClientBuilder().WithScheme(scheme).Build()

				r := &StatefulSetReconciler{
					scheme: scheme,
					client: client,
				}
				err := r.Reconciler(context.Background(), tt.rbg, tt.role, nil, expectedRevisionHash)
				if tt.expectErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)

					// Check if StatefulSet was created
					sts := &appsv1.StatefulSet{}
					err = client.Get(
						context.Background(), types.NamespacedName{
							Name:      tt.rbg.GetWorkloadName(tt.role),
							Namespace: tt.rbg.Namespace,
						}, sts,
					)
					assert.NoError(t, err)
					assert.Equal(t, tt.rbg.GetWorkloadName(tt.role), sts.Name)
					assert.Equal(t, expectedRevisionHash, sts.Labels[fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, tt.role.Name)])

					// Check if Service was created
					svc := &corev1.Service{}
					err = client.Get(
						context.Background(), types.NamespacedName{
							Name:      tt.rbg.GetServiceName(tt.role),
							Namespace: tt.rbg.Namespace,
						}, svc,
					)
					assert.NoError(t, err)
					assert.Equal(t, tt.rbg.GetServiceName(tt.role), svc.Name)
				}
			},
		)
	}
}

func TestStatefulSetReconciler_CheckWorkloadReady(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	role := wrappersv2.BuildStandaloneRole("test-role").WithWorkload("apps/v1", "StatefulSet").Obj()

	tests := []struct {
		name        string
		rbg         *workloadsv1alpha2.RoleBasedGroup
		role        *workloadsv1alpha2.RoleSpec
		sts         *appsv1.StatefulSet
		expectReady bool
		expectErr   bool
	}{
		{
			name: "workload ready",
			rbg:  rbg,
			role: &role,
			sts: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test-rbg-test-role",
					Namespace:       "default",
					OwnerReferences: claimTestOwnedBy(rbg),
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To[int32](3),
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: 3,
					Replicas:      3,
				},
			},
			expectReady: true,
			expectErr:   false,
		},
		{
			name: "workload not ready",
			rbg:  rbg,
			role: &role,
			sts: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test-rbg-test-role",
					Namespace:       "default",
					OwnerReferences: claimTestOwnedBy(rbg),
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To[int32](3),
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: 2,
					Replicas:      2,
				},
			},
			expectReady: false,
			expectErr:   false,
		},
		{
			name:        "workload not found",
			rbg:         rbg,
			role:        &role,
			sts:         nil,
			expectReady: false,
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				clientBuilder := fake.NewClientBuilder().WithScheme(scheme)
				if tt.sts != nil {
					clientBuilder = clientBuilder.WithObjects(tt.sts)
				}

				r := &StatefulSetReconciler{
					scheme: scheme,
					client: clientBuilder.Build(),
				}

				ready, err := r.CheckWorkloadReady(context.Background(), tt.rbg, tt.role)
				if tt.expectErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
					assert.Equal(t, tt.expectReady, ready)
				}
			},
		)
	}
}

func TestStatefulSetReconciler_CleanupOrphanedWorkloads(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()

	stsOwned := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-worker",
			Namespace: "default",
			Labels: map[string]string{
				constants.GroupNameLabelKey: "test-rbg",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: workloadsv1alpha2.GroupVersion.String(),
					Kind:       "RoleBasedGroup",
					Name:       "test-rbg",
					UID:        rbg.UID,
					Controller: ptr.To[bool](true),
				},
			},
		},
	}

	stsOrphaned := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-orphaned",
			Namespace: "default",
			Labels: map[string]string{
				constants.GroupNameLabelKey: "test-rbg",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: workloadsv1alpha2.GroupVersion.String(),
					Kind:       "RoleBasedGroup",
					Name:       "test-rbg",
					UID:        rbg.UID,
					Controller: ptr.To[bool](true),
				},
			},
		},
	}

	stsDifferentOwner := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-rbg-worker",
			Namespace: "default",
			Labels: map[string]string{
				constants.GroupNameLabelKey: "other-rbg",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: workloadsv1alpha2.GroupVersion.String(),
					Kind:       "RoleBasedGroup",
					Name:       "other-rbg",
					UID:        "other-uid",
					Controller: ptr.To[bool](true),
				},
			},
		},
	}

	tests := []struct {
		name          string
		rbg           *workloadsv1alpha2.RoleBasedGroup
		existingObjs  []runtime.Object
		expectDeleted []string
		expectErr     bool
	}{
		{
			name: "cleanup orphaned workloads",
			rbg:  rbg,
			existingObjs: []runtime.Object{
				stsOwned,
				stsOrphaned,
				stsDifferentOwner,
			},
			expectDeleted: []string{"test-rbg-orphaned"},
			expectErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				client := fake.NewClientBuilder().
					WithScheme(scheme).
					WithRuntimeObjects(tt.existingObjs...).
					Build()

				r := &StatefulSetReconciler{
					scheme: scheme,
					client: client,
				}

				err := r.CleanupOrphanedWorkloads(context.Background(), tt.rbg)
				if tt.expectErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)

					// Check that orphaned workloads were deleted
					for _, name := range tt.expectDeleted {
						sts := &appsv1.StatefulSet{}
						err = client.Get(
							context.Background(), types.NamespacedName{
								Name:      name,
								Namespace: tt.rbg.Namespace,
							}, sts,
						)
						assert.True(t, apierrors.IsNotFound(err), "Expected %s to be deleted", name)
					}
				}
			},
		)
	}
}

func TestStatefulSetReconciler_rollingUpdateParameters(t *testing.T) {
	// test 4 replicas sts rolling update process, maxSurge=2, maxUnavailable=2
	schema := runtime.NewScheme()
	_ = workloadsv1alpha2.AddToScheme(schema)
	_ = appsv1.AddToScheme(schema)
	_ = corev1.AddToScheme(schema)
	// the same as *RoleBasedGroup.GetCommonLabelsFromRole()
	commonLabels := map[string]string{
		constants.GroupNameLabelKey: "test-rbg",
		constants.RoleNameLabelKey:  "test-role",
		constants.GroupUIDLabelKey:  wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj().GenGroupUniqueKey(),
	}

	tests := []struct {
		name            string
		rollingStrategy *workloadsv1alpha2.RolloutStrategy
		sts             *appsv1.StatefulSet
		stsUpdated      bool
		podList         *corev1.PodList
		expectReplicas  int32
		expectPartition int32
		wantErr         bool
	}{
		{
			name: "Stage 1: add 2 new instances",
			rollingStrategy: &workloadsv1alpha2.RolloutStrategy{
				Type: workloadsv1alpha2.RollingUpdateStrategyType,
				RollingUpdate: &workloadsv1alpha2.RollingUpdate{
					MaxUnavailable: ptr.To(intstr.FromInt32(2)),
					MaxSurge:       ptr.To(intstr.FromInt32(2)),
					Partition:      ptr.To(intstr.FromInt32(0)),
				},
			},
			sts: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg-test-role",
					Namespace: "default",
					UID:       "sts-uid",
					Labels:    commonLabels,
					Annotations: map[string]string{
						constants.RoleSizeAnnotationKey: "4",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{MatchLabels: commonLabels},
					Replicas: ptr.To(int32(4)),
				},
				Status: appsv1.StatefulSetStatus{
					Replicas:        4,
					ReadyReplicas:   4,
					CurrentReplicas: 4,
					UpdatedReplicas: 4,
				},
			},
			podList: &corev1.PodList{
				Items: []corev1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-0",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "oldRevision",
								"apps.kubernetes.io/pod-index": "0",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-1",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "oldRevision",
								"apps.kubernetes.io/pod-index": "1",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-2",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "oldRevision",
								"apps.kubernetes.io/pod-index": "2",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-3",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "oldRevision",
								"apps.kubernetes.io/pod-index": "3",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
				},
			},

			stsUpdated:      true,
			expectReplicas:  6,
			expectPartition: 4,
			wantErr:         false,
		},
		{
			name: "Stage 2: rolling update 2 old instances",
			rollingStrategy: &workloadsv1alpha2.RolloutStrategy{
				Type: workloadsv1alpha2.RollingUpdateStrategyType,
				RollingUpdate: &workloadsv1alpha2.RollingUpdate{
					MaxUnavailable: ptr.To(intstr.FromInt32(2)),
					MaxSurge:       ptr.To(intstr.FromInt32(2)),
					Partition:      ptr.To(intstr.FromInt32(0)),
				},
			},
			sts: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg-test-role",
					Namespace: "default",
					UID:       "sts-uid",
					Labels:    commonLabels,
					Annotations: map[string]string{
						constants.RoleSizeAnnotationKey: "4",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{MatchLabels: commonLabels},
					Replicas: ptr.To(int32(6)),
					UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
						Type: appsv1.RollingUpdateStatefulSetStrategyType,
						RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
							Partition: ptr.To(int32(4)),
						},
					},
				},
				Status: appsv1.StatefulSetStatus{
					Replicas:        6,
					ReadyReplicas:   4,
					UpdatedReplicas: 2,
				},
			},
			podList: &corev1.PodList{
				Items: []corev1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-0",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "oldRevision",
								"apps.kubernetes.io/pod-index": "0",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-1",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "oldRevision",
								"apps.kubernetes.io/pod-index": "1",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-2",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "oldRevision",
								"apps.kubernetes.io/pod-index": "2",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-3",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "oldRevision",
								"apps.kubernetes.io/pod-index": "3",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-4",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "newRevision",
								"apps.kubernetes.io/pod-index": "4",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionFalse,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-5",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "newRevision",
								"apps.kubernetes.io/pod-index": "5",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionFalse,
								},
							},
						},
					},
				},
			},
			stsUpdated:      false,
			expectReplicas:  6,
			expectPartition: 2,
			wantErr:         false,
		},
		{
			name: "Stage 3: rolling update remaining old instances",
			rollingStrategy: &workloadsv1alpha2.RolloutStrategy{
				Type: workloadsv1alpha2.RollingUpdateStrategyType,
				RollingUpdate: &workloadsv1alpha2.RollingUpdate{
					MaxUnavailable: ptr.To(intstr.FromInt32(2)),
					MaxSurge:       ptr.To(intstr.FromInt32(2)),
					Partition:      ptr.To(intstr.FromInt32(0)),
				},
			},
			sts: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg-test-role",
					Namespace: "default",
					UID:       "sts-uid",
					Labels:    commonLabels,
					Annotations: map[string]string{
						constants.RoleSizeAnnotationKey: "4",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: commonLabels,
					},
					Replicas: ptr.To(int32(6)),
					UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
						Type: appsv1.RollingUpdateStatefulSetStrategyType,
						RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
							Partition: ptr.To(int32(2)),
						},
					},
				},
				Status: appsv1.StatefulSetStatus{
					Replicas:        6,
					ReadyReplicas:   4,
					UpdatedReplicas: 4,
				},
			},
			podList: &corev1.PodList{
				Items: []corev1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-0",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "oldRevision",
								"apps.kubernetes.io/pod-index": "0",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-1",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "oldRevision",
								"apps.kubernetes.io/pod-index": "1",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-2",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "newRevision",
								"apps.kubernetes.io/pod-index": "2",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-3",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "newRevision",
								"apps.kubernetes.io/pod-index": "3",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-4",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "newRevision",
								"apps.kubernetes.io/pod-index": "4",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-rbg-test-role-5",
							Namespace: "default",
							Labels: mergeLabels(commonLabels, map[string]string{
								"controller-revision-hash":     "newRevision",
								"apps.kubernetes.io/pod-index": "5",
							}),
						},
						Status: corev1.PodStatus{
							Phase: corev1.PodRunning,
							Conditions: []corev1.PodCondition{
								{
									Type:   corev1.PodReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
				},
			},
			stsUpdated:      false,
			expectReplicas:  5,
			expectPartition: 0,
			wantErr:         false,
		},
	}

	oldRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "oldRevision",
			Namespace: "default",
			Labels:    commonLabels,
		},
		Revision: 1,
	}
	newRevision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "newRevision",
			Namespace: "default",
			Labels:    commonLabels,
		},
		Revision: 2,
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
				rbg.Spec.Roles[0].Replicas = ptr.To(int32(4))
				if tt.rollingStrategy != nil {
					rbg.Spec.Roles[0].RolloutStrategy = tt.rollingStrategy
				}

				fakeClient := fake.NewClientBuilder().WithScheme(schema).WithRuntimeObjects(
					rbg, tt.sts, oldRevision, newRevision, tt.podList,
				).Build()
				r := NewStatefulSetReconciler(schema, fakeClient)

				ctx := log.IntoContext(context.TODO(), zap.New().WithValues("env", "test"))
				retPartition, retReplicas, retErr := r.rollingUpdateParameters(
					ctx, &rbg.Spec.Roles[0], tt.sts, tt.stsUpdated, nil)

				if tt.wantErr != (retErr != nil) {
					t.Errorf("rollingUpdateParameters() error = %v, wantErr %v", retErr, tt.wantErr)
				}

				if !tt.wantErr {
					assert.Equal(t, tt.expectPartition, retPartition)
					assert.Equal(t, tt.expectReplicas, retReplicas)
				}

			},
		)
	}
}

var errInjectedRead = errors.New("injected read failure")

func newRolloutErrScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

func newRolloutErrRBG(partition intstr.IntOrString) (*workloadsv1alpha2.RoleBasedGroup, *workloadsv1alpha2.RoleSpec) {
	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	rbg.Spec.Roles[0].Replicas = ptr.To(int32(4))
	rbg.Spec.Roles[0].RolloutStrategy = &workloadsv1alpha2.RolloutStrategy{
		Type: workloadsv1alpha2.RollingUpdateStrategyType,
		RollingUpdate: &workloadsv1alpha2.RollingUpdate{
			MaxUnavailable: ptr.To(intstr.FromInt32(1)),
			MaxSurge:       ptr.To(intstr.FromInt32(1)),
			Partition:      ptr.To(partition),
		},
	}
	return rbg, &rbg.Spec.Roles[0]
}

func failListOf[T client.ObjectList]() *interceptor.Funcs {
	return &interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(T); ok {
				return errInjectedRead
			}
			return c.List(ctx, list, opts...)
		},
	}
}

func TestRollingUpdateParameters_PreservesReadErrors(t *testing.T) {
	scheme := newRolloutErrScheme(t)
	rbg, role := newRolloutErrRBG(intstr.FromInt32(0))
	selector := rbg.GetCommonLabelsFromRole(role)
	revision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: "rev-1", Namespace: "default", Labels: selector},
		Revision:   1,
	}
	surgedSts := func(roleSize *string) *appsv1.StatefulSet {
		annotations := map[string]string{}
		if roleSize != nil {
			annotations[constants.RoleSizeAnnotationKey] = *roleSize
		}
		return &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: rbg.GetWorkloadName(role), Namespace: "default", UID: "sts-uid", Annotations: annotations,
			},
			Spec: appsv1.StatefulSetSpec{
				Replicas: ptr.To(int32(5)),
				Selector: &metav1.LabelSelector{MatchLabels: selector},
				UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
					Type:          appsv1.RollingUpdateStatefulSetStrategyType,
					RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptr.To(int32(0))},
				},
			},
		}
	}

	tests := []struct {
		name      string
		sts       *appsv1.StatefulSet
		objs      []client.Object
		funcs     *interceptor.Funcs
		wantErrIs error
	}{
		{
			name:      "pod list failure",
			sts:       surgedSts(ptr.To("4")),
			objs:      []client.Object{revision},
			funcs:     failListOf[*corev1.PodList](),
			wantErrIs: errInjectedRead,
		},
		{
			name:      "controllerrevision list failure",
			sts:       surgedSts(ptr.To("4")),
			objs:      []client.Object{revision},
			funcs:     failListOf[*appsv1.ControllerRevisionList](),
			wantErrIs: errInjectedRead,
		},
		{name: "missing controllerrevision", sts: surgedSts(ptr.To("4"))},
		{name: "malformed role-size annotation", sts: surgedSts(ptr.To("four")), objs: []client.Object{revision}},
		{name: "missing role-size annotation", sts: surgedSts(nil), objs: []client.Object{revision}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(tt.objs, tt.sts)...)
			if tt.funcs != nil {
				b = b.WithInterceptorFuncs(*tt.funcs)
			}
			r := NewStatefulSetReconciler(scheme, b.Build())

			_, replicas, err := r.rollingUpdateParameters(context.Background(), role, tt.sts, false, nil)
			require.Error(t, err, "error was swallowed; caller would apply replicas=%d", replicas)
			if tt.wantErrIs != nil {
				assert.ErrorIs(t, err, tt.wantErrIs)
			}
		})
	}
}

func TestRollingUpdateParameters_PropagatesPartitionConversionError(t *testing.T) {
	scheme := newRolloutErrScheme(t)
	rbg, role := newRolloutErrRBG(intstr.FromString("not-a-percent"))
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: rbg.GetWorkloadName(role), Namespace: "default", UID: "sts-uid"},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(4)),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type:          appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptr.To(int32(0))},
			},
		},
	}
	r := NewStatefulSetReconciler(scheme, fake.NewClientBuilder().WithScheme(scheme).Build())

	_, _, err := r.rollingUpdateParameters(context.Background(), role, sts, false, nil)
	require.Error(t, err)
}

func TestRollingUpdatePartition_ClampsIndexToLiveStates(t *testing.T) {
	// Desired 4, maxUnavailable 1, live 1, and no continuously ready replica
	// produces rollingStep -2. The partition index would be 3.
	got := rollingUpdatePartition(context.Background(), make([]replicaState, 1), 1, -2, 0)
	assert.Equal(t, int32(0), got)

	// A live count of 0 used to panic at states[0].
	got = rollingUpdatePartition(context.Background(), nil, 0, -3, 2)
	assert.Equal(t, int32(0), got)
}

func TestRollingUpdateParameters_ScalesUpWhenLiveReplicasBelowDesired(t *testing.T) {
	scheme := newRolloutErrScheme(t)
	rbg, role := newRolloutErrRBG(intstr.FromInt32(0))
	selector := rbg.GetCommonLabelsFromRole(role)
	revision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: "rev-1", Namespace: "default", Labels: selector},
		Revision:   1,
	}
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        rbg.GetWorkloadName(role),
			Namespace:   "default",
			UID:         "sts-uid",
			Annotations: map[string]string{constants.RoleSizeAnnotationKey: "4"},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type:          appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptr.To(int32(2))},
			},
		},
	}
	r := NewStatefulSetReconciler(scheme, fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts, revision).Build())

	partition, replicas, err := r.rollingUpdateParameters(context.Background(), role, sts, false, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(4), replicas)
	assert.Equal(t, int32(1), partition)
}

func TestReconcileStatefulSet_ReadFailureDoesNotScaleToZero(t *testing.T) {
	scheme := newRolloutErrScheme(t)
	rbg, role := newRolloutErrRBG(intstr.FromInt32(0))
	ctx := context.Background()
	const revisionKey = "rev-1"

	seeder := NewStatefulSetReconciler(scheme, fake.NewClientBuilder().WithScheme(scheme).Build())
	applyCfg, err := seeder.constructStatefulSetApplyConfiguration(ctx, rbg, role, &appsv1.StatefulSet{}, revisionKey)
	require.NoError(t, err)
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(applyCfg)
	require.NoError(t, err)
	live := &appsv1.StatefulSet{}
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(u, live))
	live.UID = "sts-uid"
	live.Spec.Replicas = ptr.To(int32(5))
	live.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
		Type:          appsv1.RollingUpdateStatefulSetStrategyType,
		RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptr.To(int32(0))},
	}
	revision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: revisionKey, Namespace: "default", Labels: live.Spec.Selector.MatchLabels},
		Revision:   1,
	}

	var writes []string
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(live, revision).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.PodList); ok {
				return apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("pods is forbidden"))
			}
			return c.List(ctx, list, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			data, _ := patch.Data(obj)
			writes = append(writes, string(data))
			return c.Patch(ctx, obj, patch, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			writes = append(writes, "update")
			return c.Update(ctx, obj, opts...)
		},
	}).Build()

	err = NewStatefulSetReconciler(scheme, c).reconcileStatefulSet(ctx, rbg, role, nil, revisionKey)
	assert.True(t, apierrors.IsForbidden(err), "expected the Pod List error to propagate, got %v", err)
	assert.Empty(t, writes, "StatefulSet must not be written when a rollout read fails")

	got := &appsv1.StatefulSet{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: live.Name, Namespace: live.Namespace}, got))
	assert.Equal(t, int32(5), *got.Spec.Replicas, "live replicas must be unchanged")
}

func Test_calculateRoleUnreadyReplicas(t *testing.T) {
	tests := []struct {
		name         string
		states       []replicaState
		roleReplicas int32
		expected     int32
	}{
		{
			name: "all ready and updated",
			states: []replicaState{
				{ready: true, updated: true},
				{ready: true, updated: true},
				{ready: true, updated: true},
			},
			roleReplicas: 3,
			expected:     0,
		},
		{
			name: "some unready replicas",
			states: []replicaState{
				{ready: true, updated: true},
				{ready: false, updated: true},
				{ready: true, updated: false},
			},
			roleReplicas: 3,
			expected:     2,
		},
		{
			name: "more states than role replicas",
			states: []replicaState{
				{ready: true, updated: true},
				{ready: true, updated: true},
				{ready: true, updated: true},
				{ready: false, updated: false}, // Should be ignored
			},
			roleReplicas: 3,
			expected:     0,
		},
		{
			name:         "empty states",
			states:       []replicaState{},
			roleReplicas: 3,
			expected:     3,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := calculateRoleUnreadyReplicas(tt.states, tt.roleReplicas)
				assert.Equal(t, tt.expected, result)
			},
		)
	}
}

func Test_calculateContinuousReadyReplicas(t *testing.T) {
	tests := []struct {
		name     string
		states   []replicaState
		expected int32
	}{
		{
			name: "all ready from the end",
			states: []replicaState{
				{ready: true, updated: true},
				{ready: true, updated: true},
				{ready: true, updated: true},
			},
			expected: 3,
		},
		{
			name: "some ready from the end",
			states: []replicaState{
				{ready: true, updated: true},
				{ready: false, updated: true},
				{ready: true, updated: true},
				{ready: true, updated: true},
			},
			expected: 2,
		},
		{
			name: "none ready",
			states: []replicaState{
				{ready: false, updated: false},
				{ready: false, updated: false},
			},
			expected: 0,
		},
		{
			name:     "empty states",
			states:   []replicaState{},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := calculateContinuousReadyReplicas(tt.states)
				assert.Equal(t, tt.expected, result)
			},
		)
	}
}

func mergeLabels(labels ...map[string]string) map[string]string {
	result := make(map[string]string)
	for _, label := range labels {
		for k, v := range label {
			result[k] = v
		}
	}
	return result
}

func TestConstructStatefulSetApplyConfiguration_LabelsAndAnnotations(t *testing.T) {
	role := &workloadsv1alpha2.RoleSpec{
		Name:     "test-role",
		Replicas: ptr.To(int32(3)),
		Pattern: workloadsv1alpha2.Pattern{
			StandalonePattern: &workloadsv1alpha2.StandalonePattern{
				TemplateSource: workloadsv1alpha2.TemplateSource{
					Template: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: "test-image:v1",
								},
							},
						},
					},
				},
			},
		},
	}

	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{*role},
		},
	}

	tests := []struct {
		name                string
		roleLabels          map[string]string
		roleAnnotations     map[string]string
		expectedLabels      map[string]string
		expectedAnnotations map[string]string
	}{
		{
			name: "test role labels and annotations",
			roleLabels: map[string]string{
				"app":          "my-app",
				"version":      "v1.0",
				"custom-label": "role-value",
			},
			expectedLabels: map[string]string{
				"app":                       "my-app",
				"version":                   "v1.0",
				"custom-label":              "role-value",
				constants.RoleNameLabelKey:  role.Name,
				constants.GroupNameLabelKey: rbg.Name,
				constants.GroupUIDLabelKey:  rbg.GenGroupUniqueKey(),
				fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name): expectedRevisionHash,
			},
			roleAnnotations: map[string]string{
				"description":       "custom description",
				"custom-annotation": "role-value",
			},
			expectedAnnotations: map[string]string{
				"description":                   "custom description",
				"custom-annotation":             "role-value",
				constants.RoleSizeAnnotationKey: "3",
			},
		},
		{
			name: "test role labels and annotations with priority",
			roleLabels: map[string]string{
				"app":                       "my-app",
				"version":                   "v1.0",
				"custom-label":              "role-value",
				constants.RoleNameLabelKey:  "custom",
				constants.GroupNameLabelKey: "custom",
				constants.GroupUIDLabelKey:  "custom",
				fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name): "custom",
			},
			expectedLabels: map[string]string{
				"app":                       "my-app",
				"version":                   "v1.0",
				"custom-label":              "role-value",
				constants.RoleNameLabelKey:  role.Name,
				constants.GroupNameLabelKey: rbg.Name,
				constants.GroupUIDLabelKey:  rbg.GenGroupUniqueKey(),
				fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name): expectedRevisionHash,
			},
			roleAnnotations: map[string]string{
				"description":                   "custom description",
				"custom-annotation":             "role-value",
				constants.RoleSizeAnnotationKey: "custom",
			},
			expectedAnnotations: map[string]string{
				"description":                   "custom description",
				"custom-annotation":             "role-value",
				constants.RoleSizeAnnotationKey: "3",
			},
		},
		{
			name:            "no role labels or annotations",
			roleLabels:      nil,
			roleAnnotations: nil,
			expectedLabels: map[string]string{
				constants.RoleNameLabelKey:                                role.Name,
				constants.GroupNameLabelKey:                               rbg.Name,
				constants.GroupUIDLabelKey:                                rbg.GenGroupUniqueKey(),
				fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name): expectedRevisionHash,
			},
			expectedAnnotations: map[string]string{
				constants.RoleSizeAnnotationKey: "3",
			},
		},
		{
			name:            "empty role labels and annotations",
			roleLabels:      map[string]string{},
			roleAnnotations: map[string]string{},
			expectedLabels: map[string]string{
				constants.RoleNameLabelKey:                                role.Name,
				constants.GroupNameLabelKey:                               rbg.Name,
				constants.GroupUIDLabelKey:                                rbg.GenGroupUniqueKey(),
				fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name): expectedRevisionHash,
			},
			expectedAnnotations: map[string]string{
				constants.RoleSizeAnnotationKey: "3",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = appsv1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			_ = workloadsv1alpha2.AddToScheme(scheme)

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			reconciler := NewStatefulSetReconciler(scheme, fakeClient)

			role.Labels = tt.roleLabels
			role.Annotations = tt.roleAnnotations

			result, err := reconciler.constructStatefulSetApplyConfiguration(
				context.Background(),
				rbg,
				role,
				&appsv1.StatefulSet{},
				expectedRevisionHash,
			)

			if err != nil {
				t.Fatalf("constructStatefulSetApplyConfiguration() error = %v", err)
			}

			assert.Equal(t, tt.expectedLabels, result.Labels)
			assert.Equal(t, tt.expectedAnnotations, result.Annotations)
		})
	}
}
