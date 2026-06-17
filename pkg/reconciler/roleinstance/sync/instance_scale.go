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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	componentdiscovery "sigs.k8s.io/rbgs/pkg/component-discovery"
	podinplace "sigs.k8s.io/rbgs/pkg/inplace/pod"
	portallocator "sigs.k8s.io/rbgs/pkg/port-allocator"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instancecore "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/core"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

const (
	// When batching pod creates, initialBatchSize is the size of the initial batch.
	initialBatchSize = 1
)

func (c *realControl) Scale(ctx context.Context, updateInstance *workloadsv1alpha2.RoleInstance, currentRevision, updateRevision *apps.ControllerRevision,
	revisions []*apps.ControllerRevision, pods []*v1.Pod, inactivePods []*v1.Pod) (bool, error) {
	// Record node bindings for in-place scheduling.
	// Piggybacks on the already-fetched pods — no additional API calls.
	RecordNodeBindings(c.bindings, updateInstance, pods)

	diffRes, err := c.calculateDiffsWithExpectation(ctx, updateInstance, currentRevision, updateRevision, revisions, pods, inactivePods)
	if err != nil {
		return true, err
	}
	if diffRes.toDeleteNum > 0 {
		return c.deletePods(ctx, updateInstance, diffRes.toDeletePod)
	}
	if diffRes.toScaleNum > 0 {
		return c.createPods(ctx, updateInstance, diffRes.toScaleRoleIDS, updateRevision.Name)
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
	revisions []*apps.ControllerRevision, pods []*v1.Pod, inactivePods []*v1.Pod) (*expectationDiff, error) {

	coreControl := instancecore.New(updateInstance)

	// Check RestartPolicy: if RecreateInstanceOnPodRestart and any pod Failed,
	// recreate all pods of the instance.
	// We include inactive pods (Failed/Succeeded) in the check because Failed pods are
	// filtered out by IsPodActive but must be visible to trigger recreation.
	allPods := make([]*v1.Pod, 0, len(pods)+len(inactivePods))
	allPods = append(allPods, pods...)
	allPods = append(allPods, inactivePods...)
	if c.shouldRecreateInstanceGuarded(ctx, updateInstance, allPods, updateInstance.Status.InPlaceUpdateContainerBaselines) {
		// Mark instance as restarting to prevent cascading re-triggers
		setRestartingCondition(updateInstance)
		c.recorder.Event(updateInstance, v1.EventTypeNormal, "ReCreateInstance",
			fmt.Sprintf("RestartPolicy is RecreateInstanceOnPodRestart, recreate all pods of instance: %v", klog.KObj(updateInstance)))
		return &expectationDiff{toDeleteNum: len(allPods), toDeletePod: allPods}, nil
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

	// Delete inactive (Failed) pods so that replacements can be created on next reconcile.
	// Failed pods block replacement creation because hasOrphanPod detects the same-name pod still exists.
	for _, p := range inactivePods {
		if p.Status.Phase == v1.PodFailed && p.DeletionTimestamp == nil {
			toDeleteNum++
			toDeletePods = append(toDeletePods, p)
		}
	}

	prt, err := coreControl.GetComponentsTopology(pods)
	if err != nil {
		return nil, err
	}

	// Build the per-component dependency graphs from template annotations.
	startDeps, deleteDeps, err := componentdiscovery.ParseAllComponentDependencies(updateInstance.Spec.Components)
	if err != nil {
		return nil, fmt.Errorf("failed to parse component dependencies: %w", err)
	}

	hasDeps := componentdiscovery.HasAnyDependency(updateInstance.Spec.Components)
	if hasDeps {
		// Build the final deletion-gate graph (union of reverse-of-start and explicit deleteAfter).
		deletionGates := componentdiscovery.BuildDeletionGates(startDeps, deleteDeps)

		// Cycle detection: a cycle in either graph causes a deadlock — fall back to parallel.
		if componentdiscovery.DetectCycle(startDeps) || componentdiscovery.DetectCycle(deletionGates) {
			klog.ErrorS(nil, "component-depends-on graph has a cycle; falling back to parallel (no ordering)",
				"instance", klog.KObj(updateInstance))
			hasDeps = false
		}

		if hasDeps {
			// Build a name→group index for O(1) lookups (used by deletion gate check).
			topoMap := make(map[string]*instancecore.ComponentPodGroup, len(prt.Topologies))
			for i := range prt.Topologies {
				topoMap[prt.Topologies[i].Name] = &prt.Topologies[i]
			}

			// Scale out: create pods for a component only when all its startAfter dependencies
			// have ReadyReplicas == Size in the instance's componentStatuses.
			for _, rg := range prt.Topologies {
				if rg.ToScaleIDs.Len() > 0 {
					if allNamedComponentsReady(startDeps[rg.Name], updateInstance.Status.ComponentStatuses) {
						toScaleNum += rg.ToScaleIDs.Len()
						toScaleRoleIDS[rg.Name] = rg.ToScaleIDs
					}
				}
			}

			// Scale in: delete pods for a component only when every component in its deletion
			// gates (reverse-of-start ∪ explicit deleteAfter) has no remaining excess pods.
			for _, rg := range prt.Topologies {
				if rg.ToDeleteIDs.Len() > 0 {
					if allDependentsDeleted(deletionGates[rg.Name], topoMap) {
						toDeleteNum += rg.ToDeleteIDs.Len()
						toDeletePods = append(toDeletePods, rg.ToDeletePod...)
					}
				}
			}
		}
	}

	if !hasDeps {
		// Default parallel mode: process all components concurrently.
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
	componentDiscoveryEnabled := make(map[string]bool)

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

		// Check if component-discovery injection is needed for this component
		if componentdiscovery.HasComponentDiscoveryConfig(&component.Template) {
			componentDiscoveryEnabled[component.Name] = true
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

		// Handle component-discovery injection (FQDN addresses and port env vars)
		if componentDiscoveryEnabled[componentName] {
			if err := componentdiscovery.InjectComponentDiscovery(p, updateInstance); err != nil {
				return false, fmt.Errorf("failed to inject component discovery into pod %s: %w", p.Name, err)
			}
		}

		// Handle in-place scheduling: inject nodeAffinity to prefer historical nodes
		InjectInPlaceScheduling(p, updateInstance, c.bindings)

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

// shouldRecreateInstanceGuarded wraps shouldRecreateInstance with a deferred guard:
// when the core logic decides to recreate, a final check ensures the instance isn't
// already in a restart cycle. This uses an in-memory LRU cache (instant, no informer lag)
// with a fallback to a direct API server read for the persisted Restarting condition
// (survives controller restarts).
func (c *realControl) shouldRecreateInstanceGuarded(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, pods []*v1.Pod, baselines map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline) (recreate bool) {
	defer func() {
		if recreate && c.isAlreadyRestarting(ctx, instance) {
			recreate = false
		}
	}()
	return shouldRecreateInstance(instance, pods, baselines)
}

// isAlreadyRestarting checks whether the instance is already undergoing a restart-policy
// recreation. It first checks the in-memory cache (zero latency, immune to informer lag),
// then falls back to a direct API server read to check the persisted Restarting condition.
func (c *realControl) isAlreadyRestarting(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) bool {
	// Fast path: check in-memory cache
	if _, ok := restartingCache.Load(instanceKey(instance)); ok {
		return true
	}
	// Slow path: read fresh from API server (bypasses informer cache)
	fresh := &workloadsv1alpha2.RoleInstance{}
	if err := c.apiReader.Get(ctx, client.ObjectKeyFromObject(instance), fresh); err != nil {
		return false
	}
	return isInstanceRestarting(fresh)
}

// shouldRecreateInstance checks if the instance should be recreated (all pods deleted then recreated).
// This applies when restartPolicy = RecreateRoleInstanceOnPodRestart AND:
//   - Any Pod is in Failed phase, OR
//   - Any container has restarted (RestartCount > 0)
//
// Pods with the restart-trigger-policy annotation set to "Ignore" are excluded —
// their failures and container restarts will not trigger instance recreation.
// This is useful for auxiliary components (e.g., monitoring, logging sidecars)
// whose failures should not affect the main workload.
//
// For restartPolicy=None, Pod Failed triggers replacement Pod
// creation through normal reconciliation (GetActiveAndInactivePods → createPods).
//
// Per KEP Non-Goals: Succeeded pods are NOT handled here - they represent normal completion
// and should not trigger Instance recreation.
func shouldRecreateInstance(instance *workloadsv1alpha2.RoleInstance, pods []*v1.Pod, baselines map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline) bool {
	// Only apply when restartPolicy is RecreateRoleInstanceOnPodRestart
	if instance.Spec.RestartPolicy != workloadsv1alpha2.RecreateRoleInstanceOnPodRestart {
		return false
	}

	// If no pods exist yet (initial creation), don't trigger recreate
	if len(pods) == 0 {
		return false
	}

	// Only trigger when Instance was previously Ready (stable state)
	// and is NOT in the middle of any update.
	// This avoids triggering recreate during initial creation, scaling up, or rolling/in-place updates.
	if !wasInstanceReady(instance) || instance.Generation != instance.Status.ObservedGeneration {
		return false
	}
	// CurrentRevision != UpdateRevision means a rolling update is in progress
	// (not all pods have converged to the target revision yet). Container restarts
	// during this window are from the update process, not unexpected failures.
	if instance.Status.CurrentRevision != instance.Status.UpdateRevision {
		return false
	}

	for _, p := range pods {
		if hasTriggerPolicyIgnore(p) {
			continue
		}
		// If this pod is currently undergoing an in-place update, skip it.
		// The container restart is expected and should not trigger instance recreation.
		// We continue checking other pods so that a genuine PodFailed on a sibling
		// is not masked by one pod's in-place update state.
		if isPodInPlaceUpdating(p) {
			continue
		}
		// Check if any Pod is in Failed phase (excluding pods being deleted)
		if p.Status.Phase == v1.PodFailed && p.DeletionTimestamp == nil {
			return true
		}
		// Check if any container has restarted beyond what's expected from in-place updates.
		if containerRestarted(p) && !isContainerRestartExpected(p, baselines) {
			return true
		}
	}

	return false
}

// containerRestarted checks if any container in the pod has been restarted.
func containerRestarted(pod *v1.Pod) bool {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].RestartCount > 0 {
			return true
		}
	}
	return false
}

// isPodInPlaceUpdating checks if a pod is currently undergoing an in-place update
// by looking at the InPlaceUpdateReady pod condition. When an in-place update starts,
// this condition is set to False; it returns to True after the update completes.
func isPodInPlaceUpdating(pod *v1.Pod) bool {
	cond := podinplace.GetInPlaceCondition(pod)
	return cond != nil && cond.Status == v1.ConditionFalse
}

// isContainerRestartExpected checks whether all container restarts in this pod
// are accounted for by a recent in-place update, using the pre-update baselines
// recorded in RoleInstance status.
// A container is granted +1 restart allowance only if its ImageID actually changed
// (indicating the in-place update pulled a new image). Non-updated containers
// tracked in baselines get no allowance — any restart on them is a real crash.
func isContainerRestartExpected(pod *v1.Pod, baselines map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline) bool {
	if baselines == nil {
		return false
	}
	containerBaselines, ok := baselines[pod.Name]
	if !ok {
		return false
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.RestartCount == 0 {
			continue
		}
		baseline, wasUpdated := containerBaselines[cs.Name]
		if !wasUpdated {
			// Container was not in-place updated but has restarted → real crash
			return false
		}
		if cs.RestartCount < baseline.RestartCount {
			// Pod was likely recreated (RestartCount reset to a lower value).
			// The baseline is stale; treat any restart as real.
			return false
		}
		allowed := baseline.RestartCount
		if cs.ImageID != baseline.ImageID {
			// Image actually changed; one kubelet-driven restart is expected
			allowed++
		}
		if cs.RestartCount > allowed {
			// More restarts than expected → real crash
			return false
		}
	}
	return true
}

// hasTriggerPolicyIgnore checks if the pod has the restart-trigger-policy annotation set to "Ignore".
func hasTriggerPolicyIgnore(pod *v1.Pod) bool {
	if pod.Annotations == nil {
		return false
	}
	return pod.Annotations[constants.RestartTriggerPolicyAnnotationKey] == constants.RestartTriggerPolicyIgnore
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

// isInstanceRestarting checks if the Instance is currently in a restart-policy recreation cycle.
func isInstanceRestarting(instance *workloadsv1alpha2.RoleInstance) bool {
	for _, cond := range instance.Status.Conditions {
		if cond.Type == workloadsv1alpha2.RoleInstanceRestarting && cond.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

// setRestartingCondition marks the instance as currently restarting due to restart policy.
// It updates both the in-memory cache (for immediate visibility) and the status condition
// (for persistence across controller restarts).
func setRestartingCondition(instance *workloadsv1alpha2.RoleInstance) {
	// Write to in-memory cache first for immediate visibility
	restartingCache.Store(instanceKey(instance), true)

	for i, cond := range instance.Status.Conditions {
		if cond.Type == workloadsv1alpha2.RoleInstanceRestarting {
			instance.Status.Conditions[i].Status = v1.ConditionTrue
			instance.Status.Conditions[i].LastTransitionTime = metav1.Now()
			instance.Status.Conditions[i].Reason = "RestartPolicyTriggered"
			instance.Status.Conditions[i].Message = "Instance is being recreated due to restart policy"
			return
		}
	}
	instance.Status.Conditions = append(instance.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{
		Type:               workloadsv1alpha2.RoleInstanceRestarting,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             "RestartPolicyTriggered",
		Message:            "Instance is being recreated due to restart policy",
	})
}

// ClearRestarting removes the instance from the in-memory restarting cache.
func (c *realControl) ClearRestarting(instance *workloadsv1alpha2.RoleInstance) {
	restartingCache.Delete(instanceKey(instance))
}

// instanceKey returns a unique key for the instance used in the restarting cache.
func instanceKey(instance *workloadsv1alpha2.RoleInstance) string {
	return instance.Namespace + "/" + instance.Name
}

// isGangSchedulingEnabled reports whether gang-scheduling constraints are active for the
// given RoleInstance. The annotation is derived from the parent RBG's gang-scheduling
// annotation during RoleInstanceSet reconciliation, or set directly via role.Annotations.
func isGangSchedulingEnabled(instance *workloadsv1alpha2.RoleInstance) bool {
	return instance.Annotations[constants.RoleInstanceGangSchedulingAnnotationKey] == "true"
}

// allNamedComponentsReady returns true when every component named in depNames
// has ReadyReplicas >= Size (and Size > 0) in the RoleInstance's componentStatuses.
// It is used during scale-out to gate creation of a dependent component's pods:
// the dependent will only be created once every named dependency reports fully ready
// in the status aggregated from the previous reconcile.
func allNamedComponentsReady(depNames []string, componentStatuses []workloadsv1alpha2.RoleInstanceComponentStatus) bool {
	if len(depNames) == 0 {
		return true
	}
	// Build a quick lookup: component name → status entry.
	statusMap := make(map[string]*workloadsv1alpha2.RoleInstanceComponentStatus, len(componentStatuses))
	for i := range componentStatuses {
		statusMap[componentStatuses[i].Name] = &componentStatuses[i]
	}
	for _, name := range depNames {
		st, ok := statusMap[name]
		if !ok {
			// Dependency has no status entry yet — treat as not ready to block creation.
			return false
		}
		if st.Size <= 0 || st.ReadyReplicas < st.Size {
			return false
		}
	}
	return true
}

// allDependentsDeleted returns true when every component in dependentNames has no
// excess pods remaining to delete (ToDeleteIDs is empty), meaning all components
// that depend on the current one have already been torn down or reached their
// target size. It is used during scale-in to gate deletion of a dependency's pods.
func allDependentsDeleted(dependentNames []string, topoMap map[string]*instancecore.ComponentPodGroup) bool {
	for _, name := range dependentNames {
		rg, ok := topoMap[name]
		if !ok {
			continue
		}
		if rg.ToDeleteIDs.Len() > 0 {
			return false
		}
	}
	return true
}
