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
	"sync/atomic"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	portallocator "sigs.k8s.io/rbgs/pkg/port-allocator"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	podinplace "sigs.k8s.io/rbgs/pkg/inplace/pod"
	instancecore "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/core"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

const (
	// When batching pod creates, initialBatchSize is the size of the initial batch.
	initialBatchSize = 1
)

func (c *realControl) Scale(ctx context.Context, updateInstance *workloadsv1alpha2.RoleInstance, currentRevision, updateRevision *apps.ControllerRevision,
	revisions []*apps.ControllerRevision, pods []*v1.Pod) (bool, error) {
	diffRes, err := c.calculateDiffsWithExpectation(ctx, updateInstance, currentRevision, updateRevision, revisions, pods)
	if err != nil {
		return true, err
	}
	if diffRes.toScaleNum > 0 {
		return c.createPods(ctx, updateInstance, diffRes.toScaleRoleIDS, updateRevision.Name)
	}
	if diffRes.toDeleteNum > 0 {
		return c.deletePods(ctx, updateInstance, diffRes.toDeletePod)
	}
	return false, nil
}

type expectationDiff struct {
	toDeleteNum int
	toDeletePod []*v1.Pod

	toScaleNum     int
	toScaleRoleIDS map[string]sets.Set[int32]
}

func (c *realControl) calculateDiffsWithExpectation(ctx context.Context, updateInstance *workloadsv1alpha2.RoleInstance,
	currentRevision, updateRevision *apps.ControllerRevision,
	revisions []*apps.ControllerRevision, pods []*v1.Pod) (*expectationDiff, error) {

	coreControl := instancecore.New(updateInstance)

	// Check RestartPolicy: if RecreateInstanceOnPodRestart and any pod is deleted or restarted,
	// recreate all pods of the instance
	if shouldRecreateInstance(updateInstance, pods) {
		c.recorder.Event(updateInstance, v1.EventTypeNormal, "ReCreateInstance",
			fmt.Sprintf("RestartPolicy is RecreateInstanceOnPodRestart, recreate all pods of instance: %v", klog.KObj(updateInstance)))
		return &expectationDiff{toDeleteNum: len(pods), toDeletePod: pods}, nil
	}

	if isGangSchedulingEnabled(updateInstance) {
		for i := range pods {
			oldRevision := currentRevision
			for _, r := range revisions {
				if instanceutil.EqualToRevisionHash("", pods[i], r.Name) {
					oldRevision = r
					break
				}
			}
			if !c.inplaceControl.CanUpdateInPlace(ctx, oldRevision, updateRevision, coreControl.GetUpdateOptions()) {
				c.recorder.Event(updateInstance, v1.EventTypeNormal, "ReCreateInstance", fmt.Sprintf("component %s can't inplace updated, "+
					"recreate all pods of instance: %v", instanceutil.GetPodComponentName(pods[i]), klog.KObj(updateInstance)))
				return &expectationDiff{toDeleteNum: len(pods), toDeletePod: pods}, nil
			}
		}
	}

	var (
		toDeleteNum  = 0
		toDeletePods []*v1.Pod

		toScaleNum     = 0
		toScaleRoleIDS = make(map[string]sets.Set[int32])
	)

	prt, err := coreControl.GetComponentsTopology(pods)
	if err != nil {
		return nil, err
	}
	for _, rg := range prt.Topologies {
		if rg.ToDeleteIDs.Len() > 0 {
			toDeleteNum += rg.ToDeleteIDs.Len()
			toDeletePods = append(toDeletePods, rg.ToDeletePod...)
		}
		if rg.ToScaleIDs.Len() > 0 {
			toScaleNum += rg.ToScaleIDs.Len()
			toScaleRoleIDS[rg.Name] = rg.ToScaleIDs
		}
	}

	return &expectationDiff{
		toDeleteNum:    toDeleteNum,
		toScaleNum:     toScaleNum,
		toScaleRoleIDS: toScaleRoleIDS,
		toDeletePod:    toDeletePods,
	}, nil
}

func (c *realControl) createPods(ctx context.Context, updateInstance *workloadsv1alpha2.RoleInstance, expectedCreations map[string]sets.Set[int32], updateRevision string) (bool, error) {
	coreControl := instancecore.New(updateInstance)
	var newPods []*v1.Pod
	componentPortConfigs := make(map[string]*portallocator.PortAllocatorConfig)

	for _, component := range updateInstance.Spec.Components {
		updatePods, err := coreControl.NewUpdatePods(updateRevision, component.Name, sets.List(expectedCreations[component.Name]))
		if err != nil {
			return false, err
		}
		newPods = append(newPods, updatePods...)

		// Parse port allocator config from component template
		if portallocator.HasPortAllocatorConfig(&component.Template) {
			config, err := portallocator.ParsePortAllocatorConfigFromTemplate(&component.Template)
			if err != nil {
				return false, fmt.Errorf("failed to parse port allocator config for component %s: %w", component.Name, err)
			}
			componentPortConfigs[component.Name] = config
		}
	}

	podsCreationChan := make(chan *v1.Pod, len(newPods))
	toCreatePodNum := 0
	for _, p := range newPods {
		if c.hasOrphanPod(p.Namespace, p.Name) {
			if isGangSchedulingEnabled(updateInstance) {
				return false, fmt.Errorf("orphan pod %v has not been gc, fail to create new pod", klog.KObj(p))
			}
			continue
		}

		// Handle port injection for the pod from Instance annotation
		componentName := instanceutil.GetPodComponentName(p)
		if config, exists := componentPortConfigs[componentName]; exists {
			// Inject ports into the pod spec from Instance annotation
			if err := portallocator.InjectPortsIntoPod(p, updateInstance, config, componentName); err != nil {
				return false, fmt.Errorf("failed to inject ports into pod %s: %w", p.Name, err)
			}
		}

		toCreatePodNum++
		podsCreationChan <- p
	}
	var created int64
	_, err := instanceutil.DoItSlowly(toCreatePodNum, initialBatchSize, func() error {
		pod := <-podsCreationChan
		if createErr := c.createOnePod(ctx, updateInstance, pod); createErr != nil {
			return createErr
		}
		atomic.AddInt64(&created, 1)
		return nil
	})
	if created == 0 {
		return false, err
	}
	return true, err
}

func (c *realControl) deletePods(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, podsToDelete []*v1.Pod) (bool, error) {
	var modified bool

	for _, pod := range podsToDelete {
		if err := c.Delete(ctx, pod); err != nil {
			c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedDelete", "failed to delete pod %s: %v", pod.Name, err)
			return modified, err
		}
		modified = true
		c.recorder.Event(instance, v1.EventTypeNormal, "SuccessfulDelete", fmt.Sprintf("succeed to delete pod %s", pod.Name))
	}
	return modified, nil
}

func (c *realControl) hasOrphanPod(namespace, name string) bool {
	pod := new(v1.Pod)
	err := c.Get(context.TODO(), client.ObjectKey{Namespace: namespace, Name: name}, pod)
	return err == nil
}

func (c *realControl) createOnePod(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, pod *v1.Pod) error {
	if err := c.Create(ctx, pod); err != nil {
		c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedCreate", "failed to create pod: %v, pod: %v", err, podinplace.DumpJSON(pod))
		return err
	}
	c.recorder.Eventf(instance, v1.EventTypeNormal, "SuccessfulCreate", "succeed to create pod %s", pod.Name)
	return nil
}

// shouldRecreateInstance checks if the instance should be recreated (all pods deleted then recreated).
// This applies when:
// 1. restartPolicy = RecreateRoleInstanceOnPodRestart AND Pod Failed
//   - Instead of replacement Pod, recreate entire Instance for consistency
//
// Note: This function does NOT handle container restart (Dimension 1).
// Container restart with RecreateRoleInstanceOnPodRestart is handled by LWS controller,
// RBG controller does nothing in that case.
//
// For restartPolicy=None, Pod Failed triggers replacement Pod
// creation through normal reconciliation (GetActiveAndInactivePods → createPods).
//
// Per KEP Non-Goals: Succeeded pods are NOT handled here - they represent normal completion
// and should not trigger Instance recreation.
func shouldRecreateInstance(instance *workloadsv1alpha2.RoleInstance, pods []*v1.Pod) bool {
	// Only apply when restartPolicy is RecreateRoleInstanceOnPodRestart
	if instance.Spec.RestartPolicy != workloadsv1alpha2.RoleInstanceRestartPolicyRecreateOnPodRestart {
		return false
	}

	// If no pods exist yet (initial creation), don't trigger recreate
	if len(pods) == 0 {
		return false
	}

	// Check if any Pod has become Failed (Dimension 2)
	// With RecreateRoleInstanceOnPodRestart policy, Pod Failed triggers Instance recreation
	// (instead of just replacement Pod)
	//
	// Per KEP Non-Goals: Succeeded pods are explicitly excluded - only Failed pods trigger recreation.
	// Pod being deleted (with DeletionTimestamp) is also excluded as it's handled separately.
	if wasInstanceReady(instance) && instance.Generation == instance.Status.ObservedGeneration {
		// Check if any Pod is Failed (excluding Succeeded and pods being deleted)
		for _, p := range pods {
			// Only Failed pods trigger Instance recreation
			// Succeeded pods (normal completion) are explicitly excluded per KEP Non-Goals
			// Pods being deleted are excluded as they're handled through normal deletion flow
			if p.Status.Phase == v1.PodFailed && p.DeletionTimestamp == nil {
				return true
			}
		}
	}

	return false
}

// wasInstanceReady checks if the Instance was previously in Ready state
func wasInstanceReady(instance *workloadsv1alpha2.RoleInstance) bool {
	for _, cond := range instance.Status.Conditions {
		if cond.Type == workloadsv1alpha2.RoleInstanceReady && cond.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

// getExpectedPodCount calculates the expected total pod count from instance spec
func getExpectedPodCount(instance *workloadsv1alpha2.RoleInstance) int {
	expectedPodCount := 0
	for _, component := range instance.Spec.Components {
		if component.Size != nil {
			expectedPodCount += int(*component.Size)
		}
	}
	return expectedPodCount
}

// isGangSchedulingEnabled reports whether gang-scheduling constraints are active for the
// given RoleInstance. The annotation is derived from the parent RBG's gang-scheduling
// annotation during RoleInstanceSet reconciliation, or set directly via role.Annotations.
func isGangSchedulingEnabled(instance *workloadsv1alpha2.RoleInstance) bool {
	return instance.Annotations[constants.RoleInstanceGangSchedulingAnnotationKey] == "true"
}
