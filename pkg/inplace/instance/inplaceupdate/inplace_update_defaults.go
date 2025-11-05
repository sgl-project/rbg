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
	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
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

	if opts.PatchSpecToInstance == nil {
		opts.PatchSpecToInstance = defaultPatchUpdateSpecToInstance
	}

	if opts.CheckInstanceUpdateCompleted == nil {
		opts.CheckInstanceUpdateCompleted = DefaultCheckInPlaceUpdateCompleted
	}

	if opts.CheckComponentUpdateCompleted == nil {
		opts.CheckComponentUpdateCompleted = defaultCheckInPlaceUpdateCompleted
	}

	return opts
}

// defaultPatchUpdateSpecToInstance returns new instance that merges spec into old instance
func defaultPatchUpdateSpecToInstance(instance *appsv1alpha1.Instance, spec *UpdateSpec, state *inplaceapi.InPlaceUpdateState) (*appsv1alpha1.Instance, error) {
	klog.V(5).Infof("Begin to in-place update instance %s/%s with update spec %v, state %v", instance.Namespace, instance.Name, util.DumpJSON(spec), util.DumpJSON(state))

	newInstanceSpec := &appsv1alpha1.InstanceSpec{}
	newBytes, _ := json.Marshal(spec.NewTemplate)
	if err := json.Unmarshal(newBytes, newInstanceSpec); err != nil {
		return nil, err
	}

	instance.Spec = *newInstanceSpec
	InjectVersionedInstanceSpec(instance)

	klog.V(5).Infof("Decide to in-place update instance %s/%s with state %v", instance.Namespace, instance.Name, util.DumpJSON(state))

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

// DefaultCheckInPlaceUpdateCompleted checks whether instance status has been changed since in-place update.
// If all pods owned by this instance are updated to new version, we consider this update is completed.
func DefaultCheckInPlaceUpdateCompleted(instance *appsv1alpha1.Instance) error {
	if instance.Status.ObservedGeneration < instance.Generation {
		return fmt.Errorf("waiting for instance %v generation consistent", klog.KObj(instance))
	}
	return defaultCheckInPlaceUpdateCompleted(instance)
}

func defaultCheckInPlaceUpdateCompleted(instance *appsv1alpha1.Instance) error {
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

func canNotInPlaceUpdate(newTemplate, oldTemplate *appsv1alpha1.InstanceTemplate) bool {
	return componentSizeChanges(newTemplate.Components, oldTemplate.Components)
}

func componentSizeChanges(oldComponents, newComponents []appsv1alpha1.InstanceComponent) bool {
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
