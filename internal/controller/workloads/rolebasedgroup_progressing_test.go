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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	workloadconstants "sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func newProgressingRBG() *workloadsv1alpha2.RoleBasedGroup {
	replicas := int32(2)
	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "inference", Generation: 6},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{Name: "prefill", Replicas: &replicas},
				{Name: "decode", Replicas: &replicas},
			},
		},
	}
}

func readyRoleStatuses() []workloadsv1alpha2.RoleStatus {
	return []workloadsv1alpha2.RoleStatus{
		{Name: "prefill", Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2},
		{Name: "decode", Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2},
	}
}

func risCondition(reason string, status corev1.ConditionStatus) workloadsv1alpha2.RoleInstanceSetCondition {
	return workloadsv1alpha2.RoleInstanceSetCondition{
		Type: workloadsv1alpha2.RoleInstanceSetProgressing, Status: status, Reason: reason,
	}
}

func TestComputeRBGProgressingCondition(t *testing.T) {
	cases := []struct {
		name           string
		rbg            *workloadsv1alpha2.RoleBasedGroup
		roleStatuses   []workloadsv1alpha2.RoleStatus
		risProgressing map[string]workloadsv1alpha2.RoleInstanceSetCondition
		gangWaiting    bool
		depsNotReady   []string
		wantReason     string
		wantStatus     metav1.ConditionStatus
	}{
		{
			name:         "converged",
			rbg:          newProgressingRBG(),
			roleStatuses: readyRoleStatuses(),
			risProgressing: map[string]workloadsv1alpha2.RoleInstanceSetCondition{
				"prefill": risCondition(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse),
				"decode":  risCondition(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse),
			},
			wantReason: workloadconstants.ProgressingReasonConverged,
			wantStatus: metav1.ConditionFalse,
		},
		{
			name: "gang waiting outranks activity",
			rbg:  newProgressingRBG(),
			risProgressing: map[string]workloadsv1alpha2.RoleInstanceSetCondition{
				"prefill": risCondition(workloadconstants.ProgressingReasonRoleInstanceUpdated, corev1.ConditionTrue),
			},
			gangWaiting: true,
			wantReason:  workloadconstants.ProgressingReasonWaitingForGang,
			wantStatus:  metav1.ConditionTrue,
		},
		{
			name:         "dependencies not ready",
			rbg:          newProgressingRBG(),
			depsNotReady: []string{"decode"},
			wantReason:   workloadconstants.ProgressingReasonWaitingForDependencies,
			wantStatus:   metav1.ConditionTrue,
		},
		{
			name: "ris family blocker aggregates",
			rbg:  newProgressingRBG(),
			risProgressing: map[string]workloadsv1alpha2.RoleInstanceSetCondition{
				"decode": risCondition(workloadconstants.ProgressingReasonWaitingForQuota, corev1.ConditionTrue),
			},
			wantReason: workloadconstants.ProgressingReasonWaitingForQuota,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "creating from ris outranks role instance set updated",
			rbg:  newProgressingRBG(),
			risProgressing: map[string]workloadsv1alpha2.RoleInstanceSetCondition{
				"prefill": risCondition(workloadconstants.ProgressingReasonRoleInstanceSetUpdated, corev1.ConditionTrue),
				"decode":  risCondition(workloadconstants.ProgressingReasonCreating, corev1.ConditionTrue),
			},
			wantReason: workloadconstants.ProgressingReasonCreating,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "update and scale aggregate into role instance set updated",
			rbg:  newProgressingRBG(),
			risProgressing: map[string]workloadsv1alpha2.RoleInstanceSetCondition{
				"prefill": risCondition(workloadconstants.ProgressingReasonScalingUp, corev1.ConditionTrue),
				"decode":  risCondition(workloadconstants.ProgressingReasonRoleInstanceUpdated, corev1.ConditionTrue),
			},
			wantReason: workloadconstants.ProgressingReasonRoleInstanceSetUpdated,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "recovering outranks role instance set updated",
			rbg:  newProgressingRBG(),
			risProgressing: map[string]workloadsv1alpha2.RoleInstanceSetCondition{
				"prefill": risCondition(workloadconstants.ProgressingReasonRecovering, corev1.ConditionTrue),
				"decode":  risCondition(workloadconstants.ProgressingReasonRoleInstanceSetUpdated, corev1.ConditionTrue),
			},
			wantReason: workloadconstants.ProgressingReasonRecovering,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "roles not ready when no workflow remains",
			rbg:  newProgressingRBG(),
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "prefill", Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2},
				{Name: "decode", Replicas: 2, ReadyReplicas: 1, UpdatedReplicas: 2},
			},
			risProgressing: map[string]workloadsv1alpha2.RoleInstanceSetCondition{
				"prefill": risCondition(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse),
				"decode":  risCondition(workloadconstants.ProgressingReasonInstancesNotReady, corev1.ConditionTrue),
			},
			wantReason: workloadconstants.ProgressingReasonRolesNotReady,
			wantStatus: metav1.ConditionTrue,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeRBGProgressingCondition(tc.rbg, tc.roleStatuses, tc.risProgressing, tc.gangWaiting, tc.depsNotReady)
			if got.Reason != tc.wantReason || got.Status != tc.wantStatus {
				t.Fatalf("got status=%s reason=%s message=%s, want status=%s reason=%s", got.Status, got.Reason, got.Message, tc.wantStatus, tc.wantReason)
			}
			if got.ObservedGeneration != 6 {
				t.Fatalf("observedGeneration = %d", got.ObservedGeneration)
			}
		})
	}
}

func TestUnreadyDependencies(t *testing.T) {
	rbg := newProgressingRBG()
	rbg.Spec.Roles[1].Dependencies = []string{"prefill"}
	// decode waits for prefill; prefill is not ready.
	notReady := unreadyDependencies(rbg, []workloadsv1alpha2.RoleStatus{
		{Name: "prefill", Replicas: 2, ReadyReplicas: 1},
		{Name: "decode", Replicas: 2, ReadyReplicas: 2},
	})
	if len(notReady) != 1 || notReady[0] != "decode" {
		t.Fatalf("got %v", notReady)
	}
	ready := unreadyDependencies(rbg, []workloadsv1alpha2.RoleStatus{
		{Name: "prefill", Replicas: 2, ReadyReplicas: 2},
		{Name: "decode", Replicas: 2, ReadyReplicas: 2},
	})
	if len(ready) != 0 {
		t.Fatalf("got %v", ready)
	}
}

func TestAllRolesUseRoleInstanceSet(t *testing.T) {
	if !allRolesUseRoleInstanceSet(newProgressingRBG()) {
		t.Fatal("default roles must use RoleInstanceSet")
	}
	rbg := newProgressingRBG()
	rbg.Spec.Roles[0].Annotations = map[string]string{
		"rbg.workloads.x-k8s.io/role-workload-type": "apps/v1/StatefulSet",
	}
	if allRolesUseRoleInstanceSet(rbg) {
		t.Fatal("mixed backend must not enable the new condition model")
	}
}
