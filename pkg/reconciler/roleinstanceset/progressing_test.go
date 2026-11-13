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
					Type:   workloadsv1alpha2.RoleInstanceSetProgressing,
					Status: corev1.ConditionTrue,
					Reason: workloadconstants.ProgressingReasonRoleInstanceUpdated,
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
					Type:   workloadsv1alpha2.RoleInstanceSetProgressing,
					Status: corev1.ConditionTrue,
					Reason: workloadconstants.ProgressingReasonRoleInstanceUpdated,
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
	if got := ClassifySetOperationError(nil); got != "" {
		t.Fatalf("got %s", got)
	}
	if got := ClassifySetOperationError(apierrors.NewServerTimeout(corev1.Resource("roleinstances"), "POST", 1)); got != workloadconstants.ProgressingReasonUnclassifiedBlocker {
		t.Fatalf("got %s", got)
	}
}
