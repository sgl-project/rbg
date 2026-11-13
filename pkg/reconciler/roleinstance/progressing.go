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
	"fmt"
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

	// previouslyEstablished reports that the instance previously confirmed
	// completion of an earlier target (the LastReadyGeneration latch). It only
	// matters when the persisted phase is absent, to initialize an upgraded object
	// from confirmed workflow facts instead of defaulting to initial creation.
	previouslyEstablished bool
}

// instanceBlockerReasons are the RI Progressing reasons that report a confirmed
// current-blocker family. Activity reasons (Creating, Updating, Recovering) and the
// child-readiness reason (PodsNotReady) are not blockers and must never be retained in
// place of a newly unclassifiable error.
var instanceBlockerReasons = map[string]bool{
	workloadsv1alpha2.ReasonPodCreateForbidden:            true,
	workloadsv1alpha2.ReasonPodUpdateForbidden:            true,
	workloadsv1alpha2.ReasonPodAdmissionRejected:          true,
	workloadsv1alpha2.ReasonPodCreateQuotaExceeded:        true,
	workloadsv1alpha2.ReasonPodUpdateQuotaExceeded:        true,
	workloadsv1alpha2.ReasonInstanceInsufficientResources: true,
}

// isInstanceBlockerReason reports whether the reason is a confirmed current-blocker
// family reason. PodOperationUnclassified is excluded: it is not a confirmed family.
func isInstanceBlockerReason(reason string) bool {
	return instanceBlockerReasons[reason]
}

// instanceBlockerFamilyPriority orders the RI blocker families for selection when a
// single reconcile records several operation failures. Create and Update variants
// share a family, so both list entries map to the same family priority.
var instanceBlockerFamilyPriority = []string{
	workloadsv1alpha2.ReasonPodCreateForbidden,
	workloadsv1alpha2.ReasonPodUpdateForbidden,
	workloadsv1alpha2.ReasonPodAdmissionRejected,
	workloadsv1alpha2.ReasonPodCreateQuotaExceeded,
	workloadsv1alpha2.ReasonPodUpdateQuotaExceeded,
	workloadsv1alpha2.ReasonInstanceInsufficientResources,
	workloadsv1alpha2.ReasonPodOperationUnclassified,
}

func instanceBlockerPriority(reason string) int {
	for i, family := range instanceBlockerFamilyPriority {
		if reason == family {
			return i
		}
	}
	return len(instanceBlockerFamilyPriority)
}

// nextRoleInstancePhase implements the allowed phase edges from KEP-502:
//
//   - no phase returns to Creating; Creating exits only to Running,
//     and target changes during Create convergence fold into Create convergence;
//   - Running enters Updating when an update target is identified, or Recovering
//     when required Pods are independently recreated outside an update;
//   - Updating and Recovering return to Running only after the work and
//     controller-observed readiness complete;
//   - an update target identified while recovery continues moves to Updating;
//   - deletion is not a phase: the reconciler returns before status writes once the
//     instance has a deletionTimestamp, and clients use the deletionTimestamp;
//   - an absent phase initializes from confirmed workflow facts: an update target is
//     Updating, a previously established instance with missing Pods is Recovering,
//     and only a genuinely new instance starts at Creating.
func nextRoleInstancePhase(current workloadsv1alpha2.RoleInstancePhase, facts roleInstancePhaseFacts) workloadsv1alpha2.RoleInstancePhase {
	switch current {
	case "":
		switch {
		case facts.updateActive:
			// A fresh instance cannot have a pending update before its first status
			// write: its initial revision is current. An absent phase with an active
			// update is therefore an existing object initialized mid-update.
			return workloadsv1alpha2.RoleInstancePhaseUpdating
		case facts.previouslyEstablished:
			if !facts.podsPresent {
				return workloadsv1alpha2.RoleInstancePhaseRecovering
			}
			// The instance confirmed completion of an earlier target; a readiness
			// drop alone does not demote it back to Creating.
			return workloadsv1alpha2.RoleInstancePhaseRunning
		case facts.podsPresent && facts.readySatisfied:
			return workloadsv1alpha2.RoleInstancePhaseRunning
		default:
			return workloadsv1alpha2.RoleInstancePhaseCreating
		}
	case workloadsv1alpha2.RoleInstancePhaseCreating:
		// The current creation target must have taken effect before Creating exits:
		// pods left over from a superseded target do not prove the new target is
		// established, so a target change during Create convergence folds into
		// Creating instead of passing through Running.
		if !facts.updateActive && facts.podsPresent && facts.readySatisfied {
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
		// HTTP 403 is ambiguous: admission webhooks also reject with Forbidden, so
		// the admission detail must be recognized before defaulting to RBAC.
		if mentionsAdmissionWebhook(err) {
			return workloadsv1alpha2.ReasonPodAdmissionRejected
		}
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

// instanceProgressingBlocker returns the highest-priority blocker Progressing
// condition recorded by this reconcile, if any: scale and update operations can both
// fail in one reconcile, and a lower-priority failure appended first must not hide a
// later permission failure. A confirmed blocker always outranks activity reasons.
func instanceProgressingBlocker(conditions []workloadsv1alpha2.RoleInstanceCondition) *workloadsv1alpha2.RoleInstanceCondition {
	selected, selectedPriority := (*workloadsv1alpha2.RoleInstanceCondition)(nil), len(instanceBlockerFamilyPriority)+1
	for i := range conditions {
		condition := &conditions[i]
		if condition.Type != workloadsv1alpha2.RoleInstanceProgressing ||
			condition.Status != v1.ConditionTrue || condition.Reason == "" {
			continue
		}
		if priority := instanceBlockerPriority(condition.Reason); priority < selectedPriority {
			selected, selectedPriority = condition, priority
		}
	}
	return selected
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
// resourcesMessage is non-empty when a required Pod is confirmed unschedulable.
func computeInstanceProgressingCondition(instance *workloadsv1alpha2.RoleInstance, phase workloadsv1alpha2.RoleInstancePhase,
	instanceReady bool, newInnerConditions []workloadsv1alpha2.RoleInstanceCondition, resourcesMessage string) workloadsv1alpha2.RoleInstanceCondition {
	if blocker := instanceProgressingBlocker(newInnerConditions); blocker != nil {
		// A confirmed blocker family is never replaced by an unclassifiable error.
		// Unschedulable Pods confirm the resources family, which outranks
		// Unclassified in the KEP-502 blocker precedence.
		if blocker.Reason != workloadsv1alpha2.ReasonPodOperationUnclassified || resourcesMessage == "" {
			progressing := *blocker
			progressing.ObservedGeneration = instance.Generation
			return progressing
		}
	}
	if resourcesMessage != "" {
		progressing := workloadsv1alpha2.RoleInstanceCondition{
			Type:               workloadsv1alpha2.RoleInstanceProgressing,
			Status:             v1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             workloadsv1alpha2.ReasonInstanceInsufficientResources,
			Message:            resourcesMessage,
			ObservedGeneration: instance.Generation,
		}
		return progressing
	}

	progressing := workloadsv1alpha2.RoleInstanceCondition{
		Type:               workloadsv1alpha2.RoleInstanceProgressing,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: instance.Generation,
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

// insufficientResourcesMessage returns a current-blocker message when a required Pod
// is confirmed unschedulable, or "" when no resources blocker holds. The fact is
// observed at the lowest layer that can see it: the Pod's PodScheduled condition.
// A Pending Pod without an Unschedulable condition is an incomplete observation and
// must not be guessed as a resources blocker.
func insufficientResourcesMessage(pods []*v1.Pod) string {
	var details []string
	for _, pod := range pods {
		for _, condition := range pod.Status.Conditions {
			if condition.Type == v1.PodScheduled && condition.Status == v1.ConditionFalse &&
				condition.Reason == v1.PodReasonUnschedulable {
				detail := fmt.Sprintf("Pod %s is unschedulable", pod.Name)
				if condition.Message != "" {
					detail = fmt.Sprintf("%s: %s", detail, condition.Message)
				}
				details = append(details, detail)
			}
		}
	}
	return strings.Join(details, "; ")
}
