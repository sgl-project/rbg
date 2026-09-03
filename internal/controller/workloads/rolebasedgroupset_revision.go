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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/utils"
)

// systemManagedAnnotationKeys are the annotation keys controllers own on a child RoleBasedGroup.
// The RoleBasedGroup controller records discovery-config-mode on its own object; treating that as
// template drift would make the two controllers rewrite each other forever.
var systemManagedAnnotationKeys = map[string]bool{
	constants.DiscoveryConfigModeAnnotationKey: true,
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

func (r *RoleBasedGroupSetReconciler) ensureGroupSetRevision(
	ctx context.Context, set *workloadsv1alpha2.RoleBasedGroupSet,
	template *workloadsv1alpha2.RoleBasedGroupTemplateSpec,
) (*groupSetRevision, error) {
	template = normalizedGroupSetTemplate(template)
	data, err := json.Marshal(template)
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
	current, err := r.ensureGroupSetRevision(ctx, set, groupSetChildTemplate(oldest))
	return current, update, err
}

func newRBGForSetRevision(set *workloadsv1alpha2.RoleBasedGroupSet, index int, revision *groupSetRevision) *workloadsv1alpha2.RoleBasedGroup {
	templateSet := set.DeepCopy()
	templateSet.Spec.GroupTemplate = *revision.template.DeepCopy()
	child := newRBGForSet(templateSet, index)
	child.Labels[constants.GroupSetRevisionLabelKey] = revision.name
	return child
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
	for i := range revisions.Items {
		revision := &revisions.Items[i]
		if metav1.IsControlledBy(revision, set) && !protected[revision.Name] {
			unused = append(unused, revision)
		}
	}
	sort.Slice(unused, func(i, j int) bool {
		if unused[i].Revision == unused[j].Revision {
			return unused[i].Name < unused[j].Name
		}
		return unused[i].Revision < unused[j].Revision
	})
	for i := 0; i < len(unused)-utils.DefaultRevisionHistoryLimit; i++ {
		if err := r.client.Delete(ctx, unused[i]); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}
