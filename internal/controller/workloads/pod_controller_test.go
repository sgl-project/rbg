package workloads

import (
	"reflect"
	"testing"

	"golang.org/x/net/context"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func TestPodReconciler_setRestartCondition(t *testing.T) {
	rbg := &workloadsv1alpha1.RoleBasedGroup{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RoleBasedGroup",
			APIVersion: "workloads.x-k8s.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restart-policy",
			Namespace: "default",
		},
		Status: workloadsv1alpha1.RoleBasedGroupStatus{
			RoleStatuses: []workloadsv1alpha1.RoleStatus{
				{
					Name:          "deployment",
					ReadyReplicas: 4,
					Replicas:      4,
				},
				{
					Name:          "sts",
					ReadyReplicas: 4,
					Replicas:      4,
				},
			},
		},
	}
	schema := runtime.NewScheme()
	_ = workloadsv1alpha1.AddToScheme(schema)
	rclient := fake.NewClientBuilder().WithScheme(schema).WithRuntimeObjects(rbg).Build()

	type fields struct {
		client client.Client
		scheme *runtime.Scheme
	}
	type args struct {
		ctx              context.Context
		rbg              *workloadsv1alpha1.RoleBasedGroup
		restartCompleted bool
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "test",
			fields: fields{
				client: rclient,
				scheme: schema,
			},
			args: args{
				ctx:              context.TODO(),
				rbg:              rbg,
				restartCompleted: true,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				// wait controller-runtime support SSA https://github.com/kubernetes-sigs/controller-runtime/pull/2981
				/*
					r := &PodReconciler{
						client: tt.fields.client,
						scheme: tt.fields.scheme,
					}
					if err := r.setRestartCondition(tt.args.ctx, tt.args.rbg, tt.args.restartCompleted); (err != nil) != tt.wantErr {
						t.Errorf("setRestartCondition() error = %v, wantErr %v", err, tt.wantErr)
					}
				*/
			},
		)
	}
}

func TestPodReconciler_podToRBG(t *testing.T) {
	schema := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(schema)
	_ = workloadsv1alpha1.AddToScheme(schema)

	pod := wrappers.BuildDeletingPod().WithLabels(
		map[string]string{
			workloadsv1alpha1.SetRoleLabelKey: "test-role",
			workloadsv1alpha1.SetNameLabelKey: "restart-policy",
		},
	).Obj()

	type args struct {
		ctx  context.Context
		obj  corev1.Pod
		role workloadsv1alpha1.RoleSpec
	}
	tests := []struct {
		name string
		args args
		want []reconcile.Request
	}{
		{
			name: "RecreateRBGOnPodRestart",
			args: args{
				ctx: context.TODO(),
				obj: pod,
				role: wrappers.BuildBasicRole("test-role").
					WithRestartPolicy(workloadsv1alpha1.RecreateRBGOnPodRestart).
					Obj(),
			},
			want: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      "restart-policy",
						Namespace: "default",
					},
				},
			},
		},
		{
			name: "NoneRestartPolicy",
			args: args{
				ctx: context.TODO(),
				obj: pod,
				role: wrappers.BuildBasicRole("test-role").
					WithRestartPolicy(workloadsv1alpha1.NoneRestartPolicy).
					Obj(),
			},
			want: []reconcile.Request{},
		},
		{
			name: "RecreateRoleInstanceOnPodRestart",
			args: args{
				ctx: context.TODO(),
				obj: pod,
				role: wrappers.BuildBasicRole("test-role").
					WithRestartPolicy(workloadsv1alpha1.RecreateRoleInstanceOnPodRestart).
					Obj(),
			},
			want: []reconcile.Request{},
		},
		{
			name: "pod-running",
			args: args{
				ctx: context.TODO(),
				obj: wrappers.BuildBasicPod().WithLabels(
					map[string]string{
						workloadsv1alpha1.SetRoleLabelKey: "test-role",
						workloadsv1alpha1.SetNameLabelKey: "restart-policy",
					},
				).Obj(),
				role: wrappers.BuildBasicRole("test-role").
					WithRestartPolicy(workloadsv1alpha1.RecreateRBGOnPodRestart).
					Obj(),
			},
			want: []reconcile.Request{},
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				rbg := wrappers.BuildBasicRoleBasedGroup("restart-policy", "default").
					WithRoles([]workloadsv1alpha1.RoleSpec{tt.args.role}).Obj()
				fclient := fake.NewClientBuilder().WithScheme(schema).WithObjects(&tt.args.obj, rbg).Build()

				r := &PodReconciler{
					client: fclient,
					scheme: schema,
				}
				if got := r.podToRBG(tt.args.ctx, &tt.args.obj); !reflect.DeepEqual(got, tt.want) {
					t.Errorf("podToRBG() = %v, want %v", got, tt.want)
				}
			},
		)
	}
}
