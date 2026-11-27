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
	"fmt"
	"sort"
	"time"

	"github.com/openkruise/kruise/pkg/util/requeueduration"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	componentdiscovery "sigs.k8s.io/rbgs/pkg/component-discovery"
	instancecore "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/core"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

func (c *realControl) Update(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus, _, updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision, pods []*v1.Pod, inactivePods []*v1.Pod) (time.Duration, error) {
	// Guard direct Update calls as well as the normal Scale -> Update path.
	if blocked, err := c.reconcileRestartBudgetOnce(ctx, instance, selectedControllerRevision(updateRevision)); blocked || err != nil {
		if err != nil {
			return 0, err
		}
		return RestartBudgetRequeueAfter, nil
	}
	logger := log.FromContext(ctx)
	requeueDuration := requeueduration.Duration{}
	coreControl := instancecore.New(instance)

	waitUpdateComponentPods := make(map[string][]*v1.Pod)
	componentNames := sets.New[string]()
	for _, component := range instance.Spec.Components {
		componentNames.Insert(component.Name)
	}
	for i := range pods {
		componentName := instanceutil.GetPodComponentName(pods[i])
		if componentName != "" && !componentNames.Has(componentName) {
			// The component no longer exists in the spec; the scale diff owns
			// deleting these pods. Refreshing or updating them would act on
			// pods that are already scheduled for deletion.
			continue
		}
		if res := c.inplaceControl.Refresh(ctx, pods[i], coreControl.GetUpdateOptions()); res.RefreshErr != nil {
			logger.Error(res.RefreshErr, "Instance failed to update pod condition for inplace", "pod", pods[i].Name)
			return requeueDuration.Get(), res.RefreshErr
		} else if res.DelayDuration > 0 {
			requeueDuration.Update(res.DelayDuration)
		}
		if !instanceutil.EqualToRevisionHash("", pods[i], updateRevision.Name) {
			waitUpdateComponentPods[componentName] = append(waitUpdateComponentPods[componentName], pods[i])
		}
	}
	// gateUpdateComponentPods holds every owned update-wave Pod, including
	// terminating ones. Terminating Pods are classified inactive by
	// IsPodActive, so they never reach waitUpdateComponentPods; without this
	// map a deletion gate would release as soon as the gate component's DELETE
	// was accepted instead of when its Pods have actually disappeared.
	// Non-terminating inactive Pods (Succeeded/Failed) are deliberately
	// excluded: nothing guarantees they will ever disappear, and gating on
	// them could wedge the rollout.
	gateUpdateComponentPods := make(map[string][]*v1.Pod, len(waitUpdateComponentPods))
	for componentName, componentPods := range waitUpdateComponentPods {
		gateUpdateComponentPods[componentName] = append([]*v1.Pod(nil), componentPods...)
	}
	for i := range inactivePods {
		pod := inactivePods[i]
		if pod.DeletionTimestamp == nil {
			continue
		}
		componentName := instanceutil.GetPodComponentName(pod)
		if componentName == "" || !componentNames.Has(componentName) {
			continue
		}
		if !instanceutil.EqualToRevisionHash("", pod, updateRevision.Name) {
			gateUpdateComponentPods[componentName] = append(gateUpdateComponentPods[componentName], pod)
		}
	}
	recreatesPods := instance.Spec.GetUpdatePolicy().RecreatesPods()
	ordered, gates, err := orderedUpdateComponents(instance, waitUpdateComponentPods, gateUpdateComponentPods)
	if err != nil {
		return requeueDuration.Get(), err
	}
	for _, componentName := range ordered {
		componentPods := waitUpdateComponentPods[componentName]
		if recreatesPods && !updateDeletionGatesReleased(gates[componentName], gateUpdateComponentPods) {
			// component-depends-on gates member deletion: wait until the Pods
			// gating this component have actually disappeared. The next
			// reconcile, triggered by those deletions, releases this layer.
			continue
		}
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
		return 0, c.deletePodForUpdate(ctx, instance, newStatus, pod)
	}
	if instance.Spec.GetUpdatePolicy().RecreatesPods() {
		// RecreatePod policy: replace the Pod instead of updating it in place,
		// even when an in-place update would be possible.
		logger.Info("Instance update policy is RecreatePod, so it will back off to ReCreate", "pod", klog.KObj(pod))
		return 0, c.deletePodForUpdate(ctx, instance, newStatus, pod)
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
	return 0, c.deletePodForUpdate(ctx, instance, newStatus, pod)
}

// deletePodForUpdate removes one member Pod so it is recreated from the
// current instance spec. The delete is addressed by the observed Pod UID: a
// same-name replacement created by a concurrent reconcile must never be
// removed, and an already-deleted incarnation counts as handled. The replaced
// Pod is a new incarnation, so its in-place update baselines are dropped: an
// old baseline would otherwise grant the new Pod one spurious "expected"
// restart allowance.
func (c *realControl) deletePodForUpdate(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus, pod *v1.Pod) error {
	if newStatus != nil {
		delete(newStatus.InPlaceUpdateContainerBaselines, pod.Name)
	}
	opts := &client.DeleteOptions{}
	if pod.UID != "" {
		uid := pod.UID
		opts.Preconditions = &metav1.Preconditions{UID: &uid}
	}
	if err := c.Delete(ctx, pod, opts); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedUpdatePodReCreate",
			"failed to delete pod %s for update: %v", pod.Name, err)
		return err
	}
	return nil
}

// orderedUpdateComponents returns the components holding outdated active Pods
// in the component-depends-on deletion order together with the deletion gates.
// A component is only handed to the update path after the components that gate
// its deletion — its dependents — have been emitted. gatePods extends the
// update wave with terminating (inactive) outdated Pods: they cannot be acted
// on, but they still hold ordering slots and deletion gates open until they
// actually disappear. An unparsable component-depends-on annotation fails
// closed, matching the scale path; dependency cycles fall back to parallel
// deletion after logging.
func orderedUpdateComponents(instance *workloadsv1alpha2.RoleInstance, updatePods, gatePods map[string][]*v1.Pod) ([]string, map[string][]string, error) {
	names := make([]string, 0, len(updatePods))
	for name := range updatePods {
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, nil, nil
	}
	sort.Strings(names)
	startDeps, deleteDeps, err := componentdiscovery.ParseAllComponentDependencies(instance.Spec.Components)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse component dependencies for update: %w", err)
	}
	if !componentdiscovery.HasAnyDependency(instance.Spec.Components) {
		return names, nil, nil
	}
	gates := componentdiscovery.BuildDeletionGates(startDeps, deleteDeps)
	if componentdiscovery.DetectCycle(startDeps) || componentdiscovery.DetectCycle(gates) {
		klog.ErrorS(nil, "component-depends-on graph has a cycle; falling back to parallel update", "instance", klog.KObj(instance))
		return names, nil, nil
	}
	remaining := sets.New[string]()
	for name := range gatePods {
		remaining.Insert(name)
	}
	emitted := sets.New[string]()
	ordered := make([]string, 0, len(names))
	for len(ordered) < len(names) {
		progressed := false
		for _, name := range names {
			if emitted.Has(name) {
				continue
			}
			blocked := false
			for _, dependent := range gates[name] {
				if remaining.Has(dependent) && !emitted.Has(dependent) {
					blocked = true
					break
				}
			}
			if blocked {
				continue
			}
			emitted.Insert(name)
			ordered = append(ordered, name)
			progressed = true
		}
		if !progressed {
			// The cycle checks above make this unreachable; keep a safe
			// fallback so an ordering bug can never wedge the update path.
			for _, name := range names {
				if !emitted.Has(name) {
					ordered = append(ordered, name)
					emitted.Insert(name)
				}
			}
		}
	}
	return ordered, gates, nil
}

// updateDeletionGatesReleased reports whether every component gating the
// deletion of the named component has no remaining Pods in the update wave,
// including terminating ones. Deletion alone is not enough: the gate stays
// closed until the gating member Pods have actually disappeared. Pods already
// at the update revision are not part of the wave and do not block.
func updateDeletionGatesReleased(gateComponents []string, gatePods map[string][]*v1.Pod) bool {
	for _, gate := range gateComponents {
		if len(gatePods[gate]) > 0 {
			return false
		}
	}
	return true
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
