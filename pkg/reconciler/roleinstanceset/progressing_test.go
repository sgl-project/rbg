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

package roleinstanceset

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	workloadconstants "sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func newTestSet(replicas int32) *workloadsv1alpha2.RoleInstanceSet {
	return &workloadsv1alpha2.RoleInstanceSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 4},
		Spec:       workloadsv1alpha2.RoleInstanceSetSpec{Replicas: &replicas},
	}
}

func instanceWithProgressing(reason string, phase workloadsv1alpha2.RoleInstancePhase) *workloadsv1alpha2.RoleInstance {
	return &workloadsv1alpha2.RoleInstance{
		Status: workloadsv1alpha2.RoleInstanceStatus{
			Phase: phase,
			Conditions: []workloadsv1alpha2.RoleInstanceCondition{{
				Type:   workloadsv1alpha2.RoleInstanceProgressing,
				Status: corev1.ConditionTrue,
				Reason: reason,
			}},
		},
	}
}

func TestComputeSetProgressingCondition(t *testing.T) {
	cases := []struct {
		name       string
		set        *workloadsv1alpha2.RoleInstanceSet
		newStatus  *workloadsv1alpha2.RoleInstanceSetStatus
		instances  []*workloadsv1alpha2.RoleInstance
		wantReason string
		wantStatus corev1.ConditionStatus
	}{
		{
			name:       "initial establishment with nothing created",
			set:        newTestSet(3),
			newStatus:  &workloadsv1alpha2.RoleInstanceSetStatus{},
			wantReason: workloadconstants.ProgressingReasonCreating,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "scale up creating third instance reports scaling",
			set:  newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 2, UpdatedReplicas: 2, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 2,
			},
			wantReason: workloadconstants.ProgressingReasonScalingUp,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "scale down reports scaling down",
			set:  newTestSet(2),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, UpdatedReplicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			wantReason: workloadconstants.ProgressingReasonScalingDown,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "update scope incomplete reports role instance updated",
			set:  newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, CurrentRevision: "old", UpdateRevision: "new", UpdatedReplicas: 1, ExpectedUpdatedReplicas: 3,
			},
			wantReason: workloadconstants.ProgressingReasonRoleInstanceUpdated,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "ri recovery aggregates to recovering",
			set:  newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances: []*workloadsv1alpha2.RoleInstance{
				instanceWithProgressing("", workloadsv1alpha2.RoleInstancePhaseRecovering),
			},
			wantReason: workloadconstants.ProgressingReasonRecovering,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "ri blocker maps to family and outranks activity",
			set:  newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 2, CurrentRevision: "old", UpdateRevision: "new", UpdatedReplicas: 1, ExpectedUpdatedReplicas: 3,
			},
			instances: []*workloadsv1alpha2.RoleInstance{
				instanceWithProgressing(workloadsv1alpha2.ReasonPodCreateQuotaExceeded, workloadsv1alpha2.RoleInstancePhaseCreating),
			},
			wantReason: workloadconstants.ProgressingReasonWaitingForQuota,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "no workflow and instances not ready",
			set:  newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, ReadyReplicas: 2, UpdatedReplicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			wantReason: workloadconstants.ProgressingReasonInstancesNotReady,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "converged",
			set:  newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, ReadyReplicas: 3, UpdatedReplicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			wantReason: workloadconstants.ProgressingReasonConverged,
			wantStatus: corev1.ConditionFalse,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeSetProgressingCondition(tc.set, tc.newStatus, tc.instances)
			if got.Reason != tc.wantReason || got.Status != tc.wantStatus {
				t.Fatalf("got status=%s reason=%s, want status=%s reason=%s", got.Status, got.Reason, tc.wantStatus, tc.wantReason)
			}
			if got.ObservedGeneration != 4 {
				t.Fatalf("observedGeneration = %d", got.ObservedGeneration)
			}
		})
	}
}

func TestCurrentSetBlockerPriority(t *testing.T) {
	newStatus := &workloadsv1alpha2.RoleInstanceSetStatus{
		Conditions: []workloadsv1alpha2.RoleInstanceSetCondition{{
			Type:    workloadsv1alpha2.RoleInstanceSetConditionFailedScale,
			Status:  corev1.ConditionTrue,
			Reason:  workloadconstants.ProgressingReasonPermissionDenied,
			Message: "rbac denied",
		}},
	}
	instances := []*workloadsv1alpha2.RoleInstance{
		instanceWithProgressing(workloadsv1alpha2.ReasonPodCreateQuotaExceeded, workloadsv1alpha2.RoleInstancePhaseCreating),
	}
	reason, _ := currentSetBlocker(newStatus, instances)
	if reason != workloadconstants.ProgressingReasonPermissionDenied {
		t.Fatalf("permission must outrank quota, got %s", reason)
	}
}

func TestClassifySetOperationError(t *testing.T) {
	forbiddenQuota := apierrors.NewForbidden(
		corev1.Resource("roleinstances"), "ri-0", apierrors.NewBadRequest("exceeded quota"))
	if got := ClassifySetOperationError(forbiddenQuota); got != workloadconstants.ProgressingReasonWaitingForQuota {
		t.Fatalf("got %s", got)
	}
	forbiddenRBAC := apierrors.NewForbidden(
		corev1.Resource("roleinstances"), "ri-0", apierrors.NewBadRequest("denied"))
	if got := ClassifySetOperationError(forbiddenRBAC); got != workloadconstants.ProgressingReasonPermissionDenied {
		t.Fatalf("got %s", got)
	}
	if got := ClassifySetOperationError(nil); got != "" {
		t.Fatalf("got %s", got)
	}
	if got := ClassifySetOperationError(apierrors.NewServerTimeout(corev1.Resource("roleinstances"), "POST", 1)); got != workloadconstants.ProgressingReasonUnclassifiedBlocker {
		t.Fatalf("got %s", got)
	}
}
