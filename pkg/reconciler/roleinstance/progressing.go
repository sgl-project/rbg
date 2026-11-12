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
	"strings"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// roleInstancePhaseFacts carries the controller-observed facts used by the RI phase
// state machine. Phase transitions start from the last persisted phase; external
// readiness gates are deliberately absent because they gate externally observed
// readiness, not phase completion.
type roleInstancePhaseFacts struct {
	// updateActive reports that an update target is being executed, including rolling
	// replacement and in-place updates. It is revision-based.
	updateActive bool

	// podsPresent reports that all Pods required by the current target exist.
	podsPresent bool

	// readySatisfied reports controller-observed readiness per readyPolicy.
	readySatisfied bool
}

// nextRoleInstancePhase implements the allowed phase edges from KEP-502:
//
//   - no phase returns to Creating; Creating exits only to Running or Terminating,
//     and target changes during Create convergence fold into Create convergence;
//   - Running enters Updating when an update target is identified, or Recovering
//     when required Pods are independently recreated outside an update;
//   - Updating and Recovering return to Running only after the work and
//     controller-observed readiness complete;
//   - an update target identified while recovery continues moves to Updating;
//   - deletion wins from every phase (recorded before the reconciler skips
//     deleted objects).
func nextRoleInstancePhase(current workloadsv1alpha2.RoleInstancePhase, facts roleInstancePhaseFacts) workloadsv1alpha2.RoleInstancePhase {
	switch current {
	case workloadsv1alpha2.RoleInstancePhaseTerminating:
		return workloadsv1alpha2.RoleInstancePhaseTerminating
	case workloadsv1alpha2.RoleInstancePhaseCreating, "":
		if facts.podsPresent && facts.readySatisfied {
			return workloadsv1alpha2.RoleInstancePhaseRunning
		}
		return workloadsv1alpha2.RoleInstancePhaseCreating
	case workloadsv1alpha2.RoleInstancePhaseRunning:
		if facts.updateActive {
			return workloadsv1alpha2.RoleInstancePhaseUpdating
		}
		if !facts.podsPresent {
			return workloadsv1alpha2.RoleInstancePhaseRecovering
		}
		return workloadsv1alpha2.RoleInstancePhaseRunning
	case workloadsv1alpha2.RoleInstancePhaseUpdating:
		if !facts.updateActive && facts.podsPresent && facts.readySatisfied {
			return workloadsv1alpha2.RoleInstancePhaseRunning
		}
		return workloadsv1alpha2.RoleInstancePhaseUpdating
	case workloadsv1alpha2.RoleInstancePhaseRecovering:
		if facts.updateActive {
			return workloadsv1alpha2.RoleInstancePhaseUpdating
		}
		if facts.podsPresent && facts.readySatisfied {
			return workloadsv1alpha2.RoleInstancePhaseRunning
		}
		return workloadsv1alpha2.RoleInstancePhaseRecovering
	default:
		// Unknown persisted values are treated conservatively: never fabricate
		// initial creation, and let completion re-establish the phase.
		if facts.podsPresent && facts.readySatisfied {
			return workloadsv1alpha2.RoleInstancePhaseRunning
		}
		return current
	}
}

// classifyPodOperationError maps a Pod operation error to a stable RI Progressing
// reason. Kubernetes commonly returns 403 for quota, RBAC, and admission denials,
// so classification uses error details rather than the status code alone. Errors
// whose family cannot be determined use PodOperationUnclassified.
func classifyPodOperationError(operation string, err error) string {
	if err == nil {
		return ""
	}
	switch {
	case apierrors.IsForbidden(err):
		if isQuotaExceededError(err) {
			return "Pod" + operation + "QuotaExceeded"
		}
		return "Pod" + operation + "Forbidden"
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		// Admission rejections and invalid object errors are surfaced by the API
		// server as Invalid/BadRequest with the rejecting plugin in the message.
		if mentionsAdmissionWebhook(err) {
			return workloadsv1alpha2.ReasonPodAdmissionRejected
		}
		return workloadsv1alpha2.ReasonPodOperationUnclassified
	default:
		return workloadsv1alpha2.ReasonPodOperationUnclassified
	}
}

func isQuotaExceededError(err error) bool {
	if err == nil {
		return false
	}
	// Quota rejections are Forbidden with an "exceeded quota" detail, which is not
	// distinguishable through a typed helper.
	return strings.Contains(err.Error(), "exceeded quota")
}

func mentionsAdmissionWebhook(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "admission webhook") ||
		strings.Contains(msg, "admission plugin")
}

// instanceProgressingBlockerReason selects the RI Progressing reason for the current
// blocker from the failure conditions recorded by this reconcile. A confirmed blocker
// always outranks activity reasons.
func instanceProgressingBlockerReason(conditions []workloadsv1alpha2.RoleInstanceCondition) string {
	for _, condition := range conditions {
		switch condition.Type {
		case workloadsv1alpha2.RoleInstanceFailedScale, workloadsv1alpha2.RoleInstanceFailedUpdate:
			if condition.Status == v1.ConditionTrue && condition.Reason != "" {
				return condition.Reason
			}
		}
	}
	return ""
}

// instanceProgressingActivityReason selects the activity reason from the phase.
func instanceProgressingActivityReason(phase workloadsv1alpha2.RoleInstancePhase) string {
	switch phase {
	case workloadsv1alpha2.RoleInstancePhaseCreating:
		return workloadsv1alpha2.ReasonInstanceCreating
	case workloadsv1alpha2.RoleInstancePhaseUpdating:
		return workloadsv1alpha2.ReasonInstanceUpdating
	case workloadsv1alpha2.RoleInstancePhaseRecovering:
		return workloadsv1alpha2.ReasonInstanceRecovering
	default:
		return ""
	}
}

// computeInstanceProgressingCondition builds the RI Progressing condition. Phase and
// the blocker reason stay independent: for example a quota-blocked Pod recreation
// keeps Progressing=True with the blocker reason while the phase is Updating.
func computeInstanceProgressingCondition(instance *workloadsv1alpha2.RoleInstance, phase workloadsv1alpha2.RoleInstancePhase,
	instanceReady bool, newInnerConditions []workloadsv1alpha2.RoleInstanceCondition) workloadsv1alpha2.RoleInstanceCondition {
	progressing := workloadsv1alpha2.RoleInstanceCondition{
		Type:               workloadsv1alpha2.RoleInstanceProgressing,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: instance.Generation,
	}

	if reason := instanceProgressingBlockerReason(newInnerConditions); reason != "" {
		progressing.Reason = reason
		progressing.Message = blockerMessage(newInnerConditions, reason)
		return progressing
	}
	if reason := instanceProgressingActivityReason(phase); reason != "" {
		progressing.Reason = reason
		return progressing
	}
	if !instanceReady {
		progressing.Reason = workloadsv1alpha2.ReasonInstancePodsNotReady
		return progressing
	}
	progressing.Status = v1.ConditionFalse
	progressing.Reason = workloadsv1alpha2.ReasonInstanceConverged
	return progressing
}

func blockerMessage(conditions []workloadsv1alpha2.RoleInstanceCondition, reason string) string {
	for _, condition := range conditions {
		if (condition.Type == workloadsv1alpha2.RoleInstanceFailedScale || condition.Type == workloadsv1alpha2.RoleInstanceFailedUpdate) &&
			condition.Status == v1.ConditionTrue && condition.Reason == reason {
			return condition.Message
		}
	}
	return ""
}

// preserveConditionTransitionTime copies LastTransitionTime from a live condition when
// its status is unchanged, so reason, message, and observedGeneration changes do not
// reset availability timing.
func preserveConditionTransitionTime(live []workloadsv1alpha2.RoleInstanceCondition, conditionType workloadsv1alpha2.RoleInstanceConditionType,
	status v1.ConditionStatus) metav1.Time {
	for _, condition := range live {
		if condition.Type == conditionType && condition.Status == status {
			return condition.LastTransitionTime
		}
	}
	return metav1.Now()
}
