/*
Copyright 2026 The RBG Authors.
Copyright 2019 The Kruise Authors.
Copyright 2016 The Kubernetes Authors.

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

package statefulmode

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

// statefulInstanceRegex is a regular expression that extracts the parent InstanceSet and ordinal from the Name of an Instance
var statefulInstanceRegex = regexp.MustCompile("(.*)-([0-9]+)$")

// getParentNameAndOrdinal gets the name of instance's parent InstanceSet and instance's ordinal as extracted from its Name.
func getParentNameAndOrdinal(instance *workloadsv1alpha1.Instance) (string, int) {
	parent := ""
	ordinal := -1
	subMatches := statefulInstanceRegex.FindStringSubmatch(instance.Name)
	if len(subMatches) < 3 {
		return parent, ordinal
	}
	parent = subMatches[1]
	if i, err := strconv.ParseInt(subMatches[2], 10, 32); err == nil {
		ordinal = int(i)
	}
	return parent, ordinal
}

// getParentName gets the name of instance's parent InstanceSet.
func getParentName(instance *workloadsv1alpha1.Instance) string {
	parent, _ := getParentNameAndOrdinal(instance)
	return parent
}

// getOrdinal gets instance's ordinal. If instance has no ordinal, -1 is returned.
func getOrdinal(instance *workloadsv1alpha1.Instance) int {
	_, ordinal := getParentNameAndOrdinal(instance)
	return ordinal
}

// instanceInOrdinalRange determines if the given instance's ordinal number is within the permissible range
func instanceInOrdinalRange(instance *workloadsv1alpha1.Instance, set *workloadsv1alpha1.InstanceSet) bool {
	startOrdinal, endOrdinal, reserveOrdinals := getInstanceSetReplicasRange(set)
	return instanceInOrdinalRangeWithParams(instance, startOrdinal, endOrdinal, reserveOrdinals)
}

func instanceInOrdinalRangeWithParams(instance *workloadsv1alpha1.Instance, startOrdinal, endOrdinal int, reserveOrdinals sets.Set[int]) bool {
	ordinal := getOrdinal(instance)
	return ordinal >= startOrdinal && ordinal < endOrdinal &&
		!reserveOrdinals.Has(ordinal)
}

// getInstanceName gets the name of set's child Instance with an ordinal index of ordinal
func getInstanceName(set *workloadsv1alpha1.InstanceSet, ordinal int) string {
	return fmt.Sprintf("%s-%d", set.Name, ordinal)
}

// isMemberOf tests if instance is a member of set.
func isMemberOf(set *workloadsv1alpha1.InstanceSet, instance *workloadsv1alpha1.Instance) bool {
	return getParentName(instance) == set.Name
}

// identityMatches returns true if instance has a valid identity for a member of set.
func identityMatches(set *workloadsv1alpha1.InstanceSet, instance *workloadsv1alpha1.Instance) bool {
	parent, ordinal := getParentNameAndOrdinal(instance)
	return ordinal >= 0 &&
		set.Name == parent &&
		instance.Name == getInstanceName(set, ordinal) &&
		instance.Namespace == set.Namespace &&
		instance.Labels[apps.StatefulSetPodNameLabel] == instance.Name
}

func initIdentity(set *workloadsv1alpha1.InstanceSet, instance *workloadsv1alpha1.Instance) {
	updateIdentity(set, instance)
}

// updateIdentity updates instance's name and labels to conform to set's name
func updateIdentity(set *workloadsv1alpha1.InstanceSet, instance *workloadsv1alpha1.Instance) {
	ordinal := getOrdinal(instance)
	instance.Name = getInstanceName(set, ordinal)
	instance.Namespace = set.Namespace
	if instance.Labels == nil {
		instance.Labels = make(map[string]string)
	}
	instance.Labels[apps.StatefulSetPodNameLabel] = instance.Name
	instance.Labels[apps.PodIndexLabel] = strconv.Itoa(ordinal)
}

// getInstanceSetReplicasRange returns the start ordinal, end ordinal (exclusive), and set of reserved ordinals for a InstanceSet.
func getInstanceSetReplicasRange(set *workloadsv1alpha1.InstanceSet) (int, int, sets.Set[int]) {
	// Default range is [0, Replicas)
	start := 0
	end := 0
	if set.Spec.Replicas != nil {
		end = int(*set.Spec.Replicas)
	}
	reserveOrdinals := sets.New[int]()

	// Kruise InstanceSet uses different ordinal configuration approach
	// This is a simplified version that only supports basic ordinal ranges

	return start, end, reserveOrdinals
}

// getInstanceSetKey returns the key for a InstanceSet
func getInstanceSetKey(set *workloadsv1alpha1.InstanceSet) string {
	return fmt.Sprintf("%s/%s", set.Namespace, set.Name)
}

// getInstanceRevision gets the revision of Instance by inspecting the controller revision label.
func getInstanceRevision(instance *workloadsv1alpha1.Instance) string {
	if instance.Labels == nil {
		return ""
	}
	return instance.Labels[apps.ControllerRevisionHashLabelKey]
}

// newVersionedInstance creates a new Instance for set with specific revision
func newVersionedInstance(
	currentSet, updateSet *workloadsv1alpha1.InstanceSet,
	currentRevision, updateRevision string,
	ordinal int,
	replicas []*workloadsv1alpha1.Instance) *workloadsv1alpha1.Instance {

	// Determine which set to use based on ordinal and update strategy
	useUpdateRevision := false
	setToUse := currentSet
	// Default partition is 0, meaning all instances should be updated
	var partition int32 = 0
	if currentSet.Spec.UpdateStrategy.Partition != nil && currentSet.Spec.Replicas != nil {
		// Convert IntOrString to int32
		partitionValue, err := intstrutil.GetValueFromIntOrPercent(
			currentSet.Spec.UpdateStrategy.Partition,
			int(*currentSet.Spec.Replicas),
			false,
		)
		if err == nil {
			partition = int32(partitionValue)
		}
	}
	// If ordinal >= partition, use update revision
	if ordinal >= int(partition) {
		useUpdateRevision = true
		setToUse = updateSet
	}

	instance := &workloadsv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      make(map[string]string),
			Annotations: make(map[string]string),
		},
		// Copy the InstanceSpec from InstanceTemplate
		Spec: setToUse.Spec.InstanceTemplate.InstanceSpec,
	}

	// Set basic identity
	instance.Name = getInstanceName(currentSet, ordinal)
	instance.Namespace = currentSet.Namespace

	// Copy all labels from InstanceSet to Instance (consistent with statelessmode)
	for k, v := range currentSet.Labels {
		instance.Labels[k] = v
	}
	// Also copy Selector.MatchLabels for backward compatibility
	if currentSet.Spec.Selector != nil && currentSet.Spec.Selector.MatchLabels != nil {
		for k, v := range currentSet.Spec.Selector.MatchLabels {
			instance.Labels[k] = v
		}
	}
	instance.Labels[apps.StatefulSetPodNameLabel] = instance.Name

	// Set revision label
	if useUpdateRevision {
		instance.Labels[apps.ControllerRevisionHashLabelKey] = updateRevision
	} else {
		instance.Labels[apps.ControllerRevisionHashLabelKey] = currentRevision
	}

	// Set owner reference
	instance.OwnerReferences = []metav1.OwnerReference{
		*metav1.NewControllerRef(currentSet, controllerKind),
	}

	return instance
}

// hasOwnerRef returns true if target has an ownerRef to owner.
func hasOwnerRef(target, owner metav1.Object) bool {
	ownerUID := owner.GetUID()
	for _, ownerRef := range target.GetOwnerReferences() {
		if ownerRef.UID == ownerUID {
			return true
		}
	}
	return false
}

// isHealthy returns true if instance is healthy
func isHealthy(instance *workloadsv1alpha1.Instance) bool {
	// Check if instance is ready
	for _, cond := range instance.Status.Conditions {
		if cond.Type == workloadsv1alpha1.InstanceReady && cond.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

// isCreated returns true if instance has been created
func isCreated(instance *workloadsv1alpha1.Instance) bool {
	return instance != nil && instance.UID != ""
}

// isTerminating returns true if instance is being terminated
func isTerminating(instance *workloadsv1alpha1.Instance) bool {
	return instance != nil && instance.DeletionTimestamp != nil
}

// isRunningAndReady returns true if instance is running and ready
func isRunningAndReady(instance *workloadsv1alpha1.Instance) bool {
	return isHealthy(instance) && !isTerminating(instance)
}

// isRunningAndAvailable returns true the instance is running and available given minReadySeconds
func isRunningAndAvailable(instance *workloadsv1alpha1.Instance, minReadySeconds int32) bool {
	return isHealthy(instance) && !isTerminating(instance) && isAvailableInstance(instance, minReadySeconds)
}

// isInstanceRunningAndAvailable returns true if instance is running and available, and the duration since it became available
func isInstanceRunningAndAvailable(instance *workloadsv1alpha1.Instance, minReadySeconds int32) (bool, time.Duration) {
	if !isRunningAndReady(instance) {
		return false, 0
	}

	for _, cond := range instance.Status.Conditions {
		if cond.Type == workloadsv1alpha1.InstanceReady && cond.Status == v1.ConditionTrue {
			availableFor := metav1.Now().Sub(cond.LastTransitionTime.Time)
			minDuration := time.Duration(minReadySeconds) * time.Second
			if availableFor < minDuration {
				return false, minDuration - availableFor
			}
			return true, 0
		}
	}

	return false, 0
}

// isAvailableInstance checks if the instance is available based on minReadySeconds
func isAvailableInstance(instance *workloadsv1alpha1.Instance, minReadySeconds int32) bool {
	if !isHealthy(instance) {
		return false
	}

	for _, cond := range instance.Status.Conditions {
		if cond.Type == workloadsv1alpha1.InstanceReady && cond.Status == v1.ConditionTrue {
			availableFor := metav1.Now().Sub(cond.LastTransitionTime.Time)
			if availableFor >= time.Duration(minReadySeconds)*time.Second {
				return true
			}
		}
	}

	return false
}
