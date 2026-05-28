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
	"time"

	"github.com/openkruise/kruise/pkg/util/requeueduration"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instancecore "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/core"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

func (c *realControl) Update(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus, _, updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision, pods []*v1.Pod) (time.Duration, error) {
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
	for componentName := range waitUpdateComponentPods {
		componentPods := waitUpdateComponentPods[componentName]
		for i := range componentPods {
			if duration, err := c.updatePod(ctx, instance, newStatus, updateRevision, revisions, componentPods[i]); err != nil {
				return requeueDuration.Get(), err
			} else if duration > 0 {
				requeueDuration.Update(duration)
			}
		}
	}
	return requeueDuration.Get(), nil
}

func (c *realControl) updatePod(ctx context.Context, instance *workloadsv1alpha2.RoleInstance,
	newStatus *workloadsv1alpha2.RoleInstanceStatus,
	updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision, pod *v1.Pod) (time.Duration, error) {
	logger := log.FromContext(ctx)
	var oldRevision *apps.ControllerRevision
	for _, r := range revisions {
		if instanceutil.EqualToRevisionHash("", pod, r.Name) {
			oldRevision = r
			break
		}
	}
	if oldRevision == nil {
		logger.Info("No matching ControllerRevision found for pod, falling back to ReCreate",
			"pod", klog.KObj(pod),
			"revisionHash", pod.Labels[apps.ControllerRevisionHashLabelKey])
		c.recorder.Eventf(instance, v1.EventTypeWarning, "RevisionNotFound",
			"no matching revision found for pod %s, falling back to recreate", pod.Name)
		if err := c.Delete(ctx, pod); err != nil {
			c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedUpdatePodReCreate",
				"failed to delete pod %s for update: %v", pod.Name, err)
			return 0, err
		}
		return 0, nil
	}
	coreControl := instancecore.New(instance)
	opts := coreControl.GetUpdateOptions()
	res := c.inplaceControl.Update(ctx, pod, oldRevision, updateRevision, opts)
	if res.InPlaceUpdate {
		if res.UpdateErr == nil {
			c.recorder.Eventf(instance, v1.EventTypeNormal, "SuccessfulUpdatePodInPlace", "successfully update pod %s in-place", pod.Name)
			instanceutil.ResourceVersionExpectations.Expect(&metav1.ObjectMeta{UID: pod.UID, ResourceVersion: res.NewResourceVersion})
			// Record pre-update RestartCount baselines only for containers that were
			// actually in-place updated (image changed). Non-updated containers should
			// not be protected — their restarts indicate real crashes.
			updatedContainers := c.inplaceControl.GetUpdatedContainerNames(ctx, pod, oldRevision, updateRevision, opts)
			if len(updatedContainers) == 0 {
				klog.Warningf("GetUpdatedContainerNames returned empty for pod %s/%s; baselines will not be recorded", pod.Namespace, pod.Name)
			}
			recordInPlaceUpdateBaselines(pod, newStatus, updatedContainers)
			return res.DelayDuration, nil
		}
		c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedUpdatePodInPlace", "failed to update pod %s in-place: %v", pod.Name, res.UpdateErr)
		return res.DelayDuration, res.UpdateErr
	}
	logger.Info("Instance can not update Pod in-place, so it will back off to ReCreate", "pod", klog.KObj(pod))
	if err := c.Delete(ctx, pod); err != nil {
		c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedUpdatePodReCreate", "failed to delete pod %s for update: %v", pod.Name, err)
		return 0, err
	}
	return 0, nil
}

// recordInPlaceUpdateBaselines records the pre-update RestartCount and ImageID
// for containers that were actually in-place updated (image changed) into
// newStatus.InPlaceUpdateContainerBaselines. Only updated containers are recorded
// so that crashes in non-updated containers are still correctly detected.
// Baselines are merged into existing entries to preserve baselines from prior
// in-place updates of different containers (sequential update support).
func recordInPlaceUpdateBaselines(pod *v1.Pod, newStatus *workloadsv1alpha2.RoleInstanceStatus, updatedContainers []string) {
	if len(updatedContainers) == 0 {
		return
	}
	if newStatus.InPlaceUpdateContainerBaselines == nil {
		newStatus.InPlaceUpdateContainerBaselines = make(map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline)
	}
	updatedSet := make(map[string]bool, len(updatedContainers))
	for _, name := range updatedContainers {
		updatedSet[name] = true
	}
	// Merge into existing baselines instead of overwriting — preserves
	// baselines from prior in-place updates of different containers.
	existing := newStatus.InPlaceUpdateContainerBaselines[pod.Name]
	if existing == nil {
		existing = make(map[string]workloadsv1alpha2.ContainerUpdateBaseline, len(updatedContainers))
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if updatedSet[cs.Name] {
			existing[cs.Name] = workloadsv1alpha2.ContainerUpdateBaseline{
				RestartCount: cs.RestartCount,
				ImageID:      cs.ImageID,
			}
		}
	}
	newStatus.InPlaceUpdateContainerBaselines[pod.Name] = existing
}
