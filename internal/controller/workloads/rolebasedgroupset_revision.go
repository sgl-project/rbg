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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/utils"
)

// These keys do not affect revision identity; PodGroupPolicy is still snapshotted with its effective configuration.
var systemManagedAnnotationKeys = map[string]bool{
	constants.DiscoveryConfigModeAnnotationKey:               true,
	constants.RolloutRequestAnnotationKey:                    true,
	constants.ConversionV1alpha1CoordinationAnnotationKey:    true,
	constants.ConversionV1alpha1PodGroupPolicyAnnotationKey:  true,
	constants.ConversionV1alpha2RolloutStrategyAnnotationKey: true,
	constants.ConversionV1alpha2RolloutStatusAnnotationKey:   true,
}

// liveTemplateAnnotation reports whether a key is copied from the live template to every child
// regardless of the revision the child is pinned to.
func liveTemplateAnnotation(key string) bool {
	return systemManagedAnnotationKeys[key] &&
		key != constants.ConversionV1alpha1PodGroupPolicyAnnotationKey &&
		key != constants.RolloutRequestAnnotationKey
}

type groupSetRevision struct {
	name     string
	template *workloadsv1alpha2.RoleBasedGroupTemplateSpec
}

func normalizedGroupSetTemplate(template *workloadsv1alpha2.RoleBasedGroupTemplateSpec) *workloadsv1alpha2.RoleBasedGroupTemplateSpec {
	result := template.DeepCopy()
	normalizeRolloutUpdateTypes(result.Spec.Roles)
	sort.Slice(result.Spec.Roles, func(i, j int) bool { return result.Spec.Roles[i].Name < result.Spec.Roles[j].Name })
	sort.Slice(result.Spec.RoleTemplates, func(i, j int) bool {
		return result.Spec.RoleTemplates[i].Name < result.Spec.RoleTemplates[j].Name
	})
	delete(result.Labels, constants.GroupSetNameLabelKey)
	delete(result.Labels, constants.GroupSetIndexLabelKey)
	delete(result.Labels, constants.GroupSetRevisionLabelKey)
	for k := range systemManagedAnnotationKeys {
		delete(result.Annotations, k)
	}
	return result
}

func groupSetChildTemplate(rbg *workloadsv1alpha2.RoleBasedGroup) *workloadsv1alpha2.RoleBasedGroupTemplateSpec {
	return normalizedGroupSetTemplate(&workloadsv1alpha2.RoleBasedGroupTemplateSpec{
		Labels: rbg.Labels, Annotations: rbg.Annotations, Spec: rbg.Spec,
	})
}

func groupSetTemplatesEqual(left, right *workloadsv1alpha2.RoleBasedGroupTemplateSpec) bool {
	leftData, leftErr := json.Marshal(normalizedGroupSetTemplate(left))
	rightData, rightErr := json.Marshal(normalizedGroupSetTemplate(right))
	return leftErr == nil && rightErr == nil && bytes.Equal(leftData, rightData)
}

func (r *RoleBasedGroupSetReconciler) groupSetMatchesRevision(rbg *workloadsv1alpha2.RoleBasedGroup, revision *groupSetRevision) bool {
	if name := rbg.Labels[constants.GroupSetRevisionLabelKey]; name != "" && name != revision.name {
		return false
	}
	return r.rolesEqual(rbg.Spec.Roles, revision.template.Spec.Roles) &&
		groupSetTemplatesEqual(groupSetChildTemplate(rbg), revision.template)
}

func groupSetOnlyReplicasChanged(rbg *workloadsv1alpha2.RoleBasedGroup, template *workloadsv1alpha2.RoleBasedGroupTemplateSpec) bool {
	left, right := groupSetChildTemplate(rbg), template.DeepCopy()
	for i := range left.Spec.Roles {
		left.Spec.Roles[i].Replicas = nil
	}
	for i := range right.Spec.Roles {
		right.Spec.Roles[i].Replicas = nil
	}
	return groupSetTemplatesEqual(left, right)
}

// canonicalGroupSetTemplateJSON serializes the template the way the API server re-serializes an
// object it merges: sorted keys and HTML escaping. ControllerRevision data is immutable and
// compared byte-wise, so storing any other form makes every strategic-merge patch of the revision
// fail validation -- including the ones garbage collection uses to orphan dependents.
func canonicalGroupSetTemplateJSON(template *workloadsv1alpha2.RoleBasedGroupTemplateSpec) ([]byte, error) {
	encoded, err := json.Marshal(template)
	if err != nil {
		return nil, err
	}
	var generic interface{}
	// Preserve integers as int64, exactly like the API server does before re-encoding.
	if err := utiljson.Unmarshal(encoded, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}

func (r *RoleBasedGroupSetReconciler) ensureGroupSetRevision(
	ctx context.Context, set *workloadsv1alpha2.RoleBasedGroupSet,
	template *workloadsv1alpha2.RoleBasedGroupTemplateSpec,
) (*groupSetRevision, error) {
	policy, hasPolicy := template.Annotations[constants.ConversionV1alpha1PodGroupPolicyAnnotationKey]
	template = normalizedGroupSetTemplate(template)
	data, err := canonicalGroupSetTemplateJSON(template)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(append([]byte(string(set.UID)+"\x00"), data...))
	name := fmt.Sprintf("rbgs-%x", hash[:24])
	revision := &appsv1.ControllerRevision{}
	err = r.apiReader.Get(ctx, client.ObjectKey{Namespace: set.Namespace, Name: name}, revision)
	if apierrors.IsNotFound(err) {
		revision = &appsv1.ControllerRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: set.Namespace,
				Labels: map[string]string{constants.GroupSetNameLabelKey: set.Name},
			},
			Data: runtime.RawExtension{Raw: data}, Revision: set.Generation,
		}
		if hasPolicy {
			revision.Annotations = map[string]string{constants.ConversionV1alpha1PodGroupPolicyAnnotationKey: policy}
		}
		if err = controllerutil.SetControllerReference(set, revision, r.scheme); err != nil {
			return nil, err
		}
		err = r.client.Create(ctx, revision)
		if apierrors.IsAlreadyExists(err) {
			err = r.apiReader.Get(ctx, client.ObjectKeyFromObject(revision), revision)
		}
	}
	if err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(revision, set) || !bytes.Equal(revision.Data.Raw, data) {
		return nil, fmt.Errorf("ControllerRevision %s does not match RoleBasedGroupSet %s", name, set.Name)
	}
	key := constants.ConversionV1alpha1PodGroupPolicyAnnotationKey
	if _, stored := revision.Annotations[key]; stored && !hasPolicy {
		children, err := r.listRollingGroupSetChildren(ctx, set)
		if err != nil {
			return nil, err
		}
		pendingRemoval := false
		for _, child := range children.Items {
			if _, present := child.Annotations[key]; present && child.Labels[constants.GroupSetRevisionLabelKey] == name {
				pendingRemoval = true
				break
			}
		}
		// Retain removal provenance until every child using this revision has dropped the policy.
		if !pendingRemoval {
			base := revision.DeepCopy()
			delete(revision.Annotations, key)
			if err := r.client.Patch(ctx, revision, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
				return nil, err
			}
		}
	}
	if hasPolicy {
		if stored, exists := revision.Annotations[key]; !exists || stored != policy {
			base := revision.DeepCopy()
			if revision.Annotations == nil {
				revision.Annotations = make(map[string]string)
			}
			revision.Annotations[key] = policy
			if err := r.client.Patch(ctx, revision, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
				return nil, err
			}
		}
		if template.Annotations == nil {
			template.Annotations = make(map[string]string)
		}
		template.Annotations[key] = policy
	}
	return &groupSetRevision{name: name, template: template}, nil
}

func (r *RoleBasedGroupSetReconciler) groupSetRevisions(
	ctx context.Context, set *workloadsv1alpha2.RoleBasedGroupSet, children *workloadsv1alpha2.RoleBasedGroupList,
) (*groupSetRevision, *groupSetRevision, error) {
	update, err := r.ensureGroupSetRevision(ctx, set, &set.Spec.GroupTemplate)
	if err != nil {
		return nil, nil, err
	}
	if set.Status.CurrentRevision != "" {
		revision := &appsv1.ControllerRevision{}
		if err := r.apiReader.Get(ctx, client.ObjectKey{Namespace: set.Namespace, Name: set.Status.CurrentRevision}, revision); err != nil {
			return nil, nil, err
		}
		if !metav1.IsControlledBy(revision, set) {
			return nil, nil, fmt.Errorf("ControllerRevision %s is not owned by RoleBasedGroupSet %s", revision.Name, set.Name)
		}
		template := &workloadsv1alpha2.RoleBasedGroupTemplateSpec{}
		if err := json.Unmarshal(revision.Data.Raw, template); err != nil {
			return nil, nil, err
		}
		if policy, exists := revision.Annotations[constants.ConversionV1alpha1PodGroupPolicyAnnotationKey]; exists {
			if template.Annotations == nil {
				template.Annotations = make(map[string]string)
			}
			template.Annotations[constants.ConversionV1alpha1PodGroupPolicyAnnotationKey] = policy
		}
		return &groupSetRevision{name: revision.Name, template: template}, update, nil
	}

	// Legacy children have no revision label; preserve their template before replacing any of them.
	var oldest *workloadsv1alpha2.RoleBasedGroup
	for i := range children.Items {
		child := &children.Items[i]
		index, valid := groupSetOrdinal(set, child)
		if !valid || index >= int(*set.Spec.Replicas) {
			continue
		}
		if oldest == nil {
			oldest = child
		} else if oldIndex, _ := groupSetOrdinal(set, oldest); index < oldIndex {
			oldest = child
		}
	}
	if oldest == nil {
		return update, update, nil
	}
	current, err := r.ensureGroupSetRevision(ctx, set, &workloadsv1alpha2.RoleBasedGroupTemplateSpec{
		Labels: oldest.Labels, Annotations: oldest.Annotations, Spec: oldest.Spec,
	})
	return current, update, err
}

func newRBGForSetRevision(set *workloadsv1alpha2.RoleBasedGroupSet, index int, revision *groupSetRevision) *workloadsv1alpha2.RoleBasedGroup {
	templateSet := set.DeepCopy()
	templateSet.Spec.GroupTemplate = *revision.template.DeepCopy()
	child := newRBGForSet(templateSet, index)
	// Only unversioned keys come from the live template; held revisions retain their user metadata.
	for key, value := range set.Spec.GroupTemplate.Annotations {
		if liveTemplateAnnotation(key) {
			if child.Annotations == nil {
				child.Annotations = make(map[string]string)
			}
			child.Annotations[key] = value
		}
	}
	child.Labels[constants.GroupSetRevisionLabelKey] = revision.name
	return child
}

func (r *RoleBasedGroupSetReconciler) syncGroupSetPodGroupPolicy(
	ctx context.Context, set *workloadsv1alpha2.RoleBasedGroupSet,
	child *workloadsv1alpha2.RoleBasedGroup, template *workloadsv1alpha2.RoleBasedGroupTemplateSpec,
) (bool, error) {
	key := constants.ConversionV1alpha1PodGroupPolicyAnnotationKey
	policy, desired := template.Annotations[key]
	oldPolicy, present := child.Annotations[key]
	if desired {
		if present && oldPolicy == policy {
			return false, nil
		}
		if child.Annotations == nil {
			child.Annotations = make(map[string]string)
		}
		child.Annotations[key] = policy
		return true, nil
	}
	if !present || child.Labels[constants.GroupSetRevisionLabelKey] == "" {
		return false, nil
	}
	previous := &appsv1.ControllerRevision{}
	if err := r.apiReader.Get(ctx, client.ObjectKey{Namespace: set.Namespace, Name: child.Labels[constants.GroupSetRevisionLabelKey]}, previous); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	// Only a parent-snapshotted policy may be removed; child-only conversion stashes are not ours.
	if _, managed := previous.Annotations[key]; managed && metav1.IsControlledBy(previous, set) {
		delete(child.Annotations, key)
		return true, nil
	}
	return false, nil
}

func (r *RoleBasedGroupSetReconciler) pruneGroupSetRevisions(
	ctx context.Context, set *workloadsv1alpha2.RoleBasedGroupSet, children *workloadsv1alpha2.RoleBasedGroupList,
) error {
	var revisions appsv1.ControllerRevisionList
	if err := r.apiReader.List(ctx, &revisions, client.InNamespace(set.Namespace),
		client.MatchingLabels{constants.GroupSetNameLabelKey: set.Name}); err != nil {
		return err
	}
	protected := map[string]bool{set.Status.CurrentRevision: true, set.Status.UpdateRevision: true}
	for _, child := range children.Items {
		protected[child.Labels[constants.GroupSetRevisionLabelKey]] = true
	}
	var unused []*appsv1.ControllerRevision
	parentChecked := false
	for i := range revisions.Items {
		revision := &revisions.Items[i]
		if !revision.DeletionTimestamp.IsZero() {
			continue
		}
		if metav1.GetControllerOfNoCopy(revision) == nil {
			if !parentChecked {
				fresh := &workloadsv1alpha2.RoleBasedGroupSet{}
				if err := r.apiReader.Get(ctx, client.ObjectKeyFromObject(set), fresh); err != nil {
					return err
				}
				if fresh.UID != set.UID || !fresh.DeletionTimestamp.IsZero() || !set.DeletionTimestamp.IsZero() {
					return fmt.Errorf("cannot adopt ControllerRevisions for RoleBasedGroupSet %s/%s: parent was replaced or is deleting", set.Namespace, set.Name)
				}
				parentChecked = true
			}
			base := revision.DeepCopy()
			if err := controllerutil.SetControllerReference(set, revision, r.scheme); err != nil {
				return err
			}
			if err := r.client.Patch(ctx, revision, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return err
			}
		}
		if metav1.IsControlledBy(revision, set) && !protected[revision.Name] {
			unused = append(unused, revision)
		}
	}
	// Parent generations reset on recreation, so revision numbers cannot order adopted history.
	sort.Slice(unused, func(i, j int) bool {
		if !unused[i].CreationTimestamp.Equal(&unused[j].CreationTimestamp) {
			return unused[i].CreationTimestamp.Before(&unused[j].CreationTimestamp)
		}
		if unused[i].Revision == unused[j].Revision {
			return unused[i].Name < unused[j].Name
		}
		return unused[i].Revision < unused[j].Revision
	})
	for i := 0; i < len(unused)-utils.DefaultRevisionHistoryLimit; i++ {
		revision := unused[i]
		if err := r.client.Delete(ctx, revision, client.Preconditions{UID: &revision.UID, ResourceVersion: &revision.ResourceVersion}); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}
