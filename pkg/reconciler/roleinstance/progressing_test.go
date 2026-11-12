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
		got := computeInstanceProgressingCondition(instance, workloadsv1alpha2.RoleInstancePhaseUpdating, true, conditions)
		if got.Status != corev1.ConditionTrue || got.Reason != workloadsv1alpha2.ReasonPodCreateQuotaExceeded {
			t.Fatalf("got %+v", got)
		}
		if got.ObservedGeneration != 3 {
			t.Fatalf("observedGeneration = %d", got.ObservedGeneration)
		}
	})

	t.Run("activity from phase", func(t *testing.T) {
		got := computeInstanceProgressingCondition(instance, workloadsv1alpha2.RoleInstancePhaseRecovering, false, nil)
		if got.Reason != workloadsv1alpha2.ReasonInstanceRecovering {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("converged", func(t *testing.T) {
		got := computeInstanceProgressingCondition(instance, workloadsv1alpha2.RoleInstancePhaseRunning, true, nil)
		if got.Status != corev1.ConditionFalse || got.Reason != workloadsv1alpha2.ReasonInstanceConverged {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("pods not ready without workflow", func(t *testing.T) {
		got := computeInstanceProgressingCondition(instance, workloadsv1alpha2.RoleInstancePhaseRunning, false, nil)
		if got.Status != corev1.ConditionTrue || got.Reason != workloadsv1alpha2.ReasonInstancePodsNotReady {
			t.Fatalf("got %+v", got)
		}
	})
}
