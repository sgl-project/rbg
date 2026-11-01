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

// reconcileSelectiveMembership is the only writer of replicas and skip records
// while selective scaling is in use. It also updates the ordinary template in
// the same conditional request, avoiding an SSA ownership handoff entirely.
// A conflict is returned to the workqueue, which rereads both parent and child;
// it must never retry an old plan against a new child resource version.
func (r *RoleInstanceSetReconciler) reconcileSelectiveMembership(ctx context.Context, observed *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec, strategy *workloadsv1alpha2.RollingUpdate, revision string) error {
	parent := &workloadsv1alpha2.RoleBasedGroup{}
	if err := r.apiReader.Get(ctx, client.ObjectKeyFromObject(observed), parent); err != nil {
		return err
	}
	if parent.UID != observed.UID || parent.ResourceVersion != observed.ResourceVersion || parent.DeletionTimestamp != nil {
		return apierrors.NewConflict(workloadsv1alpha2.GroupVersion.WithResource("rolebasedgroups").GroupResource(), observed.Name, fmt.Errorf("role configuration changed before membership commitment"))
	}
	if err := workloadsv1alpha2.ValidateExcludedOrdinals(parent); err != nil {
		return err
	}
	if pattern := role.Annotations[constants.RoleInstancePatternKey]; pattern != "" && pattern != string(constants.StatefulPattern) {
		return fmt.Errorf("cannot change a sparse RIS to non-Stateful mode; fill committed gaps first")
	}
	// role may contain the existing coordination scaling override; it was
	// calculated from this exact parent version and retains that role's E.
	exclusions, err := ordinals.Parse(role.Annotations[constants.ExcludedOrdinalsAnnotationKey])
	if err != nil {
		return err
	}
	old := &workloadsv1alpha2.RoleInstanceSet{}
	key := client.ObjectKey{Namespace: parent.Namespace, Name: parent.GetWorkloadName(role)}
	err = r.apiReader.Get(ctx, key, old)
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	var members []int
	if exists {
		// Stateful committed records must not be inherited from an orphan or a
		// previous parent UID, even when ordinary workload adoption is possible.
		owner := metav1.GetControllerOf(old)
		if owner == nil || owner.UID != parent.UID || owner.Name != parent.Name || owner.Kind != "RoleBasedGroup" {
			return fmt.Errorf("cannot commit membership of RIS %s: controller ownership does not match RBG UID", key)
		}
		if old.DeletionTimestamp != nil {
			return fmt.Errorf("cannot commit membership of deleting RIS %s", key)
		}
		if pattern := old.Annotations[constants.RoleInstancePatternKey]; pattern != "" && pattern != string(constants.StatefulPattern) {
			return fmt.Errorf("cannot use selective scaling on non-Stateful RIS %s", key)
		}
		members, err = old.CommittedOrdinals()
		if err != nil {
			return err
		}
	}
	next, err := ordinals.Select(members, int(ptr.Deref(role.Replicas, 1)), exclusions)
	if err != nil {
		return err
	}
	record := ordinals.Record(next)
	config, err := r.constructRoleInstanceSetApplyConfiguration(ctx, parent, role, strategy, revision, old)
	if err != nil {
		return err
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(config)
	if err != nil {
		return err
	}
	desired := &workloadsv1alpha2.RoleInstanceSet{}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(obj, desired); err != nil {
		return err
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
	if !exists {
		// Creation establishes the count, target and template in a single request.
		return r.client.Create(ctx, desired, client.FieldOwner(utils.FieldManager))
	}
	updated := old.DeepCopy()
	for i := range updated.OwnerReferences {
		if ptr.Deref(updated.OwnerReferences[i].Controller, false) {
			updated.OwnerReferences[i] = desired.OwnerReferences[0]
		}
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
	if apiequality.Semantic.DeepEqual(old.Spec, updated.Spec) && apiequality.Semantic.DeepEqual(old.Annotations, updated.Annotations) && apiequality.Semantic.DeepEqual(old.Labels, updated.Labels) && apiequality.Semantic.DeepEqual(old.OwnerReferences, updated.OwnerReferences) {
		return nil
	}
	// Include both target fields even when one value is unchanged. The resource
	// version precondition prevents applying this plan to a replacement UID too.
	patch, err := client.MergeFromWithOptions(old, client.MergeFromWithOptimisticLock{}).Data(updated)
	if err != nil {
		return err
	}
	var content map[string]interface{}
	if err = json.Unmarshal(patch, &content); err != nil {
		return err
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
		return err
	}
	return r.client.Patch(ctx, updated, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(utils.FieldManager))
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
