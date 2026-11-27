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
	stderrors "errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	workloadconstants "sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/reconciler"
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

func observation(reason string, status corev1.ConditionStatus, revision string) risObservation {
	return risObservation{
		exists:        true,
		revisionLabel: revision,
		progressing: &workloadsv1alpha2.RoleInstanceSetCondition{
			Type: workloadsv1alpha2.RoleInstanceSetProgressing, Status: status, Reason: reason,
		},
	}
}

// observationWithGeneration builds an observation with explicit set and condition
// generations, for staleness checks. A condition whose observedGeneration does not
// match the observed RoleInstanceSet generation must not be treated as current.
func observationWithGeneration(reason string, status corev1.ConditionStatus, revision string, setGeneration, conditionGeneration int64) risObservation {
	obs := observation(reason, status, revision)
	obs.generation = setGeneration
	obs.progressing.ObservedGeneration = conditionGeneration
	return obs
}

func convergedObservations() map[string]risObservation {
	return map[string]risObservation{
		"prefill": observation(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse, "hash"),
		"decode":  observation(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse, "hash"),
	}
}

func expectedRevisions() map[string]string {
	return map[string]string{"prefill": "hash", "decode": "hash"}
}

func TestComputeRBGProgressingCondition(t *testing.T) {
	cases := []struct {
		name         string
		rbg          *workloadsv1alpha2.RoleBasedGroup
		roleStatuses []workloadsv1alpha2.RoleStatus
		observations map[string]risObservation
		expected     map[string]string
		gangWaiting  bool
		depsNotReady []string
		wantReason   string
		wantStatus   metav1.ConditionStatus
	}{
		{
			name:         "converged from own target",
			rbg:          newProgressingRBG(),
			roleStatuses: readyRoleStatuses(),
			observations: convergedObservations(),
			expected:     expectedRevisions(),
			wantReason:   workloadconstants.ProgressingReasonConverged,
			wantStatus:   metav1.ConditionFalse,
		},
		{
			name: "gang waiting outranks activity",
			rbg:  newProgressingRBG(),
			observations: map[string]risObservation{
				"prefill": observation(workloadconstants.ProgressingReasonRoleInstanceUpdated, corev1.ConditionTrue, "hash"),
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
			observations: map[string]risObservation{
				"decode": observation(workloadconstants.ProgressingReasonWaitingForQuota, corev1.ConditionTrue, "hash"),
			},
			wantReason: workloadconstants.ProgressingReasonWaitingForQuota,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "family blocker outranks gang waiting",
			rbg:  newProgressingRBG(),
			observations: map[string]risObservation{
				"decode": observation(workloadconstants.ProgressingReasonWaitingForQuota, corev1.ConditionTrue, "hash"),
			},
			gangWaiting: true,
			wantReason:  workloadconstants.ProgressingReasonWaitingForQuota,
			wantStatus:  metav1.ConditionTrue,
		},
		{
			name: "ris resources blocker aggregates at rbg",
			rbg:  newProgressingRBG(),
			observations: map[string]risObservation{
				"prefill": observation(workloadconstants.ProgressingReasonWaitingForResources, corev1.ConditionTrue, "hash"),
			},
			wantReason: workloadconstants.ProgressingReasonWaitingForResources,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "ris invalid spec aggregates as the highest-priority blocker",
			rbg:  newProgressingRBG(),
			observations: map[string]risObservation{
				"prefill": observation(workloadconstants.ProgressingReasonInvalidSpec, corev1.ConditionTrue, "hash"),
				"decode":  observation(workloadconstants.ProgressingReasonWaitingForQuota, corev1.ConditionTrue, "hash"),
			},
			wantReason: workloadconstants.ProgressingReasonInvalidSpec,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "ris restart budget exhaustion aggregates at rbg",
			rbg:  newProgressingRBG(),
			observations: map[string]risObservation{
				"prefill": observation(workloadconstants.ProgressingReasonRestartBudgetExceeded, corev1.ConditionTrue, "hash"),
				"decode":  observation(workloadconstants.ProgressingReasonWaitingForQuota, corev1.ConditionTrue, "hash"),
			},
			wantReason: workloadconstants.ProgressingReasonRestartBudgetExceeded,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "configuration blockers outrank restart budget exhaustion",
			rbg:  newProgressingRBG(),
			observations: map[string]risObservation{
				"prefill": observation(workloadconstants.ProgressingReasonInvalidSpec, corev1.ConditionTrue, "hash"),
				"decode":  observation(workloadconstants.ProgressingReasonRestartBudgetExceeded, corev1.ConditionTrue, "hash"),
			},
			wantReason: workloadconstants.ProgressingReasonInvalidSpec,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "family blocker outranks dependencies",
			rbg:  newProgressingRBG(),
			observations: map[string]risObservation{
				"decode": observation(workloadconstants.ProgressingReasonPermissionDenied, corev1.ConditionTrue, "hash"),
			},
			depsNotReady: []string{"decode"},
			wantReason:   workloadconstants.ProgressingReasonPermissionDenied,
			wantStatus:   metav1.ConditionTrue,
		},
		{
			name:         "stale child condition does not confirm completion",
			rbg:          newProgressingRBG(),
			roleStatuses: readyRoleStatuses(),
			observations: map[string]risObservation{
				"prefill": observationWithGeneration(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse, "hash", 5, 4),
				"decode":  observationWithGeneration(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse, "hash", 5, 5),
			},
			expected:   expectedRevisions(),
			wantReason: workloadconstants.ProgressingReasonRoleInstanceSetUpdated,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "creating from ris outranks role instance set updated",
			rbg:  newProgressingRBG(),
			observations: map[string]risObservation{
				"prefill": observation(workloadconstants.ProgressingReasonRoleInstanceSetUpdated, corev1.ConditionTrue, "hash"),
				"decode":  observation(workloadconstants.ProgressingReasonCreating, corev1.ConditionTrue, "hash"),
			},
			wantReason: workloadconstants.ProgressingReasonCreating,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "missing child without evidence maps to creating",
			rbg:  newProgressingRBG(),
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "prefill", Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2},
				{Name: "decode", Replicas: 0, ReadyReplicas: 0, UpdatedReplicas: 0},
			},
			observations: map[string]risObservation{
				"prefill": observation(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse, "hash"),
				"decode":  {exists: false},
			},
			wantReason: workloadconstants.ProgressingReasonCreating,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "missing child with evidence maps to recovering",
			rbg: func() *workloadsv1alpha2.RoleBasedGroup {
				// ConstructRoleStatus returns a zero-valued entry when the RIS is
				// NotFound; only the persisted counters prove prior establishment.
				rbg := newProgressingRBG()
				rbg.Status.RoleStatuses = []workloadsv1alpha2.RoleStatus{
					{Name: "decode", Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2},
				}
				return rbg
			}(),
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "prefill", Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2},
				{Name: "decode", Replicas: 0, ReadyReplicas: 0, UpdatedReplicas: 0},
			},
			observations: map[string]risObservation{
				"prefill": observation(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse, "hash"),
				"decode":  {exists: false},
			},
			wantReason: workloadconstants.ProgressingReasonRecovering,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name:         "revision mismatch keeps progressing",
			rbg:          newProgressingRBG(),
			roleStatuses: readyRoleStatuses(),
			observations: map[string]risObservation{
				"prefill": observation(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse, "old"),
				"decode":  observation(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse, "hash"),
			},
			expected:   expectedRevisions(),
			wantReason: workloadconstants.ProgressingReasonRoleInstanceSetUpdated,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "coordinated gap stays progressing despite child converged",
			rbg:  newProgressingRBG(),
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "prefill", Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 1},
				{Name: "decode", Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2},
			},
			observations: convergedObservations(),
			expected:     expectedRevisions(),
			wantReason:   workloadconstants.ProgressingReasonRoleInstanceSetUpdated,
			wantStatus:   metav1.ConditionTrue,
		},
		{
			name: "role partition respected for completion",
			rbg:  newProgressingRBG(),
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "prefill", Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2},
				{Name: "decode", Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 1},
			},
			observations: convergedObservations(),
			expected:     expectedRevisions(),
			wantReason:   workloadconstants.ProgressingReasonRoleInstanceSetUpdated,
			wantStatus:   metav1.ConditionTrue,
		},
		{
			name: "roles not ready when no workflow remains",
			rbg:  newProgressingRBG(),
			roleStatuses: []workloadsv1alpha2.RoleStatus{
				{Name: "prefill", Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2},
				{Name: "decode", Replicas: 2, ReadyReplicas: 1, UpdatedReplicas: 2},
			},
			observations: map[string]risObservation{
				"prefill": observation(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse, "hash"),
				"decode":  observation(workloadconstants.ProgressingReasonInstancesNotReady, corev1.ConditionTrue, "hash"),
			},
			wantReason: workloadconstants.ProgressingReasonRolesNotReady,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "all roles paused reports paused",
			rbg:  newProgressingRBG(),
			observations: map[string]risObservation{
				"prefill": observation(workloadconstants.ProgressingReasonPaused, corev1.ConditionFalse, "hash"),
				"decode":  observation(workloadconstants.ProgressingReasonPaused, corev1.ConditionFalse, "hash"),
			},
			wantReason: workloadconstants.ProgressingReasonPaused,
			wantStatus: metav1.ConditionFalse,
		},
		{
			name:         "stale paused condition does not report paused",
			rbg:          newProgressingRBG(),
			roleStatuses: readyRoleStatuses(),
			observations: map[string]risObservation{
				"prefill": observationWithGeneration(workloadconstants.ProgressingReasonPaused, corev1.ConditionFalse, "hash", 5, 4),
				"decode":  observationWithGeneration(workloadconstants.ProgressingReasonPaused, corev1.ConditionFalse, "hash", 5, 5),
			},
			expected:   expectedRevisions(),
			wantReason: workloadconstants.ProgressingReasonRoleInstanceSetUpdated,
			wantStatus: metav1.ConditionTrue,
		},
		{
			name: "paused and converged roles report paused",
			rbg:  newProgressingRBG(),
			observations: map[string]risObservation{
				"prefill": observation(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse, "hash"),
				"decode":  observation(workloadconstants.ProgressingReasonPaused, corev1.ConditionFalse, "hash"),
			},
			wantReason: workloadconstants.ProgressingReasonPaused,
			wantStatus: metav1.ConditionFalse,
		},
		{
			name: "active role outranks paused role",
			rbg:  newProgressingRBG(),
			observations: map[string]risObservation{
				"prefill": observation(workloadconstants.ProgressingReasonScalingUp, corev1.ConditionTrue, "hash"),
				"decode":  observation(workloadconstants.ProgressingReasonPaused, corev1.ConditionFalse, "hash"),
			},
			wantReason: workloadconstants.ProgressingReasonRoleInstanceSetUpdated,
			wantStatus: metav1.ConditionTrue,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeRBGProgressingCondition(tc.rbg, tc.roleStatuses, tc.observations, tc.expected, rbgRuntimeFacts{gangWaiting: tc.gangWaiting, depsNotReady: tc.depsNotReady}, nil, nil)
			if got.Reason != tc.wantReason || got.Status != tc.wantStatus {
				t.Fatalf("got status=%s reason=%s message=%s, want status=%s reason=%s", got.Status, got.Reason, got.Message, tc.wantStatus, tc.wantReason)
			}
			if got.ObservedGeneration != 6 {
				t.Fatalf("observedGeneration = %d", got.ObservedGeneration)
			}
		})
	}
}

func TestEntryBlockerCarryAndSnapshot(t *testing.T) {
	entry := metav1.Condition{
		Type:               string(workloadsv1alpha2.RoleBasedGroupProgressing),
		Status:             metav1.ConditionTrue,
		Reason:             workloadconstants.ProgressingReasonPermissionDenied,
		Message:            "rbac denied",
		ObservedGeneration: 6,
	}

	t.Run("entry blocker is carried when children report no family", func(t *testing.T) {
		rbg := newProgressingRBG()
		// Children are healthy: without the carry, the aggregation would publish an
		// unconfirmed clearing before the role operation is retried.
		observations := map[string]risObservation{
			"prefill": observation(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse, "hash"),
			"decode":  observation(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse, "hash"),
		}
		got := computeRBGProgressingCondition(rbg, readyRoleStatuses(), observations, expectedRevisions(), rbgRuntimeFacts{}, &entry, nil)
		if got.Status != metav1.ConditionTrue || got.Reason != workloadconstants.ProgressingReasonPermissionDenied {
			t.Fatalf("got status=%s reason=%s", got.Status, got.Reason)
		}
		if got.Message != "rbac denied" {
			t.Fatalf("entry message must be preserved, got %q", got.Message)
		}
	})

	t.Run("rbgEntryOwnBlocker snapshots current-generation blockers only", func(t *testing.T) {
		rbg := newProgressingRBG()
		rbg.Status.Conditions = []metav1.Condition{entry}
		if got := rbgEntryOwnBlocker(rbg); got == nil || got.Reason != workloadconstants.ProgressingReasonPermissionDenied {
			t.Fatalf("got %+v", got)
		}
		stale := entry
		stale.ObservedGeneration = rbg.Generation - 1
		rbg.Status.Conditions = []metav1.Condition{stale}
		if got := rbgEntryOwnBlocker(rbg); got != nil {
			t.Fatalf("stale blocker must not be carried, got %+v", got)
		}
		activity := entry
		activity.Reason = workloadconstants.ProgressingReasonRoleInstanceSetUpdated
		rbg.Status.Conditions = []metav1.Condition{activity}
		if got := rbgEntryOwnBlocker(rbg); got != nil {
			t.Fatalf("activity reason must not be carried, got %+v", got)
		}
	})

	t.Run("entry snapshot is preferred over the overwritten current condition", func(t *testing.T) {
		rbg := newProgressingRBG()
		// The normal status path already overwrote the persisted condition with the
		// child aggregation; retention must still find the entry blocker.
		rbg.Status.Conditions = []metav1.Condition{{
			Type:               string(workloadsv1alpha2.RoleBasedGroupProgressing),
			Status:             metav1.ConditionTrue,
			Reason:             workloadconstants.ProgressingReasonRoleInstanceSetUpdated,
			ObservedGeneration: rbg.Generation,
		}}
		reason, message := retainRBGBlockerFamily(rbg, &entry, workloadconstants.ProgressingReasonUnclassifiedBlocker, "server timeout")
		if reason != workloadconstants.ProgressingReasonPermissionDenied {
			t.Fatalf("got %s", reason)
		}
		if !strings.Contains(message, "rbac denied") || !strings.Contains(message, "server timeout") {
			t.Fatalf("message must record both facts, got %s", message)
		}
	})

	t.Run("retention does not grow the message across retries", func(t *testing.T) {
		rbg := newProgressingRBG()
		// The persisted entry already carries the suffix from the previous retry;
		// appending again would grow the message until the API limit rejects it.
		alreadySuffixed := entry
		alreadySuffixed.Message = "rbac denied" + unclassifiedSuffixSeparator + "older timeout"
		reason, message := retainRBGBlockerFamily(rbg, &alreadySuffixed, workloadconstants.ProgressingReasonUnclassifiedBlocker, "new timeout")
		if reason != workloadconstants.ProgressingReasonPermissionDenied {
			t.Fatalf("got %s", reason)
		}
		want := "rbac denied" + unclassifiedSuffixSeparator + "new timeout"
		if message != want {
			t.Fatalf("message must not grow across retries, got %q, want %q", message, want)
		}
	})
}

func TestOwnBlockerCompetesWithFreshChildBlockers(t *testing.T) {
	// A fresh child blocker with a higher-priority family must not be replaced
	// by this controller's own lower-priority operation failure.
	rbg := newProgressingRBG()
	observations := map[string]risObservation{
		"prefill": observation(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse, "hash"),
		"decode":  observation(workloadconstants.ProgressingReasonPermissionDenied, corev1.ConditionTrue, "hash"),
	}
	got := computeRBGProgressingCondition(rbg, readyRoleStatuses(), observations, expectedRevisions(), rbgRuntimeFacts{}, nil,
		&rbgOwnBlocker{reason: workloadconstants.ProgressingReasonWaitingForQuota, message: "exceeded quota"})
	if got.Status != metav1.ConditionTrue || got.Reason != workloadconstants.ProgressingReasonPermissionDenied {
		t.Fatalf("got status=%s reason=%s", got.Status, got.Reason)
	}

	// The own blocker wins when its family has the higher precedence, and its
	// message stays visible.
	got = computeRBGProgressingCondition(rbg, readyRoleStatuses(), observations, expectedRevisions(), rbgRuntimeFacts{}, nil,
		&rbgOwnBlocker{reason: workloadconstants.ProgressingReasonInvalidSpec, message: "invalid excluded ordinals"})
	if got.Status != metav1.ConditionTrue || got.Reason != workloadconstants.ProgressingReasonInvalidSpec ||
		got.Message != "invalid excluded ordinals" {
		t.Fatalf("got status=%s reason=%s message=%s", got.Status, got.Reason, got.Message)
	}

	// Same family: the child roles and the own message are both reported.
	quotaChild := map[string]risObservation{
		"prefill": observation(workloadconstants.ProgressingReasonConverged, corev1.ConditionFalse, "hash"),
		"decode":  observation(workloadconstants.ProgressingReasonWaitingForQuota, corev1.ConditionTrue, "hash"),
	}
	got = computeRBGProgressingCondition(rbg, readyRoleStatuses(), quotaChild, expectedRevisions(), rbgRuntimeFacts{}, nil,
		&rbgOwnBlocker{reason: workloadconstants.ProgressingReasonWaitingForQuota, message: "exceeded quota"})
	if got.Status != metav1.ConditionTrue || got.Reason != workloadconstants.ProgressingReasonWaitingForQuota {
		t.Fatalf("got status=%s reason=%s", got.Status, got.Reason)
	}
	if !strings.Contains(got.Message, "exceeded quota") || !strings.Contains(got.Message, "decode") {
		t.Fatalf("message must describe child roles and the own failure, got %q", got.Message)
	}
}

func TestRecomputedBlockerFamiliesAreNotCarried(t *testing.T) {
	// Restart budgets, gang waiting, and dependencies are re-observed on every
	// reconcile. A persisted entry copy must not outlive the fresh observation.
	entry := metav1.Condition{
		Type:               string(workloadsv1alpha2.RoleBasedGroupProgressing),
		Status:             metav1.ConditionTrue,
		Reason:             workloadconstants.ProgressingReasonWaitingForDependencies,
		Message:            "dependencies for roles b are not ready",
		ObservedGeneration: 6,
	}

	t.Run("fresh observation clears the family", func(t *testing.T) {
		rbg := newProgressingRBG()
		got := computeRBGProgressingCondition(rbg, readyRoleStatuses(), convergedObservations(), expectedRevisions(), rbgRuntimeFacts{}, &entry, nil)
		if got.Status != metav1.ConditionFalse || got.Reason != workloadconstants.ProgressingReasonConverged {
			t.Fatalf("got status=%s reason=%s", got.Status, got.Reason)
		}
	})

	t.Run("entry snapshot is not captured for recomputed families", func(t *testing.T) {
		rbg := newProgressingRBG()
		rbg.Status.Conditions = []metav1.Condition{entry}
		if got := rbgEntryOwnBlocker(rbg); got != nil {
			t.Fatalf("recomputed blocker families must not be carried, got %+v", got)
		}
	})

	t.Run("unclassified errors do not retain recomputed families", func(t *testing.T) {
		rbg := newProgressingRBG()
		rbg.Status.Conditions = []metav1.Condition{entry}
		reason, _ := retainRBGBlockerFamily(rbg, &entry, workloadconstants.ProgressingReasonUnclassifiedBlocker, "operation failed")
		if reason != workloadconstants.ProgressingReasonUnclassifiedBlocker {
			t.Fatalf("recomputed blocker families must not be retained, got %s", reason)
		}
	})

	t.Run("child-derived restart budgets are not carried", func(t *testing.T) {
		rbg := newProgressingRBG()
		restartBudget := entry
		restartBudget.Reason = workloadconstants.ProgressingReasonRestartBudgetExceeded
		restartBudget.Message = "restart budget affects roles prefill"
		rbg.Status.Conditions = []metav1.Condition{restartBudget}
		if got := rbgEntryOwnBlocker(rbg); got != nil {
			t.Fatalf("child-derived restart-budget blocker must not be captured, got %+v", got)
		}
		reason, _ := retainRBGBlockerFamily(rbg, &restartBudget, workloadconstants.ProgressingReasonUnclassifiedBlocker, "operation failed")
		if reason != workloadconstants.ProgressingReasonUnclassifiedBlocker {
			t.Fatalf("child-derived restart-budget blocker must not be retained, got %s", reason)
		}
	})
}

func TestReportRBGProgressingBlockerKeepsRestartBudgetOnObservationError(t *testing.T) {
	ctx := context.Background()
	rbg := newProgressingRBG()
	rbg.Status.Conditions = []metav1.Condition{{
		Type:               string(workloadsv1alpha2.RoleBasedGroupProgressing),
		Status:             metav1.ConditionTrue,
		Reason:             workloadconstants.ProgressingReasonRestartBudgetExceeded,
		Message:            "restart budget affects roles prefill",
		ObservedGeneration: rbg.Generation,
	}}
	scheme := runtime.NewScheme()
	if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := make([]client.Object, 0, 1+len(rbg.Spec.Roles))
	objects = append(objects, rbg)
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		set := &workloadsv1alpha2.RoleInstanceSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace,
			},
		}
		objects = append(objects, set)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(objects...).
		WithObjects(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*workloadsv1alpha2.RoleInstanceSet); ok {
					return stderrors.New("observe RoleInstanceSet failed")
				}
				return cli.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	if err := c.Get(ctx, client.ObjectKey{Name: objects[1].GetName(), Namespace: objects[1].GetNamespace()}, &workloadsv1alpha2.RoleInstanceSet{}); err == nil {
		t.Fatal("expected interceptor to fail RoleInstanceSet Get")
	}
	r := &RoleBasedGroupReconciler{client: c, scheme: scheme}
	if err := r.reportRBGProgressingBlocker(
		ctx, rbg, nil, workloadconstants.ProgressingReasonPermissionDenied, "rbac denied",
	); err != nil {
		t.Fatal(err)
	}
	condition := apimeta.FindStatusCondition(rbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupProgressing))
	if condition == nil || condition.Reason != workloadconstants.ProgressingReasonRestartBudgetExceeded {
		t.Fatalf("observation failure must not downgrade restart budget, got %+v", condition)
	}
	if !strings.Contains(condition.Message, "observation incomplete") {
		t.Fatalf("retained restart budget must record incomplete observation, got %q", condition.Message)
	}
}

func TestObservationIncompleteSuffixDoesNotGrow(t *testing.T) {
	rbg := newProgressingRBG()
	rbg.Status.Conditions = []metav1.Condition{{
		Type:               string(workloadsv1alpha2.RoleBasedGroupProgressing),
		Status:             metav1.ConditionTrue,
		Reason:             workloadconstants.ProgressingReasonRestartBudgetExceeded,
		Message:            "restart budget affects roles prefill",
		ObservedGeneration: rbg.Generation,
	}}
	first := childDerivedBlockerForObservationFailure(rbg, stderrors.New("first timeout"))
	if first == nil {
		t.Fatal("first retention failed")
	}
	rbg.Status.Conditions[0].Message = first.Message
	second := childDerivedBlockerForObservationFailure(rbg, stderrors.New("second timeout"))
	if second == nil {
		t.Fatal("second retention failed")
	}
	if got := strings.Count(second.Message, observationIncompleteSuffixSeparator); got != 1 {
		t.Fatalf("suffix count = %d, want 1; message=%q", got, second.Message)
	}
	if !strings.HasSuffix(second.Message, "second timeout") {
		t.Fatalf("latest observation error must replace the previous one, got %q", second.Message)
	}
}

func TestGangObservationFailureDoesNotRetainClearedRestartBudget(t *testing.T) {
	ctx := context.Background()
	rbg := newProgressingRBG()
	rbg.Status.Conditions = []metav1.Condition{{
		Type:               string(workloadsv1alpha2.RoleBasedGroupProgressing),
		Status:             metav1.ConditionTrue,
		Reason:             workloadconstants.ProgressingReasonRestartBudgetExceeded,
		Message:            "restart budget affects roles prefill",
		ObservedGeneration: rbg.Generation,
	}}
	scheme := runtime.NewScheme()
	if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := make([]client.Object, 0, 1+len(rbg.Spec.Roles))
	objects = append(objects, rbg)
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		set := &workloadsv1alpha2.RoleInstanceSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace, Generation: rbg.Generation,
			},
			Status: workloadsv1alpha2.RoleInstanceSetStatus{
				ObservedGeneration: rbg.Generation,
				Conditions: []workloadsv1alpha2.RoleInstanceSetCondition{{
					Type:               workloadsv1alpha2.RoleInstanceSetProgressing,
					Status:             corev1.ConditionFalse,
					Reason:             workloadconstants.ProgressingReasonConverged,
					ObservedGeneration: rbg.Generation,
				}},
			},
		}
		objects = append(objects, set)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(objects...).
		WithObjects(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*workloadsv1alpha2.CoordinatedPolicy); ok {
					return stderrors.New("coordinated policy read timed out")
				}
				return cli.Get(ctx, key, obj, opts...)
			},
		}).
		Build()
	r := &RoleBasedGroupReconciler{client: c, apiReader: c, scheme: scheme}
	if err := r.reportRBGProgressingBlocker(
		ctx, rbg, nil, workloadconstants.ProgressingReasonPermissionDenied, "rbac denied",
	); err != nil {
		t.Fatal(err)
	}
	condition := apimeta.FindStatusCondition(rbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupProgressing))
	if condition == nil || condition.Reason != workloadconstants.ProgressingReasonPermissionDenied {
		t.Fatalf("gang observation failure must not retain a cleared restart budget, got %+v", condition)
	}
}

func TestPodGroupWaitingPhase(t *testing.T) {
	podGroup := func(phase string, unschedulable bool) *unstructured.Unstructured {
		object := map[string]interface{}{
			"status": map[string]interface{}{"phase": phase},
		}
		if unschedulable {
			object["status"].(map[string]interface{})["conditions"] = []interface{}{
				map[string]interface{}{
					"type":   "Unschedulable",
					"status": "True",
					"reason": "NotEnoughResources",
				},
			}
		}
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"status": object["status"],
		}}
	}
	// The Unschedulable condition is the reliable waiting signal: Volcano can
	// hold the group in Inqueue with an Unschedulable condition while the gang
	// cannot be filled.
	if !podGroupWaiting(podGroup("Inqueue", true)) {
		t.Fatal("Inqueue with an Unschedulable condition must be a waiting fact")
	}
	// Pending waits for admission and resources; Unknown means the gang is
	// partially placed and the rest cannot be scheduled.
	for _, phase := range []string{"Pending", "Unknown"} {
		if !podGroupWaiting(podGroup(phase, false)) {
			t.Fatalf("phase %s must be a waiting fact", phase)
		}
	}
	// Inqueue without the condition already allows pod creation, Scheduling
	// already placed more than minMember pods, and the terminal phases are not
	// gang waiting.
	for _, phase := range []string{"", "Inqueue", "Scheduling", "Running", "Completed", "Finished", "Failed"} {
		if podGroupWaiting(podGroup(phase, false)) {
			t.Fatalf("phase %q must not be a waiting fact", phase)
		}
	}

	// Volcano never clears a written Unschedulable condition: once the gang fits,
	// the plugin writes Scheduled=True instead and its own jobStatus only trusts
	// Unschedulable while the transitionID matches the current session. A
	// Running group is therefore not waiting, whatever conditions it carries.
	running := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"phase": "Running",
			"conditions": []interface{}{
				map[string]interface{}{"type": "Unschedulable", "status": "True", "reason": "NotEnoughResources"},
				map[string]interface{}{"type": "Scheduled", "status": "True"},
			},
		},
	}}
	if podGroupWaiting(running) {
		t.Fatal("a Running PodGroup with a leftover Unschedulable condition must not be a waiting fact")
	}
}

func TestPodGroupControlledByRBG(t *testing.T) {
	rbg := newProgressingRBG()
	rbg.UID = "rbg-uid"
	podGroup := func(controller bool, uid string) *unstructured.Unstructured {
		reference := map[string]interface{}{
			"controller": controller,
			"uid":        uid,
			"kind":       "RoleBasedGroup",
		}
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"metadata": map[string]interface{}{
				"ownerReferences": []interface{}{reference},
			},
		}}
	}
	if !podGroupControlledByRBG(podGroup(true, string(rbg.UID)), rbg) {
		t.Fatal("controller reference matching the RBG UID must be owned")
	}
	if podGroupControlledByRBG(podGroup(true, "other-uid"), rbg) {
		t.Fatal("a controller reference for another owner must not be owned")
	}
	if podGroupControlledByRBG(podGroup(false, string(rbg.UID)), rbg) {
		t.Fatal("a non-controller reference must not be owned")
	}
	orphan := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{},
	}}
	if podGroupControlledByRBG(orphan, rbg) {
		t.Fatal("a PodGroup without references must not be owned")
	}
}

func TestUnreadyDependencies(t *testing.T) {
	ctx := context.Background()
	rbg := newProgressingRBG()
	rbg.Spec.Roles[1].Dependencies = []string{"prefill"}

	scheme := runtime.NewScheme()
	if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	seedRIS := func(specReplicas, readyReplicas int32) client.Client {
		set := &workloadsv1alpha2.RoleInstanceSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
				Namespace: rbg.Namespace,
				Labels:    map[string]string{workloadconstants.GroupNameLabelKey: rbg.Name},
			},
			Spec:   workloadsv1alpha2.RoleInstanceSetSpec{Replicas: ptr.To(specReplicas)},
			Status: workloadsv1alpha2.RoleInstanceSetStatus{ReadyReplicas: readyReplicas},
		}
		return fake.NewClientBuilder().WithScheme(scheme).WithObjects(set).Build()
	}
	newReconciler := func(c client.Client) *RoleBasedGroupReconciler {
		return &RoleBasedGroupReconciler{client: c, scheme: scheme, workloadReconciler: map[string]reconciler.WorkloadReconciler{}}
	}

	// The dependency gate observes the dispatched child replicas, not the RBG's
	// final target: a coordinated step of 1/1 must not report the dependency as
	// blocked while the final target is three.
	ready, readyErr := newReconciler(seedRIS(1, 1)).unreadyDependencies(ctx, rbg)
	if len(ready) != 0 {
		t.Fatalf("dispatched dependency replicas must satisfy the gate, got %v", ready)
	}
	if readyErr != nil {
		t.Fatalf("unexpected observation error: %v", readyErr)
	}

	notReady, notReadyErr := newReconciler(seedRIS(1, 0)).unreadyDependencies(ctx, rbg)
	if len(notReady) != 1 || notReady[0] != "decode" {
		t.Fatalf("got %v", notReady)
	}
	if notReadyErr != nil {
		t.Fatalf("unexpected observation error: %v", notReadyErr)
	}

	// A missing dependency child is a fact: the dependency is not ready.
	missing, missingErr := newReconciler(fake.NewClientBuilder().WithScheme(scheme).Build()).unreadyDependencies(ctx, rbg)
	if len(missing) != 1 || missing[0] != "decode" {
		t.Fatalf("missing dependency child must be not ready, got %v", missing)
	}
	if missingErr != nil {
		t.Fatalf("unexpected observation error: %v", missingErr)
	}
}

func TestGangObservationReadErrorReportsIncomplete(t *testing.T) {
	ctx := context.Background()
	rbg := newProgressingRBG()
	rbg.Annotations = map[string]string{workloadconstants.GangSchedulingAnnotationKey: "true"}

	scheme := runtime.NewScheme()
	if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	podGroupReadErr := apierrors.NewForbidden(
		schema.GroupResource{Group: "scheduling.volcano.sh", Resource: "podgroups"},
		rbg.Name, stderrors.New("podgroups is forbidden"))
	readErrClient := func(t *testing.T, podGroupErr error) client.Client {
		t.Helper()
		return fake.NewClientBuilder().WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, cc client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if unstructuredObj, ok := obj.(*unstructured.Unstructured); ok && unstructuredObj.GetKind() == "PodGroup" {
						return podGroupErr
					}
					return cc.Get(ctx, key, obj, opts...)
				},
			}).Build()
	}

	t.Run("read failure reports observation incomplete", func(t *testing.T) {
		c := readErrClient(t, podGroupReadErr)
		r := &RoleBasedGroupReconciler{client: c, apiReader: c, scheme: scheme, workloadReconciler: map[string]reconciler.WorkloadReconciler{}}
		r.updateRBGProgressingCondition(ctx, rbg, readyRoleStatuses(), expectedRevisions(), r.observeRuntimeFacts(ctx, rbg), nil)
		condition := apimeta.FindStatusCondition(rbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupProgressing))
		if condition == nil || condition.Status != metav1.ConditionUnknown || condition.Reason != "ObservationIncomplete" {
			t.Fatalf("a failed PodGroup read must report Unknown/ObservationIncomplete, got %+v", condition)
		}
		if !strings.Contains(condition.Message, "podgroups is forbidden") {
			t.Fatalf("message must describe the observation error, got %q", condition.Message)
		}
	})

	t.Run("missing pod group is a fact, not an error", func(t *testing.T) {
		c := readErrClient(t, apierrors.NewNotFound(schema.GroupResource{Group: "scheduling.volcano.sh", Resource: "podgroups"}, rbg.Name))
		r := &RoleBasedGroupReconciler{client: c, apiReader: c, scheme: scheme, workloadReconciler: map[string]reconciler.WorkloadReconciler{}}
		gangWaiting, err := r.observeGangWaiting(ctx, rbg)
		if err != nil {
			t.Fatalf("an absent PodGroup must not be an observation error: %v", err)
		}
		if gangWaiting {
			t.Fatal("an absent PodGroup must not be a waiting fact")
		}
	})

	t.Run("gang strategy read failure is an observation error", func(t *testing.T) {
		// The gang strategy resolves through CoordinatedPolicy; a read failure
		// cannot determine whether gang scheduling applies at all and must not
		// become a definite not-waiting fact while the reconcile later aborts.
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, cc client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*workloadsv1alpha2.CoordinatedPolicy); ok {
						return apierrors.NewForbidden(
							schema.GroupResource{Group: "workloads.x-k8s.io", Resource: "coordinatedpolicies"},
							key.Name, stderrors.New("coordinatedpolicies is forbidden"))
					}
					return cc.Get(ctx, key, obj, opts...)
				},
			}).Build()
		r := &RoleBasedGroupReconciler{client: c, apiReader: c, scheme: scheme, workloadReconciler: map[string]reconciler.WorkloadReconciler{}}
		if _, err := r.observeGangWaiting(ctx, rbg); err == nil {
			t.Fatal("a CoordinatedPolicy read failure must be reported as an observation error")
		}
	})
}

func TestRetainRBGBlockerFamily(t *testing.T) {
	progressing := func(reason string, observedGeneration int64) metav1.Condition {
		return metav1.Condition{
			Type:               string(workloadsv1alpha2.RoleBasedGroupProgressing),
			Status:             metav1.ConditionTrue,
			Reason:             reason,
			ObservedGeneration: observedGeneration,
			Message:            "rbac denied",
		}
	}

	t.Run("unclassified error retains current-generation family", func(t *testing.T) {
		rbg := newProgressingRBG()
		rbg.Status.Conditions = []metav1.Condition{
			progressing(workloadconstants.ProgressingReasonPermissionDenied, rbg.Generation),
		}
		reason, message := retainRBGBlockerFamily(rbg, nil, workloadconstants.ProgressingReasonUnclassifiedBlocker, "server timeout")
		if reason != workloadconstants.ProgressingReasonPermissionDenied {
			t.Fatalf("got %s", reason)
		}
		if !strings.Contains(message, "rbac denied") || !strings.Contains(message, "server timeout") {
			t.Fatalf("message must record both facts, got %s", message)
		}
	})

	t.Run("stale generation and activity reasons are not retained", func(t *testing.T) {
		rbg := newProgressingRBG()
		rbg.Status.Conditions = []metav1.Condition{
			progressing(workloadconstants.ProgressingReasonPermissionDenied, rbg.Generation-1),
		}
		if reason, _ := retainRBGBlockerFamily(rbg, nil, workloadconstants.ProgressingReasonUnclassifiedBlocker, "timeout"); reason != workloadconstants.ProgressingReasonUnclassifiedBlocker {
			t.Fatalf("stale blocker must not be retained, got %s", reason)
		}
		rbg.Status.Conditions = []metav1.Condition{
			progressing(workloadconstants.ProgressingReasonRoleInstanceSetUpdated, rbg.Generation),
		}
		if reason, _ := retainRBGBlockerFamily(rbg, nil, workloadconstants.ProgressingReasonUnclassifiedBlocker, "timeout"); reason != workloadconstants.ProgressingReasonUnclassifiedBlocker {
			t.Fatalf("activity reason must not be retained, got %s", reason)
		}
	})

	t.Run("specific error replaces prior family", func(t *testing.T) {
		rbg := newProgressingRBG()
		rbg.Status.Conditions = []metav1.Condition{
			progressing(workloadconstants.ProgressingReasonPermissionDenied, rbg.Generation),
		}
		if reason, _ := retainRBGBlockerFamily(rbg, nil, workloadconstants.ProgressingReasonWaitingForQuota, "exceeded quota"); reason != workloadconstants.ProgressingReasonWaitingForQuota {
			t.Fatalf("specific error must win, got %s", reason)
		}
	})
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

func TestClassifyGangIncompatibility(t *testing.T) {
	cases := []struct {
		message string
		want    string
	}{
		{"the installed Volcano version does not support this feature", workloadconstants.ProgressingReasonSchedulerUnsupported},
		{"unsupported subGroupPolicy", workloadconstants.ProgressingReasonSchedulerUnsupported},
		{"gang minReplicas above replicas", workloadconstants.ProgressingReasonIncompatibleGangConfig},
	}
	for _, tc := range cases {
		if got := classifyGangIncompatibility(stderrors.New(tc.message)); got != tc.want {
			t.Fatalf("classifyGangIncompatibility(%q) = %s, want %s", tc.message, got, tc.want)
		}
	}
}
