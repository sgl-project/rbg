package workloads

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func TestPodReconciler_setCondition(t *testing.T) {
	tests := []struct {
		name           string
		initialStatus  workloadsv1alpha1.RoleBasedGroupStatus
		newCondition   metav1.Condition
		expectedStatus workloadsv1alpha1.RoleBasedGroupStatus
	}{
		{
			name: "Add new condition",
			initialStatus: workloadsv1alpha1.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{},
			},
			newCondition: metav1.Condition{
				Type:   string(workloadsv1alpha1.RoleBasedGroupRestartInProgress),
				Status: metav1.ConditionTrue,
				Reason: "TestReason",
			},
			expectedStatus: workloadsv1alpha1.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(workloadsv1alpha1.RoleBasedGroupRestartInProgress),
						Status: metav1.ConditionTrue,
						Reason: "TestReason",
					},
				},
			},
		},
		{
			name: "Update existing condition",
			initialStatus: workloadsv1alpha1.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(workloadsv1alpha1.RoleBasedGroupRestartInProgress),
						Status: metav1.ConditionFalse,
						Reason: "OldReason",
					},
				},
			},
			newCondition: metav1.Condition{
				Type:   string(workloadsv1alpha1.RoleBasedGroupRestartInProgress),
				Status: metav1.ConditionTrue,
				Reason: "NewReason",
			},
			expectedStatus: workloadsv1alpha1.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(workloadsv1alpha1.RoleBasedGroupRestartInProgress),
						Status: metav1.ConditionTrue,
						Reason: "NewReason",
					},
				},
			},
		},
		{
			name: "No update when status unchanged",
			initialStatus: workloadsv1alpha1.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(workloadsv1alpha1.RoleBasedGroupRestartInProgress),
						Status: metav1.ConditionTrue,
						Reason: "ExistingReason",
					},
				},
			},
			newCondition: metav1.Condition{
				Type:   string(workloadsv1alpha1.RoleBasedGroupRestartInProgress),
				Status: metav1.ConditionTrue,
				Reason: "NewReason",
			},
			expectedStatus: workloadsv1alpha1.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(workloadsv1alpha1.RoleBasedGroupRestartInProgress),
						Status: metav1.ConditionTrue,
						Reason: "ExistingReason", // Should remain unchanged
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				rbg := &workloadsv1alpha1.RoleBasedGroup{
					Status: tt.initialStatus,
				}

				setCondition(rbg, tt.newCondition)

				// Check that the number of conditions is correct
				assert.Equal(t, len(tt.expectedStatus.Conditions), len(rbg.Status.Conditions))

				// Check each condition
				for i, expectedCondition := range tt.expectedStatus.Conditions {
					actualCondition := rbg.Status.Conditions[i]
					assert.Equal(t, expectedCondition.Type, actualCondition.Type)
					assert.Equal(t, expectedCondition.Status, actualCondition.Status)
					assert.Equal(t, expectedCondition.Reason, actualCondition.Reason)
				}
			},
		)
	}
}

func TestPodReconciler_restartConditionTrue(t *testing.T) {
	tests := []struct {
		name     string
		status   workloadsv1alpha1.RoleBasedGroupStatus
		expected bool
	}{
		{
			name: "Condition true",
			status: workloadsv1alpha1.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(workloadsv1alpha1.RoleBasedGroupRestartInProgress),
						Status: metav1.ConditionTrue,
					},
				},
			},
			expected: true,
		},
		{
			name: "Condition false",
			status: workloadsv1alpha1.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(workloadsv1alpha1.RoleBasedGroupRestartInProgress),
						Status: metav1.ConditionFalse,
					},
				},
			},
			expected: false,
		},
		{
			name: "Condition not found",
			status: workloadsv1alpha1.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:   "OtherCondition",
						Status: metav1.ConditionTrue,
					},
				},
			},
			expected: false,
		},
		{
			name: "Empty conditions",
			status: workloadsv1alpha1.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := restartConditionTrue(tt.status)
				assert.Equal(t, tt.expected, result)
			},
		)
	}
}

func TestPodReconciler_podToRBG_EdgeCases(t *testing.T) {
	schema := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(schema)
	_ = workloadsv1alpha1.AddToScheme(schema)

	type args struct {
		ctx context.Context
		pod *corev1.Pod
	}
	tests := []struct {
		name     string
		args     args
		setupRBG *workloadsv1alpha1.RoleBasedGroup
		want     []reconcile.Request
	}{
		{
			name: "Pod without RBG label",
			args: args{
				ctx: context.TODO(),
				pod: wrappers.BuildBasicPod().Obj(),
			},
			want: []reconcile.Request{},
		},
		{
			name: "Pod with RBG label but RBG not found",
			args: args{
				ctx: context.TODO(),
				pod: wrappers.BuildBasicPod().WithLabels(
					map[string]string{
						workloadsv1alpha1.SetNameLabelKey: "non-existent-rbg",
					},
				).Obj(),
			},
			want: []reconcile.Request{},
		},
		{
			name: "Pod with RBG label but no role label",
			args: args{
				ctx: context.TODO(),
				pod: wrappers.BuildBasicPod().WithLabels(
					map[string]string{
						workloadsv1alpha1.SetNameLabelKey: "test-rbg",
					},
				).Obj(),
			},
			setupRBG: wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj(),
			want:     []reconcile.Request{},
		},
		{
			name: "Pod with RBG and role labels but role not found in RBG",
			args: args{
				ctx: context.TODO(),
				pod: wrappers.BuildBasicPod().WithLabels(
					map[string]string{
						workloadsv1alpha1.SetNameLabelKey: "test-rbg",
						workloadsv1alpha1.SetRoleLabelKey: "non-existent-role",
					},
				).Obj(),
			},
			setupRBG: wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").Obj(),
			want:     []reconcile.Request{},
		},
		{
			name: "RBG already in restart status",
			args: args{
				ctx: context.TODO(),
				pod: wrappers.BuildDeletingPod().WithLabels(
					map[string]string{
						workloadsv1alpha1.SetNameLabelKey: "test-rbg",
						workloadsv1alpha1.SetRoleLabelKey: "test-role",
					},
				).Obj(),
			},
			setupRBG: wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").
				WithRoles(
					[]workloadsv1alpha1.RoleSpec{
						wrappers.BuildBasicRole("test-role").
							WithRestartPolicy(workloadsv1alpha1.RecreateRBGOnPodRestart).
							Obj(),
					},
				).
				WithStatus(
					workloadsv1alpha1.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(workloadsv1alpha1.RoleBasedGroupRestartInProgress),
								Status: metav1.ConditionTrue,
							},
						},
					},
				).Obj(),
			want: []reconcile.Request{},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				var objects []client.Object
				if tt.setupRBG != nil {
					objects = append(objects, tt.setupRBG)
				}
				objects = append(objects, tt.args.pod)

				fclient := fake.NewClientBuilder().WithScheme(schema).WithObjects(objects...).Build()
				r := &PodReconciler{
					client: fclient,
					scheme: schema,
				}
				got := r.podToRBG(tt.args.ctx, tt.args.pod)
				assert.Equal(t, tt.want, got)
			},
		)
	}
}

func TestPodReconciler_Reconcile(t *testing.T) {
	schema := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(schema)
	_ = workloadsv1alpha1.AddToScheme(schema)

	obj := wrappers.BuildBasicRoleBasedGroup(
		"test-rbg", "default",
	).WithStatus(
		workloadsv1alpha1.RoleBasedGroupStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(workloadsv1alpha1.RoleBasedGroupReady),
					Status: metav1.ConditionTrue,
				},
			},
		},
	).Obj()

	fclient := fake.NewClientBuilder().WithScheme(schema).WithRuntimeObjects(obj).Build()
	r := &PodReconciler{
		client: fclient,
		scheme: schema,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-rbg",
			Namespace: "default",
		},
	}

	logger := zap.New().WithValues("env", "unit-test")
	ctx := ctrl.LoggerInto(context.TODO(), logger)

	// Test successful reconciliation
	result, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Logf("out err: %s", err.Error())
	}
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)

	// Test reconciliation with non-existent RBG (should not return error)
	nonExistentReq := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "non-existent",
			Namespace: "default",
		},
	}
	result, err = r.Reconcile(context.TODO(), nonExistentReq)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}
