/*
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
	"encoding/json"
	"fmt"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/kubernetes/pkg/controller/history"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

var patchCodec = scheme.Codecs.LegacyCodec(workloadsv1alpha1.SchemeGroupVersion)

// nextRevision finds the next valid revision number based on revisions
func nextRevision(revisions []*apps.ControllerRevision) int64 {
	count := len(revisions)
	if count <= 0 {
		return 1
	}
	return revisions[count-1].Revision + 1
}

// newRevision creates a new ControllerRevision containing a patch that reapplies the target state of set
func newRevision(set *workloadsv1alpha1.InstanceSet, revision int64, collisionCount *int32) (*apps.ControllerRevision, error) {
	patch, err := getPatch(set)
	if err != nil {
		return nil, err
	}
	var matchLabels map[string]string
	if set.Spec.Selector != nil {
		matchLabels = set.Spec.Selector.MatchLabels
	}
	cr, err := history.NewControllerRevision(set,
		controllerKind,
		matchLabels,
		runtime.RawExtension{Raw: patch},
		revision,
		collisionCount)
	if err != nil {
		return nil, err
	}
	if cr.ObjectMeta.Annotations == nil {
		cr.ObjectMeta.Annotations = make(map[string]string)
	}
	for key, value := range set.Annotations {
		cr.ObjectMeta.Annotations[key] = value
	}
	return cr, nil
}

// getPatch returns a strategic merge patch that can be applied to restore a InstanceSet to a
// previous version. If the returned error is nil the patch is valid.
func getPatch(set *workloadsv1alpha1.InstanceSet) ([]byte, error) {
	str, err := runtime.Encode(patchCodec, set)
	if err != nil {
		return nil, err
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(str, &raw); err != nil {
		return nil, err
	}
	objCopy := make(map[string]interface{})
	specCopy := make(map[string]interface{})

	// Check if spec exists and is a map
	specInterface, ok := raw["spec"]
	if !ok || specInterface == nil {
		return nil, fmt.Errorf("spec field is missing or nil in InstanceSet")
	}
	spec, ok := specInterface.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("spec field is not a map[string]interface{}")
	}

	// Check if instanceTemplate exists and is a map
	templateInterface, ok := spec["instanceTemplate"]
	if !ok || templateInterface == nil {
		return nil, fmt.Errorf("instanceTemplate field is missing or nil in InstanceSet spec")
	}
	template, ok := templateInterface.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("instanceTemplate field is not a map[string]interface{}")
	}

	specCopy["instanceTemplate"] = template
	template["$patch"] = "replace"
	objCopy["spec"] = specCopy
	patch, err := json.Marshal(objCopy)
	return patch, err
}

// ApplyRevision returns a new InstanceSet constructed by applying the given revision to a clone of set
func ApplyRevision(set *workloadsv1alpha1.InstanceSet, revision *apps.ControllerRevision) (*workloadsv1alpha1.InstanceSet, error) {
	clone := set.DeepCopy()
	patched, err := strategicpatch.StrategicMergePatch([]byte(runtime.EncodeOrDie(patchCodec, clone)), revision.Data.Raw, clone)
	if err != nil {
		return nil, err
	}
	restoredSet := &workloadsv1alpha1.InstanceSet{}
	err = json.Unmarshal(patched, restoredSet)
	if err != nil {
		return nil, err
	}
	return restoredSet, nil
}

// updateStatus updates the status fields of set based on the current and update revisions and instances
func updateStatus(
	status *workloadsv1alpha1.InstanceSetStatus,
	minReadySeconds int32,
	currentRevision *apps.ControllerRevision,
	updateRevision *apps.ControllerRevision,
	instanceLists ...[]*workloadsv1alpha1.Instance) {

	status.Replicas = 0
	status.ReadyReplicas = 0
	status.AvailableReplicas = 0
	status.CurrentReplicas = 0
	status.UpdatedReplicas = 0
	status.UpdatedReadyReplicas = 0

	for _, instances := range instanceLists {
		for i := range instances {
			if instances[i] == nil {
				continue
			}

			// count all instances
			status.Replicas++

			// count ready instances
			if isHealthy(instances[i]) {
				status.ReadyReplicas++
			}

			// count available instances (Ready for at least minReadySeconds)
			if isInstanceAvailable(instances[i], minReadySeconds) {
				status.AvailableReplicas++
			}

			// count current instances
			if getInstanceRevision(instances[i]) == currentRevision.Name {
				status.CurrentReplicas++
			}

			// count updated instances
			if getInstanceRevision(instances[i]) == updateRevision.Name {
				status.UpdatedReplicas++
				if isHealthy(instances[i]) {
					status.UpdatedReadyReplicas++
				}
			}
		}
	}
}

// isInstanceAvailable returns true if instance is available (Ready for at least minReadySeconds)
func isInstanceAvailable(instance *workloadsv1alpha1.Instance, minReadySeconds int32) bool {
	if !isHealthy(instance) {
		return false
	}

	if minReadySeconds <= 0 {
		return true
	}

	// Check if instance has been ready for minReadySeconds
	for _, cond := range instance.Status.Conditions {
		if cond.Type == workloadsv1alpha1.InstanceReady && cond.Status == v1.ConditionTrue {
			readyTime := cond.LastTransitionTime.Time
			return time.Since(readyTime) >= time.Duration(minReadySeconds)*time.Second
		}
	}

	return false
}

// allowsBurst checks if the InstanceSet allows bursting (parallel operations)
func allowsBurst(set *workloadsv1alpha1.InstanceSet) bool {
	return set.Spec.UpdateStrategy.Type == workloadsv1alpha1.RecreateUpdateStrategyType
}

// getMinReadySeconds returns the minReadySeconds for the InstanceSet
func getMinReadySeconds(set *workloadsv1alpha1.InstanceSet) int32 {
	return set.Spec.MinReadySeconds
}

// getScaleMaxUnavailable returns the maximum unavailable instances during scaling
func getScaleMaxUnavailable(set *workloadsv1alpha1.InstanceSet) (int, error) {
	if set.Spec.Replicas == nil {
		return 1, nil
	}
	if set.Spec.ScaleStrategy.MaxUnavailable == nil {
		return int(*set.Spec.Replicas), nil
	}
	maxUnavailable, err := intstrutil.GetValueFromIntOrPercent(
		set.Spec.ScaleStrategy.MaxUnavailable,
		int(*set.Spec.Replicas),
		false,
	)
	if err != nil {
		return 0, err
	}
	if maxUnavailable < 1 {
		maxUnavailable = 1
	}
	return maxUnavailable, nil
}

// runForAll runs the given function for all items, respecting monotonic mode
func runForAll(items interface{}, fn func(int) (bool, error), monotonic bool) (bool, error) {
	var length int
	switch v := items.(type) {
	case []*workloadsv1alpha1.Instance:
		length = len(v)
	default:
		return false, fmt.Errorf("unsupported type")
	}

	for i := 0; i < length; i++ {
		shouldExit, err := fn(i)
		if err != nil {
			return shouldExit, err
		}
		if shouldExit && monotonic {
			return true, nil
		}
	}
	return false, nil
}

// runForAllWithBreak runs the given function for all items with additional break control
func runForAllWithBreak(items interface{}, fn func(int) (bool, bool, error), monotonic bool) (bool, error) {
	var length int
	switch v := items.(type) {
	case []*workloadsv1alpha1.Instance:
		length = len(v)
	default:
		return false, fmt.Errorf("unsupported type")
	}

	for i := 0; i < length; i++ {
		shouldExit, modified, err := fn(i)
		if err != nil {
			return shouldExit, err
		}
		if shouldExit && monotonic {
			return true, nil
		}
		if modified && monotonic {
			return true, nil
		}
	}
	return false, nil
}

// getPodRevision returns the revision of the instance (for compatibility with existing code)
func getPodRevision(instance *workloadsv1alpha1.Instance) string {
	return getInstanceRevision(instance)
}
