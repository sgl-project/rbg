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

package core

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/appscode/jsonpatch"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/kubernetes/pkg/apis/core/validation"
	"k8s.io/utils/integer"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	inplaceutil "sigs.k8s.io/rbgs/pkg/inplace/instance"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
	portallocator "sigs.k8s.io/rbgs/pkg/port-allocator"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statelessmode/utils"
)

const (
	shortNameLimitation = 63
)

var (
	inPlaceUpdateTemplateSpecPatchRexp = regexp.MustCompile("/spec/template/spec/components/[0-9]+/size")
)

type commonControl struct {
	*workloadsv1alpha2.RoleInstanceSet
}

var _ Control = &commonControl{}

func (c *commonControl) IsInitializing() bool {
	return false
}

func (c *commonControl) Selector() labels.Selector {
	return labels.SelectorFromSet(map[string]string{
		constants.RoleInstanceOwnerLabelKey: string(c.UID),
	})
}

func (c *commonControl) SetRevisionTemplate(revisionSpec map[string]interface{}, template map[string]interface{}) {
	revisionSpec["roleInstanceTemplate"] = template
	template["$patch"] = "replace"
}

func (c *commonControl) ApplyRevisionPatch(patched []byte) (*workloadsv1alpha2.RoleInstanceSet, error) {
	restoredSet := &workloadsv1alpha2.RoleInstanceSet{}
	if err := json.Unmarshal(patched, restoredSet); err != nil {
		return nil, err
	}
	return restoredSet, nil
}

func (c *commonControl) IsReadyToScale() bool {
	return true
}

func (c *commonControl) NewVersionedInstances(currentSet, updateSet *workloadsv1alpha2.RoleInstanceSet,
	currentRevision, updateRevision string,
	expectedCreations, expectedCurrentCreations int,
	availableIDs []string,
) ([]*workloadsv1alpha2.RoleInstance, error) {
	var newInstances []*workloadsv1alpha2.RoleInstance
	if expectedCreations <= expectedCurrentCreations {
		newInstances = c.newVersionedInstances(currentSet, currentRevision, expectedCreations, &availableIDs)
	} else {
		newInstances = c.newVersionedInstances(currentSet, currentRevision, expectedCurrentCreations, &availableIDs)
		newInstances = append(newInstances, c.newVersionedInstances(updateSet, updateRevision, expectedCreations-expectedCurrentCreations, &availableIDs)...)
	}
	return newInstances, nil
}

func (c *commonControl) newVersionedInstances(set *workloadsv1alpha2.RoleInstanceSet, revision string, replicas int, availableIDs *[]string) []*workloadsv1alpha2.RoleInstance {
	var newInstances []*workloadsv1alpha2.RoleInstance
	for i := 0; i < replicas; i++ {
		if len(*availableIDs) == 0 {
			return newInstances
		}
		id := (*availableIDs)[0]
		*availableIDs = (*availableIDs)[1:]

		instance, _ := GenInstanceFromTemplate(&set.Spec.RoleInstanceTemplate, set, metav1.NewControllerRef(set, utils.ControllerKind))
		c.injectNewVersionedInstances(instance, set, revision, id)
		newInstances = append(newInstances, instance)
	}
	return newInstances
}

func (c *commonControl) injectNewVersionedInstances(instance *workloadsv1alpha2.RoleInstance, set *workloadsv1alpha2.RoleInstanceSet, revision string, id string) {
	// inject metadata
	instance.Name = generateInstanceName(set.Name, id)
	instance.Namespace = set.Namespace
	if instance.Labels == nil {
		instance.Labels = make(map[string]string)
	}
	instance.Labels[constants.RoleInstanceIDLabelKey] = id
	instance.Labels[constants.RoleInstanceOwnerLabelKey] = string(set.UID)
	utils.WriteRevisionHash(instance, revision)

	// inject spec
	inplaceupdate.InjectVersionedRoleInstanceSpec(instance)

	portallocator.AllocatePortsForInstance(instance, set)
}

func (c *commonControl) IsInstanceUpdatePaused(instance *workloadsv1alpha2.RoleInstance) bool {
	return false
}

func (c *commonControl) IsInstanceUpdateReady(instance *workloadsv1alpha2.RoleInstance, minReadySeconds int32) bool {
	if !utils.IsRunningAndAvailable(instance, minReadySeconds) {
		return false
	}
	condition := inplaceutil.GetRoleInstanceCondition(instance, workloadsv1alpha2.RoleInstanceReady)
	if condition != nil && condition.Status != v1.ConditionTrue {
		return false
	}
	return true
}

func (c *commonControl) GetInstancesSortFunc(instances []*workloadsv1alpha2.RoleInstance, waitUpdateIndexes []int) func(i, j int) bool {
	// not-ready < ready, unscheduled < scheduled, and pending < running
	return func(i, j int) bool {
		return utils.ActiveInstancesAvailableRank{
			Instances:     instances,
			AvailableFunc: func(instance *workloadsv1alpha2.RoleInstance) bool { return c.IsInstanceUpdateReady(instance, 0) },
		}.Less(waitUpdateIndexes[i], waitUpdateIndexes[j])
	}
}

func (c *commonControl) GetUpdateOptions() *inplaceupdate.UpdateOptions {
	opts := &inplaceupdate.UpdateOptions{}
	if c.Spec.UpdateStrategy.InPlaceUpdateStrategy != nil {
		opts.GracePeriodSeconds = c.Spec.UpdateStrategy.InPlaceUpdateStrategy.GracePeriodSeconds
	}
	return opts
}

func (c *commonControl) ValidateInstanceSetUpdate(oldSet, newSet *workloadsv1alpha2.RoleInstanceSet) error {
	if newSet.Spec.UpdateStrategy.Type != workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType {
		return nil
	}

	oldTempJSON, _ := json.Marshal(oldSet.Spec.RoleInstanceTemplate.RoleInstanceSpec)
	newTempJSON, _ := json.Marshal(newSet.Spec.RoleInstanceTemplate.RoleInstanceSpec)
	patches, err := jsonpatch.CreatePatch(oldTempJSON, newTempJSON)
	if err != nil {
		return fmt.Errorf("failed calculate patches between old/new template spec")
	}

	for _, p := range patches {
		if p.Operation == "replace" && inPlaceUpdateTemplateSpecPatchRexp.MatchString(p.Path) {
			return fmt.Errorf("do not allowed to update component size in spec for %s, but found %s %s",
				workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType, p.Operation, p.Path)
		}
	}
	return nil
}

func (c *commonControl) ExtraStatusCalculation(status *workloadsv1alpha2.RoleInstanceSetStatus, instances []*workloadsv1alpha2.RoleInstance) error {
	return nil
}

func generateInstanceName(prefix, id string) string {
	maxPrefixLen := integer.IntMax(integer.IntMin(len(prefix), shortNameLimitation-len(id)-1), 0)
	return fmt.Sprintf("%s-%s", prefix[:maxPrefixLen], id)
}

func GenInstanceFromTemplate(template *workloadsv1alpha2.RoleInstanceTemplate, set *workloadsv1alpha2.RoleInstanceSet, controllerRef *metav1.OwnerReference) (*workloadsv1alpha2.RoleInstance, error) {
	desiredLabels := genInstanceLabelSet(set)
	desiredFinalizers := genInstanceFinalizers(set)
	accessor, err := meta.Accessor(set)
	if err != nil {
		return nil, fmt.Errorf("parentObject does not have ObjectMeta, %v", err)
	}
	prefix := genInstanceNamePrefix(accessor.GetName())

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Labels:       desiredLabels,
			GenerateName: prefix,
			Finalizers:   desiredFinalizers,
		},
	}
	if controllerRef != nil {
		instance.OwnerReferences = append(instance.OwnerReferences, *controllerRef)
	}
	instance.Spec = *template.RoleInstanceSpec.DeepCopy()
	// Propagate gang-scheduling annotation from RoleInstanceSet to RoleInstance so that
	// the instance controller can enforce gang-scheduling constraints (e.g. orphan-pod check,
	// atomic pod recreation) without access to the parent RBG.
	if v, ok := set.Annotations[constants.RoleInstanceGangSchedulingAnnotationKey]; ok {
		if instance.Annotations == nil {
			instance.Annotations = make(map[string]string)
		}
		instance.Annotations[constants.RoleInstanceGangSchedulingAnnotationKey] = v
	}
	return instance, nil
}

func genInstanceLabelSet(set *workloadsv1alpha2.RoleInstanceSet) labels.Set {
	desiredLabels := make(labels.Set)
	for k, v := range set.Labels {
		desiredLabels[k] = v
	}
	return desiredLabels
}

func genInstanceFinalizers(set *workloadsv1alpha2.RoleInstanceSet) []string {
	desiredFinalizers := make([]string, len(set.Finalizers))
	copy(desiredFinalizers, set.Finalizers)
	return desiredFinalizers
}

func genInstanceNamePrefix(controllerName string) string {
	// use the dash (if the name isn't too long) to make the instance name a bit prettier
	prefix := fmt.Sprintf("%s-", controllerName)
	if len(validation.ValidatePodName(prefix, true)) != 0 {
		prefix = controllerName
	}
	return prefix
}
