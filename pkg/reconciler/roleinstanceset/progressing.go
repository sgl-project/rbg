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

// Package roleinstanceset implements status-model helpers shared by the stateless and
// stateful RoleInstanceSet modes (KEP-502).
package roleinstanceset

import (
	"strings"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	workloadconstants "sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// familyPriority is the blocker-family reporting order from KEP-502. It affects
// reporting only, not controller execution order.
var familyPriority = []string{
	workloadconstants.ProgressingReasonPermissionDenied,
	workloadconstants.ProgressingReasonAdmissionDenied,
	workloadconstants.ProgressingReasonWaitingForQuota,
	workloadconstants.ProgressingReasonWaitingForResources,
	workloadconstants.ProgressingReasonUnclassifiedBlocker,
}

// instanceReasonFamily maps operation-specific RI Progressing reasons to the RIS
// canonical family reason.
var instanceReasonFamily = map[string]string{
	workloadsv1alpha2.ReasonPodCreateForbidden:            workloadconstants.ProgressingReasonPermissionDenied,
	workloadsv1alpha2.ReasonPodUpdateForbidden:            workloadconstants.ProgressingReasonPermissionDenied,
	workloadsv1alpha2.ReasonPodAdmissionRejected:          workloadconstants.ProgressingReasonAdmissionDenied,
	workloadsv1alpha2.ReasonPodCreateQuotaExceeded:        workloadconstants.ProgressingReasonWaitingForQuota,
	workloadsv1alpha2.ReasonPodUpdateQuotaExceeded:        workloadconstants.ProgressingReasonWaitingForQuota,
	workloadsv1alpha2.ReasonInstanceInsufficientResources: workloadconstants.ProgressingReasonWaitingForResources,
	workloadsv1alpha2.ReasonPodOperationUnclassified:      workloadconstants.ProgressingReasonUnclassifiedBlocker,
}

// ClassifySetOperationError maps a RoleInstanceSet CR-operation error to the canonical
// RIS blocker reason. Unclassified errors use UnclassifiedBlocker; RIS must not guess a
// family from the status code alone.
func ClassifySetOperationError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case apierrors.IsForbidden(err):
		if strings.Contains(err.Error(), "exceeded quota") {
			return workloadconstants.ProgressingReasonWaitingForQuota
		}
		return workloadconstants.ProgressingReasonPermissionDenied
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		if msg := err.Error(); strings.Contains(msg, "admission webhook") || strings.Contains(msg, "admission plugin") {
			return workloadconstants.ProgressingReasonAdmissionDenied
		}
		return workloadconstants.ProgressingReasonUnclassifiedBlocker
	default:
		return workloadconstants.ProgressingReasonUnclassifiedBlocker
	}
}

// SetBlocker carries a blocker fact observed by this controller's own operations.
type SetBlocker struct {
	Reason  string
	Message string
}

// ApplySetProgressingCondition computes the RIS Progressing condition and merges it into
// newStatus. RIS reports lower-level blockers by reading the RI Progressing condition
// and mapping reason families, without parsing messages. A blocker Progressing
// condition already present in newStatus carries this controller's own operation
// failure; it is replaced here by the finalized condition.
func ApplySetProgressingCondition(set *workloadsv1alpha2.RoleInstanceSet, newStatus *workloadsv1alpha2.RoleInstanceSetStatus, instances []*workloadsv1alpha2.RoleInstance) {
	condition := ComputeSetProgressingCondition(set, newStatus, instances, extractOwnSetBlocker(newStatus), priorSetBlocker(set))
	condition.LastTransitionTime = preserveTransitionTime(set.Status.Conditions, condition.Status)
	// A scale failure and an update failure in the same reconcile can both append a
	// raw Progressing blocker. Remove every raw entry so exactly one finalized
	// condition is persisted; duplicate type keys would make readers pick a stale
	// or lower-priority fact.
	conditions := make([]workloadsv1alpha2.RoleInstanceSetCondition, 0, len(newStatus.Conditions)+1)
	for _, existing := range newStatus.Conditions {
		if existing.Type != workloadsv1alpha2.RoleInstanceSetProgressing {
			conditions = append(conditions, existing)
		}
	}
	newStatus.Conditions = append(conditions, condition)
}

// ComputeSetProgressingCondition builds the RIS Progressing condition. Blockers outrank
// activity reasons; activity precedence is Creating, Recovering, RoleInstanceUpdated,
// and ScalingUp / ScalingDown; child readiness applies only when no workflow remains.
func ComputeSetProgressingCondition(set *workloadsv1alpha2.RoleInstanceSet, newStatus *workloadsv1alpha2.RoleInstanceSetStatus, instances []*workloadsv1alpha2.RoleInstance, ownBlocker, priorBlocker *SetBlocker) workloadsv1alpha2.RoleInstanceSetCondition {
	condition := workloadsv1alpha2.RoleInstanceSetCondition{
		Type:               workloadsv1alpha2.RoleInstanceSetProgressing,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: set.Generation,
	}

	if reason, message := currentSetBlocker(ownBlocker, priorBlocker, instances); reason != "" {
		condition.Reason = reason
		condition.Message = message
		return condition
	}

	target := int32(0)
	if set.Spec.Replicas != nil {
		target = *set.Spec.Replicas
	}

	updatePending := updateActive(set, newStatus, instances)

	// Creating is used only during initial set establishment, before the RIS has
	// established RIs for a previous target. Revisions are published on the first
	// reconcile, so a non-empty revision is not evidence of establishment; only a
	// previous convergence is. A zero-replica target is structurally complete at
	// creation and never reports Creating.
	if !setEstablished(set) && newStatus.Replicas < target {
		condition.Reason = workloadconstants.ProgressingReasonCreating
		return condition
	}

	// A required RI that does not exist is not Creating once the set is
	// established: when the target did not grow, the missing instance is repair or
	// replacement, not a scale-up. Update-driven replacement is excluded because
	// it remains RoleInstanceUpdated.
	if anyInstanceRecovering(instances) || establishedInstanceMissing(set, newStatus, target, updatePending) {
		condition.Reason = workloadconstants.ProgressingReasonRecovering
		return condition
	}

	// A paused update is not executing, so it must not mask a concurrent workflow.
	paused := set.Spec.UpdateStrategy.Paused
	if updatePending {
		if !paused {
			condition.Reason = workloadconstants.ProgressingReasonRoleInstanceUpdated
			return condition
		}
		condition.Message = "update progress is paused"
	} else if paused {
		condition.Message = "update progress is paused"
	}

	switch {
	case newStatus.Replicas < target:
		condition.Reason = workloadconstants.ProgressingReasonScalingUp
		return condition
	case newStatus.Replicas > target:
		condition.Reason = workloadconstants.ProgressingReasonScalingDown
		return condition
	}

	if paused && updatePending {
		// A fully paused update with no other workflow is not progress toward the
		// target: all relevant progress is paused.
		condition.Status = v1.ConditionFalse
		condition.Reason = workloadconstants.ProgressingReasonPaused
		return condition
	}

	if newStatus.ReadyReplicas < newStatus.Replicas {
		condition.Reason = workloadconstants.ProgressingReasonInstancesNotReady
		return condition
	}

	condition.Status = v1.ConditionFalse
	condition.Reason = workloadconstants.ProgressingReasonConverged
	return condition
}

// setEstablishmentReasons are Progressing reasons that prove the set previously got
// past initial establishment. Blocker reasons are excluded: a blocked fresh set is not
// established, and Creating explicitly denies establishment.
var setEstablishmentReasons = map[string]bool{
	workloadconstants.ProgressingReasonConverged:           true,
	workloadconstants.ProgressingReasonPaused:              true,
	workloadconstants.ProgressingReasonInstancesNotReady:   true,
	workloadconstants.ProgressingReasonScalingUp:           true,
	workloadconstants.ProgressingReasonScalingDown:         true,
	workloadconstants.ProgressingReasonRoleInstanceUpdated: true,
	workloadconstants.ProgressingReasonRecovering:          true,
}

// setEstablished reports whether the RoleInstanceSet has established the instances of
// a previous target. Freshly published revisions are not evidence: both modes
// initialize CurrentRevision before any RoleInstance exists, and a ready subset during
// OrderedReady initial creation is not establishment.
func setEstablished(set *workloadsv1alpha2.RoleInstanceSet) bool {
	for _, condition := range set.Status.Conditions {
		if condition.Type != workloadsv1alpha2.RoleInstanceSetProgressing {
			continue
		}
		switch {
		case condition.Reason == workloadconstants.ProgressingReasonCreating:
			// The controller is still establishing this set.
			return false
		case setEstablishmentReasons[condition.Reason]:
			return true
		}
	}
	// Legacy fallback for sets written before this condition existed: every instance
	// of a previous target was updated to the previously expected scope and ready.
	// During initial creation UpdatedReplicas stays below ExpectedUpdatedReplicas, so
	// a partially created set does not qualify.
	return set.Status.Replicas > 0 && set.Status.ReadyReplicas == set.Status.Replicas &&
		set.Status.UpdatedReplicas == set.Status.ExpectedUpdatedReplicas
}

// establishedInstanceMissing reports whether an established set lost instances it
// still requires: the target did not grow, no update workflow explains the loss,
// and the current instance count is below both the target and the previously
// established count.
func establishedInstanceMissing(set *workloadsv1alpha2.RoleInstanceSet, newStatus *workloadsv1alpha2.RoleInstanceSetStatus, target int32, updatePending bool) bool {
	return !updatePending && setEstablished(set) &&
		newStatus.Replicas < target && set.Status.Replicas >= target
}

// extractOwnSetBlocker returns the highest-priority blocker recorded by this
// controller's own operations as Progressing conditions in newStatus. Scale and
// update failures can both be recorded in one reconcile; they compete through the
// documented family precedence instead of the append order.
func extractOwnSetBlocker(newStatus *workloadsv1alpha2.RoleInstanceSetStatus) *SetBlocker {
	candidates := map[string]string{}
	for _, condition := range newStatus.Conditions {
		if condition.Type != workloadsv1alpha2.RoleInstanceSetProgressing ||
			condition.Status != v1.ConditionTrue || condition.Reason == "" {
			continue
		}
		if isSetBlockerFamily(condition.Reason) {
			if _, exists := candidates[condition.Reason]; !exists {
				candidates[condition.Reason] = condition.Message
			}
		}
	}
	for _, family := range familyPriority {
		if message, ok := candidates[family]; ok {
			return &SetBlocker{Reason: family, Message: message}
		}
	}
	return nil
}

// isSetBlockerFamily reports whether the reason is a current-blocker family reason.
func isSetBlockerFamily(reason string) bool {
	for _, family := range familyPriority {
		if family == reason {
			return true
		}
	}
	return false
}

// priorSetBlocker returns the previously persisted current-generation specific blocker
// family for this controller's own operations, if any. It is used to keep a confirmed
// family when a later same-generation failure cannot be classified.
func priorSetBlocker(set *workloadsv1alpha2.RoleInstanceSet) *SetBlocker {
	for _, condition := range set.Status.Conditions {
		if condition.Type != workloadsv1alpha2.RoleInstanceSetProgressing ||
			condition.Status != v1.ConditionTrue ||
			condition.ObservedGeneration != set.Generation ||
			condition.Reason == workloadconstants.ProgressingReasonUnclassifiedBlocker ||
			!isSetBlockerFamily(condition.Reason) {
			continue
		}
		return &SetBlocker{Reason: condition.Reason, Message: condition.Message}
	}
	return nil
}

// currentSetBlocker selects the current blocker reason: a confirmed family from this
// controller's own operations, a family mapped from RI Progressing conditions, or a
// prior own-operation family retained when the new failure is unclassified. A
// confirmed blocker family is never replaced by an unclassified error.
func currentSetBlocker(ownBlocker, priorBlocker *SetBlocker, instances []*workloadsv1alpha2.RoleInstance) (string, string) {
	candidates := map[string]string{}
	if ownBlocker != nil && ownBlocker.Reason != "" &&
		ownBlocker.Reason != workloadconstants.ProgressingReasonUnclassifiedBlocker {
		candidates[ownBlocker.Reason] = ownBlocker.Message
	}
	if ownBlocker != nil && ownBlocker.Reason == workloadconstants.ProgressingReasonUnclassifiedBlocker &&
		priorBlocker != nil && priorBlocker.Reason != "" &&
		priorBlocker.Reason != workloadconstants.ProgressingReasonUnclassifiedBlocker {
		// The prior family stays when the new failure cannot be classified; a fresh
		// successful observation of the current target clears it, and an
		// unclassifiable error does not establish that the confirmed family no
		// longer holds.
		if _, exists := candidates[priorBlocker.Reason]; !exists {
			candidates[priorBlocker.Reason] = priorBlocker.Message
		}
	}

	for _, instance := range instances {
		for _, condition := range instance.Status.Conditions {
			if condition.Type != workloadsv1alpha2.RoleInstanceProgressing || condition.Status != v1.ConditionTrue {
				continue
			}
			// A condition written for an older RoleInstance generation does not
			// describe the current target; a legacy writer omitting
			// observedGeneration does not confirm the current generation either.
			if condition.ObservedGeneration != instance.Generation {
				continue
			}
			if family, ok := instanceReasonFamily[condition.Reason]; ok {
				if _, exists := candidates[family]; !exists {
					candidates[family] = condition.Message
				}
			}
		}
	}

	for _, family := range familyPriority {
		if message, ok := candidates[family]; ok {
			return family, message
		}
	}
	// An unclassified own error is reported only when no specific family is confirmed.
	if ownBlocker != nil && ownBlocker.Reason != "" {
		return ownBlocker.Reason, ownBlocker.Message
	}
	return "", ""
}

func anyInstanceRecovering(instances []*workloadsv1alpha2.RoleInstance) bool {
	for _, instance := range instances {
		if instance.Status.Phase == workloadsv1alpha2.RoleInstancePhaseRecovering ||
			instanceProgressingReason(instance) == workloadsv1alpha2.ReasonInstanceRecovering {
			return true
		}
	}
	return false
}

// instanceProgressingReason returns the fresh RI Progressing reason for the
// instance's current generation, or "" when the condition is absent or stale.
// RIS aggregates lower-level facts through reason families; it must not parse
// condition messages.
func instanceProgressingReason(instance *workloadsv1alpha2.RoleInstance) string {
	for _, condition := range instance.Status.Conditions {
		if condition.Type != workloadsv1alpha2.RoleInstanceProgressing || condition.Status != v1.ConditionTrue {
			continue
		}
		if condition.ObservedGeneration == instance.Generation {
			return condition.Reason
		}
	}
	return ""
}

// EmitOperationFailureEvent emits an edge-triggered Warning Event for a failed
// RoleInstanceSet operation. Events are diagnostic history; the current blocker fact
// stays in the Progressing condition. The event is emitted only when the failure
// message changes, so repeated identical failures do not drain the event budget or
// trigger the client-go spam filter.
func EmitOperationFailureEvent(recorder record.EventRecorder, set *workloadsv1alpha2.RoleInstanceSet, reason string, err error) {
	for _, condition := range set.Status.Conditions {
		if condition.Type == workloadsv1alpha2.RoleInstanceSetProgressing && condition.Message == err.Error() {
			return
		}
	}
	recorder.Eventf(set, v1.EventTypeWarning, reason, "%v", err)
}

// updateActive reports whether update work remains: instances below the policy's
// expected update scope, or an instance still executing its own update.
func updateActive(set *workloadsv1alpha2.RoleInstanceSet, newStatus *workloadsv1alpha2.RoleInstanceSetStatus, instances []*workloadsv1alpha2.RoleInstance) bool {
	// A below-target updated count only means update work when a revision rollout
	// is in progress. With equal revisions it reflects missing instances, which is
	// classified as recovery or scaling, not as an update.
	if newStatus.UpdateRevision != newStatus.CurrentRevision &&
		newStatus.UpdatedReplicas < newStatus.ExpectedUpdatedReplicas {
		return true
	}
	for _, instance := range instances {
		if instance.Status.Phase == workloadsv1alpha2.RoleInstancePhaseUpdating ||
			instanceProgressingReason(instance) == workloadsv1alpha2.ReasonInstanceUpdating {
			return true
		}
	}
	return false
}

func preserveTransitionTime(live []workloadsv1alpha2.RoleInstanceSetCondition, status v1.ConditionStatus) metav1.Time {
	for _, condition := range live {
		if condition.Type == workloadsv1alpha2.RoleInstanceSetProgressing && condition.Status == status {
			return condition.LastTransitionTime
		}
	}
	return metav1.Now()
}

// InconsistentConditions reports whether two condition lists differ in status, reason,
// or message, so condition-only changes still trigger a status write.
func InconsistentConditions(oldConditions, newConditions []workloadsv1alpha2.RoleInstanceSetCondition) bool {
	if len(oldConditions) != len(newConditions) {
		return true
	}
	oldMap := make(map[workloadsv1alpha2.RoleInstanceSetConditionType]workloadsv1alpha2.RoleInstanceSetCondition, len(oldConditions))
	for _, condition := range oldConditions {
		oldMap[condition.Type] = condition
	}
	for _, condition := range newConditions {
		old, ok := oldMap[condition.Type]
		if !ok || old.Status != condition.Status || old.Reason != condition.Reason || old.Message != condition.Message {
			return true
		}
	}
	return false
}
