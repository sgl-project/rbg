package reconciler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func TestStatefulSetReconciler_Reconciler(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	role := wrappers.BuildBasicRole("test-role").Obj()
	rollingRole := wrappers.BuildBasicRole("test-role").WithReplicas(4).
		WithRollingUpdate(
			workloadsv1alpha1.RollingUpdate{
				MaxUnavailable: intstr.FromInt32(2),
				MaxSurge:       intstr.FromInt32(2),
				Partition:      ptr.To(int32(1)),
			},
		).Obj()

	tests := []struct {
		name      string
		rbg       *workloadsv1alpha1.RoleBasedGroup
		role      *workloadsv1alpha1.RoleSpec
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
			name:      "rbg name start with numeric",
			rbg:       wrappers.BuildBasicRoleBasedGroup("123-rbg", "default").Obj(),
			role:      &role,
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

				expectedRevisionHash := "revision-hash-value"
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
					assert.Equal(t, expectedRevisionHash, sts.Labels[fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, tt.role.Name)])

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
	_ = workloadsv1alpha1.AddToScheme(scheme)

	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	role := wrappers.BuildBasicRole("test-role").Obj()

	tests := []struct {
		name        string
		rbg         *workloadsv1alpha1.RoleBasedGroup
		role        *workloadsv1alpha1.RoleSpec
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
					Name:      "test-rbg-test-role",
					Namespace: "default",
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
					Name:      "test-rbg-test-role",
					Namespace: "default",
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
	_ = workloadsv1alpha1.AddToScheme(scheme)

	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()

	stsOwned := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-worker",
			Namespace: "default",
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: "test-rbg",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: workloadsv1alpha1.GroupVersion.String(),
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
				workloadsv1alpha1.SetNameLabelKey: "test-rbg",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: workloadsv1alpha1.GroupVersion.String(),
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
				workloadsv1alpha1.SetNameLabelKey: "other-rbg",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: workloadsv1alpha1.GroupVersion.String(),
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
		rbg           *workloadsv1alpha1.RoleBasedGroup
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

func TestStatefulSetReconciler_RecreateWorkload(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
	role := wrappers.BuildBasicRole("test-role").Obj()
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-test-role",
			Namespace: "default",
			UID:       "sts-uid",
		},
	}

	tests := []struct {
		name          string
		client        client.Client
		rbg           *workloadsv1alpha1.RoleBasedGroup
		role          *workloadsv1alpha1.RoleSpec
		mockReconcile bool
		expectErr     bool
	}{
		{
			name:          "recreate existing workload",
			client:        fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts).Build(),
			rbg:           rbg,
			role:          &role,
			mockReconcile: true,
			expectErr:     false,
		},
		{
			name:          "recreate non-existing workload",
			client:        fake.NewClientBuilder().WithScheme(scheme).Build(),
			rbg:           rbg,
			role:          &role,
			mockReconcile: true,
			expectErr:     false,
		},
		{
			name:          "nil rbg",
			client:        fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts).Build(),
			rbg:           nil,
			role:          &role,
			mockReconcile: false,
			expectErr:     false,
		},
		{
			name:          "nil role",
			client:        fake.NewClientBuilder().WithScheme(scheme).WithObjects(sts).Build(),
			rbg:           rbg,
			role:          nil,
			mockReconcile: false,
			expectErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				r := &StatefulSetReconciler{
					scheme: scheme,
					client: tt.client,
				}

				ctx := log.IntoContext(context.TODO(), zap.New().WithValues("env", "test"))

				if tt.mockReconcile {
					// mock rbg controller reconcile
					go func() {
						for i := 0; i < 60; i++ {
							newSts := &appsv1.StatefulSet{
								ObjectMeta: metav1.ObjectMeta{
									Name:      rbg.GetWorkloadName(&role),
									Namespace: "default",
								},
							}
							err := r.client.Create(ctx, newSts)
							if err != nil && !apierrors.IsAlreadyExists(err) {
								t.Logf("create failed: %v", err)
							}
							time.Sleep(5 * time.Second)
						}
					}()
				}

				err := r.RecreateWorkload(ctx, tt.rbg, tt.role)
				if (err != nil) != tt.expectErr {
					t.Errorf("StsReconciler.RecreateWorkload() error = %v, expectError %v", err, tt.expectErr)
				}
			},
		)
	}
}

func TestStatefulSetReconciler_rollingUpdateParameters(t *testing.T) {
	// test 4 replicas sts rolling update process, maxSurge=2, maxUnavailable=2
	schema := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(schema)
	_ = appsv1.AddToScheme(schema)
	_ = corev1.AddToScheme(schema)
	// the same as *RoleBasedGroup.GetCommonLabelsFromRole()
	commonLabels := map[string]string{
		workloadsv1alpha1.SetNameLabelKey:            "test-rbg",
		workloadsv1alpha1.SetRoleLabelKey:            "test-role",
		workloadsv1alpha1.SetGroupUniqueHashLabelKey: wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj().GenGroupUniqueKey(),
	}

	tests := []struct {
		name            string
		rollingStrategy *workloadsv1alpha1.RolloutStrategy
		sts             *appsv1.StatefulSet
		stsUpdated      bool
		podList         *corev1.PodList
		expectReplicas  int32
		expectPartition int32
		wantErr         bool
	}{
		{
			name: "Stage 1: add 2 new instances",
			rollingStrategy: &workloadsv1alpha1.RolloutStrategy{
				Type: workloadsv1alpha1.RollingUpdateStrategyType,
				RollingUpdate: &workloadsv1alpha1.RollingUpdate{
					MaxUnavailable: intstr.FromInt32(2),
					MaxSurge:       intstr.FromInt32(2),
					Partition:      ptr.To(int32(0)),
				},
			},
			sts: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg-test-role",
					Namespace: "default",
					UID:       "sts-uid",
					Labels:    commonLabels,
					Annotations: map[string]string{
						workloadsv1alpha1.RoleSizeAnnotationKey: "4",
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
			rollingStrategy: &workloadsv1alpha1.RolloutStrategy{
				Type: workloadsv1alpha1.RollingUpdateStrategyType,
				RollingUpdate: &workloadsv1alpha1.RollingUpdate{
					MaxUnavailable: intstr.FromInt32(2),
					MaxSurge:       intstr.FromInt32(2),
					Partition:      ptr.To(int32(0)),
				},
			},
			sts: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg-test-role",
					Namespace: "default",
					UID:       "sts-uid",
					Labels:    commonLabels,
					Annotations: map[string]string{
						workloadsv1alpha1.RoleSizeAnnotationKey: "4",
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
			rollingStrategy: &workloadsv1alpha1.RolloutStrategy{
				Type: workloadsv1alpha1.RollingUpdateStrategyType,
				RollingUpdate: &workloadsv1alpha1.RollingUpdate{
					MaxUnavailable: intstr.FromInt32(2),
					MaxSurge:       intstr.FromInt32(2),
					Partition:      ptr.To(int32(0)),
				},
			},
			sts: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-rbg-test-role",
					Namespace: "default",
					UID:       "sts-uid",
					Labels:    commonLabels,
					Annotations: map[string]string{
						workloadsv1alpha1.RoleSizeAnnotationKey: "4",
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
				rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj()
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
