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
	"slices"
	"sync/atomic"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
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

	// restartRequeue drives the single extra reconcile that acknowledges a new
	// generation before ordinary Failed cleanup can consume its startup signal,
	// so it stays short.
	restartRequeue = 2 * time.Second

	// drainSafetyRequeue only backstops a drain that is already in progress. Pod
	// update/delete events and this controller's own status writes already enqueue
	// the owner, so a tighter interval would not shorten recovery; it would only
	// add uncached reads for every round a member is held by a finalizer or stuck
	// on an unreachable node.
	drainSafetyRequeue = 30 * time.Second

	retirementStalledAfter = 5 * time.Minute
)

func (c *realControl) Scale(ctx context.Context, updateInstance *workloadsv1alpha2.RoleInstance, currentRevision, updateRevision *apps.ControllerRevision,
	revisions []*apps.ControllerRevision, pods []*v1.Pod, inactivePods []*v1.Pod) (bool, time.Duration, error) {
	// Suppression and explicit recovery precede ordinary scaling and backoff.
	if blocked, err := c.reconcileRestartBudget(ctx, updateInstance); blocked || err != nil {
		return true, RestartBudgetRequeueAfter, err
	}
	// Record node bindings for in-place scheduling.
	// Piggybacks on the already-fetched pods — no additional API calls.
	RecordNodeBindings(c.bindings, updateInstance, pods)

	diffRes, err := c.calculateDiffsWithExpectation(ctx, updateInstance, currentRevision, updateRevision, revisions, pods, inactivePods)
	if err != nil {
		return true, 0, err
	}
	// Backoff requeue: delay not elapsed, skip all scaling and just requeue.
	if diffRes.requeueAfter > 0 {
		return false, diffRes.requeueAfter, nil
	}
	if diffRes.toDeleteNum > 0 {
		restarting := len(updateInstance.Status.RetiringPods) > 0
		scaled, scaleErr := c.deletePods(ctx, updateInstance, diffRes.toDeletePod)
		if restarting {
			return true, drainSafetyRequeue, scaleErr
		}
		return scaled, 0, scaleErr
	}
	if diffRes.toScaleNum > 0 {
		scaled, scaleErr := c.createPods(ctx, updateInstance, diffRes.toScaleRoleIDS, updateRevision.Name)
		return scaled, 0, scaleErr
	}
	return false, 0, nil
}

type expectationDiff struct {
	toDeleteNum int
	toDeletePod []*v1.Pod

	toScaleNum     int
	toScaleRoleIDS map[string]sets.Set[int32]

	// requeueAfter is set when the reconcile should be requeued after a delay
	// without performing any scaling actions (used by restart backoff).
	requeueAfter time.Duration
}

func (c *realControl) calculateDiffsWithExpectation(ctx context.Context, updateInstance *workloadsv1alpha2.RoleInstance,
	currentRevision, updateRevision *apps.ControllerRevision,
	revisions []*apps.ControllerRevision, pods []*v1.Pod, inactivePods []*v1.Pod) (*expectationDiff, error) {

	// The persisted UID list owns an accepted drain, independently of readiness.
	freshInstance, err := c.freshRestartStatus(ctx, updateInstance)
	if err != nil {
		return nil, err
	}

	allPods := make([]*v1.Pod, 0, len(pods)+len(inactivePods))
	allPods = append(allPods, pods...)
	allPods = append(allPods, inactivePods...)

	if requeue := c.checkRestartBackoff(updateInstance, freshInstance, pods, inactivePods, currentRevision.Name, updateRevision.Name); requeue > 0 {
		return &expectationDiff{requeueAfter: requeue}, nil
	}

	coreControl := instancecore.New(updateInstance)

	recreate, err := c.shouldRecreateInstanceGuarded(ctx, updateInstance, allPods, updateInstance.Status.InPlaceUpdateContainerBaselines, currentRevision.Name, updateRevision.Name)
	if err != nil {
		return nil, err
	}
	if updateInstance.Status.RestartBudgetExhausted {
		return &expectationDiff{requeueAfter: RestartBudgetRequeueAfter}, nil
	}
	if recreate {
		return &expectationDiff{
			toDeleteNum: max(len(allPods), len(updateInstance.Status.RetiringPods)),
			toDeletePod: allPods,
		}, nil
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

	// Delete inactive (Failed) pods so that replacements can be created on a later
	// reconcile. Same-name foreign pods are handled separately by createPods.
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
	var foreignPodErrs []error
	for _, p := range newPods {
		existingPod, err := c.getExistingPod(ctx, p.Namespace, p.Name)
		if err != nil {
			return false, err
		}
		if existingPod != nil {
			if !metav1.IsControlledBy(existingPod, updateInstance) {
				err := fmt.Errorf(
					"pod %s is not controlled by RoleInstance %s (UID %s)",
					klog.KObj(existingPod), klog.KObj(updateInstance), updateInstance.UID,
				)
				c.recorder.Eventf(updateInstance, v1.EventTypeWarning, "FailedCreate",
					"cannot create replacement pod: %v", err)
				// Gang scheduling requires all Pods to be created together. For
				// ordinary instances, preserve non-colliding sibling creation and
				// return the collision after those Pods have been created.
				if isGangSchedulingEnabled(updateInstance) {
					return false, err
				}
				foreignPodErrs = append(foreignPodErrs, err)
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
	if len(foreignPodErrs) > 0 {
		errs := append([]error{}, foreignPodErrs...)
		if err != nil {
			errs = append(errs, err)
		}
		return created > 0, utilerrors.NewAggregate(errs)
	}
	return created > 0, err
}

func (c *realControl) deletePods(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, podsToDelete []*v1.Pod) (bool, error) {
	var modified bool
	if len(instance.Status.RetiringPods) > 0 {
		// The recorded (name, UID) pairs decide who may be deleted; the informer view
		// is enough to find them. Until the last old member is gone a cache miss only
		// costs one more round, because the reference stays in the list, and the UID
		// precondition below pins the incarnation.
		retiringPods := podsMatchingReferences(podsToDelete, instance.Status.RetiringPods)
		if len(retiringPods) == 0 {
			// The cache reports every old UID gone, and that is what releases
			// creation. Confirm against the API before believing it: any survivor is
			// deleted from the fresh object, and only an empty answer completes the
			// drain.
			confirmed, done, err := c.confirmDrain(ctx, instance)
			if err != nil {
				return false, err
			}
			if done {
				return true, nil
			}
			retiringPods = confirmed
		}
		podsToDelete = retiringPods
	}

	for _, pod := range podsToDelete {
		if instance.UID != "" && !metav1.IsControlledBy(pod, instance) {
			return modified, fmt.Errorf("refusing to delete foreign pod %s", pod.Name)
		}
		if pod.DeletionTimestamp != nil {
			if len(instance.Status.RetiringPods) > 0 && time.Since(pod.DeletionTimestamp.Time) > retirementStalledAfter && c.recorder != nil {
				// A stable message lets the normal EventRecorder aggregate repeats.
				// Never force deletion or release the UID barrier on a timeout.
				c.recorder.Eventf(instance, v1.EventTypeWarning, "RetirementStalled",
					"Retiring member %s (UID %s) has been terminating for more than 5 minutes; waiting for deletion", pod.Name, pod.UID)
			}
			continue
		}
		// Only the UID precondition: it pins the Pod incarnation, which is what the
		// delete is addressed by (namespace + name), so a same-name replacement
		// created between the read and the delete is never removed. The
		// resourceVersion comes from the informer snapshot, so any concurrent Pod
		// update (a kubelet status write is enough) would turn this delete into a 409
		// and surface FailedDelete/FailedScale for a delete that is still safe.
		opts := &client.DeleteOptions{}
		if pod.UID != "" {
			uid := pod.UID
			opts.Preconditions = &metav1.Preconditions{UID: &uid}
		}
		if err := c.Delete(ctx, pod, opts); err != nil && !apierrors.IsNotFound(err) {
			c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedDelete", "failed to delete pod %s: %v", pod.Name, err)
			return modified, err
		}
		modified = true
		c.recorder.Event(instance, v1.EventTypeNormal, "SuccessfulDelete", fmt.Sprintf("succeed to delete pod %s", pod.Name))
	}
	return modified, nil
}

// podsMatchingReferences keeps the Pods whose (name, UID) is recorded in the
// retiring list: the list, not the caller, decides what may be deleted.
func podsMatchingReferences(pods []*v1.Pod, references []workloadsv1alpha2.RoleInstancePodReference) []*v1.Pod {
	if len(references) == 0 {
		return nil
	}
	byName := make(map[string]*v1.Pod, len(pods))
	for _, pod := range pods {
		byName[pod.Name] = pod
	}
	matched := make([]*v1.Pod, 0, len(references))
	for _, ref := range references {
		if pod, ok := byName[ref.Name]; ok && pod.UID == ref.UID {
			matched = append(matched, pod)
		}
	}
	return matched
}

// confirmDrain re-reads the recorded retiring references from the API. It returns
// the surviving members as fresh objects, or reports completion after clearing the
// list under an optimistic lock.
func (c *realControl) confirmDrain(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) ([]*v1.Pod, bool, error) {
	reader := client.Reader(c.Client)
	if c.apiReader != nil {
		reader = c.apiReader
	}
	retiring := &workloadsv1alpha2.RoleInstance{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(instance), retiring); err != nil {
		return nil, false, err
	}
	if retiring.UID != instance.UID || retiring.DeletionTimestamp != nil {
		return nil, false, fmt.Errorf("instance %s changed UID or is deleting", instance.Name)
	}
	instance.Status = *retiring.Status.DeepCopy()
	if len(retiring.Status.RetiringPods) == 0 {
		return nil, false, nil // a stale caller cannot authorize any deletion
	}
	var survivors []*v1.Pod
	for _, ref := range retiring.Status.RetiringPods {
		pod := &v1.Pod{}
		if err := reader.Get(ctx, client.ObjectKey{Namespace: instance.Namespace, Name: ref.Name}, pod); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, false, err
		}
		if pod.UID == ref.UID {
			survivors = append(survivors, pod)
		}
	}
	if len(survivors) > 0 {
		return survivors, false, nil
	}
	before := retiring.DeepCopy()
	retiring.Status.RetiringPods = nil
	retiring.Status.Conditions = slices.DeleteFunc(retiring.Status.Conditions, func(condition workloadsv1alpha2.RoleInstanceCondition) bool {
		return condition.Type == workloadsv1alpha2.RoleInstanceRestarting
	})
	// Complete by old UID absence, not Ready or accepted Delete. A stale completion
	// cannot clear a newer list because the patch checks RV.
	if err := c.Status().Patch(ctx, retiring, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return nil, false, err
	}
	instance.Status = *retiring.Status.DeepCopy()
	return nil, true, nil
}

func (c *realControl) getExistingPod(ctx context.Context, namespace, name string) (*v1.Pod, error) {
	pod := &v1.Pod{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return pod, nil
}

func (c *realControl) createOnePod(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, pod *v1.Pod) error {
	if err := c.Create(ctx, pod); err != nil {
		c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedCreate", "failed to create pod: %v, pod: %v", err, podinplace.DumpJSON(pod))
		return err
	}
	c.recorder.Eventf(instance, v1.EventTypeNormal, "SuccessfulCreate", "succeed to create pod %s", pod.Name)
	return nil
}

// shouldRecreateInstanceGuarded keeps the original predicate call site and
// commits its deletion list/count/time before the existing deletePods branch.
func (c *realControl) shouldRecreateInstanceGuarded(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, pods []*v1.Pod, baselines map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline, currentRevision, updateRevision string) (bool, error) {
	if c.isAlreadyRestarting(instance) {
		return true, nil // resume saved UIDs through deletePods, without charging
	}
	if currentRevision != updateRevision {
		return false, nil
	}
	if !shouldRecreateInstance(instance, pods, baselines) {
		return false, nil
	}
	reader := client.Reader(c.Client)
	if c.apiReader != nil {
		reader = c.apiReader
	}
	fresh := &workloadsv1alpha2.RoleInstance{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(instance), fresh); err != nil {
		return false, err
	}
	if fresh.UID != instance.UID || fresh.Generation != instance.Generation || fresh.DeletionTimestamp != nil {
		return false, fmt.Errorf("instance %s changed before recording restart", instance.Name)
	}
	if fresh.Status.RestartBudgetExhausted {
		instance.Status = *fresh.Status.DeepCopy()
		return false, nil
	}
	if len(fresh.Status.RetiringPods) > 0 {
		instance.Status = *fresh.Status.DeepCopy()
		return true, nil // resume the newly observed list without another charge
	}
	// The RI read alone cannot validate informer Pod snapshots. Re-read only
	// this instance's labeled members, then verify controller ownership by UID.
	// The owner UID field index is cache-only and cannot be sent to the API server.
	podList := &v1.PodList{}
	if err := reader.List(ctx, podList, client.InNamespace(fresh.Namespace),
		client.MatchingLabels(instanceutil.GetSelectorMatchLabels(fresh.Name))); err != nil {
		return false, fmt.Errorf("list current pods for instance %s: %w", fresh.Name, err)
	}
	livePods := make([]*v1.Pod, 0, len(podList.Items))
	for i := range podList.Items {
		pod := &podList.Items[i]
		if metav1.IsControlledBy(pod, fresh) {
			livePods = append(livePods, pod)
		}
	}
	if !shouldRecreateInstance(fresh, livePods, fresh.Status.InPlaceUpdateContainerBaselines) {
		if c.recorder != nil {
			c.recorder.Eventf(instance, v1.EventTypeWarning, "RestartSignalDropped",
				"Restart policy trigger no longer applies to live members; repairing individually")
		}
		return false, nil
	}
	if c.checkRestartBackoff(fresh, fresh, livePods, nil) > 0 {
		return false, fmt.Errorf("restart eligibility changed for instance %s; retry", instance.Name)
	}
	trigger, _ := restartTrigger(fresh, livePods, fresh.Status.InPlaceUpdateContainerBaselines)
	before := fresh.DeepCopy()
	// Budget and retirement are accepted in one status transaction. A failed
	// patch cannot consume an attempt without recording the old Pod UIDs.
	if !prepareGroupRestartBudget(fresh) {
		if err := c.exhaustRestartBudget(ctx, fresh); err != nil {
			return false, err
		}
		instance.Status = *fresh.Status.DeepCopy()
		return false, c.terminateBudgetPods(ctx, fresh)
	}
	if err := updateRestartTracking(fresh, livePods...); err != nil {
		return false, err
	}
	setRestartingCondition(fresh)
	if err := c.Status().Patch(ctx, fresh, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return false, err
	}
	instance.Status = *fresh.Status.DeepCopy()
	c.recorder.Event(instance, v1.EventTypeNormal, "ReCreateInstance",
		fmt.Sprintf("RestartPolicy is %s, recreate all pods of instance: %v (restartCount=%d, trigger: %s)", instance.Spec.GetRestartPolicy(), klog.KObj(instance), instance.Status.RestartCount, trigger))
	return true, nil
}

// needsFreshRestartStatus reports whether this reconcile has to re-read the
// instance status from the API instead of trusting the informer snapshot.
//
// No fresh read is needed during an accepted drain: the persisted UIDs pin every
// deletion and confirmDrain validates the API before releasing creation. This also
// keeps long-held finalizers off the uncached read hot path.
func needsFreshRestartStatus(instance *workloadsv1alpha2.RoleInstance) bool {
	return len(instance.Status.RetiringPods) == 0 &&
		workloadsv1alpha2.IsRoleInstanceRecreatePolicy(instance.Spec.GetRestartPolicy())
}

// freshRestartStatus returns the authoritative instance status, or nil when the
// restart bookkeeping is irrelevant for this reconcile (see
// needsFreshRestartStatus).
func (c *realControl) freshRestartStatus(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) (*workloadsv1alpha2.RoleInstance, error) {
	if !needsFreshRestartStatus(instance) {
		return nil, nil
	}
	return c.syncRestartTrackingFromAPI(ctx, instance)
}

// syncRestartTrackingFromAPI reuses the existing fresh RI read. Because the
// list/count/time are committed before Delete, API status is authoritative even
// after a reset or explicit clear. Read errors must block all Pod writes.
func (c *realControl) syncRestartTrackingFromAPI(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) (*workloadsv1alpha2.RoleInstance, error) {
	if c.apiReader == nil {
		return nil, nil
	}
	fresh := &workloadsv1alpha2.RoleInstance{}
	if err := c.apiReader.Get(ctx, client.ObjectKeyFromObject(instance), fresh); err != nil {
		return nil, err
	}
	if fresh.UID != instance.UID || fresh.DeletionTimestamp != nil {
		return nil, fmt.Errorf("instance %s changed UID or is deleting", instance.Name)
	}
	instance.Status = *fresh.Status.DeepCopy()
	return fresh, nil
}

func (c *realControl) checkRestartBackoff(instance *workloadsv1alpha2.RoleInstance,
	fresh *workloadsv1alpha2.RoleInstance, pods []*v1.Pod, inactivePods []*v1.Pod, revisions ...string) time.Duration {
	// Already-recorded deletion must finish before any new eligibility/backoff
	// decision, including after spec/policy changes or an empty informer list.
	restarting := len(instance.Status.RetiringPods) > 0
	if fresh != nil {
		restarting = len(fresh.Status.RetiringPods) > 0
	}
	if restarting {
		return 0
	}
	if len(revisions) == 2 && revisions[0] != revisions[1] {
		return 0
	}
	// Only applies when restart policy is RecreateRoleInstanceOnPodRestart
	if !workloadsv1alpha2.IsRoleInstanceRecreatePolicy(instance.Spec.GetRestartPolicy()) {
		return 0
	}
	// Acknowledge an early generation before ordinary Failed cleanup can
	// consume its startup signal. Rolling revisions were excluded above.
	if instance.Generation != instance.Status.ObservedGeneration ||
		(fresh != nil && fresh.Generation != instance.Generation) {
		return restartRequeue
	}

	allPods := make([]*v1.Pod, 0, len(pods)+len(inactivePods))
	allPods = append(allPods, pods...)
	allPods = append(allPods, inactivePods...)

	// Ready no longer gates explicit failures, so a false predicate also means
	// backoff must not block normal updates, scale-in or Ignore handling.
	if !shouldRecreateInstance(instance, allPods, instance.Status.InPlaceUpdateContainerBaselines) {
		return 0
	}

	// Use fresh API data (synced by syncRestartTrackingFromAPI) for restart
	// tracking fields. The informer cache may be stale for RestartCount,
	// LastRestartTime, and the Restarting condition.
	restartCount := instance.Status.RestartCount
	lastRestartTime := instance.Status.LastRestartTime
	if fresh != nil {
		restartCount = fresh.Status.RestartCount
		lastRestartTime = fresh.Status.LastRestartTime
	}

	// No delay config — proceed immediately
	baseDelay := instance.Spec.GetBaseDelaySeconds()
	maxDelay := instance.Spec.GetMaxDelaySeconds()
	if baseDelay == 0 && maxDelay == 0 {
		return 0
	}

	if lastRestartTime == nil {
		return 0
	}

	delay := calculateRestartDelay(baseDelay, maxDelay, restartCount)
	if delay == 0 {
		return 0
	}

	elapsed := time.Since(lastRestartTime.Time)
	remaining := time.Duration(delay)*time.Second - elapsed
	if remaining > 0 {
		klog.V(2).Infof("Restart backoff: instance %s waiting %v (restartCount=%d, delay=%ds)",
			klog.KObj(instance), remaining.Round(time.Second), restartCount, delay)
		return remaining
	}
	return 0
}

// calculateRestartDelay computes the exponential backoff delay for a restart attempt.
// Formula: min(baseDelay * 2^(restartCount-1), maxDelay)
// The first backoff (restartCount=1) equals baseDelay, then doubles each round.
// A maxDelaySeconds of 0 is treated as no cap, but CEL validation requires
// maxDelaySeconds >= baseDelaySeconds, so this only occurs when both are 0
// (backoff disabled) — the no-cap branch is effectively unreachable for valid input.
func calculateRestartDelay(baseDelaySeconds, maxDelaySeconds, restartCount int32) int32 {
	if baseDelaySeconds <= 0 || restartCount <= 0 {
		return 0
	}
	// Use (restartCount-1) so the first backoff equals baseDelay.
	rc := restartCount - 1
	// Guard against int64 overflow BEFORE the shift. For any base >= 1,
	// base * 2^rc overflows int64 when rc >= 63 - bits.Len64(base).
	// rc >= 62 always overflows; for larger bases the threshold is lower.
	// In all overflow cases the result far exceeds any practical maxDelay.
	if rc >= 62 || int64(baseDelaySeconds) > (int64(1)<<(62-rc)) {
		if maxDelaySeconds > 0 {
			return maxDelaySeconds
		}
		return 0x7FFFFFFF
	}
	delay := int64(baseDelaySeconds) * (int64(1) << rc)
	if maxDelaySeconds > 0 && delay > int64(maxDelaySeconds) {
		return maxDelaySeconds
	}
	if delay > int64(0x7FFFFFFF) {
		return 0x7FFFFFFF
	}
	return int32(delay)
}

// isAlreadyRestarting checks the saved deletion list read by calculateDiffs.
// Legacy Restarting conditions/cache entries cannot block pre-Ready retries.
func (c *realControl) isAlreadyRestarting(instance *workloadsv1alpha2.RoleInstance) bool {
	return len(instance.Status.RetiringPods) > 0
}

// shouldRecreateInstance checks if the instance should be recreated (all pods deleted then recreated).
// Both recreate policies handle Failed Pods and unexpected ordinary-container
// restarts. The opt-in RecreateRoleInstanceOnPodRestartFromCreation policy additionally handles
// observed desired-member deletion and does not require a prior Ready condition.
// Init containers and native sidecars are not considered restart triggers.
//
// Pods with the restart-trigger-policy annotation set to "Ignore" are excluded —
// their failures and container restarts will not trigger instance recreation.
// This is useful for auxiliary components (e.g., monitoring, logging sidecars)
// whose failures should not affect the main workload.
//
// For restartPolicy=None, Pod Failed triggers replacement Pod
// creation through normal reconciliation (GetActiveAndInactivePods → createPods).
//
// Per KEP Non-Goals, a member that completes on its own is normal completion and
// must not trigger recreation: only Failed and an observed deletion are signals,
// so a Pod that merely reached Succeeded while it is still alive is ignored. Once
// such a Pod is being deleted, the deletion is the signal whatever its phase
// (note that for restartPolicy=Always kubelet assigns Succeeded only while the Pod
// is terminating, so a Succeeded member under the default policy is always a
// cleanly terminated one).
func shouldRecreateInstance(instance *workloadsv1alpha2.RoleInstance, pods []*v1.Pod, baselines map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline) bool {
	policy := instance.Spec.GetRestartPolicy()
	if !workloadsv1alpha2.IsRoleInstanceRecreatePolicy(policy) {
		return false
	}

	// If no pods exist yet (initial creation), don't trigger recreate
	if len(pods) == 0 {
		return false
	}

	// Keep the legacy Ready/recent-restart guard; the new policy also handles
	// explicit failures before its first Ready. Neither interrupts spec updates.
	if (policy == workloadsv1alpha2.RecreateRoleInstanceOnPodRestart && !wasInstanceReady(instance) && !hasRecentRestart(instance)) ||
		instance.Generation != instance.Status.ObservedGeneration {
		return false
	}
	// CurrentRevision != UpdateRevision means a rolling update is in progress
	// (not all pods have converged to the target revision yet). Container restarts
	// during this window are from the update process, not unexpected failures.
	if instance.Status.CurrentRevision != instance.Status.UpdateRevision {
		return false
	}

	_, triggered := restartTrigger(instance, pods, baselines)
	return triggered
}

// restartTrigger names the member and signal for the accepted-restart event.
func restartTrigger(instance *workloadsv1alpha2.RoleInstance, pods []*v1.Pod, baselines map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline) (string, bool) {
	policy := instance.Spec.GetRestartPolicy()
	for _, p := range pods {
		if policy == workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation && !isDesiredMember(instance, p) {
			continue
		}
		if hasTriggerPolicyIgnore(p) {
			continue
		}
		// The opt-in policy treats explicit loss before the in-place exemption:
		// an update explains container restarts, never Failed or deletion.
		// Keep legacy ordering and its lack of a deletion trigger unchanged.
		if policy == workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation {
			if p.Status.Phase == v1.PodFailed {
				return fmt.Sprintf("member %s is Failed", p.Name), true
			}
			if p.DeletionTimestamp != nil {
				return fmt.Sprintf("member %s is being deleted", p.Name), true
			}
		}
		// If this pod is currently undergoing an in-place update, skip it.
		// The container restart is expected and should not trigger instance recreation.
		// We continue checking other pods so that a genuine PodFailed on a sibling
		// is not masked by one pod's in-place update state.
		if isPodInPlaceUpdating(p) {
			continue
		}
		if p.Status.Phase == v1.PodFailed && p.DeletionTimestamp == nil {
			return fmt.Sprintf("member %s is Failed", p.Name), true
		}
		// Check if any container has restarted beyond what's expected from in-place updates.
		if containerRestarted(p) && !isContainerRestartExpected(p, baselines) {
			return fmt.Sprintf("member %s restarted unexpectedly", p.Name), true
		}
	}

	return "", false
}

// isDesiredMember excludes removed components and ordinals during normal scale-in.
func isDesiredMember(instance *workloadsv1alpha2.RoleInstance, pod *v1.Pod) bool {
	id := instanceutil.GetPodComponentID(pod)
	for _, component := range instance.Spec.Components {
		if component.Name == instanceutil.GetPodComponentName(pod) && component.Size != nil && id >= 0 && id < *component.Size {
			return true
		}
	}
	return false
}

// containerRestarted reports whether an ordinary container of the Pod has been
// restarted. Init containers, including native sidecars, are deliberately not
// considered: restart-trigger-policy is per Pod, so a sidecar restart could not be
// exempted without also exempting the main container.
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

// wasInstanceReady retains the legacy policy's readiness guard.
func wasInstanceReady(instance *workloadsv1alpha2.RoleInstance) bool {
	for _, condition := range instance.Status.Conditions {
		if condition.Type == workloadsv1alpha2.RoleInstanceReady && condition.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

// hasRecentRestart retains the bounded Ready bypass for the legacy policy.
func hasRecentRestart(instance *workloadsv1alpha2.RoleInstance) bool {
	return instance.Status.LastRestartTime != nil && time.Since(instance.Status.LastRestartTime.Time) <= stableThreshold(instance.Spec.GetMaxDelaySeconds())
}

// setRestartingCondition marks the instance as currently restarting due to restart policy.
// The condition is only a projection of status.retiringPods: it is written together
// with the retiring list and cleared once those UIDs are gone, not when Ready returns.
func setRestartingCondition(instance *workloadsv1alpha2.RoleInstance) {
	for i, cond := range instance.Status.Conditions {
		if cond.Type == workloadsv1alpha2.RoleInstanceRestarting {
			instance.Status.Conditions[i].Status = v1.ConditionTrue
			instance.Status.Conditions[i].LastTransitionTime = metav1.Now()
			instance.Status.Conditions[i].Reason = restartPolicyTriggeredReason
			instance.Status.Conditions[i].Message = restartPolicyTriggeredMessage
			return
		}
	}
	instance.Status.Conditions = append(instance.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{
		Type:               workloadsv1alpha2.RoleInstanceRestarting,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Reason:             restartPolicyTriggeredReason,
		Message:            restartPolicyTriggeredMessage,
	})
}

// stableThreshold returns the duration after which the instance is considered
// stable: max(maxDelaySeconds*2, 10 minutes). Used by updateRestartTracking to
// reset RestartCount after a long quiet period.
func stableThreshold(maxDelaySeconds int32) time.Duration {
	threshold := time.Duration(maxDelaySeconds) * 2 * time.Second
	if threshold < 10*time.Minute {
		threshold = 10 * time.Minute
	}
	return threshold
}

// updateRestartTracking increments RestartCount and updates LastRestartTime
// on the instance status. Called when a restart-policy-triggered recreation
// is about to happen. The updated values are used by checkRestartBackoff
// on subsequent reconciles to compute exponential backoff delays.
//
// If the instance has been stable for a long period (no restarts for
// max(maxDelaySeconds*2, 10 minutes)), the RestartCount is reset to 0
// so that a future crash starts with a short backoff instead of the
// accumulated maximum.
func updateRestartTracking(instance *workloadsv1alpha2.RoleInstance, pods ...*v1.Pod) error {
	for _, pod := range pods {
		if pod.UID == "" || !metav1.IsControlledBy(pod, instance) {
			return fmt.Errorf("cannot retire pod %s without a verified owner and UID", pod.Name)
		}
		instance.Status.RetiringPods = append(instance.Status.RetiringPods,
			workloadsv1alpha2.RoleInstancePodReference{Name: pod.Name, UID: pod.UID})
	}
	// A full group rebuild invalidates every old allowance, including absent or
	// renamed slots. Clear them only after all selected Pods pass validation.
	instance.Status.InPlaceUpdateContainerBaselines = nil
	// Reset counter if the instance has been stable for a long period.
	if instance.Status.LastRestartTime != nil {
		if time.Since(instance.Status.LastRestartTime.Time) > stableThreshold(instance.Spec.GetMaxDelaySeconds()) {
			instance.Status.RestartCount = 0
		}
	}
	instance.Status.RestartCount++
	now := metav1.Now()
	instance.Status.LastRestartTime = &now
	return nil
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
