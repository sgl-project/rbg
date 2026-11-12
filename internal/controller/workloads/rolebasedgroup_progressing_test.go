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
	stderrors "errors"
	"strings"
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
			got := computeRBGProgressingCondition(tc.rbg, tc.roleStatuses, tc.observations, tc.expected, tc.gangWaiting, tc.depsNotReady)
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
		reason, message := retainRBGBlockerFamily(rbg, workloadconstants.ProgressingReasonUnclassifiedBlocker, "server timeout")
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
		if reason, _ := retainRBGBlockerFamily(rbg, workloadconstants.ProgressingReasonUnclassifiedBlocker, "timeout"); reason != workloadconstants.ProgressingReasonUnclassifiedBlocker {
			t.Fatalf("stale blocker must not be retained, got %s", reason)
		}
		rbg.Status.Conditions = []metav1.Condition{
			progressing(workloadconstants.ProgressingReasonRoleInstanceSetUpdated, rbg.Generation),
		}
		if reason, _ := retainRBGBlockerFamily(rbg, workloadconstants.ProgressingReasonUnclassifiedBlocker, "timeout"); reason != workloadconstants.ProgressingReasonUnclassifiedBlocker {
			t.Fatalf("activity reason must not be retained, got %s", reason)
		}
	})

	t.Run("specific error replaces prior family", func(t *testing.T) {
		rbg := newProgressingRBG()
		rbg.Status.Conditions = []metav1.Condition{
			progressing(workloadconstants.ProgressingReasonPermissionDenied, rbg.Generation),
		}
		if reason, _ := retainRBGBlockerFamily(rbg, workloadconstants.ProgressingReasonWaitingForQuota, "exceeded quota"); reason != workloadconstants.ProgressingReasonWaitingForQuota {
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
