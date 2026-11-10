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

package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	"sigs.k8s.io/rbgs/api/workloads/ordinals"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/utils"
)

func filteredRoleAnnotations(annotations map[string]string) map[string]string {
	result := maps.Clone(annotations)
	delete(result, constants.ExcludedOrdinalsAnnotationKey)
	delete(result, constants.CommittedSkipOrdinalsAnnotationKey)
	return result
}

func usesSelectiveScaleDown(role *workloadsv1alpha2.RoleSpec, set *workloadsv1alpha2.RoleInstanceSet) bool {
	_, present := role.Annotations[constants.ExcludedOrdinalsAnnotationKey]
	return present || set.Annotations[constants.CommittedSkipOrdinalsAnnotationKey] != ""
}

// reconcileSelectiveMembership commits replicas, record and template through
// one conditional writer while selective scaling is active. Ordinary SSA and its
// ownership handoff resume only after E and the sparse record are cleared.
// Conflicts return to the workqueue for a fresh parent/child read and plan.
func (r *RoleInstanceSetReconciler) reconcileSelectiveMembership(ctx context.Context, observed *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec, strategy *workloadsv1alpha2.RollingUpdate, revision string) error {
	parent, err := r.readSelectiveParent(ctx, observed, role)
	if err != nil {
		return err
	}
	old, exists, err := r.readSelectiveChild(ctx, parent, role)
	if err != nil {
		return err
	}
	next, err := planSelectiveMembership(old, exists, role)
	if err != nil {
		return err
	}
	record := ordinals.Record(next)
	desired, err := r.buildSelectiveTarget(ctx, parent, role, strategy, revision, old, record)
	if err != nil {
		return err
	}
	if !exists {
		return r.client.Create(ctx, desired, client.FieldOwner(utils.FieldManager))
	}
	updated := mergeSelectiveTarget(old, desired, record)
	if apiequality.Semantic.DeepEqual(old.Spec, updated.Spec) && apiequality.Semantic.DeepEqual(old.Annotations, updated.Annotations) &&
		apiequality.Semantic.DeepEqual(old.Labels, updated.Labels) && apiequality.Semantic.DeepEqual(old.OwnerReferences, updated.OwnerReferences) {
		return nil
	}
	patch, err := buildSelectivePatch(old, updated, record)
	if err != nil {
		return err
	}
	return r.client.Patch(ctx, updated, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(utils.FieldManager))
}

func (r *RoleInstanceSetReconciler) readSelectiveParent(ctx context.Context, observed *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec) (*workloadsv1alpha2.RoleBasedGroup, error) {
	parent := &workloadsv1alpha2.RoleBasedGroup{}
	if err := r.apiReader.Get(ctx, client.ObjectKeyFromObject(observed), parent); err != nil {
		return nil, err
	}
	// Status writes advance resourceVersion without changing role configuration.
	// UID and Generation reject replaced parents and stale spec-derived plans.
	if parent.UID != observed.UID || parent.Generation != observed.Generation || parent.DeletionTimestamp != nil {
		return nil, apierrors.NewConflict(workloadsv1alpha2.GroupVersion.WithResource("rolebasedgroups").GroupResource(), observed.Name, fmt.Errorf("role configuration changed before membership commitment"))
	}
	if err := workloadsv1alpha2.ValidateExcludedOrdinals(parent); err != nil {
		return nil, err
	}
	if pattern := role.Annotations[constants.RoleInstancePatternKey]; pattern != "" && pattern != string(constants.StatefulPattern) {
		return nil, fmt.Errorf("cannot change a sparse RIS to non-Stateful mode; fill committed gaps first")
	}
	return parent, nil
}

func (r *RoleInstanceSetReconciler) readSelectiveChild(ctx context.Context, parent *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec) (*workloadsv1alpha2.RoleInstanceSet, bool, error) {
	old := &workloadsv1alpha2.RoleInstanceSet{}
	key := client.ObjectKey{Namespace: parent.Namespace, Name: parent.GetWorkloadName(role)}
	if err := r.apiReader.Get(ctx, key, old); err != nil {
		if apierrors.IsNotFound(err) {
			return old, false, nil
		}
		return nil, false, err
	}
	if old.DeletionTimestamp != nil {
		return nil, false, fmt.Errorf("cannot commit membership of deleting RIS %s", key)
	}
	if pattern := old.Annotations[constants.RoleInstancePatternKey]; pattern != "" && pattern != string(constants.StatefulPattern) {
		return nil, false, fmt.Errorf("cannot use selective scaling on non-Stateful RIS %s", key)
	}
	if old.Annotations[constants.CommittedSkipOrdinalsAnnotationKey] != "" {
		// Never inherit sparse membership from an orphan or a previous parent.
		owner := metav1.GetControllerOf(old)
		if owner == nil || owner.UID != parent.UID || owner.Name != parent.Name || owner.Kind != "RoleBasedGroup" {
			return nil, false, fmt.Errorf("cannot commit membership of RIS %s: controller ownership does not match RBG UID", key)
		}
	} else if err := checkWorkloadClaimable(old, parent); err != nil {
		return nil, false, err
	}
	return old, true, nil
}

func planSelectiveMembership(old *workloadsv1alpha2.RoleInstanceSet, exists bool, role *workloadsv1alpha2.RoleSpec) ([]int, error) {
	// The effective role may include a coordination replica override calculated
	// from the same parent generation, while retaining the role exclusion input.
	exclusions, err := ordinals.Parse(role.Annotations[constants.ExcludedOrdinalsAnnotationKey])
	if err != nil {
		return nil, err
	}
	var members []int
	if exists {
		members, err = old.CommittedOrdinals()
		if err != nil {
			return nil, err
		}
	}
	return ordinals.Select(members, int(ptr.Deref(role.Replicas, 1)), exclusions)
}

func (r *RoleInstanceSetReconciler) buildSelectiveTarget(ctx context.Context, parent *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec, strategy *workloadsv1alpha2.RollingUpdate, revision string, old *workloadsv1alpha2.RoleInstanceSet, record string) (*workloadsv1alpha2.RoleInstanceSet, error) {
	config, err := r.constructRoleInstanceSetApplyConfiguration(ctx, parent, role, strategy, revision, old)
	if err != nil {
		return nil, err
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(config)
	if err != nil {
		return nil, err
	}
	desired := &workloadsv1alpha2.RoleInstanceSet{}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(obj, desired); err != nil {
		return nil, err
	}
	desired.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(parent, workloadsv1alpha2.GroupVersion.WithKind("RoleBasedGroup"))}
	if desired.Annotations == nil {
		desired.Annotations = map[string]string{}
	}
	delete(desired.Annotations, constants.ExcludedOrdinalsAnnotationKey)
	if record != "" {
		desired.Annotations[constants.CommittedSkipOrdinalsAnnotationKey] = record
	} else {
		delete(desired.Annotations, constants.CommittedSkipOrdinalsAnnotationKey)
	}
	return desired, nil
}

func mergeSelectiveTarget(old, desired *workloadsv1alpha2.RoleInstanceSet, record string) *workloadsv1alpha2.RoleInstanceSet {
	updated := old.DeepCopy()
	hasController := false
	for i, owner := range updated.OwnerReferences {
		if ptr.Deref(owner.Controller, false) || owner.UID == desired.OwnerReferences[0].UID {
			updated.OwnerReferences[i] = desired.OwnerReferences[0]
			hasController = true
		}
	}
	if !hasController {
		updated.OwnerReferences = append(updated.OwnerReferences, desired.OwnerReferences[0])
	}
	updated.Spec = desired.Spec
	// These child-level controls are not written by the RBG template builder.
	updated.Spec.ScaleStrategy = *old.Spec.ScaleStrategy.DeepCopy()
	updated.Spec.Lifecycle = old.Spec.Lifecycle.DeepCopy()
	updated.Spec.RevisionHistoryLimit = old.Spec.RevisionHistoryLimit
	if updated.Spec.PodManagementPolicy == "" {
		updated.Spec.PodManagementPolicy = constants.ParallelPodManagement
	}
	updated.Spec.UpdateStrategy.Type = workloadsv1alpha2.NormalizeUpdateStrategyType(updated.Spec.UpdateStrategy.Type)
	// Preserve annotations and labels written by other controllers, including
	// status/operational metadata that is not part of the role template.
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	removePreviouslyOwnedMetadata(old, updated, desired)
	for key, value := range desired.Annotations {
		updated.Annotations[key] = value
	}
	delete(updated.Annotations, constants.ExcludedOrdinalsAnnotationKey)
	if record == "" {
		delete(updated.Annotations, constants.CommittedSkipOrdinalsAnnotationKey)
	}
	if updated.Labels == nil {
		updated.Labels = map[string]string{}
	}
	for key, value := range desired.Labels {
		updated.Labels[key] = value
	}
	return updated
}

func buildSelectivePatch(old, updated *workloadsv1alpha2.RoleInstanceSet, record string) ([]byte, error) {
	// Include both target fields even when one value is unchanged. The resource
	// version precondition prevents applying this plan to a replacement UID too.
	patch, err := client.MergeFromWithOptions(old, client.MergeFromWithOptimisticLock{}).Data(updated)
	if err != nil {
		return nil, err
	}
	var content map[string]interface{}
	if err = json.Unmarshal(patch, &content); err != nil {
		return nil, err
	}
	spec, ok := content["spec"].(map[string]interface{})
	if !ok {
		spec = map[string]interface{}{}
		content["spec"] = spec
	}
	spec["replicas"] = *updated.Spec.Replicas
	metadata := content["metadata"].(map[string]interface{})
	annotations, ok := metadata["annotations"].(map[string]interface{})
	if !ok {
		annotations = map[string]interface{}{}
		metadata["annotations"] = annotations
	}
	if record == "" {
		annotations[constants.CommittedSkipOrdinalsAnnotationKey] = nil
	} else {
		annotations[constants.CommittedSkipOrdinalsAnnotationKey] = record
	}
	patch, err = json.Marshal(content)
	if err != nil {
		return nil, err
	}
	return patch, nil
}

// Retain ordinary synchronization's removal semantics without deleting metadata
// owned by other controllers. Managed fields record both legacy apply and our
// conditional patch writes; no additional annotation is needed for this.
func removePreviouslyOwnedMetadata(old, updated, desired *workloadsv1alpha2.RoleInstanceSet) {
	for _, entry := range old.ManagedFields {
		if entry.Manager != utils.FieldManager || entry.Subresource != "" || entry.FieldsV1 == nil {
			continue
		}
		var fields map[string]interface{}
		if json.Unmarshal(entry.FieldsV1.Raw, &fields) != nil {
			continue
		}
		metadata, _ := fields["f:metadata"].(map[string]interface{})
		for _, kind := range []string{"annotations", "labels"} {
			keys, _ := metadata["f:"+kind].(map[string]interface{})
			target, want := updated.Annotations, desired.Annotations
			if kind == "labels" {
				target, want = updated.Labels, desired.Labels
			}
			for owned := range keys {
				if len(owned) < 2 || owned[:2] != "f:" {
					continue
				}
				key := owned[2:]
				if key == constants.CommittedSkipOrdinalsAnnotationKey {
					continue
				}
				if _, exists := want[key]; !exists {
					delete(target, key)
				}
			}
		}
	}
}
