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

package roleinstance

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestNextRoleInstancePhase(t *testing.T) {
	complete := roleInstancePhaseFacts{podsPresent: true, readySatisfied: true}
	incomplete := roleInstancePhaseFacts{}
	updating := roleInstancePhaseFacts{updateActive: true}
	updatingComplete := roleInstancePhaseFacts{updateActive: false, podsPresent: true, readySatisfied: true}

	cases := []struct {
		name    string
		current workloadsv1alpha2.RoleInstancePhase
		facts   roleInstancePhaseFacts
		want    workloadsv1alpha2.RoleInstancePhase
	}{
		{"creation completes", workloadsv1alpha2.RoleInstancePhaseCreating, complete, workloadsv1alpha2.RoleInstancePhaseRunning},
		{"creation incomplete stays creating", workloadsv1alpha2.RoleInstancePhaseCreating, incomplete, workloadsv1alpha2.RoleInstancePhaseCreating},
		// Target changes during Create convergence fold into Create: no direct edge to Updating/Recovering.
		{"target change during creating folds", workloadsv1alpha2.RoleInstancePhaseCreating, updating, workloadsv1alpha2.RoleInstancePhaseCreating},
		// Pods left over from a superseded target must not let Creating exit to
		// Running before the current target has taken effect.
		{"target change during creating with superseded pods stays creating", workloadsv1alpha2.RoleInstancePhaseCreating,
			roleInstancePhaseFacts{updateActive: true, podsPresent: true, readySatisfied: true},
			workloadsv1alpha2.RoleInstancePhaseCreating},
		{"absent phase with pending update initializes updating", "",
			roleInstancePhaseFacts{updateActive: true, podsPresent: true, readySatisfied: true},
			workloadsv1alpha2.RoleInstancePhaseUpdating},
		{"absent established phase with missing pods initializes recovering", "",
			roleInstancePhaseFacts{previouslyEstablished: true, podsPresent: false},
			workloadsv1alpha2.RoleInstancePhaseRecovering},
		{"absent established phase stays running on readiness drop", "",
			roleInstancePhaseFacts{previouslyEstablished: true, podsPresent: true, readySatisfied: false},
			workloadsv1alpha2.RoleInstancePhaseRunning},
		{"absent and incomplete enters creating", "", incomplete, workloadsv1alpha2.RoleInstancePhaseCreating},
		{"absent and complete enters running", "", complete, workloadsv1alpha2.RoleInstancePhaseRunning},
		{"running enters updating", workloadsv1alpha2.RoleInstancePhaseRunning, updating, workloadsv1alpha2.RoleInstancePhaseUpdating},
		{"running recovers missing pods", workloadsv1alpha2.RoleInstancePhaseRunning, roleInstancePhaseFacts{podsPresent: false}, workloadsv1alpha2.RoleInstancePhaseRecovering},
		{"running stays when only readiness drops", workloadsv1alpha2.RoleInstancePhaseRunning, roleInstancePhaseFacts{podsPresent: true, readySatisfied: false}, workloadsv1alpha2.RoleInstancePhaseRunning},
		{"updating completes", workloadsv1alpha2.RoleInstancePhaseUpdating, updatingComplete, workloadsv1alpha2.RoleInstancePhaseRunning},
		{"updating continues", workloadsv1alpha2.RoleInstancePhaseUpdating, updating, workloadsv1alpha2.RoleInstancePhaseUpdating},
		{"recovery completes", workloadsv1alpha2.RoleInstancePhaseRecovering, complete, workloadsv1alpha2.RoleInstancePhaseRunning},
		{"update interrupts recovery", workloadsv1alpha2.RoleInstancePhaseRecovering, updating, workloadsv1alpha2.RoleInstancePhaseUpdating},
		{"unknown phase completes conservatively", "Weird", complete, workloadsv1alpha2.RoleInstancePhaseRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextRoleInstancePhase(tc.current, tc.facts)
			if got != tc.want {
				t.Fatalf("nextRoleInstancePhase(%q, %+v) = %q, want %q", tc.current, tc.facts, got, tc.want)
			}
		})
	}
}

func TestClassifyPodOperationError(t *testing.T) {
	forbiddenQuota := apierrors.NewForbidden(
		corev1.Resource("pods"), "ri-0", apierrors.NewBadRequest("pods \"ri-0\" is forbidden: exceeded quota"))
	forbiddenRBAC := apierrors.NewForbidden(
		corev1.Resource("pods"), "ri-0", apierrors.NewBadRequest("pods \"ri-0\" is forbidden: user cannot create"))
	admission := &apierrors.StatusError{ErrStatus: metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    422,
		Reason:  metav1.StatusReasonInvalid,
		Message: "admission webhook \"example\" denied the request",
	}}

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"quota create", forbiddenQuota, workloadsv1alpha2.ReasonPodCreateQuotaExceeded},
		{"rbac create", forbiddenRBAC, workloadsv1alpha2.ReasonPodCreateForbidden},
		{"admission", admission, workloadsv1alpha2.ReasonPodAdmissionRejected},
		{"unclassified", apierrors.NewServerTimeout(corev1.Resource("pods"), "POST", 1), workloadsv1alpha2.ReasonPodOperationUnclassified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyPodOperationError("Create", tc.err)
			if got != tc.want {
				t.Fatalf("classifyPodOperationError = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestComputeInstanceProgressingCondition(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Generation: 3},
	}

	t.Run("blocker outranks activity", func(t *testing.T) {
		conditions := []workloadsv1alpha2.RoleInstanceCondition{{
			Type:    workloadsv1alpha2.RoleInstanceProgressing,
			Status:  corev1.ConditionTrue,
			Reason:  workloadsv1alpha2.ReasonPodCreateQuotaExceeded,
			Message: "exceeded quota",
		}}
		got := computeInstanceProgressingCondition(instance, workloadsv1alpha2.RoleInstancePhaseUpdating, true, conditions, "")
		if got.Status != corev1.ConditionTrue || got.Reason != workloadsv1alpha2.ReasonPodCreateQuotaExceeded {
			t.Fatalf("got %+v", got)
		}
		if got.ObservedGeneration != 3 {
			t.Fatalf("observedGeneration = %d", got.ObservedGeneration)
		}
	})

	t.Run("activity from phase", func(t *testing.T) {
		got := computeInstanceProgressingCondition(instance, workloadsv1alpha2.RoleInstancePhaseRecovering, false, nil, "")
		if got.Reason != workloadsv1alpha2.ReasonInstanceRecovering {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("converged", func(t *testing.T) {
		got := computeInstanceProgressingCondition(instance, workloadsv1alpha2.RoleInstancePhaseRunning, true, nil, "")
		if got.Status != corev1.ConditionFalse || got.Reason != workloadsv1alpha2.ReasonInstanceConverged {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("pods not ready without workflow", func(t *testing.T) {
		got := computeInstanceProgressingCondition(instance, workloadsv1alpha2.RoleInstancePhaseRunning, false, nil, "")
		if got.Status != corev1.ConditionTrue || got.Reason != workloadsv1alpha2.ReasonInstancePodsNotReady {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("unschedulable pods report insufficient resources", func(t *testing.T) {
		got := computeInstanceProgressingCondition(instance, workloadsv1alpha2.RoleInstancePhaseCreating, false, nil,
			"Pod ri-0 is unschedulable: 3 Insufficient cpu")
		if got.Status != corev1.ConditionTrue || got.Reason != workloadsv1alpha2.ReasonInstanceInsufficientResources {
			t.Fatalf("got %+v", got)
		}
		if got.Message != "Pod ri-0 is unschedulable: 3 Insufficient cpu" {
			t.Fatalf("message = %q", got.Message)
		}
		if got.ObservedGeneration != 3 {
			t.Fatalf("observedGeneration = %d", got.ObservedGeneration)
		}
	})

	t.Run("specific blocker outranks insufficient resources", func(t *testing.T) {
		conditions := []workloadsv1alpha2.RoleInstanceCondition{{
			Type:    workloadsv1alpha2.RoleInstanceProgressing,
			Status:  corev1.ConditionTrue,
			Reason:  workloadsv1alpha2.ReasonPodCreateQuotaExceeded,
			Message: "exceeded quota",
		}}
		got := computeInstanceProgressingCondition(instance, workloadsv1alpha2.RoleInstancePhaseCreating, false, conditions,
			"Pod ri-0 is unschedulable")
		if got.Status != corev1.ConditionTrue || got.Reason != workloadsv1alpha2.ReasonPodCreateQuotaExceeded {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("unclassified error does not replace insufficient resources", func(t *testing.T) {
		conditions := []workloadsv1alpha2.RoleInstanceCondition{{
			Type:    workloadsv1alpha2.RoleInstanceProgressing,
			Status:  corev1.ConditionTrue,
			Reason:  workloadsv1alpha2.ReasonPodOperationUnclassified,
			Message: "boom",
		}}
		got := computeInstanceProgressingCondition(instance, workloadsv1alpha2.RoleInstancePhaseCreating, false, conditions,
			"Pod ri-0 is unschedulable")
		if got.Status != corev1.ConditionTrue || got.Reason != workloadsv1alpha2.ReasonInstanceInsufficientResources {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestStableBlockerReason(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Generation: 2},
	}
	progressing := func(reason string, observedGeneration int64) []workloadsv1alpha2.RoleInstanceCondition {
		return []workloadsv1alpha2.RoleInstanceCondition{{
			Type:               workloadsv1alpha2.RoleInstanceProgressing,
			Status:             corev1.ConditionTrue,
			Reason:             reason,
			ObservedGeneration: observedGeneration,
		}}
	}
	r := &reconciler{}

	// A confirmed blocker family for the current target is retained.
	instance.Status.Conditions = progressing(workloadsv1alpha2.ReasonPodCreateQuotaExceeded, 2)
	if got := r.stableBlockerReason(instance, workloadsv1alpha2.ReasonPodOperationUnclassified); got != workloadsv1alpha2.ReasonPodCreateQuotaExceeded {
		t.Fatalf("confirmed blocker family must be retained, got %s", got)
	}

	// Activity reasons are not blockers and must not replace an unclassified error.
	instance.Status.Conditions = progressing(workloadsv1alpha2.ReasonInstanceUpdating, 2)
	if got := r.stableBlockerReason(instance, workloadsv1alpha2.ReasonPodOperationUnclassified); got != workloadsv1alpha2.ReasonPodOperationUnclassified {
		t.Fatalf("activity reason must not be retained, got %s", got)
	}

	// A child-readiness reason is not a blocker either.
	instance.Status.Conditions = progressing(workloadsv1alpha2.ReasonInstancePodsNotReady, 2)
	if got := r.stableBlockerReason(instance, workloadsv1alpha2.ReasonPodOperationUnclassified); got != workloadsv1alpha2.ReasonPodOperationUnclassified {
		t.Fatalf("child-readiness reason must not be retained, got %s", got)
	}

	// A stale condition does not describe the current target.
	instance.Status.Conditions = progressing(workloadsv1alpha2.ReasonPodCreateQuotaExceeded, 1)
	if got := r.stableBlockerReason(instance, workloadsv1alpha2.ReasonPodOperationUnclassified); got != workloadsv1alpha2.ReasonPodOperationUnclassified {
		t.Fatalf("stale blocker must not be retained, got %s", got)
	}

	// A specific new error is not rewritten.
	instance.Status.Conditions = progressing(workloadsv1alpha2.ReasonPodCreateQuotaExceeded, 2)
	if got := r.stableBlockerReason(instance, workloadsv1alpha2.ReasonPodCreateForbidden); got != workloadsv1alpha2.ReasonPodCreateForbidden {
		t.Fatalf("specific new error must win, got %s", got)
	}
}

func TestInsufficientResourcesMessage(t *testing.T) {
	unschedulable := func(name, message string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  corev1.PodReasonUnschedulable,
				Message: message,
			}}},
		}
	}

	t.Run("no pods", func(t *testing.T) {
		if got := insufficientResourcesMessage(nil); got != "" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("pending pod without unschedulable condition is not a blocker", func(t *testing.T) {
		pending := &corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionFalse,
		}}}}
		if got := insufficientResourcesMessage([]*corev1.Pod{pending}); got != "" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("unschedulable pods are described", func(t *testing.T) {
		pods := []*corev1.Pod{
			unschedulable("ri-0", "0/2 nodes are available: 2 Insufficient cpu."),
			unschedulable("ri-1", ""),
		}
		want := "Pod ri-0 is unschedulable: 0/2 nodes are available: 2 Insufficient cpu.; Pod ri-1 is unschedulable"
		if got := insufficientResourcesMessage(pods); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}
