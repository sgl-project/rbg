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

// ApplySetProgressingCondition computes the RIS Progressing condition and merges it into
// newStatus. RIS reports lower-level blockers by reading the RI Progressing condition
// and mapping reason families, without parsing messages.
func ApplySetProgressingCondition(set *workloadsv1alpha2.RoleInstanceSet, newStatus *workloadsv1alpha2.RoleInstanceSetStatus, instances []*workloadsv1alpha2.RoleInstance) {
	condition := ComputeSetProgressingCondition(set, newStatus, instances)
	condition.LastTransitionTime = preserveTransitionTime(set.Status.Conditions, condition.Status)
	for i := range newStatus.Conditions {
		if newStatus.Conditions[i].Type == workloadsv1alpha2.RoleInstanceSetProgressing {
			newStatus.Conditions[i] = condition
			return
		}
	}
	newStatus.Conditions = append(newStatus.Conditions, condition)
}

// ComputeSetProgressingCondition builds the RIS Progressing condition. Blockers outrank
// activity reasons; activity precedence is Creating, Recovering, RoleInstanceUpdated,
// and ScalingUp / ScalingDown; child readiness applies only when no workflow remains.
func ComputeSetProgressingCondition(set *workloadsv1alpha2.RoleInstanceSet, newStatus *workloadsv1alpha2.RoleInstanceSetStatus, instances []*workloadsv1alpha2.RoleInstance) workloadsv1alpha2.RoleInstanceSetCondition {
	condition := workloadsv1alpha2.RoleInstanceSetCondition{
		Type:               workloadsv1alpha2.RoleInstanceSetProgressing,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: set.Generation,
	}

	if reason, message := currentSetBlocker(newStatus, instances); reason != "" {
		condition.Reason = reason
		condition.Message = message
		return condition
	}

	// Creating is used only during initial set establishment, before the RIS has an
	// established revision or established RIs for its target.
	established := newStatus.Replicas > 0 || set.Status.CurrentRevision != "" || newStatus.CurrentRevision != ""
	if !established {
		condition.Reason = workloadconstants.ProgressingReasonCreating
		return condition
	}

	if anyInstanceRecovering(instances) {
		condition.Reason = workloadconstants.ProgressingReasonRecovering
		return condition
	}

	if updateActive(set, newStatus, instances) {
		condition.Reason = workloadconstants.ProgressingReasonRoleInstanceUpdated
		return condition
	}

	target := int32(0)
	if set.Spec.Replicas != nil {
		target = *set.Spec.Replicas
	}
	switch {
	case newStatus.Replicas < target:
		condition.Reason = workloadconstants.ProgressingReasonScalingUp
		return condition
	case newStatus.Replicas > target:
		condition.Reason = workloadconstants.ProgressingReasonScalingDown
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

// currentSetBlocker selects the current blocker reason: a confirmed family from this
// reconcile's own failure conditions, or a family mapped from RI Progressing conditions.
// A confirmed blocker family is never replaced by an unclassified error.
func currentSetBlocker(newStatus *workloadsv1alpha2.RoleInstanceSetStatus, instances []*workloadsv1alpha2.RoleInstance) (string, string) {
	candidates := map[string]string{}
	for _, condition := range newStatus.Conditions {
		switch condition.Type {
		case workloadsv1alpha2.RoleInstanceSetConditionFailedScale, workloadsv1alpha2.RoleInstanceSetConditionFailedUpdate:
			// A confirmed specific family from this controller's own operations
			// competes in the same priority order as child facts.
			if condition.Status == v1.ConditionTrue && condition.Reason != "" &&
				condition.Reason != workloadconstants.ProgressingReasonUnclassifiedBlocker {
				if _, exists := candidates[condition.Reason]; !exists {
					candidates[condition.Reason] = condition.Message
				}
			}
		}
	}

	for _, instance := range instances {
		for _, condition := range instance.Status.Conditions {
			if condition.Type != workloadsv1alpha2.RoleInstanceProgressing || condition.Status != v1.ConditionTrue {
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
	for _, condition := range newStatus.Conditions {
		switch condition.Type {
		case workloadsv1alpha2.RoleInstanceSetConditionFailedScale, workloadsv1alpha2.RoleInstanceSetConditionFailedUpdate:
			if condition.Status == v1.ConditionTrue {
				return condition.Reason, condition.Message
			}
		}
	}
	return "", ""
}

func anyInstanceRecovering(instances []*workloadsv1alpha2.RoleInstance) bool {
	for _, instance := range instances {
		if instance.Status.Phase == workloadsv1alpha2.RoleInstancePhaseRecovering {
			return true
		}
	}
	return false
}

// updateActive reports whether update work remains: instances below the policy's
// expected update scope, or an instance still executing its own update.
func updateActive(set *workloadsv1alpha2.RoleInstanceSet, newStatus *workloadsv1alpha2.RoleInstanceSetStatus, instances []*workloadsv1alpha2.RoleInstance) bool {
	if newStatus.UpdatedReplicas < newStatus.ExpectedUpdatedReplicas {
		return true
	}
	for _, instance := range instances {
		if instance.Status.Phase == workloadsv1alpha2.RoleInstancePhaseUpdating {
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
