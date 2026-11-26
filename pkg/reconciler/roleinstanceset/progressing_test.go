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
	stderrors "errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

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

// bareInstances returns observed RoleInstance objects without status; the
// structural instance count comes from observed instances, not the counters.
func bareInstances(replicas int32) []*workloadsv1alpha2.RoleInstance {
	instances := make([]*workloadsv1alpha2.RoleInstance, replicas)
	for i := range instances {
		instances[i] = &workloadsv1alpha2.RoleInstance{}
	}
	return instances
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
			name: "initial establishment reports creating even though revisions exist",
			// Both modes publish CurrentRevision / UpdateRevision on the first
			// status write, before any RoleInstance exists; a non-empty revision
			// alone must not turn initial creation into scaling.
			set: newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				CurrentRevision: "rev", UpdateRevision: "rev",
			},
			wantReason: workloadconstants.ProgressingReasonCreating,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "partially created initial establishment stays creating",
			set:  newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 2, CurrentRevision: "rev", UpdateRevision: "rev",
			},
			wantReason: workloadconstants.ProgressingReasonCreating,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "ordered ready subset during initial creation stays creating",
			// A previously ready subset is not establishment: UpdatedReplicas stays
			// below ExpectedUpdatedReplicas until the whole initial target exists.
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 1, 1, 1
				set.Status.ExpectedUpdatedReplicas = 3
				set.Status.CurrentRevision, set.Status.UpdateRevision = "rev", "rev"
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 2, CurrentRevision: "rev", UpdateRevision: "rev",
			},
			wantReason: workloadconstants.ProgressingReasonCreating,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "converged zero replica set scaling up reports scaling",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
					Type:   workloadsv1alpha2.RoleInstanceSetProgressing,
					Status: corev1.ConditionFalse,
					Reason: workloadconstants.ProgressingReasonConverged,
				}}
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				CurrentRevision: "rev", UpdateRevision: "rev",
			},
			wantReason: workloadconstants.ProgressingReasonScalingUp,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "zero replica target is never creating",
			set:  newTestSet(0),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				CurrentRevision: "rev", UpdateRevision: "rev",
			},
			wantReason: workloadconstants.ProgressingReasonConverged,
			wantStatus: corev1.ConditionFalse,
		},
		{
			name: "paused update with no other workflow reports paused",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Spec.UpdateStrategy.Paused = true
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, CurrentRevision: "old", UpdateRevision: "new",
				UpdatedReplicas: 1, ExpectedUpdatedReplicas: 3,
			},
			instances:  bareInstances(3),
			wantReason: workloadconstants.ProgressingReasonPaused,
			wantStatus: corev1.ConditionFalse,
		},
		{
			name: "paused update with outstanding scale up stays scaling",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Spec.UpdateStrategy.Paused = true
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 2, 2, 2
				set.Status.ExpectedUpdatedReplicas = 2
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 2, CurrentRevision: "old", UpdateRevision: "new",
				UpdatedReplicas: 1, ExpectedUpdatedReplicas: 3,
			},
			instances:  bareInstances(2),
			wantReason: workloadconstants.ProgressingReasonScalingUp,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "paused update with recovering instance stays recovering",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Spec.UpdateStrategy.Paused = true
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, CurrentRevision: "old", UpdateRevision: "new",
				UpdatedReplicas: 1, ExpectedUpdatedReplicas: 3,
			},
			instances: append(bareInstances(2),
				instanceWithProgressing("", workloadsv1alpha2.RoleInstancePhaseRecovering)),
			wantReason: workloadconstants.ProgressingReasonRecovering,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "scale up creating third instance reports scaling",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 2, 2, 2
				set.Status.ExpectedUpdatedReplicas = 2
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 2, UpdatedReplicas: 2, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 2,
			},
			instances:  bareInstances(2),
			wantReason: workloadconstants.ProgressingReasonScalingUp,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "scale down reports scaling down",
			set:  newTestSet(2),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, UpdatedReplicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances:  bareInstances(3),
			wantReason: workloadconstants.ProgressingReasonScalingDown,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "update scope incomplete reports role instance updated",
			set:  newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, CurrentRevision: "old", UpdateRevision: "new", UpdatedReplicas: 1, ExpectedUpdatedReplicas: 3,
			},
			instances:  bareInstances(3),
			wantReason: workloadconstants.ProgressingReasonRoleInstanceUpdated,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "completed partition rollout converges",
			// A non-zero partition pins instances on CurrentRevision after the
			// rollout completes, and CurrentRevision is never advanced then:
			// revision inequality alone must not keep the set updating forever.
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Spec.UpdateStrategy.Partition = ptr.To(intstr.FromInt32(2))
				set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
					Type:   workloadsv1alpha2.RoleInstanceSetProgressing,
					Status: corev1.ConditionTrue,
					Reason: workloadconstants.ProgressingReasonRoleInstanceUpdated,
				}}
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, ReadyReplicas: 3, AvailableReplicas: 3,
				CurrentRevision: "old", UpdateRevision: "new",
				UpdatedReplicas: 1, UpdatedReadyReplicas: 1, ExpectedUpdatedReplicas: 1,
			},
			instances:  bareInstances(3),
			wantReason: workloadconstants.ProgressingReasonConverged,
			wantStatus: corev1.ConditionFalse,
		},
		{
			name: "reversed scale target reports scaling down while the surplus instance is still starting",
			// After 2 -> 3, lowering the target to 1 leaves the still-Creating
			// third instance as part of the deletion, not a scale-up.
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(1)
				set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
					Type:   workloadsv1alpha2.RoleInstanceSetProgressing,
					Status: corev1.ConditionTrue,
					Reason: workloadconstants.ProgressingReasonScalingUp,
				}}
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 2, UpdatedReplicas: 2, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 1,
			},
			instances: append(bareInstances(1),
				instanceWithProgressing("", workloadsv1alpha2.RoleInstancePhaseCreating)),
			wantReason: workloadconstants.ProgressingReasonScalingDown,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "paused set with in-flight child update keeps the update workflow",
			// Pausing does not stop updates already dispatched to RoleInstances,
			// so the set must not report Paused while the child update executes.
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Spec.UpdateStrategy.Paused = true
				set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
					Type:               workloadsv1alpha2.RoleInstanceSetProgressing,
					Status:             corev1.ConditionTrue,
					Reason:             workloadconstants.ProgressingReasonRoleInstanceUpdated,
					ObservedGeneration: set.Generation,
				}}
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, CurrentRevision: "old", UpdateRevision: "new",
				UpdatedReplicas: 1, ExpectedUpdatedReplicas: 3,
			},
			instances: []*workloadsv1alpha2.RoleInstance{
				instanceWithProgressing("", workloadsv1alpha2.RoleInstancePhaseUpdating),
			},
			wantReason: workloadconstants.ProgressingReasonRoleInstanceUpdated,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "stale RI status does not complete the rollout",
			// Right after the set writes an RI's new spec, the RI still reports
			// status for the previous generation: its revision label already moved,
			// and its Ready condition predated the update. Such status must not
			// count toward completion.
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Spec.UpdateStrategy.Partition = ptr.To(intstr.FromInt32(2))
				set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
					Type:               workloadsv1alpha2.RoleInstanceSetProgressing,
					Status:             corev1.ConditionTrue,
					Reason:             workloadconstants.ProgressingReasonRoleInstanceUpdated,
					ObservedGeneration: set.Generation,
				}}
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, ReadyReplicas: 3, AvailableReplicas: 3,
				CurrentRevision: "old", UpdateRevision: "new",
				UpdatedReplicas: 1, UpdatedReadyReplicas: 1, ExpectedUpdatedReplicas: 1,
			},
			instances: []*workloadsv1alpha2.RoleInstance{
				{},
				{},
				{ObjectMeta: metav1.ObjectMeta{Generation: 2}, Status: workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: 1}},
			},
			wantReason: workloadconstants.ProgressingReasonRoleInstanceUpdated,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "stale RI status outranks not-ready counters",
			// The last in-place update can lower ReadyReplicas in the same status
			// write in which the RI still reports the old generation. The workflow
			// must not transiently collapse to InstancesNotReady.
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
					Type:               workloadsv1alpha2.RoleInstanceSetProgressing,
					Status:             corev1.ConditionTrue,
					Reason:             workloadconstants.ProgressingReasonRoleInstanceUpdated,
					ObservedGeneration: set.Generation,
				}}
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, ReadyReplicas: 2, AvailableReplicas: 2,
				CurrentRevision: "rev", UpdateRevision: "rev", UpdatedReplicas: 3, ExpectedUpdatedReplicas: 3,
			},
			instances: []*workloadsv1alpha2.RoleInstance{
				{},
				{},
				{ObjectMeta: metav1.ObjectMeta{Generation: 2}, Status: workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: 1}},
			},
			wantReason: workloadconstants.ProgressingReasonRoleInstanceUpdated,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "never-written RI keeps initial creation active",
			// Object existence alone is not startup completion; the first RI
			// status write can lag object creation.
			set: newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, ReadyReplicas: 3, AvailableReplicas: 3,
				CurrentRevision: "rev", UpdateRevision: "rev", UpdatedReplicas: 3, ExpectedUpdatedReplicas: 3,
			},
			instances: []*workloadsv1alpha2.RoleInstance{
				{ObjectMeta: metav1.ObjectMeta{Generation: 2}},
				{ObjectMeta: metav1.ObjectMeta{Generation: 2}},
				{ObjectMeta: metav1.ObjectMeta{Generation: 2}},
			},
			wantReason: workloadconstants.ProgressingReasonCreating,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "degraded set upgraded without conditions is established by RI latches",
			// A set degraded at upgrade time never fully converged under the new
			// model, but its instances carry the readiness latches written by the
			// previous controller generation: a later scale-up is ScalingUp, not
			// Creating.
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(4)
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 3, 2, 3
				set.Status.ExpectedUpdatedReplicas = 3
				set.Status.CurrentRevision, set.Status.UpdateRevision = "rev", "rev"
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances: []*workloadsv1alpha2.RoleInstance{
				{Status: workloadsv1alpha2.RoleInstanceStatus{LastReadyGeneration: 2}},
				{},
				{Status: workloadsv1alpha2.RoleInstanceStatus{LastReadyGeneration: 2}},
			},
			wantReason: workloadconstants.ProgressingReasonScalingUp,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "stale persisted update reason from an older generation does not attribute a scale-up",
			// Workflow evidence must describe the current target: a persisted
			// RoleInstanceUpdated from the prior generation must not turn a pure
			// scale-up gap into update work.
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 2, 2, 2
				set.Status.ExpectedUpdatedReplicas = 2
				set.Status.CurrentRevision, set.Status.UpdateRevision = "rev", "rev"
				set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
					Type:               workloadsv1alpha2.RoleInstanceSetProgressing,
					Status:             corev1.ConditionTrue,
					Reason:             workloadconstants.ProgressingReasonRoleInstanceUpdated,
					ObservedGeneration: set.Generation - 1,
				}}
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 2, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 2,
			},
			instances:  bareInstances(2),
			wantReason: workloadconstants.ProgressingReasonScalingUp,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "terminating RI with stale status does not block completion",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Spec.UpdateStrategy.Partition = ptr.To(intstr.FromInt32(2))
				set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
					Type:   workloadsv1alpha2.RoleInstanceSetProgressing,
					Status: corev1.ConditionTrue,
					Reason: workloadconstants.ProgressingReasonRoleInstanceUpdated,
				}}
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, ReadyReplicas: 3, AvailableReplicas: 3,
				CurrentRevision: "old", UpdateRevision: "new",
				UpdatedReplicas: 1, UpdatedReadyReplicas: 1, ExpectedUpdatedReplicas: 1,
			},
			instances: []*workloadsv1alpha2.RoleInstance{
				{},
				{},
				{ObjectMeta: metav1.ObjectMeta{
					Generation:        2,
					DeletionTimestamp: ptr.To(metav1.Now()),
				}, Status: workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: 1}},
			},
			wantReason: workloadconstants.ProgressingReasonConverged,
			wantStatus: corev1.ConditionFalse,
		},
		{
			name: "ri recovery aggregates to recovering",
			set:  newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances: append(bareInstances(2),
				instanceWithProgressing("", workloadsv1alpha2.RoleInstancePhaseRecovering)),
			wantReason: workloadconstants.ProgressingReasonRecovering,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "established set with missing instance reports recovering",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 3, 3, 3
				set.Status.ExpectedUpdatedReplicas = 3
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 2, UpdatedReplicas: 2, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances:  bareInstances(2),
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
			name: "ri restart budget exhaustion maps to its blocker family",
			set:  newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 2, CurrentRevision: "old", UpdateRevision: "new", UpdatedReplicas: 1, ExpectedUpdatedReplicas: 3,
			},
			instances: []*workloadsv1alpha2.RoleInstance{
				instanceWithProgressing(workloadsv1alpha2.ReasonGroupRestartBudgetExceeded, workloadsv1alpha2.RoleInstancePhaseRecovering),
			},
			wantReason: workloadconstants.ProgressingReasonRestartBudgetExceeded,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "ri insufficient resources maps to waiting for resources",
			set:  newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances: append(bareInstances(2), func() *workloadsv1alpha2.RoleInstance {
				instance := instanceWithProgressing(workloadsv1alpha2.ReasonInstanceInsufficientResources, workloadsv1alpha2.RoleInstancePhaseRunning)
				instance.ObjectMeta = metav1.ObjectMeta{Generation: 7}
				instance.Status.Conditions[0].Message = "Pod ri-0 is unschedulable: 3 Insufficient cpu"
				instance.Status.Conditions[0].ObservedGeneration = 7
				return instance
			}()),
			wantReason: workloadconstants.ProgressingReasonWaitingForResources,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "no workflow and instances not ready",
			set:  newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, ReadyReplicas: 2, UpdatedReplicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances:  bareInstances(3),
			wantReason: workloadconstants.ProgressingReasonInstancesNotReady,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "converged",
			set:  newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, ReadyReplicas: 3, AvailableReplicas: 3, UpdatedReplicas: 3,
				CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances:  bareInstances(3),
			wantReason: workloadconstants.ProgressingReasonConverged,
			wantStatus: corev1.ConditionFalse,
		},
		{
			name: "stateful synthetic slots do not hide initial creation",
			// updateStatefulInstanceSet fills missing slots with synthetic
			// RoleInstances and updateStatus counts them: newStatus.Replicas already
			// reports the target while only one instance is observed.
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 1, 1, 1
				set.Status.ExpectedUpdatedReplicas = 3
				set.Status.CurrentRevision, set.Status.UpdateRevision = "rev", "rev"
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances:  bareInstances(1),
			wantReason: workloadconstants.ProgressingReasonCreating,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "stateful synthetic slots do not hide missing-instance recovery",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 3, 3, 3
				set.Status.ExpectedUpdatedReplicas = 3
				set.Status.CurrentRevision, set.Status.UpdateRevision = "rev", "rev"
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances:  bareInstances(2),
			wantReason: workloadconstants.ProgressingReasonRecovering,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "minReadySeconds keeps the set progressing before availability",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 3, 3, 3
				set.Status.ExpectedUpdatedReplicas = 3
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, ReadyReplicas: 3, AvailableReplicas: 1, UpdatedReplicas: 3,
				CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances:  bareInstances(3),
			wantReason: workloadconstants.ProgressingReasonInstancesNotReady,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "persisted recovering reason keeps replacement stable after counters decay",
			// The first shortage write lowers persisted Replicas below the target;
			// the persisted Recovering reason proves replacement is in progress.
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 2, 2, 2
				set.Status.ExpectedUpdatedReplicas = 3
				set.Status.CurrentRevision, set.Status.UpdateRevision = "rev", "rev"
				set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
					Type:   workloadsv1alpha2.RoleInstanceSetProgressing,
					Status: corev1.ConditionTrue,
					Reason: workloadconstants.ProgressingReasonRecovering,
				}}
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 2, UpdatedReplicas: 2, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances:  bareInstances(2),
			wantReason: workloadconstants.ProgressingReasonRecovering,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "creating persists while the initial instances are starting",
			// KEP-502 keeps the workflow reason until the instances it added
			// complete startup; the reason must not collapse to InstancesNotReady
			// once the objects exist.
			set: newTestSet(3),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances:  append(bareInstances(2), instanceWithProgressing("", workloadsv1alpha2.RoleInstancePhaseCreating)),
			wantReason: workloadconstants.ProgressingReasonCreating,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "scaling up persists while the new instances are starting",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 2, 2, 2
				set.Status.ExpectedUpdatedReplicas = 2
				set.Status.CurrentRevision, set.Status.UpdateRevision = "rev", "rev"
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances:  append(bareInstances(2), instanceWithProgressing("", workloadsv1alpha2.RoleInstancePhaseCreating)),
			wantReason: workloadconstants.ProgressingReasonScalingUp,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "recreate pod replacement gap keeps role instance updated",
			// After all old instances are deleted, stateless mode can already
			// report current == update with zero replicas; the persisted
			// RoleInstanceUpdated reason keeps the gap attributed to the rollout.
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(2)
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 2, 2, 2
				set.Status.ExpectedUpdatedReplicas = 2
				set.Status.CurrentRevision, set.Status.UpdateRevision = "new", "new"
				set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
					Type:               workloadsv1alpha2.RoleInstanceSetProgressing,
					Status:             corev1.ConditionTrue,
					Reason:             workloadconstants.ProgressingReasonRoleInstanceUpdated,
					ObservedGeneration: set.Generation,
				}}
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				CurrentRevision: "new", UpdateRevision: "new",
			},
			instances:  nil,
			wantReason: workloadconstants.ProgressingReasonRoleInstanceUpdated,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "replacement startup after revisions converge keeps role instance updated",
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(2)
				set.Status.CurrentRevision, set.Status.UpdateRevision = "new", "new"
				set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
					Type:               workloadsv1alpha2.RoleInstanceSetProgressing,
					Status:             corev1.ConditionTrue,
					Reason:             workloadconstants.ProgressingReasonRoleInstanceUpdated,
					ObservedGeneration: set.Generation,
				}}
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 2, CurrentRevision: "new", UpdateRevision: "new", ExpectedUpdatedReplicas: 2,
			},
			instances:  bareInstances(1),
			wantReason: workloadconstants.ProgressingReasonRoleInstanceUpdated,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "surge cleanup stays role instance updated instead of scaling down",
			// One old-revision instance is still pending deletion after the update
			// scope is complete; the revisions have not converged yet, so the
			// deletion belongs to the rollout, not to a scale-down.
			set: newTestSet(2),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 3, CurrentRevision: "old", UpdateRevision: "new",
				UpdatedReplicas: 2, ExpectedUpdatedReplicas: 2,
			},
			instances:  bareInstances(3),
			wantReason: workloadconstants.ProgressingReasonRoleInstanceUpdated,
			wantStatus: corev1.ConditionTrue,
		},
		{
			name: "never-ready set stays creating after losing an instance",
			// A persisted InstancesNotReady condition is written before the set
			// has ever been ready, so it must not count as establishment evidence.
			set: func() *workloadsv1alpha2.RoleInstanceSet {
				set := newTestSet(3)
				set.Status.CurrentRevision, set.Status.UpdateRevision = "rev", "rev"
				set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
					Type:   workloadsv1alpha2.RoleInstanceSetProgressing,
					Status: corev1.ConditionTrue,
					Reason: workloadconstants.ProgressingReasonInstancesNotReady,
				}}
				return set
			}(),
			newStatus: &workloadsv1alpha2.RoleInstanceSetStatus{
				Replicas: 2, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
			},
			instances:  bareInstances(2),
			wantReason: workloadconstants.ProgressingReasonCreating,
			wantStatus: corev1.ConditionTrue,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeSetProgressingCondition(tc.set, tc.newStatus, tc.instances, nil, nil)
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
	own := &SetBlocker{
		Reason:  workloadconstants.ProgressingReasonPermissionDenied,
		Message: "rbac denied",
	}
	instances := []*workloadsv1alpha2.RoleInstance{
		instanceWithProgressing(workloadsv1alpha2.ReasonPodCreateQuotaExceeded, workloadsv1alpha2.RoleInstancePhaseCreating),
	}
	reason, message := currentSetBlocker(own, nil, instances)
	if reason != workloadconstants.ProgressingReasonPermissionDenied {
		t.Fatalf("permission must outrank quota, got %s", reason)
	}
	if message != "rbac denied" {
		t.Fatalf("own message must be preserved, got %s", message)
	}

	instances = []*workloadsv1alpha2.RoleInstance{
		func() *workloadsv1alpha2.RoleInstance {
			instance := instanceWithProgressing(workloadsv1alpha2.ReasonGroupRestartBudgetExceeded, workloadsv1alpha2.RoleInstancePhaseRecovering)
			instance.Name = "router-0"
			instance.Generation = 1
			instance.Status.ObservedGeneration = 1
			instance.Status.Conditions[0].ObservedGeneration = 1
			return instance
		}(),
	}
	reason, message = currentSetBlocker(own, nil, instances)
	if reason != workloadconstants.ProgressingReasonRestartBudgetExceeded {
		t.Fatalf("restart budget exhaustion must outrank permission, got %s", reason)
	}
	if !strings.Contains(message, "router-0") {
		t.Fatalf("restart-budget message must name the affected RoleInstance, got %s", message)
	}
}

func TestRestartBudgetMessageSortsInstanceNames(t *testing.T) {
	instance := func(name string) *workloadsv1alpha2.RoleInstance {
		instance := instanceWithProgressing(workloadsv1alpha2.ReasonGroupRestartBudgetExceeded, workloadsv1alpha2.RoleInstancePhaseRecovering)
		instance.Name = name
		instance.Generation = 2
		instance.Status.ObservedGeneration = 2
		instance.Status.Conditions[0].ObservedGeneration = 2
		return instance
	}
	instances := []*workloadsv1alpha2.RoleInstance{instance("router-9"), instance("router-1"), instance("router-5")}
	_, message := currentSetBlocker(nil, nil, instances)
	want := "restart budget exhausted for RoleInstances router-1, router-5, router-9"
	if message != want {
		t.Fatalf("got %q, want %q", message, want)
	}
}

func TestCurrentSetBlockerRetainsPriorFamily(t *testing.T) {
	prior := &SetBlocker{
		Reason:  workloadconstants.ProgressingReasonPermissionDenied,
		Message: "rbac denied",
	}
	own := &SetBlocker{
		Reason:  workloadconstants.ProgressingReasonUnclassifiedBlocker,
		Message: "server timeout",
	}
	reason, _ := currentSetBlocker(own, prior, nil)
	if reason != workloadconstants.ProgressingReasonPermissionDenied {
		t.Fatalf("confirmed permission blocker must survive an unclassified error, got %s", reason)
	}

	// A fresh successful observation clears the prior family.
	reason, _ = currentSetBlocker(nil, prior, nil)
	if reason != "" {
		t.Fatalf("successful observation must clear the prior blocker, got %s", reason)
	}

	// The retained family must carry the latest operation error so diagnostics and
	// event deduplication follow the current attempt.
	_, message := currentSetBlocker(own, prior, nil)
	if message != "server timeout" {
		t.Fatalf("retained blocker must record the latest error, got %q", message)
	}
}

func TestPriorSetBlockerRequiresCurrentGeneration(t *testing.T) {
	set := newTestSet(3)
	set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
		Type:               workloadsv1alpha2.RoleInstanceSetProgressing,
		Status:             corev1.ConditionTrue,
		Reason:             workloadconstants.ProgressingReasonPermissionDenied,
		Message:            "rbac denied",
		ObservedGeneration: 4,
	}}
	if prior := priorSetBlocker(set); prior == nil || prior.Reason != workloadconstants.ProgressingReasonPermissionDenied {
		t.Fatalf("current-generation blocker expected, got %+v", prior)
	}
	set.Status.Conditions[0].ObservedGeneration = 3
	if prior := priorSetBlocker(set); prior != nil {
		t.Fatalf("stale-generation blocker must not be retained, got %+v", prior)
	}

	set.Status.Conditions[0].Reason = workloadconstants.ProgressingReasonRestartBudgetExceeded
	set.Status.Conditions[0].ObservedGeneration = 4
	if prior := priorSetBlocker(set); prior != nil {
		t.Fatalf("child-derived restart-budget blocker must not be retained as own evidence, got %+v", prior)
	}
}

func TestApplySetProgressingConditionDeduplicates(t *testing.T) {
	set := newTestSet(3)
	set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 2, 2, 2
	set.Status.ExpectedUpdatedReplicas = 2
	newStatus := &workloadsv1alpha2.RoleInstanceSetStatus{
		CurrentRevision: "rev", UpdateRevision: "rev", Replicas: 2,
		Conditions: []workloadsv1alpha2.RoleInstanceSetCondition{
			// A scale failure and an update failure in the same reconcile append two
			// raw blockers; the lower-priority one is first.
			{
				Type: workloadsv1alpha2.RoleInstanceSetProgressing, Status: corev1.ConditionTrue,
				Reason: workloadconstants.ProgressingReasonUnclassifiedBlocker, Message: "scale timeout",
			},
			{
				Type: workloadsv1alpha2.RoleInstanceSetProgressing, Status: corev1.ConditionTrue,
				Reason: workloadconstants.ProgressingReasonPermissionDenied, Message: "update forbidden",
			},
		},
	}
	ApplySetProgressingCondition(set, newStatus, nil)
	var progressing []*workloadsv1alpha2.RoleInstanceSetCondition
	for i := range newStatus.Conditions {
		if newStatus.Conditions[i].Type == workloadsv1alpha2.RoleInstanceSetProgressing {
			progressing = append(progressing, &newStatus.Conditions[i])
		}
	}
	if len(progressing) != 1 {
		t.Fatalf("exactly one Progressing condition expected, got %d", len(progressing))
	}
	if progressing[0].Reason != workloadconstants.ProgressingReasonPermissionDenied {
		t.Fatalf("permission must outrank the unclassified first error, got %s", progressing[0].Reason)
	}
}

func TestEstablishmentSurvivesDominantBlocker(t *testing.T) {
	// A converged set scales from two to three; quota blocks the new instance
	// and becomes the dominant Progressing reason, erasing ScalingUp.
	establishedSet := func() *workloadsv1alpha2.RoleInstanceSet {
		set := newTestSet(3)
		set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 2, 2, 2
		set.Status.ExpectedUpdatedReplicas = 2
		set.Status.CurrentRevision, set.Status.UpdateRevision = "rev", "rev"
		set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
			Type:   workloadsv1alpha2.RoleInstanceSetProgressing,
			Status: corev1.ConditionTrue,
			Reason: workloadconstants.ProgressingReasonScalingUp,
		}}
		return set
	}()
	blockedStatus := &workloadsv1alpha2.RoleInstanceSetStatus{
		Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2,
		CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
		Conditions: []workloadsv1alpha2.RoleInstanceSetCondition{{
			Type: workloadsv1alpha2.RoleInstanceSetProgressing, Status: corev1.ConditionTrue,
			Reason: workloadconstants.ProgressingReasonWaitingForQuota, Message: "exceeded quota",
		}},
	}
	ApplySetProgressingCondition(establishedSet, blockedStatus, bareInstances(2))

	var established *workloadsv1alpha2.RoleInstanceSetCondition
	for i := range blockedStatus.Conditions {
		if blockedStatus.Conditions[i].Type == workloadsv1alpha2.RoleInstanceSetEstablished {
			established = &blockedStatus.Conditions[i]
		}
	}
	if established == nil || established.Status != corev1.ConditionTrue {
		t.Fatalf("establishment must be persisted independently of the dominant blocker, got %+v", blockedStatus.Conditions)
	}

	// Quota clears and the new instance is still starting: the set must return
	// to its scale-up workflow, not to initial creation.
	persisted := establishedSet.DeepCopy()
	persisted.Status = *blockedStatus.DeepCopy()
	resumedStatus := &workloadsv1alpha2.RoleInstanceSetStatus{
		Replicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
	}
	instances := append(bareInstances(2),
		instanceWithProgressing("", workloadsv1alpha2.RoleInstancePhaseCreating))
	ApplySetProgressingCondition(persisted, resumedStatus, instances)
	var resumed *workloadsv1alpha2.RoleInstanceSetCondition
	for i := range resumedStatus.Conditions {
		if resumedStatus.Conditions[i].Type == workloadsv1alpha2.RoleInstanceSetProgressing {
			resumed = &resumedStatus.Conditions[i]
		}
	}
	if resumed == nil || resumed.Reason != workloadconstants.ProgressingReasonScalingUp {
		t.Fatalf("a blocked scale-up must resume as ScalingUp, got %+v", resumed)
	}
}

func TestFreshBlockedSetDoesNotUseEstablishmentFallback(t *testing.T) {
	set := newTestSet(3)
	set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
		Type:   workloadsv1alpha2.RoleInstanceSetProgressing,
		Status: corev1.ConditionTrue,
		Reason: workloadconstants.ProgressingReasonCreating,
	}}
	blockedStatus := &workloadsv1alpha2.RoleInstanceSetStatus{
		Replicas: 1, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
		Conditions: []workloadsv1alpha2.RoleInstanceSetCondition{{
			Type:    workloadsv1alpha2.RoleInstanceSetProgressing,
			Status:  corev1.ConditionTrue,
			Reason:  workloadconstants.ProgressingReasonWaitingForQuota,
			Message: "exceeded quota",
		}},
	}
	instances := []*workloadsv1alpha2.RoleInstance{
		{ObjectMeta: metav1.ObjectMeta{Generation: 2}, Status: workloadsv1alpha2.RoleInstanceStatus{
			ObservedGeneration: 2, LastReadyGeneration: 2,
		}},
		{ObjectMeta: metav1.ObjectMeta{Generation: 1}},
		{ObjectMeta: metav1.ObjectMeta{Generation: 1}},
	}
	ApplySetProgressingCondition(set, blockedStatus, instances)
	if slices.ContainsFunc(blockedStatus.Conditions, func(condition workloadsv1alpha2.RoleInstanceSetCondition) bool {
		return condition.Type == workloadsv1alpha2.RoleInstanceSetEstablished
	}) {
		t.Fatalf("a fresh set blocked by quota must not become established, got %+v", blockedStatus.Conditions)
	}

	// Once quota is removed, the persisted blocker proves the set has never
	// completed initial establishment; the remaining replicas report Creating.
	persisted := set.DeepCopy()
	persisted.Status = *blockedStatus.DeepCopy()
	resumedStatus := &workloadsv1alpha2.RoleInstanceSetStatus{
		Replicas: 3, CurrentRevision: "rev", UpdateRevision: "rev", ExpectedUpdatedReplicas: 3,
	}
	got := ComputeSetProgressingCondition(persisted, resumedStatus, instances, nil, nil)
	if got.Status != corev1.ConditionTrue || got.Reason != workloadconstants.ProgressingReasonCreating {
		t.Fatalf("got status=%s reason=%s, want Creating", got.Status, got.Reason)
	}
}

func TestFirstConvergedWriteRecordsEstablishment(t *testing.T) {
	set := newTestSet(3)
	newStatus := &workloadsv1alpha2.RoleInstanceSetStatus{
		Replicas: 3, ReadyReplicas: 3, AvailableReplicas: 3,
		CurrentRevision: "rev", UpdateRevision: "rev", UpdatedReplicas: 3, ExpectedUpdatedReplicas: 3,
	}
	ApplySetProgressingCondition(set, newStatus, bareInstances(3))
	var established *workloadsv1alpha2.RoleInstanceSetCondition
	for i := range newStatus.Conditions {
		if newStatus.Conditions[i].Type == workloadsv1alpha2.RoleInstanceSetEstablished {
			established = &newStatus.Conditions[i]
		}
	}
	if established == nil || established.Status != corev1.ConditionTrue {
		t.Fatalf("first Converged write must record Established, got %+v", established)
	}
}

func TestFirstActivityWriteDoesNotRecordEstablishment(t *testing.T) {
	set := newTestSet(1)
	set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
		Type:               workloadsv1alpha2.RoleInstanceSetProgressing,
		Status:             corev1.ConditionTrue,
		Reason:             workloadconstants.ProgressingReasonCreating,
		ObservedGeneration: set.Generation,
	}}
	newStatus := &workloadsv1alpha2.RoleInstanceSetStatus{
		Replicas: 2, ReadyReplicas: 2, AvailableReplicas: 2,
		CurrentRevision: "rev", UpdateRevision: "rev", UpdatedReplicas: 2, ExpectedUpdatedReplicas: 2,
	}
	instances := []*workloadsv1alpha2.RoleInstance{
		{ObjectMeta: metav1.ObjectMeta{Generation: 1}, Status: workloadsv1alpha2.RoleInstanceStatus{
			ObservedGeneration: 1, Phase: workloadsv1alpha2.RoleInstancePhaseRunning,
		}},
		{ObjectMeta: metav1.ObjectMeta{Generation: 1}, Status: workloadsv1alpha2.RoleInstanceStatus{
			ObservedGeneration: 1, Phase: workloadsv1alpha2.RoleInstancePhaseRunning,
		}},
	}
	ApplySetProgressingCondition(set, newStatus, instances)
	for _, condition := range newStatus.Conditions {
		if condition.Type == workloadsv1alpha2.RoleInstanceSetEstablished {
			t.Fatalf("first activity write must not record Established, got %+v", condition)
		}
	}
}

func TestStaleBlockerDoesNotDisableLegacyEstablishmentFallback(t *testing.T) {
	set := newTestSet(3)
	set.Status.Conditions = []workloadsv1alpha2.RoleInstanceSetCondition{{
		Type:               workloadsv1alpha2.RoleInstanceSetProgressing,
		Status:             corev1.ConditionTrue,
		Reason:             workloadconstants.ProgressingReasonPermissionDenied,
		ObservedGeneration: set.Generation - 1,
	}}
	instances := []*workloadsv1alpha2.RoleInstance{
		{ObjectMeta: metav1.ObjectMeta{Generation: 3}, Status: workloadsv1alpha2.RoleInstanceStatus{
			LastReadyGeneration: 3,
		}},
	}
	if !setEstablished(set, instances) {
		t.Fatal("stale blocker must not disable the legacy establishment fallback")
	}
}

func TestSetProgressingWithStaleOrUnwrittenInstances(t *testing.T) {
	instance := func(generation, observed, latched int64) *workloadsv1alpha2.RoleInstance {
		return &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Generation: generation},
			Status: workloadsv1alpha2.RoleInstanceStatus{
				ObservedGeneration: observed, LastReadyGeneration: latched,
			},
		}
	}
	progressing := func(reason string, generation int64) workloadsv1alpha2.RoleInstanceSetCondition {
		return workloadsv1alpha2.RoleInstanceSetCondition{
			Type: workloadsv1alpha2.RoleInstanceSetProgressing, Status: corev1.ConditionTrue,
			Reason: reason, ObservedGeneration: generation,
		}
	}
	established := workloadsv1alpha2.RoleInstanceSetCondition{
		Type: workloadsv1alpha2.RoleInstanceSetEstablished, Status: corev1.ConditionTrue,
	}
	status := func(ready int32) *workloadsv1alpha2.RoleInstanceSetStatus {
		return &workloadsv1alpha2.RoleInstanceSetStatus{
			Replicas: 3, ReadyReplicas: ready, AvailableReplicas: ready,
			CurrentRevision: "rev", UpdateRevision: "rev", UpdatedReplicas: 3,
			UpdatedReadyReplicas: ready, ExpectedUpdatedReplicas: 3,
		}
	}
	cases := []struct {
		name       string
		replicas   int32
		paused     bool
		conditions []workloadsv1alpha2.RoleInstanceSetCondition
		newStatus  *workloadsv1alpha2.RoleInstanceSetStatus
		instances  []*workloadsv1alpha2.RoleInstance
		want       string
	}{{
		name:       "behind-generation RI without current-generation update evidence",
		replicas:   3,
		conditions: []workloadsv1alpha2.RoleInstanceSetCondition{progressing(workloadconstants.ProgressingReasonRoleInstanceUpdated, 3), established},
		newStatus:  status(3),
		instances:  []*workloadsv1alpha2.RoleInstance{instance(2, 2, 2), instance(2, 2, 2), instance(2, 1, 1)},
		want:       workloadconstants.ProgressingReasonRoleInstanceUpdated,
	}, {
		name:       "paused right after the last dispatch",
		replicas:   3,
		paused:     true,
		conditions: []workloadsv1alpha2.RoleInstanceSetCondition{progressing(workloadconstants.ProgressingReasonRoleInstanceUpdated, 3), established},
		newStatus:  status(3),
		instances:  []*workloadsv1alpha2.RoleInstance{instance(2, 2, 2), instance(2, 2, 2), instance(2, 1, 1)},
		want:       workloadconstants.ProgressingReasonRoleInstanceUpdated,
	}, {
		name:       "established scale-up with a never-written RI",
		replicas:   3,
		conditions: []workloadsv1alpha2.RoleInstanceSetCondition{progressing(workloadconstants.ProgressingReasonScalingUp, 4), established},
		newStatus:  status(2),
		instances:  []*workloadsv1alpha2.RoleInstance{instance(1, 1, 1), instance(1, 1, 1), instance(1, 0, 0)},
		want:       workloadconstants.ProgressingReasonScalingUp,
	}, {
		name:       "recovery replacement that has not written status",
		replicas:   3,
		conditions: []workloadsv1alpha2.RoleInstanceSetCondition{progressing(workloadconstants.ProgressingReasonRecovering, 4), established},
		newStatus:  status(2),
		instances:  []*workloadsv1alpha2.RoleInstance{instance(1, 1, 1), instance(1, 1, 1), instance(1, 0, 0)},
		want:       workloadconstants.ProgressingReasonRecovering,
	}, {
		name:       "set upgraded without latches keeps the RI-latch fallback",
		replicas:   4,
		conditions: []workloadsv1alpha2.RoleInstanceSetCondition{progressing(workloadconstants.ProgressingReasonInstancesNotReady, 3)},
		newStatus:  status(2),
		instances:  []*workloadsv1alpha2.RoleInstance{instance(1, 1, 1), instance(1, 1, 1), instance(1, 1, 0)},
		want:       workloadconstants.ProgressingReasonScalingUp,
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := newTestSet(tc.replicas)
			set.Spec.UpdateStrategy.Paused = tc.paused
			set.Status.Conditions = tc.conditions
			set.Status.Replicas, set.Status.ReadyReplicas = tc.newStatus.Replicas, tc.newStatus.ReadyReplicas
			set.Status.UpdatedReplicas, set.Status.ExpectedUpdatedReplicas = tc.newStatus.UpdatedReplicas, tc.newStatus.ExpectedUpdatedReplicas
			got := ComputeSetProgressingCondition(set, tc.newStatus, tc.instances, nil, nil)
			if got.Reason != tc.want {
				t.Fatalf("got %s/%s, want %s", got.Status, got.Reason, tc.want)
			}
		})
	}
}

func TestExtractOwnSetBlocker(t *testing.T) {
	empty := &workloadsv1alpha2.RoleInstanceSetStatus{}
	if own := extractOwnSetBlocker(empty); own != nil {
		t.Fatalf("expected nil, got %+v", own)
	}
	withBlocker := &workloadsv1alpha2.RoleInstanceSetStatus{
		Conditions: []workloadsv1alpha2.RoleInstanceSetCondition{{
			Type:    workloadsv1alpha2.RoleInstanceSetProgressing,
			Status:  corev1.ConditionTrue,
			Reason:  workloadconstants.ProgressingReasonUnclassifiedBlocker,
			Message: "boom",
		}},
	}
	own := extractOwnSetBlocker(withBlocker)
	if own == nil || own.Reason != workloadconstants.ProgressingReasonUnclassifiedBlocker || own.Message != "boom" {
		t.Fatalf("got %+v", own)
	}

	multiple := &workloadsv1alpha2.RoleInstanceSetStatus{
		Conditions: []workloadsv1alpha2.RoleInstanceSetCondition{
			{
				Type: workloadsv1alpha2.RoleInstanceSetProgressing, Status: corev1.ConditionTrue,
				Reason: workloadconstants.ProgressingReasonUnclassifiedBlocker, Message: "scale timeout",
			},
			{
				Type: workloadsv1alpha2.RoleInstanceSetProgressing, Status: corev1.ConditionTrue,
				Reason: workloadconstants.ProgressingReasonPermissionDenied, Message: "update forbidden",
			},
		},
	}
	own = extractOwnSetBlocker(multiple)
	if own == nil || own.Reason != workloadconstants.ProgressingReasonPermissionDenied {
		t.Fatalf("highest-priority own blocker expected, got %+v", own)
	}
}

func TestClassifySetOperationError(t *testing.T) {
	forbiddenQuota := apierrors.NewForbidden(
		corev1.Resource("roleinstances"), "ri-0", apierrors.NewBadRequest("exceeded quota"))
	if got := ClassifySetOperationError(forbiddenQuota); got != workloadconstants.ProgressingReasonWaitingForQuota {
		t.Fatalf("got %s", got)
	}
	forbiddenRBAC := apierrors.NewForbidden(
		corev1.Resource("roleinstances"), "ri-0", apierrors.NewBadRequest(
			`User "alice" cannot create resource "roleinstances" in API group "workloads.x-k8s.io" in the namespace "default"`))
	if got := ClassifySetOperationError(forbiddenRBAC); got != workloadconstants.ProgressingReasonPermissionDenied {
		t.Fatalf("got %s", got)
	}
	// Admission webhooks can reject with HTTP 403; the detail must be recognized
	// before defaulting to RBAC.
	forbiddenAdmission := apierrors.NewForbidden(
		corev1.Resource("roleinstances"), "ri-0",
		apierrors.NewBadRequest(`admission webhook "example" denied the request`))
	if got := ClassifySetOperationError(forbiddenAdmission); got != workloadconstants.ProgressingReasonAdmissionDenied {
		t.Fatalf("got %s", got)
	}
	// A joined role-operation error must be classified per constituent error; the
	// Forbidden status of one role must not borrow another role's quota message.
	joined := stderrors.Join(
		apierrors.NewForbidden(corev1.Resource("roleinstances"), "ri-a", apierrors.NewBadRequest(
			`User "alice" cannot create resource "roleinstances" in API group "workloads.x-k8s.io" in the namespace "default"`)),
		apierrors.NewForbidden(corev1.Resource("roleinstances"), "ri-b", apierrors.NewBadRequest("exceeded quota")))
	if got := ClassifySetOperationError(joined); got != workloadconstants.ProgressingReasonPermissionDenied {
		t.Fatalf("permission must outrank quota in a joined error, got %s", got)
	}
	// A ValidatingAdmissionPolicy with the default reason rejects with 422
	// Invalid and mentions neither a webhook nor a plugin.
	vap := &apierrors.StatusError{ErrStatus: metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    422,
		Reason:  metav1.StatusReasonInvalid,
		Message: `ValidatingAdmissionPolicy 'require-labels' with binding 'check-roleinstances' denied request: metadata.labels: required`,
	}}
	if got := ClassifySetOperationError(vap); got != workloadconstants.ProgressingReasonAdmissionDenied {
		t.Fatalf("ValidatingAdmissionPolicy denial must be admission, got %s", got)
	}
	if got := ClassifySetOperationError(nil); got != "" {
		t.Fatalf("got %s", got)
	}
	if got := ClassifySetOperationError(apierrors.NewServerTimeout(corev1.Resource("roleinstances"), "POST", 1)); got != workloadconstants.ProgressingReasonUnclassifiedBlocker {
		t.Fatalf("got %s", got)
	}
}
