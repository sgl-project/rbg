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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/utils"
	"sigs.k8s.io/rbgs/test/wrappers"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func TestPodReconciler_setCondition(t *testing.T) {
	existingTransitionTime := metav1.Unix(123, 0)

	tests := []struct {
		name           string
		initialStatus  workloadsv1alpha2.RoleBasedGroupStatus
		newCondition   metav1.Condition
		expectedStatus workloadsv1alpha2.RoleBasedGroupStatus
	}{
		{
			name: "Add new condition",
			initialStatus: workloadsv1alpha2.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{},
			},
			newCondition: metav1.Condition{
				Type:   string(workloadsv1alpha2.RoleBasedGroupRestartInProgress),
				Status: metav1.ConditionTrue,
				Reason: "TestReason",
			},
			expectedStatus: workloadsv1alpha2.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(workloadsv1alpha2.RoleBasedGroupRestartInProgress),
						Status: metav1.ConditionTrue,
						Reason: "TestReason",
					},
				},
			},
		},
		{
			name: "Update existing condition",
			initialStatus: workloadsv1alpha2.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(workloadsv1alpha2.RoleBasedGroupRestartInProgress),
						Status: metav1.ConditionFalse,
						Reason: "OldReason",
					},
				},
			},
			newCondition: metav1.Condition{
				Type:   string(workloadsv1alpha2.RoleBasedGroupRestartInProgress),
				Status: metav1.ConditionTrue,
				Reason: "NewReason",
			},
			expectedStatus: workloadsv1alpha2.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(workloadsv1alpha2.RoleBasedGroupRestartInProgress),
						Status: metav1.ConditionTrue,
						Reason: "NewReason",
					},
				},
			},
		},
		{
			name: "Update condition fields when status unchanged",
			initialStatus: workloadsv1alpha2.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:               string(workloadsv1alpha2.RoleBasedGroupRestartInProgress),
						Status:             metav1.ConditionTrue,
						Reason:             "ExistingReason",
						Message:            "ExistingMessage",
						ObservedGeneration: 1,
						LastTransitionTime: existingTransitionTime,
					},
				},
			},
			newCondition: metav1.Condition{
				Type:               string(workloadsv1alpha2.RoleBasedGroupRestartInProgress),
				Status:             metav1.ConditionTrue,
				Reason:             "NewReason",
				Message:            "NewMessage",
				ObservedGeneration: 2,
			},
			expectedStatus: workloadsv1alpha2.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:               string(workloadsv1alpha2.RoleBasedGroupRestartInProgress),
						Status:             metav1.ConditionTrue,
						Reason:             "NewReason",
						Message:            "NewMessage",
						ObservedGeneration: 2,
						LastTransitionTime: existingTransitionTime,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				rbg := &workloadsv1alpha2.RoleBasedGroup{
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
					assert.Equal(t, expectedCondition.Message, actualCondition.Message)
					assert.Equal(t, expectedCondition.ObservedGeneration, actualCondition.ObservedGeneration)
					if !expectedCondition.LastTransitionTime.IsZero() {
						assert.Equal(t, expectedCondition.LastTransitionTime, actualCondition.LastTransitionTime)
					}
				}
			},
		)
	}
}

func TestToRBGApplyConfigurationForStatus_PreservesObservedGeneration(t *testing.T) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
		},
		Status: workloadsv1alpha2.RoleBasedGroupStatus{
			ObservedGeneration: 7,
			Conditions: []metav1.Condition{
				{
					Type:               string(workloadsv1alpha2.RoleBasedGroupReady),
					Status:             metav1.ConditionTrue,
					ObservedGeneration: 7,
				},
			},
			RoleStatuses: []workloadsv1alpha2.RoleStatus{
				{
					Name:            "worker",
					Replicas:        1,
					ReadyReplicas:   1,
					UpdatedReplicas: 1,
				},
			},
		},
	}

	cfg := ToRBGApplyConfigurationForStatus(rbg)
	if assert.NotNil(t, cfg) && assert.NotNil(t, cfg.Status) && assert.NotNil(t, cfg.Status.ObservedGeneration) {
		assert.Equal(t, int64(7), *cfg.Status.ObservedGeneration)
	}
	if assert.Len(t, cfg.Status.Conditions, 1) && assert.NotNil(t, cfg.Status.Conditions[0].ObservedGeneration) {
		assert.Equal(t, int64(7), *cfg.Status.Conditions[0].ObservedGeneration)
	}
}

func TestPodReconciler_restartConditionTrue(t *testing.T) {
	tests := []struct {
		name     string
		status   workloadsv1alpha2.RoleBasedGroupStatus
		expected bool
	}{
		{
			name: "Condition true",
			status: workloadsv1alpha2.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(workloadsv1alpha2.RoleBasedGroupRestartInProgress),
						Status: metav1.ConditionTrue,
					},
				},
			},
			expected: true,
		},
		{
			name: "Condition false",
			status: workloadsv1alpha2.RoleBasedGroupStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(workloadsv1alpha2.RoleBasedGroupRestartInProgress),
						Status: metav1.ConditionFalse,
					},
				},
			},
			expected: false,
		},
		{
			name: "Condition not found",
			status: workloadsv1alpha2.RoleBasedGroupStatus{
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
			status: workloadsv1alpha2.RoleBasedGroupStatus{
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

// Tests for Dimension 1: Pod Controller only handles container restart and pod deletion
func TestPodReconciler_podToRBG_Dimension1(t *testing.T) {
	schema := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(schema)
	_ = workloadsv1alpha2.AddToScheme(schema)

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("test-role").
				WithRestartPolicy(workloadsv1alpha2.RecreateRBGOnPodRestart).
				Obj(),
		}).Obj()

	tests := []struct {
		name string
		pod  *corev1.Pod
		want []reconcile.Request
		desc string
	}{
		{
			name: "Container restart triggers reconciliation (Dimension 1)",
			desc: "Pod Controller handles container restart for RecreateRBGOnPodRestart",
			pod: wrappers.BuildBasicPod().WithLabels(map[string]string{
				constants.GroupNameLabelKey: "test-rbg",
				constants.RoleNameLabelKey:  "test-role",
			}).Obj(),
			want: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      "test-rbg",
						Namespace: "default",
					},
				},
			},
		},
		{
			name: "Pod deletion triggers reconciliation (Dimension 1)",
			desc: "Pod Controller handles pod deletion for RecreateRBGOnPodRestart",
			pod: wrappers.BuildDeletingPod().WithLabels(map[string]string{
				constants.GroupNameLabelKey: "test-rbg",
				constants.RoleNameLabelKey:  "test-role",
			}).Obj(),
			want: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Name:      "test-rbg",
						Namespace: "default",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set namespace for pod to match RBG namespace
			tt.pod.Namespace = "default"

			// For container restart test, set restart count
			if tt.name == "Container restart triggers reconciliation (Dimension 1)" {
				tt.pod.Status.Phase = corev1.PodRunning
				tt.pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{Name: "nginx", RestartCount: 1},
				}
			}

			objects := []client.Object{rbg, tt.pod}
			fclient := fake.NewClientBuilder().WithScheme(schema).WithObjects(objects...).Build()
			r := &PodReconciler{
				client:    fclient,
				apiReader: fclient,
				scheme:    schema,
			}
			got := r.podToRBG(context.TODO(), tt.pod)
			assert.Equal(t, tt.want, got, tt.desc)
		})
	}
}

// Test that Pod Failed (Dimension 2) does NOT trigger Pod Controller
func TestPodReconciler_podToRBG_Dimension2_NotTriggered(t *testing.T) {
	schema := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(schema)
	_ = workloadsv1alpha2.AddToScheme(schema)

	rbg := wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
		WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("test-role").
				WithRestartPolicy(workloadsv1alpha2.RecreateRBGOnPodRestart).
				Obj(),
		}).Obj()

	tests := []struct {
		name string
		pod  *corev1.Pod
		desc string
	}{
		{
			name: "Pod Failed does NOT trigger Pod Controller",
			desc: "Pod Failed (Dimension 2) is handled by RoleInstance Controller, not Pod Controller",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "failed-pod",
					Namespace: "default",
					Labels: map[string]string{
						constants.GroupNameLabelKey: "test-rbg",
						constants.RoleNameLabelKey:  "test-role",
					},
				},
				Status: corev1.PodStatus{
					Phase:  corev1.PodFailed,
					Reason: "Evicted",
				},
			},
		},
		{
			name: "Pod with UnexpectedAdmissionError does NOT trigger Pod Controller",
			desc: "Pod Failed with admission error is Dimension 2, not handled by Pod Controller",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "admission-error-pod",
					Namespace: "default",
					Labels: map[string]string{
						constants.GroupNameLabelKey: "test-rbg",
						constants.RoleNameLabelKey:  "test-role",
					},
				},
				Status: corev1.PodStatus{
					Phase:  corev1.PodFailed,
					Reason: "UnexpectedAdmissionError",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := []client.Object{rbg, tt.pod}
			fclient := fake.NewClientBuilder().WithScheme(schema).WithObjects(objects...).Build()
			r := &PodReconciler{
				client:    fclient,
				apiReader: fclient,
				scheme:    schema,
			}
			got := r.podToRBG(context.TODO(), tt.pod)
			// Pod Failed should NOT trigger Pod Controller
			assert.Empty(t, got, tt.desc)
		})
	}
}

// Test predicate only captures container restart count changes (Dimension 1)
func TestPodReconciler_Predicate_Dimension1(t *testing.T) {
	podPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPod, ok1 := e.ObjectOld.(*corev1.Pod)
			newPod, ok2 := e.ObjectNew.(*corev1.Pod)
			if ok1 && ok2 {
				_, oldExist := oldPod.Labels[constants.GroupNameLabelKey]
				_, newExist := newPod.Labels[constants.GroupNameLabelKey]

				if !oldExist || !newExist {
					return false
				}

				// Dimension 1: Only capture container restart count changes
				return utils.ContainerRestartCountChanged(oldPod, newPod)
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			if pod, ok := e.Object.(*corev1.Pod); ok {
				_, exist := pod.Labels[constants.GroupNameLabelKey]
				return exist
			}
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}

	tests := []struct {
		name     string
		oldPod   *corev1.Pod
		newPod   *corev1.Pod
		expected bool
		desc     string
	}{
		{
			name: "Container restart count changed - triggers predicate",
			desc: "Predicate captures container restart (Dimension 1)",
			oldPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{constants.GroupNameLabelKey: "test-rbg"},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "nginx", RestartCount: 0},
					},
				},
			},
			newPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{constants.GroupNameLabelKey: "test-rbg"},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "nginx", RestartCount: 1},
					},
				},
			},
			expected: true,
		},
		{
			name: "Pod became Failed - does NOT trigger predicate",
			desc: "Predicate does NOT capture Pod Failed (Dimension 2 handled elsewhere)",
			oldPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{constants.GroupNameLabelKey: "test-rbg"},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			},
			newPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{constants.GroupNameLabelKey: "test-rbg"},
				},
				Status: corev1.PodStatus{
					Phase:  corev1.PodFailed,
					Reason: "Evicted",
				},
			},
			expected: false,
		},
		{
			name: "Pod status update without restart count change - does NOT trigger",
			desc: "Predicate ignores status updates that don't change restart count",
			oldPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{constants.GroupNameLabelKey: "test-rbg"},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "nginx", RestartCount: 0},
					},
				},
			},
			newPod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{constants.GroupNameLabelKey: "test-rbg"},
				},
				Status: corev1.PodStatus{
					Phase:   corev1.PodRunning,
					Message: "Updated status message",
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "nginx", RestartCount: 0},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := event.UpdateEvent{
				ObjectOld: tt.oldPod,
				ObjectNew: tt.newPod,
			}
			result := podPredicate.UpdateFunc(e)
			assert.Equal(t, tt.expected, result, tt.desc)
		})
	}
}

func TestPodReconciler_podToRBG_EdgeCases(t *testing.T) {
	schema := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(schema)
	_ = workloadsv1alpha2.AddToScheme(schema)

	type args struct {
		ctx context.Context
		pod *corev1.Pod
	}
	tests := []struct {
		name     string
		args     args
		setupRBG *workloadsv1alpha2.RoleBasedGroup
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
						constants.GroupNameLabelKey: "non-existent-rbg",
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
						constants.GroupNameLabelKey: "test-rbg",
					},
				).Obj(),
			},
			setupRBG: wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj(),
			want:     []reconcile.Request{},
		},
		{
			name: "Pod with RBG and role labels but role not found in RBG",
			args: args{
				ctx: context.TODO(),
				pod: wrappers.BuildBasicPod().WithLabels(
					map[string]string{
						constants.GroupNameLabelKey: "test-rbg",
						constants.RoleNameLabelKey:  "non-existent-role",
					},
				).Obj(),
			},
			setupRBG: wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").Obj(),
			want:     []reconcile.Request{},
		},
		{
			name: "RBG already in restart status",
			args: args{
				ctx: context.TODO(),
				pod: wrappers.BuildDeletingPod().WithLabels(
					map[string]string{
						constants.GroupNameLabelKey: "test-rbg",
						constants.RoleNameLabelKey:  "test-role",
					},
				).Obj(),
			},
			setupRBG: wrappersv2.BuildBasicRoleBasedGroup("test-rbg", "default").
				WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("test-role").
							WithRestartPolicy(workloadsv1alpha2.RecreateRBGOnPodRestart).
							Obj(),
					},
				).
				WithStatus(
					workloadsv1alpha2.RoleBasedGroupStatus{
						Conditions: []metav1.Condition{
							{
								Type:   string(workloadsv1alpha2.RoleBasedGroupRestartInProgress),
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
					client:    fclient,
					apiReader: fclient,
					scheme:    schema,
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
	_ = workloadsv1alpha2.AddToScheme(schema)

	obj := wrappersv2.BuildBasicRoleBasedGroup(
		"test-rbg", "default",
	).WithStatus(
		workloadsv1alpha2.RoleBasedGroupStatus{
			Conditions: []metav1.Condition{
				{
					Type:   string(workloadsv1alpha2.RoleBasedGroupReady),
					Status: metav1.ConditionTrue,
				},
			},
		},
	).Obj()

	fclient := fake.NewClientBuilder().WithScheme(schema).WithObjects(obj).WithStatusSubresource(obj).Build()
	r := &PodReconciler{
		client:    fclient,
		apiReader: fclient,
		scheme:    schema,
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
