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

package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openkruise/kruise/pkg/util/requeueduration"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"
	portallocator "sigs.k8s.io/rbgs/pkg/port-allocator"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instancecore "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/core"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

func (c *realControl) Update(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, currentRevision, updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision, pods []*v1.Pod) (time.Duration, error) {
	logger := log.FromContext(ctx)
	requeueDuration := requeueduration.Duration{}
	coreControl := instancecore.New(instance)

	waitUpdateComponentPods := make(map[string][]*v1.Pod)
	for i := range pods {
		if res := c.inplaceControl.Refresh(ctx, pods[i], coreControl.GetUpdateOptions()); res.RefreshErr != nil {
			logger.Error(res.RefreshErr, "Instance failed to update pod condition for inplace", "pod", pods[i].Name)
			return requeueDuration.Get(), res.RefreshErr
		} else if res.DelayDuration > 0 {
			requeueDuration.Update(res.DelayDuration)
		}
		if !instanceutil.EqualToRevisionHash("", pods[i], updateRevision.Name) {
			waitUpdateComponentPods[instanceutil.GetPodComponentName(pods[i])] = append(waitUpdateComponentPods[instanceutil.GetPodComponentName(pods[i])], pods[i])
		}
	}

	// Handle port allocation changes for pods that need update
	// This must be done before pod update to ensure ports are properly allocated/released
	// Collect all pods that need update
	var podsToUpdate []*v1.Pod
	for _, componentPods := range waitUpdateComponentPods {
		podsToUpdate = append(podsToUpdate, componentPods...)
	}
	if len(podsToUpdate) > 0 {
		if err := c.syncPortAllocationsForUpdate(ctx, instance, currentRevision, updateRevision, podsToUpdate); err != nil {
			logger.Error(err, "Failed to sync port allocations for update")
			return requeueDuration.Get(), err
		}
	}

	for componentName := range waitUpdateComponentPods {
		componentPods := waitUpdateComponentPods[componentName]
		for i := range componentPods {
			if duration, err := c.updatePod(ctx, instance, updateRevision, revisions, componentPods[i]); err != nil {
				return requeueDuration.Get(), err
			} else if duration > 0 {
				requeueDuration.Update(duration)
			}
		}
	}
	return requeueDuration.Get(), nil
}

func (c *realControl) updatePod(ctx context.Context, instance *workloadsv1alpha2.RoleInstance,
	updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision, pod *v1.Pod) (time.Duration, error) {
	logger := log.FromContext(ctx)
	var oldRevision *apps.ControllerRevision
	for _, r := range revisions {
		if instanceutil.EqualToRevisionHash("", pod, r.Name) {
			oldRevision = r
			break
		}
	}

	// Check if port configuration has changed for this pod's component
	// If port config changes, env will change, which requires pod recreation (not inplace update)
	if oldRevision != nil && updateRevision != nil {
		componentName := instanceutil.GetPodComponentName(pod)
		if hasPortConfigChanged(oldRevision, updateRevision, componentName) {
			logger.Info("Port configuration changed, Instance can not update Pod in-place, so it will back off to ReCreate", "pod", klog.KObj(pod))
			if err := c.Delete(context.TODO(), pod); err != nil {
				c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedUpdatePodReCreate", "failed to delete pod %s for update: %v", pod.Name, err)
				return 0, err
			}
			return 0, nil
		}
	}

	coreControl := instancecore.New(instance)
	res := c.inplaceControl.Update(ctx, pod, oldRevision, updateRevision, coreControl.GetUpdateOptions())
	if res.InPlaceUpdate {
		if res.UpdateErr == nil {
			c.recorder.Eventf(instance, v1.EventTypeNormal, "SuccessfulUpdatePodInPlace", "successfully update pod %s in-place", pod.Name)
			instanceutil.ResourceVersionExpectations.Expect(&metav1.ObjectMeta{UID: pod.UID, ResourceVersion: res.NewResourceVersion})
			return res.DelayDuration, nil
		}
		c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedUpdatePodInPlace", "failed to update pod %s in-place: %v", pod.Name, res.UpdateErr)
		return res.DelayDuration, res.UpdateErr
	}
	logger.Info("Instance can not update Pod in-place, so it will back off to ReCreate", "pod", klog.KObj(pod))
	if err := c.Delete(context.TODO(), pod); err != nil {
		c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedUpdatePodReCreate", "failed to delete pod %s for update: %v", pod.Name, err)
		return 0, err
	}
	return 0, nil
}

// syncPortAllocationsForUpdate handles port allocation changes when pods need to be updated
// It compares the old and new port allocator configurations and allocates/releases ports accordingly
func (c *realControl) syncPortAllocationsForUpdate(ctx context.Context, instance *workloadsv1alpha2.RoleInstance,
	currentRevision, updateRevision *apps.ControllerRevision, pods []*v1.Pod) error {
	// Build component to port config mapping for old and new revisions
	oldConfigs := make(map[string]*portallocator.PortAllocatorConfig)
	newConfigs := make(map[string]*portallocator.PortAllocatorConfig)

	// Parse old revision configs
	if currentRevision != nil {
		oldComponents, err := getComponentsFromRevision(currentRevision)
		if err == nil {
			for _, component := range oldComponents {
				if portallocator.HasPortAllocatorConfig(&component.Template) {
					config, err := portallocator.ParsePortAllocatorConfigFromTemplate(&component.Template)
					if err == nil {
						oldConfigs[component.Name] = config
					}
				}
			}
		}
	}

	// Parse new revision configs
	if updateRevision != nil {
		newComponents, err := getComponentsFromRevision(updateRevision)
		if err == nil {
			for _, component := range newComponents {
				if portallocator.HasPortAllocatorConfig(&component.Template) {
					config, err := portallocator.ParsePortAllocatorConfigFromTemplate(&component.Template)
					if err == nil {
						newConfigs[component.Name] = config
					}
				}
			}
		}
	}

	// If no port configs in either revision, skip
	if len(oldConfigs) == 0 && len(newConfigs) == 0 {
		return nil
	}

	// Create port manager
	portManager, err := portallocator.NewPortManager(ctx, c.Client, instance)
	if err != nil {
		return fmt.Errorf("failed to create port manager: %w", err)
	}

	// Process each pod that needs update
	for _, pod := range pods {
		componentName := instanceutil.GetPodComponentName(pod)
		oldConfig := oldConfigs[componentName]
		newConfig := newConfigs[componentName]

		// Skip if no port config change for this component
		if oldConfig == nil && newConfig == nil {
			continue
		}

		// Sync port allocations for this pod (updates ConfigMap)
		if err := portManager.SyncPortAllocations(ctx, pod, oldConfig, newConfig, componentName); err != nil {
			klog.V(2).InfoS("Failed to sync port allocations for pod", "pod", klog.KObj(pod), "error", err)
			return fmt.Errorf("failed to sync port allocations for pod %s: %w", pod.Name, err)
		}

		// Note: When port config changes, env will change, which triggers pod recreation.
		// The new pod will automatically get port configs from ConfigMap during creation.
		// So we don't need to inject ports here - just let the pod be recreated.
	}

	return nil
}

// getComponentsFromRevision extracts components from Instance ControllerRevision
// The revision data structure is: {"spec": {"components": [...]}}
func getComponentsFromRevision(revision *apps.ControllerRevision) ([]workloadsv1alpha2.InstanceComponent, error) {
	if revision == nil || revision.Data.Raw == nil {
		return nil, nil
	}

	var patchObj struct {
		Spec struct {
			Components []workloadsv1alpha2.InstanceComponent `json:"components"`
		} `json:"spec"`
	}

	if err := json.Unmarshal(revision.Data.Raw, &patchObj); err != nil {
		return nil, err
	}

	return patchObj.Spec.Components, nil
}

// hasPortConfigChanged checks if port allocator configuration has changed for a specific component
// between old and new revisions
func hasPortConfigChanged(oldRevision, newRevision *apps.ControllerRevision, componentName string) bool {
	// Parse old revision config
	oldComponents, err := getComponentsFromRevision(oldRevision)
	if err != nil {
		return false
	}

	// Parse new revision config
	newComponents, err := getComponentsFromRevision(newRevision)
	if err != nil {
		return false
	}

	// Find old component config
	var oldConfig *portallocator.PortAllocatorConfig
	for _, component := range oldComponents {
		if component.Name == componentName {
			if portallocator.HasPortAllocatorConfig(&component.Template) {
				config, err := portallocator.ParsePortAllocatorConfigFromTemplate(&component.Template)
				if err == nil {
					oldConfig = config
				}
			}
			break
		}
	}

	// Find new component config
	var newConfig *portallocator.PortAllocatorConfig
	for _, component := range newComponents {
		if component.Name == componentName {
			if portallocator.HasPortAllocatorConfig(&component.Template) {
				config, err := portallocator.ParsePortAllocatorConfigFromTemplate(&component.Template)
				if err == nil {
					newConfig = config
				}
			}
			break
		}
	}

	// Compare configs
	// If both nil, no change
	if oldConfig == nil && newConfig == nil {
		return false
	}

	// If one is nil and other is not, changed
	if oldConfig == nil || newConfig == nil {
		return true
	}

	// Compare the actual configs (simple string comparison of JSON representation)
	oldJSON, _ := json.Marshal(oldConfig)
	newJSON, _ := json.Marshal(newConfig)

	return string(oldJSON) != string(newJSON)
}
