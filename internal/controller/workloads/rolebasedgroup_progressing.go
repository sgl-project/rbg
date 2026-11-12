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
	"fmt"
	"sort"
	"strings"

	v1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	workloadconstants "sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/utils"
)

// rbgBlockerFamilyPriority is the RBG reporting order from KEP-502. Configuration-family
// blockers rank first because a human must change the spec, referenced configuration, or
// environment. The order affects reporting only, not controller execution order.
var rbgBlockerFamilyPriority = []string{
	workloadconstants.ProgressingReasonInvalidSpec,
	workloadconstants.ProgressingReasonSchedulerUnsupported,
	workloadconstants.ProgressingReasonPermissionDenied,
	workloadconstants.ProgressingReasonAdmissionDenied,
	workloadconstants.ProgressingReasonWaitingForQuota,
	workloadconstants.ProgressingReasonWaitingForResources,
	workloadconstants.ProgressingReasonWaitingForGang,
	workloadconstants.ProgressingReasonWaitingForDependencies,
	workloadconstants.ProgressingReasonUnclassifiedBlocker,
}

// rbgActivityPriority is the RBG activity precedence from KEP-502.
var rbgActivityPriority = []string{
	workloadconstants.ProgressingReasonCreating,
	workloadconstants.ProgressingReasonRecovering,
	workloadconstants.ProgressingReasonRoleInstanceSetUpdated,
}

// risReasonToRBG maps RIS Progressing reasons to their RBG aggregates.
var risReasonToRBG = map[string]string{
	workloadconstants.ProgressingReasonCreating:            workloadconstants.ProgressingReasonCreating,
	workloadconstants.ProgressingReasonRecovering:          workloadconstants.ProgressingReasonRecovering,
	workloadconstants.ProgressingReasonRoleInstanceUpdated: workloadconstants.ProgressingReasonRoleInstanceSetUpdated,
	workloadconstants.ProgressingReasonScalingUp:           workloadconstants.ProgressingReasonRoleInstanceSetUpdated,
	workloadconstants.ProgressingReasonScalingDown:         workloadconstants.ProgressingReasonRoleInstanceSetUpdated,
	workloadconstants.ProgressingReasonPermissionDenied:    workloadconstants.ProgressingReasonPermissionDenied,
	workloadconstants.ProgressingReasonAdmissionDenied:     workloadconstants.ProgressingReasonAdmissionDenied,
	workloadconstants.ProgressingReasonWaitingForQuota:     workloadconstants.ProgressingReasonWaitingForQuota,
	workloadconstants.ProgressingReasonWaitingForResources: workloadconstants.ProgressingReasonWaitingForResources,
	workloadconstants.ProgressingReasonUnclassifiedBlocker: workloadconstants.ProgressingReasonUnclassifiedBlocker,
	workloadconstants.ProgressingReasonInstancesNotReady:   workloadconstants.ProgressingReasonRolesNotReady,
}

// allRolesUseRoleInstanceSet reports whether the RBG qualifies for the new condition
// model. Mixed-backend RBGs keep the legacy status behavior.
func allRolesUseRoleInstanceSet(rbg *workloadsv1alpha2.RoleBasedGroup) bool {
	for i := range rbg.Spec.Roles {
		if rbg.Spec.Roles[i].GetWorkloadType() != workloadconstants.RoleInstanceSetWorkloadType {
			return false
		}
	}
	return true
}

// updateRBGProgressingCondition computes the RBG Progressing condition and records it on
// the live status object. apimeta.SetStatusCondition preserves LastTransitionTime when
// the condition status is unchanged.
func (r *RoleBasedGroupReconciler) updateRBGProgressingCondition(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	roleStatuses []workloadsv1alpha2.RoleStatus,
	gangWaiting bool,
) error {
	if !allRolesUseRoleInstanceSet(rbg) {
		return nil
	}

	risProgressing, err := r.collectRISProgressingConditions(ctx, rbg)
	if err != nil {
		// Observation failure must not fabricate a converged verdict; keep the last
		// known condition and surface the read error.
		return err
	}
	depsNotReady := unreadyDependencies(rbg, roleStatuses)

	condition := computeRBGProgressingCondition(rbg, roleStatuses, risProgressing, gangWaiting, depsNotReady)
	apimeta.SetStatusCondition(&rbg.Status.Conditions, condition)
	return nil
}

// reportRBGProgressingBlocker records a controller-owned blocker and observedGeneration
// even when reconciliation stops before the normal status path, so preflight failures
// never skip the generation write required by KEP-502.
func (r *RoleBasedGroupReconciler) reportRBGProgressingBlocker(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	reason, message string,
) error {
	condition := metav1.Condition{
		Type:               string(workloadsv1alpha2.RoleBasedGroupProgressing),
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
		ObservedGeneration: rbg.Generation,
	}
	apimeta.SetStatusCondition(&rbg.Status.Conditions, condition)
	rbg.Status.ObservedGeneration = rbg.Generation
	return utils.PatchObjectApplyConfiguration(ctx, r.client, toRBGApplyConfigurationForStatus(rbg), utils.PatchStatus)
}

// collectRISProgressingConditions reads each role's RoleInstanceSet Progressing
// condition. Gang waiting and dependencies are RBG-owned facts and are not read from RIS.
func (r *RoleBasedGroupReconciler) collectRISProgressingConditions(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) (map[string]workloadsv1alpha2.RoleInstanceSetCondition, error) {
	result := make(map[string]workloadsv1alpha2.RoleInstanceSetCondition, len(rbg.Spec.Roles))
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		set := &workloadsv1alpha2.RoleInstanceSet{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, set); err != nil {
			return nil, err
		}
		for _, condition := range set.Status.Conditions {
			if condition.Type == workloadsv1alpha2.RoleInstanceSetProgressing {
				result[role.Name] = condition
				break
			}
		}
	}
	return result, nil
}

// observeGangWaiting reports whether the gang is waiting from runtime gang-scheduling
// state. A missing PodGroup before its first reconcile is not a waiting fact.
func (r *RoleBasedGroupReconciler) observeGangWaiting(ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup) bool {
	if r.apiReader == nil {
		// No gang observation is available; do not report a waiting fact.
		return false
	}
	podGroup := &unstructured.Unstructured{}
	podGroup.SetAPIVersion("scheduling.volcano.sh/v1beta1")
	podGroup.SetKind("PodGroup")
	if err := r.apiReader.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup); err != nil {
		return false
	}
	phase, _, _ := unstructured.NestedString(podGroup.Object, "status", "phase")
	return phase != "" && phase != "Running"
}

// unreadyDependencies returns role names whose dependencies are not ready yet.
// Dependencies are declared in the RBG spec and cross roles, so RIS cannot observe them.
func unreadyDependencies(rbg *workloadsv1alpha2.RoleBasedGroup, roleStatuses []workloadsv1alpha2.RoleStatus) []string {
	ready := make(map[string]bool, len(roleStatuses))
	targets := make(map[string]int32, len(rbg.Spec.Roles))
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		if role.Replicas != nil {
			targets[role.Name] = *role.Replicas
		}
	}
	for _, roleStatus := range roleStatuses {
		ready[roleStatus.Name] = roleStatus.ReadyReplicas == targets[roleStatus.Name]
	}
	var notReady []string
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		for _, dependency := range role.Dependencies {
			if !ready[dependency] {
				notReady = append(notReady, role.Name)
				break
			}
		}
	}
	sort.Strings(notReady)
	return notReady
}

// computeRBGProgressingCondition builds the single RBG Progressing condition.
func computeRBGProgressingCondition(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	roleStatuses []workloadsv1alpha2.RoleStatus,
	risProgressing map[string]workloadsv1alpha2.RoleInstanceSetCondition,
	gangWaiting bool,
	depsNotReady []string,
) metav1.Condition {
	condition := metav1.Condition{
		Type:               string(workloadsv1alpha2.RoleBasedGroupProgressing),
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: rbg.Generation,
	}

	blockerReason, blockerMessage := rbgCurrentBlocker(risProgressing, gangWaiting, depsNotReady)
	if blockerReason != "" {
		condition.Reason = blockerReason
		condition.Message = blockerMessage
		return condition
	}

	if reason, message := rbgCurrentActivity(risProgressing); reason != "" {
		condition.Reason = reason
		condition.Message = message
		return condition
	}

	if reason, message := rbgChildReadiness(rbg, roleStatuses); reason != "" {
		condition.Reason = reason
		condition.Message = message
		return condition
	}

	condition.Status = metav1.ConditionFalse
	condition.Reason = workloadconstants.ProgressingReasonConverged
	return condition
}

func rbgCurrentBlocker(
	risProgressing map[string]workloadsv1alpha2.RoleInstanceSetCondition,
	gangWaiting bool,
	depsNotReady []string,
) (string, string) {
	if gangWaiting {
		return workloadconstants.ProgressingReasonWaitingForGang, "gang scheduling preconditions are not satisfied"
	}
	if len(depsNotReady) > 0 {
		return workloadconstants.ProgressingReasonWaitingForDependencies,
			fmt.Sprintf("dependencies for roles %s are not ready", strings.Join(depsNotReady, ", "))
	}

	// Map RIS family blockers; RBG does not probe Pod-level errors itself.
	byFamily := make(map[string][]string)
	for roleName, condition := range risProgressing {
		if condition.Status != v1.ConditionTrue {
			continue
		}
		mapped, ok := risReasonToRBG[condition.Reason]
		if !ok {
			continue
		}
		switch mapped {
		case workloadconstants.ProgressingReasonPermissionDenied,
			workloadconstants.ProgressingReasonAdmissionDenied,
			workloadconstants.ProgressingReasonWaitingForQuota,
			workloadconstants.ProgressingReasonWaitingForResources,
			workloadconstants.ProgressingReasonUnclassifiedBlocker:
			byFamily[mapped] = append(byFamily[mapped], roleName)
		}
	}
	for _, family := range rbgBlockerFamilyPriority {
		if roles, ok := byFamily[family]; ok {
			return family, fmt.Sprintf("%s affects roles %s", family, strings.Join(sortedCopy(roles), ", "))
		}
	}
	return "", ""
}

func rbgCurrentActivity(risProgressing map[string]workloadsv1alpha2.RoleInstanceSetCondition) (string, string) {
	for _, activity := range rbgActivityPriority {
		var roles []string
		for roleName, condition := range risProgressing {
			if condition.Status != v1.ConditionTrue {
				continue
			}
			if mapped, ok := risReasonToRBG[condition.Reason]; ok && mapped == activity {
				roles = append(roles, roleName)
			}
		}
		if len(roles) > 0 {
			return activity, fmt.Sprintf("%s in roles %s", activity, strings.Join(sortedCopy(roles), ", "))
		}
	}
	return "", ""
}

func rbgChildReadiness(rbg *workloadsv1alpha2.RoleBasedGroup, roleStatuses []workloadsv1alpha2.RoleStatus) (string, string) {
	statusMap := make(map[string]workloadsv1alpha2.RoleStatus, len(roleStatuses))
	for _, roleStatus := range roleStatuses {
		statusMap[roleStatus.Name] = roleStatus
	}
	var notReady []string
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		roleStatus, ok := statusMap[role.Name]
		if !ok || role.Replicas == nil || *role.Replicas != roleStatus.Replicas || roleStatus.Replicas != roleStatus.ReadyReplicas {
			notReady = append(notReady, role.Name)
		}
	}
	if len(notReady) == 0 {
		return "", ""
	}
	return workloadconstants.ProgressingReasonRolesNotReady,
		fmt.Sprintf("roles %s are not ready", strings.Join(notReady, ", "))
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
