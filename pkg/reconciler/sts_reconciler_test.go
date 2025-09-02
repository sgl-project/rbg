package reconciler

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/rbgs/test/wrappers"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func getFakeClient() client.Client {

	crList := &appsv1.ControllerRevisionList{
		Items: []appsv1.ControllerRevision{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "rolling-update-test-7468d9f96c",
					Labels: map[string]string{
						workloadsv1alpha1.SetNameLabelKey: "rolling-update-test",
					},
				},
				Revision: 1,
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "rolling-update-test-98b55cfff",
					Labels: map[string]string{
						workloadsv1alpha1.SetNameLabelKey: "rolling-update-test",
					},
				},
				Revision: 2,
			},
		},
	}

	podList := &corev1.PodList{
		Items: []corev1.Pod{
			wrappers.BuildBasicPod().
				WithName("rolling-update-test-0").
				WithLabels(map[string]string{
					"controller-revision-hash":        "rolling-update-test-7468d9f96c",
					workloadsv1alpha1.SetNameLabelKey: "rolling-update-test",
					"apps.kubernetes.io/pod-index":    "0",
				}).
				WithReadyCondition(true).
				Obj(),
			wrappers.BuildBasicPod().
				WithName("rolling-update-test-1").
				WithLabels(map[string]string{
					"controller-revision-hash":        "rolling-update-test-7468d9f96c",
					workloadsv1alpha1.SetNameLabelKey: "rolling-update-test",
					"apps.kubernetes.io/pod-index":    "1",
				}).
				WithReadyCondition(true).
				Obj(),
			wrappers.BuildBasicPod().
				WithName("rolling-update-test-2").
				WithLabels(map[string]string{
					"controller-revision-hash":        "rolling-update-test-7468d9f96c",
					workloadsv1alpha1.SetNameLabelKey: "rolling-update-test",
					"apps.kubernetes.io/pod-index":    "2",
				}).
				WithReadyCondition(true).
				Obj(),
			wrappers.BuildBasicPod().
				WithName("rolling-update-test-3").
				WithLabels(map[string]string{
					"controller-revision-hash":        "rolling-update-test-7468d9f96c",
					workloadsv1alpha1.SetNameLabelKey: "rolling-update-test",
					"apps.kubernetes.io/pod-index":    "3",
				}).
				WithReadyCondition(true).
				Obj(),
			wrappers.BuildBasicPod().
				WithName("rolling-update-test-4").
				WithLabels(map[string]string{
					"controller-revision-hash":        "rolling-update-test-98b55cfff",
					workloadsv1alpha1.SetNameLabelKey: "rolling-update-test",
					"apps.kubernetes.io/pod-index":    "4",
				}).
				WithReadyCondition(true).
				Obj(),
			wrappers.BuildBasicPod().
				WithName("rolling-update-test-5").
				WithLabels(map[string]string{
					"controller-revision-hash":        "rolling-update-test-98b55cfff",
					workloadsv1alpha1.SetNameLabelKey: "rolling-update-test",
					"apps.kubernetes.io/pod-index":    "5",
				}).
				WithReadyCondition(false).
				Obj(),
		},
	}

	objs := []runtime.Object{crList, podList}
	return fake.NewClientBuilder().WithRuntimeObjects(objs...).Build()
}

func TestStatefulSetReconciler_ConstructRoleStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = appsv1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)
	type fields struct {
		client client.Client
		scheme *runtime.Scheme
	}
	type args struct {
		ctx  context.Context
		rbg  *workloadsv1alpha1.RoleBasedGroup
		role *workloadsv1alpha1.RoleSpec
	}

	// 测试用 StatefulSet 模板
	testSTS := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg-test-role",
			Namespace: "default",
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To[int32](3),
		},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: 2,
		},
	}

	tests := []struct {
		name             string
		fields           fields
		args             args
		wantStatus       workloadsv1alpha1.RoleStatus
		wantUpdateStatus bool
		wantErr          bool
	}{
		{
			name: "case 1: status-changed-needs-update",
			fields: fields{
				scheme: scheme,
				client: fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(testSTS.DeepCopy()).
					Build(),
			},
			args: args{
				ctx: context.Background(),
				rbg: &workloadsv1alpha1.RoleBasedGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rbg",
						Namespace: "default",
					},
					Status: workloadsv1alpha1.RoleBasedGroupStatus{
						RoleStatuses: []workloadsv1alpha1.RoleStatus{
							{
								Name:          "test-role",
								Replicas:      2,
								ReadyReplicas: 1,
							},
						},
					},
				},
				role: &workloadsv1alpha1.RoleSpec{Name: "test-role"},
			},
			wantStatus: workloadsv1alpha1.RoleStatus{
				Name:          "test-role",
				Replicas:      3,
				ReadyReplicas: 2,
			},
			wantUpdateStatus: true,
			wantErr:          false,
		},
		{
			name: "case 2: status-unchanged-no-update",
			fields: fields{
				scheme: scheme,
				client: fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(testSTS.DeepCopy()).
					Build(),
			},
			args: args{
				ctx: context.Background(),
				rbg: &workloadsv1alpha1.RoleBasedGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rbg",
						Namespace: "default",
					},
					Status: workloadsv1alpha1.RoleBasedGroupStatus{
						RoleStatuses: []workloadsv1alpha1.RoleStatus{
							{
								Name:          "test-role",
								Replicas:      3,
								ReadyReplicas: 2,
							},
						},
					},
				},
				role: &workloadsv1alpha1.RoleSpec{Name: "test-role"},
			},
			wantStatus: workloadsv1alpha1.RoleStatus{
				Name:          "test-role",
				Replicas:      3,
				ReadyReplicas: 2,
			},
			wantUpdateStatus: false,
			wantErr:          false,
		},
		{
			name: "case 3: initial-status-creation",
			fields: fields{
				scheme: scheme,
				client: fake.NewClientBuilder().
					WithScheme(scheme).
					WithObjects(testSTS.DeepCopy()).
					Build(),
			},
			args: args{
				ctx: context.Background(),
				rbg: &workloadsv1alpha1.RoleBasedGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rbg",
						Namespace: "default",
					},
					Status: workloadsv1alpha1.RoleBasedGroupStatus{
						RoleStatuses: []workloadsv1alpha1.RoleStatus{},
					},
				},
				role: &workloadsv1alpha1.RoleSpec{Name: "test-role"},
			},
			wantStatus: workloadsv1alpha1.RoleStatus{
				Name:          "test-role",
				Replicas:      3,
				ReadyReplicas: 2,
			},
			wantUpdateStatus: true,
			wantErr:          false,
		},
		{
			name: "case 4: statefulset-not-found",
			fields: fields{
				scheme: scheme,
				client: fake.NewClientBuilder().
					WithScheme(scheme).
					Build(),
			},
			args: args{
				ctx: context.Background(),
				rbg: &workloadsv1alpha1.RoleBasedGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-rbg",
						Namespace: "default",
					},
					Status: workloadsv1alpha1.RoleBasedGroupStatus{
						RoleStatuses: []workloadsv1alpha1.RoleStatus{
							{Name: "test-role", Replicas: 2, ReadyReplicas: 1},
						},
					},
				},
				role: &workloadsv1alpha1.RoleSpec{Name: "test-role"},
			},
			wantStatus:       workloadsv1alpha1.RoleStatus{},
			wantUpdateStatus: false,
			wantErr:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &StatefulSetReconciler{
				scheme: tt.fields.scheme,
				client: tt.fields.client,
			}
			gotStatus, gotUpdateStatus, err := r.ConstructRoleStatus(tt.args.ctx, tt.args.rbg, tt.args.role)

			if (err != nil) != tt.wantErr {
				t.Errorf("testCase %s: error = %v, wantErr %v", tt.name, err, tt.wantErr)
				return
			}

			if !reflect.DeepEqual(gotStatus, tt.wantStatus) {
				t.Errorf("testCase %s: gotStatus = %v, want %v", tt.name, gotStatus, tt.wantStatus)
			}

			if gotUpdateStatus != tt.wantUpdateStatus {
				t.Errorf("testCase %s: gotUpdateStatus = %v, want %v", tt.name, gotUpdateStatus, tt.wantUpdateStatus)
			}
		})
	}
}

func TestStatefulSetReconciler_rollingUpdateParameters(t *testing.T) {
	roleWrapper := wrappers.BuildBasicRole("role").WithReplicas(4)

	type fields struct {
		client client.Client
		scheme *runtime.Scheme
	}
	type args struct {
		ctx        context.Context
		role       workloadsv1alpha1.RoleSpec
		sts        *appsv1.StatefulSet
		stsUpdated bool
	}
	tests := []struct {
		name      string
		fields    fields
		args      args
		partition int32
		replicas  int32
		wantErr   bool
	}{
		{
			name: "case 1: sts is not created",
			fields: fields{
				client: fake.NewFakeClient(),
				scheme: runtime.NewScheme(),
			},
			args: args{
				ctx:        context.TODO(),
				role:       roleWrapper.WithMaxUnavailable(2).WithMaxSurge(2).Obj(),
				sts:        &appsv1.StatefulSet{},
				stsUpdated: false,
			},
			partition: 0,
			replicas:  4,
			wantErr:   false,
		},
		{
			name: "case 2: sts has been updated, and rolling update has started",
			fields: fields{
				client: fake.NewFakeClient(),
				scheme: runtime.NewScheme(),
			},
			args: args{
				ctx:  context.TODO(),
				role: roleWrapper.WithMaxUnavailable(2).WithMaxSurge(2).Obj(),
				sts: &appsv1.StatefulSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "rolling-update-test",
						Annotations: map[string]string{
							workloadsv1alpha1.RoleSizeAnnotationKey: "4",
						},
						Labels: map[string]string{
							workloadsv1alpha1.SetNameLabelKey: "rolling-update-test",
						},
						UID: uuid.NewUUID(),
					},
					Spec: appsv1.StatefulSetSpec{
						Replicas: pointer.Int32(4),
						Template: wrappers.BuildBasicPodTemplateSpec().Obj(),
					},
				},
				stsUpdated: true,
			},
			partition: 4,
			replicas:  6,
			wantErr:   false,
		},
		{
			name: "case 3: rolling update is in progress",
			fields: fields{
				client: getFakeClient(),
				scheme: runtime.NewScheme(),
			},
			args: args{
				ctx:  context.TODO(),
				role: roleWrapper.WithMaxUnavailable(2).WithMaxSurge(2).Obj(),
				sts: &appsv1.StatefulSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "rolling-update-test",
						Annotations: map[string]string{
							workloadsv1alpha1.RoleSizeAnnotationKey: "4",
						},
						Labels: map[string]string{
							workloadsv1alpha1.SetNameLabelKey: "rolling-update-test",
						},
						UID: uuid.NewUUID(),
					},
					Spec: appsv1.StatefulSetSpec{
						Replicas: pointer.Int32(6),
						UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
							Type: appsv1.RollingUpdateStatefulSetStrategyType,
							RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
								Partition: ptr.To(int32(4)),
							},
						},
						Template: wrappers.BuildBasicPodTemplateSpec().Obj(),
					},
				},
				stsUpdated: false,
			},
			partition: 2,
			replicas:  6,
			wantErr:   false,
		},
		{
			name: "case 4: rolling update has been completed",
			fields: fields{
				client: getFakeClient(),
				scheme: runtime.NewScheme(),
			},
			args: args{
				ctx:  context.TODO(),
				role: roleWrapper.WithMaxUnavailable(2).WithMaxSurge(2).Obj(),
				sts: &appsv1.StatefulSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "rolling-update-test",
						Annotations: map[string]string{
							workloadsv1alpha1.RoleSizeAnnotationKey: "4",
						},
						Labels: map[string]string{
							workloadsv1alpha1.SetNameLabelKey: "rolling-update-test",
						},
					},
					Spec: appsv1.StatefulSetSpec{
						Replicas: pointer.Int32(4),
						UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
							Type: appsv1.RollingUpdateStatefulSetStrategyType,
							RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
								Partition: ptr.To(int32(0)),
							},
						},
						Template: wrappers.BuildBasicPodTemplateSpec().Obj(),
					},
				},
				stsUpdated: false,
			},
			partition: 0,
			replicas:  4,
			wantErr:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &StatefulSetReconciler{
				scheme: tt.fields.scheme,
				client: tt.fields.client,
			}
			partition, replicas, err := r.rollingUpdateParameters(tt.args.ctx, &tt.args.role, tt.args.sts, tt.args.stsUpdated)
			if (err != nil) != tt.wantErr {
				t.Errorf("rollingUpdateParameters() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if partition != tt.partition {
				t.Errorf("rollingUpdateParameters() partition = %v, want %v", partition, tt.partition)
			}
			if replicas != tt.replicas {
				t.Errorf("rollingUpdateParameters() replicas = %v, want %v", replicas, tt.replicas)
			}
		})
	}
}
