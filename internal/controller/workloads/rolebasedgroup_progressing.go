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
	constants.ProgressingReasonIncompatibleGangConfig,
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
	constants.ProgressingReasonInvalidSpec:         constants.ProgressingReasonInvalidSpec,
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

	// generation is the observed RoleInstanceSet generation, used to reject stale
	// conditions that do not describe the child's current target.
	generation int64

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
// between steps. It returns the roles whose persisted role status must be preserved as
// prior-establishment evidence for a currently missing RoleInstanceSet.
func (r *RoleBasedGroupReconciler) updateRBGProgressingCondition(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	roleStatuses []workloadsv1alpha2.RoleStatus,
	expectedRevisions map[string]string,
	gangWaiting bool,
	entryBlocker *metav1.Condition,
) map[string]bool {
	if !allRolesUseRoleInstanceSet(rbg) {
		return nil
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
		return nil
	}
	depsNotReady := r.unreadyDependencies(ctx, rbg)

	condition := computeRBGProgressingCondition(rbg, roleStatuses, observations, expectedRevisions, gangWaiting, depsNotReady, entryBlocker)
	apimeta.SetStatusCondition(&rbg.Status.Conditions, condition)

	_, recovering := missingChildRoles(rbg, observations)
	preserved := make(map[string]bool, len(recovering))
	for _, role := range recovering {
		preserved[role] = true
	}
	return preserved
}

// reportRBGProgressingBlocker records a controller-owned blocker and observedGeneration
// even when reconciliation stops before the normal status path, so preflight failures
// never skip the generation write required by KEP-502.
func (r *RoleBasedGroupReconciler) reportRBGProgressingBlocker(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	entryBlocker *metav1.Condition,
	reason, message string,
) error {
	if !allRolesUseRoleInstanceSet(rbg) {
		// Mixed-backend RBGs keep the legacy status behavior.
		return nil
	}
	reason, message = retainRBGBlockerFamily(rbg, entryBlocker, reason, message)
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

// finalizeRBGProgressingCondition recomputes the Progressing condition after the
// role operations succeeded, without carrying the entry blocker. A fresh successful
// observation is what clears a previously confirmed controller-owned blocker family.
func (r *RoleBasedGroupReconciler) finalizeRBGProgressingCondition(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	roleStatuses []workloadsv1alpha2.RoleStatus,
	expectedRevisions map[string]string,
) error {
	if !allRolesUseRoleInstanceSet(rbg) {
		return nil
	}
	r.updateRBGProgressingCondition(ctx, rbg, roleStatuses, expectedRevisions, r.observeGangWaiting(ctx, rbg), nil)
	return utils.PatchObjectApplyConfiguration(ctx, r.client, toRBGApplyConfigurationForStatus(rbg), utils.PatchStatus)
}

// isRBGBlockerFamily reports whether the reason is a current-blocker family reason
// rather than an activity or child-readiness reason.
func isRBGBlockerFamily(reason string) bool {
	for _, family := range rbgBlockerFamilyPriority {
		if family == reason {
			return true
		}
	}
	return false
}

// retainRBGBlockerFamily keeps a previously confirmed blocker family for the current
// target when the new controller-owned error cannot be classified: an unclassifiable
// error does not establish that the confirmed blocker no longer holds, and the new
// error stays in the message. The entry snapshot is preferred because the normal
// status path can already have overwritten the persisted condition with the child
// aggregation; the current condition remains a fallback for child-reported families.
// A specific new error or a fresh success replaces the prior family.
func retainRBGBlockerFamily(rbg *workloadsv1alpha2.RoleBasedGroup, entryBlocker *metav1.Condition, reason, message string) (string, string) {
	if reason != constants.ProgressingReasonUnclassifiedBlocker {
		return reason, message
	}
	for _, source := range []*metav1.Condition{entryBlocker, apimeta.FindStatusCondition(rbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupProgressing))} {
		if source == nil ||
			source.Status != metav1.ConditionTrue ||
			source.ObservedGeneration != rbg.Generation ||
			source.Reason == constants.ProgressingReasonUnclassifiedBlocker ||
			!isRBGBlockerFamily(source.Reason) {
			continue
		}
		return source.Reason, fmt.Sprintf("%s; latest unclassified error: %s", source.Message, message)
	}
	return reason, message
}

// rbgEntryOwnBlocker returns the Progressing blocker confirmed for the RBG's current
// generation at reconcile entry, before the normal status path can overwrite it. It
// is carried through aggregation and retained when a later role operation fails with
// an unclassifiable error.
func rbgEntryOwnBlocker(rbg *workloadsv1alpha2.RoleBasedGroup) *metav1.Condition {
	condition := apimeta.FindStatusCondition(rbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupProgressing))
	if condition == nil ||
		condition.Status != metav1.ConditionTrue ||
		condition.ObservedGeneration != rbg.Generation ||
		condition.Reason == constants.ProgressingReasonUnclassifiedBlocker ||
		!isRBGBlockerFamily(condition.Reason) {
		return nil
	}
	return condition.DeepCopy()
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
			generation:    set.Generation,
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
// state. A missing PodGroup before its first reconcile is not a waiting fact, and a
// same-name PodGroup that is not controlled by this RBG is never this RBG's fact.
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
		if !podGroupControlledByRBG(podGroup, rbg) {
			continue
		}
		return podGroupWaitingPhase(gvk[0], podGroup)
	}
	return false
}

// podGroupControlledByRBG reports whether the PodGroup carries this RBG's controller
// reference. Both supported gang implementations create PodGroups with one.
func podGroupControlledByRBG(podGroup *unstructured.Unstructured, rbg *workloadsv1alpha2.RoleBasedGroup) bool {
	references, ok, _ := unstructured.NestedSlice(podGroup.Object, "metadata", "ownerReferences")
	if !ok {
		return false
	}
	for _, reference := range references {
		entry, ok := reference.(map[string]interface{})
		if !ok {
			continue
		}
		controller, _, _ := unstructured.NestedBool(entry, "controller")
		uid, _, _ := unstructured.NestedString(entry, "uid")
		if controller && uid == string(rbg.UID) {
			return true
		}
	}
	return false
}

// podGroupWaitingPhase classifies backend-specific waiting phases. Not every
// non-Running phase waits for gang admission: Volcano's Inqueue already allows pod
// creation, scheduler-plugins' Scheduling already placed more than minMember pods,
// and Completed / Finished / Failed are terminal states.
func podGroupWaitingPhase(apiVersion string, podGroup *unstructured.Unstructured) bool {
	phase, _, _ := unstructured.NestedString(podGroup.Object, "status", "phase")
	switch apiVersion {
	case "scheduling.volcano.sh/v1beta1", "scheduling.x-k8s.io/v1alpha1":
		// Pending waits for admission and resources; Unknown means part of the
		// gang is placed but the rest cannot be scheduled.
		return phase == "Pending" || phase == "Unknown"
	default:
		return false
	}
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

// unreadyDependencies returns role names whose dependencies fail the same readiness
// gate the reconciler uses for execution (CheckWorkloadReady over the dispatched
// child replicas, including the maxSkew coordination exception). Reusing the
// execution predicate keeps the reported blocker from disagreeing with the gate
// during coordinated rollouts. Dependencies are declared in the RBG spec and cross
// roles, so RIS cannot observe them.
func (r *RoleBasedGroupReconciler) unreadyDependencies(ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup) []string {
	var notReady []string
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		if len(role.Dependencies) == 0 {
			continue
		}
		for _, dependency := range role.Dependencies {
			depRole, err := rbg.GetRole(dependency)
			if err != nil {
				// The dependency role is declared in the spec; a lookup failure means
				// the gate itself cannot be satisfied.
				notReady = append(notReady, role.Name)
				break
			}
			workloadReconciler, err := r.getOrCreateWorkloadReconciler(ctx, depRole.GetWorkloadSpec())
			if err != nil {
				// An observation error must not fabricate a blocker.
				continue
			}
			ready, err := workloadReconciler.CheckWorkloadReady(ctx, rbg, depRole)
			if err != nil {
				if apierrors.IsNotFound(err) {
					// A missing dependency child is a fact: the dependency is not ready.
					notReady = append(notReady, role.Name)
					break
				}
				// An observation error must not fabricate a blocker.
				continue
			}
			if !ready {
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
	entryBlocker *metav1.Condition,
) metav1.Condition {
	condition := metav1.Condition{
		Type:               string(workloadsv1alpha2.RoleBasedGroupProgressing),
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: rbg.Generation,
	}

	blockerReason, blockerMessage := rbgCurrentBlocker(observations, gangWaiting, depsNotReady, entryBlocker)
	if blockerReason != "" {
		condition.Reason = blockerReason
		condition.Message = blockerMessage
		return condition
	}

	if reason, message := rbgCurrentActivity(rbg, observations); reason != "" {
		condition.Reason = reason
		condition.Message = message
		return condition
	}

	if allRoleProgressPaused(observations) {
		condition.Status = metav1.ConditionFalse
		condition.Reason = constants.ProgressingReasonPaused
		condition.Message = "all role progress is paused"
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
	entryBlocker *metav1.Condition,
) (string, string) {
	// All current blockers, including RBG-owned gang and dependency facts, compete
	// through the KEP-502 family precedence: gang and dependencies rank below
	// permission, admission, quota, and resources blockers.
	// RBG does not probe Pod-level errors itself.
	byFamily := make(map[string][]string)
	if gangWaiting {
		byFamily[constants.ProgressingReasonWaitingForGang] = []string{}
	}
	if len(depsNotReady) > 0 {
		byFamily[constants.ProgressingReasonWaitingForDependencies] = append(
			byFamily[constants.ProgressingReasonWaitingForDependencies], depsNotReady...)
	}
	entryMessages := map[string]string{}
	if entryBlocker != nil && entryBlocker.Status == metav1.ConditionTrue &&
		entryBlocker.Reason != "" && entryBlocker.Reason != constants.ProgressingReasonUnclassifiedBlocker &&
		isRBGBlockerFamily(entryBlocker.Reason) {
		// A controller-owned blocker confirmed for the current target is carried
		// until the role operation itself is retried later in this reconcile; the
		// child aggregation alone must not publish an unconfirmed clearing.
		if _, exists := byFamily[entryBlocker.Reason]; !exists {
			byFamily[entryBlocker.Reason] = []string{}
		}
		entryMessages[entryBlocker.Reason] = entryBlocker.Message
	}
	for roleName, observation := range observations {
		if observation.progressing == nil || observation.progressing.Status != v1.ConditionTrue {
			continue
		}
		if observation.progressing.ObservedGeneration != observation.generation {
			// A condition written for an older RIS generation does not describe the
			// current target, and a legacy writer omitting observedGeneration does
			// not confirm the current generation.
			continue
		}
		mapped, ok := risReasonToRBG[observation.progressing.Reason]
		if !ok {
			continue
		}
		switch mapped {
		case constants.ProgressingReasonInvalidSpec,
			constants.ProgressingReasonPermissionDenied,
			constants.ProgressingReasonAdmissionDenied,
			constants.ProgressingReasonWaitingForQuota,
			constants.ProgressingReasonWaitingForResources,
			constants.ProgressingReasonUnclassifiedBlocker:
			byFamily[mapped] = append(byFamily[mapped], roleName)
		}
	}
	for _, family := range rbgBlockerFamilyPriority {
		if roles, ok := byFamily[family]; ok {
			if family == constants.ProgressingReasonWaitingForGang {
				return family, "gang scheduling preconditions are not satisfied"
			}
			if family == constants.ProgressingReasonWaitingForDependencies {
				return family, fmt.Sprintf("dependencies for roles %s are not ready", strings.Join(sortedCopy(roles), ", "))
			}
			if len(roles) == 0 {
				if message := entryMessages[family]; message != "" {
					return family, message
				}
			}
			return family, fmt.Sprintf("%s affects roles %s", family, strings.Join(sortedCopy(roles), ", "))
		}
	}
	return "", ""
}

func rbgCurrentActivity(rbg *workloadsv1alpha2.RoleBasedGroup, observations map[string]risObservation) (string, string) {
	creating, recovering := missingChildRoles(rbg, observations)

	for _, activity := range rbgActivityPriority {
		var roles []string
		var roleDetails []string
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
			if observation.progressing.ObservedGeneration != observation.generation {
				continue
			}
			if mapped, ok := risReasonToRBG[observation.progressing.Reason]; ok && mapped == activity {
				roles = append(roles, roleName)
			}
		}
		if len(roles) > 0 {
			for _, role := range sortedCopy(roles) {
				roleDetails = append(roleDetails, describeRoleActivity(role, observations, activity))
			}
			return activity, fmt.Sprintf("%s in roles %s", activity, strings.Join(roleDetails, ", "))
		}
	}
	return "", ""
}

// describeRoleActivity names the role and, when the RIS reports a concrete workflow
// reason, the role's activity, so the message describes every concurrent activity
// for humans without becoming a machine interface.
func describeRoleActivity(roleName string, observations map[string]risObservation, aggregate string) string {
	observation, ok := observations[roleName]
	if !ok || observation.progressing == nil {
		return roleName
	}
	reason := observation.progressing.Reason
	if reason == "" || reason == aggregate {
		return roleName
	}
	return fmt.Sprintf("%s (%s)", roleName, reason)
}

// allRoleProgressPaused reports whether every role's RoleInstanceSet is observed and
// paused or converged, with at least one role paused. It runs only after the blocker
// and activity checks, so a role that is still recovering, updating, or scaling keeps
// the overall condition True.
func allRoleProgressPaused(observations map[string]risObservation) bool {
	paused := false
	for _, observation := range observations {
		if !observation.exists || observation.progressing == nil {
			return false
		}
		if observation.progressing.ObservedGeneration != observation.generation {
			// A stale child condition must not publish Paused for the current target.
			return false
		}
		switch {
		case observation.progressing.Reason == constants.ProgressingReasonPaused &&
			observation.progressing.Status == v1.ConditionFalse:
			paused = true
		case observation.progressing.Status == v1.ConditionFalse &&
			observation.progressing.Reason == constants.ProgressingReasonConverged:
		default:
			return false
		}
	}
	return paused
}

// missingChildRoles classifies required RIS objects that do not exist. It reads the
// persisted rbg.Status.RoleStatuses rather than the freshly constructed statuses: a
// missing RIS yields a fresh zero-valued entry, while the persisted non-zero counters
// are the durable evidence that the child was previously established and is now being
// replaced. Zero or absent persisted evidence maps to Creating.
func missingChildRoles(rbg *workloadsv1alpha2.RoleBasedGroup, observations map[string]risObservation) (creating, recovering []string) {
	statusMap := make(map[string]workloadsv1alpha2.RoleStatus, len(rbg.Status.RoleStatuses))
	for _, roleStatus := range rbg.Status.RoleStatuses {
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
			observation.progressing.Reason != constants.ProgressingReasonConverged ||
			observation.progressing.ObservedGeneration != observation.generation {
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
