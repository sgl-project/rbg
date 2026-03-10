/*
Copyright 2025.

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

package inplaceupdate

import (
	"encoding/json"
	"fmt"

	apps "k8s.io/api/apps/v1"
	"k8s.io/klog/v2"

	inplaceapi "sigs.k8s.io/rbgs/api/workloads/inplaceupdate/instance"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	inplaceutil "sigs.k8s.io/rbgs/pkg/inplace/instance"
	util "sigs.k8s.io/rbgs/pkg/utils"
)

func SetOptionsDefaults(opts *UpdateOptions) *UpdateOptions {
	if opts == nil {
		opts = &UpdateOptions{}
	}

	if opts.CalculateSpec == nil {
		opts.CalculateSpec = defaultCalculateInPlaceUpdateSpec
	}

	if opts.PatchSpecToRoleInstance == nil {
		opts.PatchSpecToRoleInstance = defaultPatchUpdateSpecToRoleInstance
	}

	if opts.CheckRoleInstanceUpdateCompleted == nil {
		opts.CheckRoleInstanceUpdateCompleted = DefaultCheckInPlaceUpdateCompleted
	}

	if opts.CheckComponentUpdateCompleted == nil {
		opts.CheckComponentUpdateCompleted = defaultCheckInPlaceUpdateCompleted
	}

	return opts
}

// defaultPatchUpdateSpecToRoleInstance returns new role instance that merges spec into old role instance
func defaultPatchUpdateSpecToRoleInstance(instance *workloadsv1alpha2.RoleInstance, spec *UpdateSpec, state *inplaceapi.InPlaceUpdateState) (*workloadsv1alpha2.RoleInstance, error) {
	klog.V(5).Infof("Begin to in-place update role instance %s/%s with update spec %v, state %v", instance.Namespace, instance.Name, util.DumpJSON(spec), util.DumpJSON(state))

	newRoleInstanceSpec := &workloadsv1alpha2.RoleInstanceSpec{}
	newBytes, _ := json.Marshal(spec.NewTemplate)
	if err := json.Unmarshal(newBytes, newRoleInstanceSpec); err != nil {
		return nil, err
	}

	instance.Spec = *newRoleInstanceSpec
	InjectVersionedRoleInstanceSpec(instance)

	klog.V(5).Infof("Decide to in-place update role instance %s/%s with state %v", instance.Namespace, instance.Name, util.DumpJSON(state))

	inPlaceUpdateStateJSON, _ := json.Marshal(state)
	instance.Annotations[inplaceapi.InPlaceUpdateStateKey] = string(inPlaceUpdateStateJSON)
	return instance, nil
}

// defaultCalculateInPlaceUpdateSpec calculates diff between old and update revisions.
// If the diff just contains replace operation of spec.containers[x].image, it will returns an UpdateSpec.
// Otherwise, it returns nil which means can not use in-place update.
func defaultCalculateInPlaceUpdateSpec(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) *UpdateSpec {
	if oldRevision == nil || newRevision == nil {
		return nil
	}
	opts = SetOptionsDefaults(opts)

	oldTemp, err := GetTemplateFromRevision(oldRevision)
	if err != nil {
		return nil
	}
	newTemp, err := GetTemplateFromRevision(newRevision)
	if err != nil {
		return nil
	}

	// some fields cannot be in-place update, fallback to ReCreate.
	if canNotInPlaceUpdate(newTemp, oldTemp) {
		return nil
	}

	updateSpec := &UpdateSpec{
		Revision:    newRevision.Name,
		OldTemplate: oldTemp,
		NewTemplate: newTemp,
	}
	if opts.GetRevision != nil {
		updateSpec.Revision = opts.GetRevision(newRevision)
	}
	return updateSpec
}

// DefaultCheckInPlaceUpdateCompleted checks whether role instance status has been changed since in-place update.
// If all pods owned by this role instance are updated to new version, we consider this update is completed.
func DefaultCheckInPlaceUpdateCompleted(instance *workloadsv1alpha2.RoleInstance) error {
	if instance.Status.ObservedGeneration < instance.Generation {
		return fmt.Errorf("waiting for role instance %v generation consistent", klog.KObj(instance))
	}
	return defaultCheckInPlaceUpdateCompleted(instance)
}

func defaultCheckInPlaceUpdateCompleted(instance *workloadsv1alpha2.RoleInstance) error {
	if len(instance.Spec.Components) != len(instance.Status.ComponentStatuses) {
		return fmt.Errorf("waiting for role status")
	}
	componentSize := make(map[string]int32, len(instance.Spec.Components))
	for _, component := range instance.Spec.Components {
		componentSize[component.Name] = inplaceutil.GetComponentSize(&component)
	}
	for _, status := range instance.Status.ComponentStatuses {
		replicas, ok := componentSize[status.Name]
		if !ok {
			return fmt.Errorf("waiting for role status")
		}
		if status.UpdatedReplicas != replicas {
			return fmt.Errorf("waiting for role updated")
		}
	}
	return nil
}

func canNotInPlaceUpdate(newTemplate, oldTemplate *workloadsv1alpha2.RoleInstanceTemplate) bool {
	return componentSizeChanges(newTemplate.Components, oldTemplate.Components)
}

func componentSizeChanges(oldComponents, newComponents []workloadsv1alpha2.RoleInstanceComponent) bool {
	if len(oldComponents) != len(newComponents) {
		return true
	}
	oldComponentSize := make(map[string]int32, len(oldComponents))
	for _, component := range oldComponents {
		oldComponentSize[component.Name] = inplaceutil.GetComponentSize(&component)
	}
	for _, component := range newComponents {
		size, ok := oldComponentSize[component.Name]
		if !ok || size != inplaceutil.GetComponentSize(&component) {
			return true
		}
	}
	return false
}
