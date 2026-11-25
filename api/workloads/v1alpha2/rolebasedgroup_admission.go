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

package v1alpha2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/rbgs/api/workloads/constants"
)

// RoleBasedGroupValidator implements admission.CustomValidator for RoleBasedGroup.
// Wired into the manager by SetupWebhookWithManager.
//
// +kubebuilder:object:generate=false
type RoleBasedGroupValidator struct {
	Client client.Client
	// APIReader reads directly from the API server. The legacy-set parent lookup
	// uses it instead of the (eventually consistent) cached client: a stale parent
	// copy could otherwise keep granting the replacement-child exemption after the
	// live set has already been migrated or replaced.
	APIReader client.Reader
	// EnableDeprecatedWorkloadTypes reports whether existing deprecated workload
	// types (Deployment, StatefulSet, LeaderWorkerSet, and the v1alpha1
	// InstanceSet) remain accepted during migration. New uses are rejected in both
	// modes; when false, existing uses are rejected as well. Only the first three
	// still have reconcilers: an InstanceSet role is writable but does not
	// reconcile, so keeping it out of new writes is the safety net.
	EnableDeprecatedWorkloadTypes bool
}

var _ admission.CustomValidator = &RoleBasedGroupValidator{}

// validateRBGStaticSpec checks the deterministic local spec errors that KEP-502
// assigns to admission: the gang-scheduling annotation mutual exclusion, role
// templates, and template references. The controller re-runs these checks as
// defense in depth and reports persisted violations through Progressing.
func validateRBGStaticSpec(rbg *RoleBasedGroup) []error {
	var errs []error
	if rbg.Annotations[constants.GangSchedulingAnnotationKey] == "true" &&
		rbg.Annotations[constants.RoleInstanceGangSchedulingAnnotationKey] == "true" {
		errs = append(errs, fmt.Errorf(
			"annotations %q and %q cannot be set simultaneously on the same RoleBasedGroup; "+
				"use %q for group-level gang scheduling, or set %q per role via role.Annotations",
			constants.GangSchedulingAnnotationKey,
			constants.RoleInstanceGangSchedulingAnnotationKey,
			constants.GangSchedulingAnnotationKey,
			constants.RoleInstanceGangSchedulingAnnotationKey,
		))
	}
	if err := ValidateRoleTemplates(rbg); err != nil {
		errs = append(errs, fmt.Errorf("invalid role templates: %w", err))
	}
	if err := ValidateRoleTemplateReferences(rbg); err != nil {
		errs = append(errs, fmt.Errorf("invalid template references: %w", err))
	}
	if err := validateRoleInstanceLifecycle(rbg); err != nil {
		errs = append(errs, err)
	}
	return errs
}

func validateRoleInstanceLifecycle(rbg *RoleBasedGroup) error {
	return validateRoleInstanceLifecycleList("spec.roles", rbg.Spec.Roles)
}

func validateRoleInstanceLifecycleList(path string, roles []RoleSpec) error {
	for i := range roles {
		role := &roles[i]
		if !HasRoleInstanceLifecycleHooks(role.Lifecycle) {
			continue
		}
		if err := ValidateRoleInstanceLifecycle(
			fmt.Sprintf("%s[%d].lifecycle", path, i), role.Lifecycle, nil); err != nil {
			return err
		}
	}
	return nil
}

// ValidateCreate validates a RoleBasedGroup on creation.
func (v *RoleBasedGroupValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	rbg, ok := obj.(*RoleBasedGroup)
	if !ok {
		return nil, fmt.Errorf("expected *RoleBasedGroup but got %T", obj)
	}
	klog.V(4).InfoS("validating RoleBasedGroup on create", "name", rbg.Name, "namespace", rbg.Namespace)

	if err := ValidateRestartPolicyWorkloadCompatibility("spec.roles", rbg.Spec.Roles); err != nil {
		return nil, err
	}

	if err := ValidateExcludedOrdinals(rbg); err != nil {
		return nil, err
	}
	var allErrs []error
	if err := ValidateRoleBasedGroupName(rbg); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := ValidateRollingUpdate(rbg); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := ValidateRoleDependencies(rbg); err != nil {
		allErrs = append(allErrs, err)
	}
	allErrs = append(allErrs, validateRBGStaticSpec(rbg)...)
	if err := validateNoNewEngineRuntimeViolations(rbg.Spec.Roles, nil); err != nil {
		allErrs = append(allErrs, err)
	}
	// A RoleBasedGroupSet controller replaces deleted children by re-creating
	// them from its own template. Those children are not new user-facing uses:
	// a deprecated or unsupported workload type already exists in the set's
	// template, so the replacement inherits it. Look up the verified parent
	// template, and the held revision a child below a rollout partition is
	// re-created from, and use them as the pre-existing role set, without
	// exempting unrelated creates. In strict mode a deprecated type is rejected whatever
	// the template says, so only unsupported types are worth the lookup.
	exemptible := isUnsupportedWorkloadType
	if v.EnableDeprecatedWorkloadTypes {
		exemptible = func(wt string) bool { return isDeprecatedWorkloadType(wt) || isUnsupportedWorkloadType(wt) }
	}
	oldRoles, err := v.legacySetTemplateRoles(ctx, rbg, exemptible)
	if err != nil {
		allErrs = append(allErrs, err)
	} else {
		if err := validateNoNewUnsupportedWorkloadTypes("spec.roles", rbg.Spec.Roles, oldRoles); err != nil {
			allErrs = append(allErrs, err)
		}
		if v.EnableDeprecatedWorkloadTypes {
			if err := validateNoNewDeprecatedWorkloadTypes("spec.roles", rbg.Spec.Roles, oldRoles); err != nil {
				allErrs = append(allErrs, err)
			}
		}
	}
	if !v.EnableDeprecatedWorkloadTypes {
		if err := validateNoDeprecatedWorkloadTypes("spec.roles", rbg.Spec.Roles); err != nil {
			allErrs = append(allErrs, err)
		}
	}
	// Keep discovery-version rejection last so more specific workload and
	// render errors surface first when both apply.
	if err := ValidateDiscoveryConfigVersion(rbg); err != nil {
		allErrs = append(allErrs, err)
	}

	// A single validation error is returned unwrapped so apierrors.IsInvalid and
	// friends keep recognizing the rejection type.
	if len(allErrs) == 1 {
		return nil, allErrs[0]
	}
	return nil, utilerrors.NewAggregate(allErrs)
}

// legacySetTemplateRoles returns the role list from the owning
// RoleBasedGroupSet's group template when the object being created carries a
// workload type that exemptible accepts (a deprecated one, or one no reconciler
// supports) and is controlled by that set. It returns nil for every other
// create, so unrelated uses of those types stay rejected.
//
// The exemption is owner-reference aware but does not trust the owner reference
// alone: the parent is fetched by name through the uncached API reader, its UID
// must match the reference, and only template roles whose name and workload
// type match an exemptible role in the new child are returned. A reference with
// an unknown UID, or a stale cached copy of a parent that has since been
// migrated or replaced, grants nothing.
//
// The live template is not the only one the set creates children from. During a
// partitioned rollout a missing child below the partition is re-created from the
// held ControllerRevision its groupset-revision label names, and that revision can
// still carry a type the live template has since moved off. Roles the live
// template does not account for are therefore also looked up in that revision,
// which must be controlled by the same verified parent and be one the parent's
// status names as its current or update revision. Retained history is never used
// to re-create a child, so it grants nothing.
//
// The check does not authenticate the writer. A user who can read the parent
// set (its name and UID are ordinary readable metadata) and create
// RoleBasedGroups in its namespace can mint a controller-shaped reference and
// claim the same exemption the set controller gets; the set controller then
// adopts the child and reconciles it. In compatibility mode this is bounded —
// deprecated types still reconcile and garbage collection ties the child to
// the parent — but it is a known boundary of the exemption, not a proof of
// controller identity.
func (v *RoleBasedGroupValidator) legacySetTemplateRoles(
	ctx context.Context, rbg *RoleBasedGroup, exemptible func(workloadType string) bool,
) ([]RoleSpec, error) {
	if !containsWorkloadType(rbg.Spec.Roles, exemptible) {
		return nil, nil
	}
	// Prefer the uncached API reader for the security decision; fall back to the
	// cached client only for test harnesses that wire a single client.
	reader := v.APIReader
	if reader == nil {
		reader = v.Client
	}
	if reader == nil {
		return nil, nil
	}

	owner := metav1.GetControllerOfNoCopy(rbg)
	if owner == nil || owner.Kind != roleBasedGroupSetKind || owner.APIVersion != GroupVersion.String() {
		return nil, nil
	}

	parent := &RoleBasedGroupSet{}
	if err := reader.Get(ctx, types.NamespacedName{Namespace: rbg.Namespace, Name: owner.Name}, parent); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("unable to verify RoleBasedGroupSet owner %s/%s: %w", rbg.Namespace, owner.Name, err)
	}
	if parent.UID != owner.UID {
		return nil, nil
	}

	wanted := make(map[string]string, len(rbg.Spec.Roles))
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		wt := role.GetWorkloadType()
		if exemptible(wt) {
			wanted[role.Name] = wt
		}
	}

	exempt := takeMatchingRoles(parent.Spec.GroupTemplate.Spec.Roles, wanted)
	if len(wanted) > 0 {
		heldRoles, err := heldRevisionRoles(ctx, reader, rbg, parent)
		if err != nil {
			return nil, err
		}
		exempt = append(exempt, takeMatchingRoles(heldRoles, wanted)...)
	}
	if len(exempt) == 0 {
		return nil, nil
	}
	return exempt, nil
}

// takeMatchingRoles returns copies of the template roles whose name and workload
// type match an entry in wanted, and removes the matched entries from wanted.
func takeMatchingRoles(templateRoles []RoleSpec, wanted map[string]string) []RoleSpec {
	var matched []RoleSpec
	for i := range templateRoles {
		role := &templateRoles[i]
		if wt, ok := wanted[role.Name]; ok && wt == role.GetWorkloadType() {
			matched = append(matched, *role.DeepCopy())
			delete(wanted, role.Name)
		}
	}
	return matched
}

// heldRevisionRoles returns the template roles of the ControllerRevision that the
// child's groupset-revision label names, or nil when the child names none, the
// label names neither of the parent's status-managed revisions, the revision does
// not exist, or it is not controlled by parent.
//
// The set controller persists status.currentRevision and status.updateRevision
// before it creates children, and re-creates a missing child only from one of
// those two, so a retained older revision is not a template the set builds from.
// parent has already been verified against the child's owner reference, so a
// revision of another set, or one left behind by a deleted set of the same name,
// grants nothing either.
func heldRevisionRoles(
	ctx context.Context, reader client.Reader, rbg *RoleBasedGroup, parent *RoleBasedGroupSet,
) ([]RoleSpec, error) {
	name := rbg.Labels[constants.GroupSetRevisionLabelKey]
	if name == "" || (name != parent.Status.CurrentRevision && name != parent.Status.UpdateRevision) {
		return nil, nil
	}
	revision := &appsv1.ControllerRevision{}
	if err := reader.Get(ctx, types.NamespacedName{Namespace: rbg.Namespace, Name: name}, revision); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("unable to verify ControllerRevision %s/%s of RoleBasedGroupSet %s: %w",
			rbg.Namespace, name, parent.Name, err)
	}
	if !metav1.IsControlledBy(revision, parent) {
		return nil, nil
	}
	template := &RoleBasedGroupTemplateSpec{}
	if err := json.Unmarshal(revision.Data.Raw, template); err != nil {
		return nil, fmt.Errorf("unable to read ControllerRevision %s/%s of RoleBasedGroupSet %s: %w",
			rbg.Namespace, name, parent.Name, err)
	}
	return template.Spec.Roles, nil
}

// containsWorkloadType reports whether any role in the slice has a workload type
// that match accepts. It gates the parent lookup so ordinary creates never pay
// for an extra read.
func containsWorkloadType(roles []RoleSpec, match func(workloadType string) bool) bool {
	for i := range roles {
		if match(roles[i].GetWorkloadType()) {
			return true
		}
	}
	return false
}

// ValidateUpdate validates a RoleBasedGroup on update.
func (v *RoleBasedGroupValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldRBG, ok := oldObj.(*RoleBasedGroup)
	if !ok {
		return nil, fmt.Errorf("expected *RoleBasedGroup but got %T", oldObj)
	}
	rbg, ok := newObj.(*RoleBasedGroup)
	if !ok {
		return nil, fmt.Errorf("expected *RoleBasedGroup but got %T", newObj)
	}
	klog.V(4).InfoS("validating RoleBasedGroup on update", "name", rbg.Name, "namespace", rbg.Namespace)

	if err := ValidateRestartPolicyWorkloadCompatibility("spec.roles", rbg.Spec.Roles); err != nil {
		return nil, err
	}
	var allErrs []error
	if rbg.DeletionTimestamp != nil {
		// The deletion flow must not be blocked by spec validation: foreground
		// garbage collection removes finalizers through PATCHes that carry a
		// deletionTimestamp, and objects persisted before these checks existed
		// still need to be deletable.
	} else {
		// Removing or replacing the owner in this update must not bypass placement
		// validation: the proposed object is restricted when it is managed in either
		// snapshot. The persisted snapshot is validated with its own ownership, so a
		// newly managed object cannot smuggle a now-forbidden configuration through
		// the ratchet as if it had always been violating.
		managed := isRBGSetManaged(oldRBG) || isRBGSetManaged(rbg)
		allErrs = append(allErrs, ratchetUpdateableSpec(oldRBG, rbg, isRBGSetManaged(oldRBG), managed)...)
	}
	if err := ValidateScalingAdapterReplicas(ctx, v.Client, oldRBG, rbg); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := validateNoNewEngineRuntimeViolations(rbg.Spec.Roles, oldRBG.Spec.Roles); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := validateNoNewUnsupportedWorkloadTypes("spec.roles", rbg.Spec.Roles, oldRBG.Spec.Roles); err != nil {
		allErrs = append(allErrs, err)
	}
	if v.EnableDeprecatedWorkloadTypes {
		if err := validateNoNewDeprecatedWorkloadTypes("spec.roles", rbg.Spec.Roles, oldRBG.Spec.Roles); err != nil {
			allErrs = append(allErrs, err)
		}
	} else {
		if err := validateNoDeprecatedWorkloadTypes("spec.roles", rbg.Spec.Roles); err != nil {
			allErrs = append(allErrs, err)
		}
	}
	// A single validation error is returned unwrapped so apierrors.IsInvalid and
	// friends keep recognizing the rejection type.
	if len(allErrs) == 1 {
		return nil, allErrs[0]
	}
	return nil, utilerrors.NewAggregate(allErrs)
}

// ratchetUpdateableSpec validates the locally checkable spec on update with a
// ratchet: violations that already exist in the persisted object do not block
// the update, so objects written before these webhook checks existed (or while
// the webhook was unavailable) stay updatable and fixable; only newly introduced
// violations are rejected.
//
// Violations are compared individually: joined errors, wrapped aggregates, and
// API status errors are split into their concrete violations before old and new
// are compared, so partially repairing an invalid object is allowed. The old and
// new snapshots are validated with their own RBGSet ownership (the proposed
// object also stays restricted when ownership is being removed in this update).
func ratchetUpdateableSpec(oldRBG, rbg *RoleBasedGroup, oldManaged, newManaged bool) []error {
	checks := []func(*RoleBasedGroup, bool) error{
		validateExcludedOrdinals,
		func(g *RoleBasedGroup, _ bool) error { return ValidateRollingUpdate(g) },
		func(g *RoleBasedGroup, _ bool) error { return ValidateRoleDependencies(g) },
		func(g *RoleBasedGroup, _ bool) error { return ValidateDiscoveryConfigVersion(g) },
	}
	var newErrs, oldErrs []error
	for _, check := range checks {
		if err := check(rbg, newManaged); err != nil {
			newErrs = append(newErrs, err)
		}
		if err := check(oldRBG, oldManaged); err != nil {
			oldErrs = append(oldErrs, err)
		}
	}
	newErrs = append(newErrs, validateRBGStaticSpec(rbg)...)
	oldErrs = append(oldErrs, validateRBGStaticSpec(oldRBG)...)

	oldViolation := make(map[string]bool, len(oldErrs))
	for _, err := range oldErrs {
		for _, key := range violationKeys(err) {
			oldViolation[key] = true
		}
	}
	var ratcheted []error
	for _, err := range newErrs {
		if ratchetError(err, oldViolation) != nil {
			ratcheted = append(ratcheted, err)
		}
	}
	return ratcheted
}

// ratchetError returns the original error when at least one of its concrete
// violations is new, nil when every violation already existed in the persisted
// object. Returning the original error keeps rejection messages recognizable.
func ratchetError(err error, oldViolation map[string]bool) error {
	keys := violationKeys(err)
	if len(keys) == 0 {
		return nil
	}
	for _, key := range keys {
		if !oldViolation[key] {
			return err
		}
	}
	return nil
}

// violationKeys extracts one stable key per concrete violation. Aggregates are
// split into constituents, wrapped errors are unwrapped, and API status errors
// are keyed by their causes (field, type, reason, and message identify the
// violation; the aggregate error string does not, because it changes whenever
// any sibling violation is repaired).
func violationKeys(err error) []string {
	if err == nil {
		return nil
	}
	var keys []string
	if aggregate, ok := err.(interface{ Errors() []error }); ok {
		for _, single := range aggregate.Errors() {
			keys = append(keys, violationKeys(single)...)
		}
		return keys
	}
	if aggregate, ok := err.(interface{ Unwrap() []error }); ok {
		for _, single := range aggregate.Unwrap() {
			keys = append(keys, violationKeys(single)...)
		}
		return keys
	}
	if statusErr, ok := err.(*apierrors.StatusError); ok && statusErr.Status().Details != nil {
		for _, cause := range statusErr.Status().Details.Causes {
			keys = append(keys, fmt.Sprintf("%s: %s: %s",
				cause.Field, cause.Type, cause.Message))
		}
		if len(keys) > 0 {
			return keys
		}
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return violationKeys(wrapped)
	}
	return []string{err.Error()}
}

// ValidateDelete just implements admission.CustomValidator. This verb is currently no-op.
func (v *RoleBasedGroupValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
