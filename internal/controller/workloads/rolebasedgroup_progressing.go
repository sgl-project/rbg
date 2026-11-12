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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	intstr "k8s.io/apimachinery/pkg/util/intstr"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/utils"
)

// rbgBlockerFamilyPriority is the RBG reporting order from KEP-502. Configuration-family
// blockers rank first because a human must change the spec, referenced configuration, or
// environment. The order affects reporting only, not controller execution order.
var rbgBlockerFamilyPriority = []string{
	constants.ProgressingReasonInvalidSpec,
	constants.ProgressingReasonSchedulerUnsupported,
	constants.ProgressingReasonPermissionDenied,
	constants.ProgressingReasonAdmissionDenied,
	constants.ProgressingReasonWaitingForQuota,
	constants.ProgressingReasonWaitingForResources,
	constants.ProgressingReasonWaitingForGang,
	constants.ProgressingReasonWaitingForDependencies,
	constants.ProgressingReasonUnclassifiedBlocker,
}

// rbgActivityPriority is the RBG activity precedence from KEP-502.
var rbgActivityPriority = []string{
	constants.ProgressingReasonCreating,
	constants.ProgressingReasonRecovering,
	constants.ProgressingReasonRoleInstanceSetUpdated,
}

// risReasonToRBG maps RIS Progressing reasons to their RBG aggregates.
var risReasonToRBG = map[string]string{
	constants.ProgressingReasonCreating:            constants.ProgressingReasonCreating,
	constants.ProgressingReasonRecovering:          constants.ProgressingReasonRecovering,
	constants.ProgressingReasonRoleInstanceUpdated: constants.ProgressingReasonRoleInstanceSetUpdated,
	constants.ProgressingReasonScalingUp:           constants.ProgressingReasonRoleInstanceSetUpdated,
	constants.ProgressingReasonScalingDown:         constants.ProgressingReasonRoleInstanceSetUpdated,
	constants.ProgressingReasonPermissionDenied:    constants.ProgressingReasonPermissionDenied,
	constants.ProgressingReasonAdmissionDenied:     constants.ProgressingReasonAdmissionDenied,
	constants.ProgressingReasonWaitingForQuota:     constants.ProgressingReasonWaitingForQuota,
	constants.ProgressingReasonWaitingForResources: constants.ProgressingReasonWaitingForResources,
	constants.ProgressingReasonUnclassifiedBlocker: constants.ProgressingReasonUnclassifiedBlocker,
	constants.ProgressingReasonInstancesNotReady:   constants.ProgressingReasonRolesNotReady,
}

// allRolesUseRoleInstanceSet reports whether the RBG qualifies for the new condition
// model. Mixed-backend RBGs keep the legacy status behavior.
func allRolesUseRoleInstanceSet(rbg *workloadsv1alpha2.RoleBasedGroup) bool {
	for i := range rbg.Spec.Roles {
		if rbg.Spec.Roles[i].GetWorkloadType() != constants.RoleInstanceSetWorkloadType {
			return false
		}
	}
	return true
}

// risObservation carries one role's RoleInstanceSet facts used by the RBG Progressing
// computation.
type risObservation struct {
	// progressing is the RIS Progressing condition, nil when absent.
	progressing *workloadsv1alpha2.RoleInstanceSetCondition

	// revisionLabel is the role revision hash the RBG dispatched to this RIS.
	revisionLabel string

	// exists reports whether the RIS object was observed.
	exists bool
}

// updateRBGProgressingCondition computes the RBG Progressing condition and records it on
// the live status object. apimeta.SetStatusCondition preserves LastTransitionTime when
// the condition status is unchanged. RBG completion is derived from the RBG's own
// target: the dispatched revision label and the role's final update scope, never from a
// child's converged verdict alone, so a coordinated rollout stays Progressing=True
// between steps.
func (r *RoleBasedGroupReconciler) updateRBGProgressingCondition(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	roleStatuses []workloadsv1alpha2.RoleStatus,
	expectedRevisions map[string]string,
	gangWaiting bool,
) {
	if !allRolesUseRoleInstanceSet(rbg) {
		return
	}

	observations, readErr := r.collectRISObservations(ctx, rbg)
	if readErr != nil {
		// An observation failure must not fabricate a converged verdict or a blocker:
		// report Unknown with ObservationIncomplete per KEP-502.
		apimeta.SetStatusCondition(&rbg.Status.Conditions, metav1.Condition{
			Type:               string(workloadsv1alpha2.RoleBasedGroupProgressing),
			Status:             metav1.ConditionUnknown,
			LastTransitionTime: metav1.Now(),
			Reason:             "ObservationIncomplete",
			Message:            readErr.Error(),
			ObservedGeneration: rbg.Generation,
		})
		return
	}
	depsNotReady := unreadyDependencies(rbg, roleStatuses)

	condition := computeRBGProgressingCondition(rbg, roleStatuses, observations, expectedRevisions, gangWaiting, depsNotReady)
	apimeta.SetStatusCondition(&rbg.Status.Conditions, condition)
}

// reportRBGProgressingBlocker records a controller-owned blocker and observedGeneration
// even when reconciliation stops before the normal status path, so preflight failures
// never skip the generation write required by KEP-502.
func (r *RoleBasedGroupReconciler) reportRBGProgressingBlocker(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	reason, message string,
) error {
	if !allRolesUseRoleInstanceSet(rbg) {
		// Mixed-backend RBGs keep the legacy status behavior.
		return nil
	}
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

// collectRISObservations reads each role's RoleInstanceSet. A missing RIS is a fact
// (child not established or being replaced), not an observation failure; any other read
// error is returned so the caller reports Unknown.
func (r *RoleBasedGroupReconciler) collectRISObservations(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) (map[string]risObservation, error) {
	result := make(map[string]risObservation, len(rbg.Spec.Roles))
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		set := &workloadsv1alpha2.RoleInstanceSet{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, set); err != nil {
			if apierrors.IsNotFound(err) {
				result[role.Name] = risObservation{exists: false}
				continue
			}
			return nil, fmt.Errorf("observe RoleInstanceSet for role %s: %w", role.Name, err)
		}
		observation := risObservation{
			exists:        true,
			revisionLabel: set.Labels[fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name)],
		}
		for j := range set.Status.Conditions {
			if set.Status.Conditions[j].Type == workloadsv1alpha2.RoleInstanceSetProgressing {
				observation.progressing = &set.Status.Conditions[j]
				break
			}
		}
		result[role.Name] = observation
	}
	return result, nil
}

// gangPodGroupGVKs covers both supported gang implementations.
var gangPodGroupGVKs = [][2]string{
	{"scheduling.volcano.sh/v1beta1", "PodGroup"},
	{"scheduling.x-k8s.io/v1alpha1", "PodGroup"},
}

// observeGangWaiting reports whether the gang is waiting from runtime gang-scheduling
// state. A missing PodGroup before its first reconcile is not a waiting fact.
func (r *RoleBasedGroupReconciler) observeGangWaiting(ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup) bool {
	if r.apiReader == nil {
		// No gang observation is available; do not report a waiting fact.
		return false
	}
	for _, gvk := range gangPodGroupGVKs {
		podGroup := &unstructured.Unstructured{}
		podGroup.SetAPIVersion(gvk[0])
		podGroup.SetKind(gvk[1])
		if err := r.apiReader.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup); err != nil {
			continue
		}
		phase, _, _ := unstructured.NestedString(podGroup.Object, "status", "phase")
		return phase != "" && phase != "Running"
	}
	return false
}

// classifyGangIncompatibility distinguishes missing scheduler capability from an
// unsatisfiable gang configuration.
func classifyGangIncompatibility(err error) string {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "not support") || strings.Contains(message, "unsupported") ||
		strings.Contains(message, "does not support") {
		return constants.ProgressingReasonSchedulerUnsupported
	}
	return constants.ProgressingReasonIncompatibleGangConfig
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
	observations map[string]risObservation,
	expectedRevisions map[string]string,
	gangWaiting bool,
	depsNotReady []string,
) metav1.Condition {
	condition := metav1.Condition{
		Type:               string(workloadsv1alpha2.RoleBasedGroupProgressing),
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: rbg.Generation,
	}

	blockerReason, blockerMessage := rbgCurrentBlocker(observations, gangWaiting, depsNotReady)
	if blockerReason != "" {
		condition.Reason = blockerReason
		condition.Message = blockerMessage
		return condition
	}

	if reason, message := rbgCurrentActivity(rbg, roleStatuses, observations); reason != "" {
		condition.Reason = reason
		condition.Message = message
		return condition
	}

	if reason, message := rbgChildReadiness(rbg, roleStatuses); reason != "" {
		condition.Reason = reason
		condition.Message = message
		return condition
	}

	// RBG completion is computed from the RBG's own target: every role's dispatched
	// revision label matches the expected revision, the RIS reports Converged at its
	// scope, and the role reached the final update scope. A coordinated rollout between
	// steps fails the update-scope check, so RBG stays Progressing=True.
	if !rbgOwnTargetComplete(rbg, roleStatuses, observations, expectedRevisions) {
		condition.Reason = constants.ProgressingReasonRoleInstanceSetUpdated
		return condition
	}

	condition.Status = metav1.ConditionFalse
	condition.Reason = constants.ProgressingReasonConverged
	return condition
}

func rbgCurrentBlocker(
	observations map[string]risObservation,
	gangWaiting bool,
	depsNotReady []string,
) (string, string) {
	if gangWaiting {
		return constants.ProgressingReasonWaitingForGang, "gang scheduling preconditions are not satisfied"
	}
	if len(depsNotReady) > 0 {
		return constants.ProgressingReasonWaitingForDependencies,
			fmt.Sprintf("dependencies for roles %s are not ready", strings.Join(depsNotReady, ", "))
	}

	// Map RIS family blockers; RBG does not probe Pod-level errors itself.
	byFamily := make(map[string][]string)
	for roleName, observation := range observations {
		if observation.progressing == nil || observation.progressing.Status != v1.ConditionTrue {
			continue
		}
		mapped, ok := risReasonToRBG[observation.progressing.Reason]
		if !ok {
			continue
		}
		switch mapped {
		case constants.ProgressingReasonPermissionDenied,
			constants.ProgressingReasonAdmissionDenied,
			constants.ProgressingReasonWaitingForQuota,
			constants.ProgressingReasonWaitingForResources,
			constants.ProgressingReasonUnclassifiedBlocker:
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

func rbgCurrentActivity(rbg *workloadsv1alpha2.RoleBasedGroup, roleStatuses []workloadsv1alpha2.RoleStatus, observations map[string]risObservation) (string, string) {
	creating, recovering := missingChildRoles(rbg, roleStatuses, observations)

	for _, activity := range rbgActivityPriority {
		var roles []string
		switch activity {
		case constants.ProgressingReasonCreating:
			roles = creating
		case constants.ProgressingReasonRecovering:
			roles = recovering
		}
		// RIS-reported activity aggregates with the RBG-owned classification: an RIS
		// that exists but is still establishing or recovering reports its own reason.
		for roleName, observation := range observations {
			if observation.progressing == nil || observation.progressing.Status != v1.ConditionTrue {
				continue
			}
			if mapped, ok := risReasonToRBG[observation.progressing.Reason]; ok && mapped == activity {
				roles = append(roles, roleName)
			}
		}
		if len(roles) > 0 {
			return activity, fmt.Sprintf("%s in roles %s", activity, strings.Join(sortedCopy(roles), ", "))
		}
	}
	return "", ""
}

// missingChildRoles classifies required RIS objects that do not exist. Non-zero
// role-status counters prove prior establishment and map to Recovering; otherwise the
// observable fact is creation in progress.
func missingChildRoles(rbg *workloadsv1alpha2.RoleBasedGroup, roleStatuses []workloadsv1alpha2.RoleStatus, observations map[string]risObservation) (creating, recovering []string) {
	statusMap := make(map[string]workloadsv1alpha2.RoleStatus, len(roleStatuses))
	for _, roleStatus := range roleStatuses {
		statusMap[roleStatus.Name] = roleStatus
	}
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		observation, ok := observations[role.Name]
		if !ok || observation.exists {
			continue
		}
		roleStatus := statusMap[role.Name]
		if roleStatus.Replicas > 0 || roleStatus.ReadyReplicas > 0 || roleStatus.UpdatedReplicas > 0 {
			recovering = append(recovering, role.Name)
		} else {
			creating = append(creating, role.Name)
		}
	}
	return creating, recovering
}

// rbgOwnTargetComplete reports whether the RBG's own target is complete: every role's
// dispatched revision matches, every RIS reports Converged, and every role reached the
// final update scope required by its own rollout strategy.
func rbgOwnTargetComplete(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	roleStatuses []workloadsv1alpha2.RoleStatus,
	observations map[string]risObservation,
	expectedRevisions map[string]string,
) bool {
	statusMap := make(map[string]workloadsv1alpha2.RoleStatus, len(roleStatuses))
	for _, roleStatus := range roleStatuses {
		statusMap[roleStatus.Name] = roleStatus
	}
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		roleStatus, ok := statusMap[role.Name]
		if !ok || role.Replicas == nil {
			return false
		}
		observation, ok := observations[role.Name]
		if !ok || !observation.exists {
			return false
		}
		if expected, found := expectedRevisions[role.Name]; found && observation.revisionLabel != expected {
			return false
		}
		if observation.progressing == nil || observation.progressing.Status != v1.ConditionFalse ||
			observation.progressing.Reason != constants.ProgressingReasonConverged {
			return false
		}
		if roleStatus.Replicas != *role.Replicas || roleStatus.ReadyReplicas != roleStatus.Replicas {
			return false
		}
		finalPartition := int32(0)
		if role.RolloutStrategy != nil && role.RolloutStrategy.RollingUpdate != nil && role.RolloutStrategy.RollingUpdate.Partition != nil {
			if value, err := intstr.GetScaledValueFromIntOrPercent(role.RolloutStrategy.RollingUpdate.Partition, int(*role.Replicas), true); err == nil {
				finalPartition = int32(value)
			}
		}
		if roleStatus.UpdatedReplicas < *role.Replicas-finalPartition {
			return false
		}
	}
	return true
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
	return constants.ProgressingReasonRolesNotReady,
		fmt.Sprintf("roles %s are not ready", strings.Join(notReady, ", "))
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
