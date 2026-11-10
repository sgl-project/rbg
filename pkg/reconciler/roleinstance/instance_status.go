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

package roleinstance

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instancecore "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/core"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/lifecycle"
	synccontrol "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/sync"
	instanceutils "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

var initialBatchSize = 3

const notReadyMessage = "NotReady"

// StatusUpdater is interface for updating CloneSet status.
type StatusUpdater interface {
	UpdateInstanceStatus(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus, pods []*v1.Pod) error
}

func newStatusUpdater(c client.Client, readers ...client.Reader) StatusUpdater {
	r := &realStatusUpdater{Client: c, lifecycleControl: lifecycle.New(c)}
	if len(readers) > 0 {
		r.apiReader = readers[0]
	}
	return r
}

type realStatusUpdater struct {
	client.Client
	apiReader        client.Reader
	lifecycleControl lifecycle.Interface
}

func (r *realStatusUpdater) UpdateInstanceStatus(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus, pods []*v1.Pod) error {
	conditions := make([]workloadsv1alpha2.RoleInstanceCondition, 0, len(newStatus.Conditions))
	conditions = append(conditions, newStatus.Conditions...)
	r.calculateStatus(instance, newStatus, conditions, pods)
	var updateStatusErr error
	if r.inconsistentStatus(instance, newStatus) {
		updateStatusErr = r.updateStatus(ctx, instance, newStatus, pods...)
	}
	updatePodsConditionErr := r.updatePodsLifeCycle(ctx, instance, pods)
	return utilerrors.NewAggregate([]error{updateStatusErr, updatePodsConditionErr})
}

// controllerOwnedConditionTypes returns the stable set of condition types whose
// values in newStatus.Conditions take precedence over whatever is in the live object.
// All other condition types are considered externally-owned and must be carried
// forward from the freshly-fetched clone on every status update.
//
// NOTE: Do NOT derive this set from newStatus.Conditions at call time. By the
// point this function is called, setInstanceConditions has already appended
// externally-owned conditions copied from the live object into newStatus.Conditions.
// Deriving the set dynamically would therefore misclassify those external types as
// owned, silently clobbering concurrent writes from other controllers.
//
// The rule for inclusion is:
//   - Ready and AllPodsReady are generated fresh each reconcile.
//   - FailedScale and FailedUpdate are generated when the corresponding operation
//     reports a failure. Their absence means no failure was reported in this
//     reconcile: the operation succeeded or was not attempted. Either way,
//     setInstanceConditions drops stale copies from the live object.
//   - Restarting is part of the retirement projection (projectRetirement), applied when
//     the status is computed and again by updateStatus as a correction against the
//     authoritative deletion list.
//   - Custom/in-place readiness conditions remain owned by their external writers.
func controllerOwnedConditionTypes() sets.Set[workloadsv1alpha2.RoleInstanceConditionType] {
	return sets.New[workloadsv1alpha2.RoleInstanceConditionType](
		// Computed by this controller on every reconcile.
		workloadsv1alpha2.RoleInstanceReady,
		workloadsv1alpha2.RoleInstanceAllPodsReady,
		workloadsv1alpha2.RoleInstanceFailedScale,
		workloadsv1alpha2.RoleInstanceFailedUpdate,
		workloadsv1alpha2.RoleInstanceRestarting,
	)
}

func controllerFailureConditionTypes() sets.Set[workloadsv1alpha2.RoleInstanceConditionType] {
	return sets.New[workloadsv1alpha2.RoleInstanceConditionType](
		workloadsv1alpha2.RoleInstanceFailedScale,
		workloadsv1alpha2.RoleInstanceFailedUpdate,
	)
}

func (r *realStatusUpdater) calculateStatus(instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus,
	conditions []workloadsv1alpha2.RoleInstanceCondition, pods []*v1.Pod) {
	coreControl := instancecore.New(instance)
	componentStatuses := make(map[string]workloadsv1alpha2.RoleInstanceComponentStatus)

	for _, pod := range pods {
		componentStatus := componentStatuses[instanceutils.GetPodComponentName(pod)]
		componentStatus.Size++
		if coreControl.IsPodUpdateReady(pod, 0) {
			componentStatus.ReadyReplicas++
			componentStatus.AvailableReplicas++
		}
		if instanceutils.IsPodScheduled(pod) {
			componentStatus.ScheduledReplicas++
		}
		if instanceutils.EqualToRevisionHash("", pod, newStatus.UpdateRevision) {
			componentStatus.UpdatedReplicas++
		}
		if instanceutils.EqualToRevisionHash("", pod, newStatus.UpdateRevision) && coreControl.IsPodUpdateReady(pod, 0) {
			componentStatus.UpdatedReadyReplicas++
		}
		componentStatuses[instanceutils.GetPodComponentName(pod)] = componentStatus
	}

	var (
		newStatusUpdatedReplicas int32
		newStatusReplicas        = int32(len(pods))
		componentSize            int32
	)
	newStatus.ComponentStatuses = make([]workloadsv1alpha2.RoleInstanceComponentStatus, len(instance.Spec.Components))
	for i, component := range instance.Spec.Components {
		newStatus.ComponentStatuses[i] = componentStatuses[component.Name]
		newStatus.ComponentStatuses[i].Name = component.Name
		newStatusUpdatedReplicas += componentStatuses[component.Name].UpdatedReplicas
		componentSize += *component.Size
	}
	if newStatusUpdatedReplicas == newStatusReplicas && newStatusReplicas == componentSize {
		newStatus.CurrentRevision = newStatus.UpdateRevision
		// Garbage-collect in-place update baselines for pods that no longer exist.
		// Keep baselines for existing pods so that the expected restart (RestartCount
		// = baseline+1) remains protected against false recreation on subsequent
		// reconciles. Baselines are naturally overwritten on the next in-place update.
		if newStatus.InPlaceUpdateContainerBaselines != nil {
			activePodNames := make(map[string]bool, len(pods))
			for _, pod := range pods {
				activePodNames[pod.Name] = true
			}
			for podName := range newStatus.InPlaceUpdateContainerBaselines {
				if !activePodNames[podName] {
					delete(newStatus.InPlaceUpdateContainerBaselines, podName)
				}
			}
			if len(newStatus.InPlaceUpdateContainerBaselines) == 0 {
				newStatus.InPlaceUpdateContainerBaselines = nil
			}
		}
	}
	r.setInstanceConditions(instance, newStatus, conditions)
}

func (r *realStatusUpdater) setInstanceConditions(instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus,
	newInnerConditions []workloadsv1alpha2.RoleInstanceCondition) {
	conditions := make([]workloadsv1alpha2.RoleInstanceCondition, 0, len(instance.Status.Conditions)+1)
	conditions = append(conditions, newInnerConditions...)
	podsReadyCondition := r.getInstancePodsReadyCondition(instance)
	// newInnerConditions only ever carries the failure conditions, so the types
	// appended below cannot already be present.
	conditions = append(conditions, getInstanceReadyCondition(
		instance,
		podsReadyCondition,
		r.getInstanceInplaceUpdateReadyCondition(instance),
	))
	if instance.Spec.ReadyPolicy == workloadsv1alpha2.RoleInstanceReadyOnAllPodReady && podsReadyCondition != nil {
		conditions = append(conditions, *podsReadyCondition)
	}
	innerConditionTypes := sets.New[workloadsv1alpha2.RoleInstanceConditionType]()
	for _, c := range conditions {
		innerConditionTypes.Insert(c.Type)
	}
	failureConditionTypes := controllerFailureConditionTypes()
	var customInstanceConditions []workloadsv1alpha2.RoleInstanceCondition
	for _, c := range instance.Status.Conditions {
		if failureConditionTypes.Has(c.Type) {
			continue
		}
		if !innerConditionTypes.Has(c.Type) {
			customInstanceConditions = append(customInstanceConditions, c)
		}
	}
	conditions = append(conditions, customInstanceConditions...)
	newStatus.Conditions = projectRetirement(conditions, instance.Status.RetiringPods)
	// Budget suppression takes precedence over the drain's Ready reason.
	projected := &workloadsv1alpha2.RoleInstance{Status: *newStatus}
	projected.Status.RestartBudgetExhausted = instance.Status.RestartBudgetExhausted
	synccontrol.EnforceRestartBudgetConditions(projected)
	newStatus.Conditions = projected.Status.Conditions
}

// retirementReadyCondition is the Ready condition of an instance whose previous
// member Pods are still being removed. It is the single definition of the
// draining Ready state, used both when the status is computed and when the write
// path re-derives it from the authoritative deletion list.
func retirementReadyCondition() workloadsv1alpha2.RoleInstanceCondition {
	return workloadsv1alpha2.RoleInstanceCondition{
		Type:               workloadsv1alpha2.RoleInstanceReady,
		Status:             v1.ConditionFalse,
		Reason:             "Restarting",
		Message:            "Removing the previous member Pods",
		LastTransitionTime: metav1.NewTime(time.Now()),
	}
}

// restartingCondition reports that the previous member Pods are being removed.
func restartingCondition() workloadsv1alpha2.RoleInstanceCondition {
	return workloadsv1alpha2.RoleInstanceCondition{
		Type:               workloadsv1alpha2.RoleInstanceRestarting,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(time.Now()),
		Reason:             "RestartPolicyTriggered",
		Message:            "Instance is being recreated due to restart policy",
	}
}

// projectRetirement is shared by computed and persisted status. The write path
// reapplies it to the authoritative list when the informer snapshot lags behind.
func projectRetirement(conditions []workloadsv1alpha2.RoleInstanceCondition, retiring []workloadsv1alpha2.RoleInstancePodReference) []workloadsv1alpha2.RoleInstanceCondition {
	if len(retiring) == 0 {
		return slices.DeleteFunc(conditions, func(condition workloadsv1alpha2.RoleInstanceCondition) bool {
			return condition.Type == workloadsv1alpha2.RoleInstanceRestarting
		})
	}
	conditions = replaceCondition(conditions, retirementReadyCondition())
	restarting := restartingCondition()
	for i := range conditions {
		if conditions[i].Type == workloadsv1alpha2.RoleInstanceRestarting {
			restarting.LastTransitionTime = conditions[i].LastTransitionTime
			conditions[i] = restarting
			return conditions
		}
	}
	return append(conditions, restarting)
}

// replaceCondition swaps the condition of the same type in place.
func replaceCondition(conditions []workloadsv1alpha2.RoleInstanceCondition, replacement workloadsv1alpha2.RoleInstanceCondition) []workloadsv1alpha2.RoleInstanceCondition {
	for i := range conditions {
		if conditions[i].Type == replacement.Type {
			conditions[i] = replacement
			return conditions
		}
	}
	return append(conditions, replacement)
}

// downgradeReadyForLiveGate clears a Ready that the freshly fetched readiness
// gate contradicts. The gate condition is written by the in-place-update
// controller, so a value read from the informer snapshot cannot outvote it.
func downgradeReadyForLiveGate(conditions []workloadsv1alpha2.RoleInstanceCondition) {
	ready := -1
	gateBlocked := false
	for i := range conditions {
		switch conditions[i].Type {
		case workloadsv1alpha2.RoleInstanceReady:
			ready = i
		case workloadsv1alpha2.RoleInstanceInPlaceUpdateReady:
			gateBlocked = conditions[i].Status == v1.ConditionFalse
		}
	}
	if ready < 0 || !gateBlocked || conditions[ready].Status == v1.ConditionFalse {
		return
	}
	conditions[ready].Status = v1.ConditionFalse
	conditions[ready].Reason = "InplaceUpdateNotReady"
	conditions[ready].Message = notReadyMessage
}

func (r *realStatusUpdater) getInstancePodsReadyCondition(instance *workloadsv1alpha2.RoleInstance) *workloadsv1alpha2.RoleInstanceCondition {
	policy := instance.Spec.ReadyPolicy
	if policy == "" {
		policy = workloadsv1alpha2.RoleInstanceReadyOnAllPodReady
	}
	switch policy {
	case workloadsv1alpha2.RoleInstanceReadyOnAllPodReady:
		return r.getAllReadyCondition(instance)
	case workloadsv1alpha2.RoleInstanceReadyPolicyTypeNone:
		return nil
	default:
		return r.getAllReadyCondition(instance)
	}
}

func (r *realStatusUpdater) getAllReadyCondition(instance *workloadsv1alpha2.RoleInstance) *workloadsv1alpha2.RoleInstanceCondition {
	readyCondition := &workloadsv1alpha2.RoleInstanceCondition{
		Type:               workloadsv1alpha2.RoleInstanceAllPodsReady,
		LastTransitionTime: metav1.NewTime(time.Now()),
		Status:             v1.ConditionFalse,
	}
	coreControl := instancecore.New(instance)
	pods, err := r.getExpectOwnedPods(instance)
	if err != nil {
		readyCondition.Message = err.Error()
		return readyCondition
	}
	for _, pod := range pods {
		if !coreControl.IsPodUpdateReady(pod, 0) {
			readyCondition.Message = fmt.Sprintf("instance need all pods to be ready, "+
				"but %v is in notready condition", klog.KObj(pod))
			readyCondition.Reason = "AtLeastOnePodNotReady"
			return readyCondition
		}
	}
	readyCondition.Status = v1.ConditionTrue
	readyCondition.Reason = "PodsReady"
	readyCondition.Message = "Ready"
	return readyCondition
}

func (r *realStatusUpdater) getExpectOwnedPods(instance *workloadsv1alpha2.RoleInstance) ([]*v1.Pod, error) {
	var pods []*v1.Pod
	for _, component := range instance.Spec.Components {
		size := *component.Size
		for i := 0; i < int(size); i++ {
			pod := new(v1.Pod)
			if err := r.Get(context.TODO(), client.ObjectKey{
				Namespace: instance.Namespace,
				Name: instanceutils.FormatComponentPodName(instance.Name, component.Name, int32(i),
					instance.GetRoleTemplateType()),
			}, pod); err != nil {
				return nil, err
			}
			// A same-name Pod can still belong to a previous instance UID.
			if !metav1.IsControlledBy(pod, instance) {
				return nil, fmt.Errorf("pod %s is not controlled by RoleInstance %s (UID %s)",
					klog.KObj(pod), klog.KObj(instance), instance.UID)
			}
			if pod.DeletionTimestamp != nil {
				return nil, fmt.Errorf("pod %s is terminating", klog.KObj(pod))
			}
			pods = append(pods, pod)
		}
	}
	return pods, nil
}

func (r *realStatusUpdater) getInstanceInplaceUpdateReadyCondition(instance *workloadsv1alpha2.RoleInstance) *workloadsv1alpha2.RoleInstanceCondition {
	if !containsInstanceInplaceUpdateReadinessGates(instance) {
		return nil
	}
	for _, condition := range instance.Status.Conditions {
		if condition.Type == workloadsv1alpha2.RoleInstanceInPlaceUpdateReady {
			return &condition
		}
	}
	return &workloadsv1alpha2.RoleInstanceCondition{
		Type:               workloadsv1alpha2.RoleInstanceInPlaceUpdateReady,
		Status:             v1.ConditionFalse,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}
}

func containsInstanceInplaceUpdateReadinessGates(instance *workloadsv1alpha2.RoleInstance) bool {
	for _, gate := range instance.Spec.ReadinessGates {
		if gate.ConditionType == workloadsv1alpha2.RoleInstanceInPlaceUpdateReady {
			return true
		}
	}
	return false
}

func getInstanceReadyCondition(instance *workloadsv1alpha2.RoleInstance, podsReady, inplace *workloadsv1alpha2.RoleInstanceCondition) workloadsv1alpha2.RoleInstanceCondition {
	instanceReady := workloadsv1alpha2.RoleInstanceCondition{
		Type:               workloadsv1alpha2.RoleInstanceReady,
		Status:             v1.ConditionFalse,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}
	if instance.Status.RestartBudgetExhausted {
		instanceReady.Reason = synccontrol.GroupRestartBudgetExceeded
		instanceReady.Message = synccontrol.GroupRestartBudgetMessage
		return instanceReady
	}
	if containsInstanceInplaceUpdateReadinessGates(instance) {
		if inplace != nil {
			if inplace.Status == v1.ConditionFalse {
				instanceReady.Reason = "InplaceUpdateNotReady"
				instanceReady.Message = notReadyMessage
				return instanceReady
			}
		} else {
			instanceReady.Reason = "ReadinessGatesNotReady"
			instanceReady.Message = fmt.Sprintf("corresponding condition of pod readiness gate \"%s\" does not exist.",
				workloadsv1alpha2.RoleInstanceInPlaceUpdateReady)
			return instanceReady
		}
	}
	if podsReady != nil && podsReady.Status == v1.ConditionFalse {
		instanceReady.Reason = "PodsNotReady"
		instanceReady.Message = notReadyMessage
		return instanceReady
	}
	instanceReady.Status = v1.ConditionTrue
	instanceReady.Reason = "Ready"
	return instanceReady
}

func (r *realStatusUpdater) inconsistentStatus(instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus) bool {
	oldStatus := instance.Status
	if newStatus.ObservedGeneration > oldStatus.ObservedGeneration ||
		newStatus.LabelSelector != oldStatus.LabelSelector ||
		newStatus.CurrentRevision != oldStatus.CurrentRevision ||
		newStatus.UpdateRevision != oldStatus.UpdateRevision ||
		newStatus.RestartCount != oldStatus.RestartCount ||
		inconsistentRestartTime(oldStatus.LastRestartTime, newStatus.LastRestartTime) {
		return true
	}
	if len(oldStatus.ComponentStatuses) != len(newStatus.ComponentStatuses) {
		return true
	}
	for i := range newStatus.ComponentStatuses {
		if inconsistentComponentStatus(oldStatus.ComponentStatuses[i], newStatus.ComponentStatuses[i]) {
			return true
		}
	}
	if inconsistentCondition(oldStatus.Conditions, newStatus.Conditions) {
		return true
	}
	return inconsistentBaselines(oldStatus.InPlaceUpdateContainerBaselines, newStatus.InPlaceUpdateContainerBaselines)
}

// inconsistentBaselines checks whether InPlaceUpdateContainerBaselines has changed.
func inconsistentBaselines(old, new map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline) bool {
	if len(old) != len(new) {
		return true
	}
	for podName, oldContainers := range old {
		newContainers, ok := new[podName]
		if !ok || len(oldContainers) != len(newContainers) {
			return true
		}
		for cName, oldBaseline := range oldContainers {
			newBaseline, exists := newContainers[cName]
			if !exists || oldBaseline.RestartCount != newBaseline.RestartCount || oldBaseline.ImageID != newBaseline.ImageID {
				return true
			}
		}
	}
	return false
}

// inconsistentRestartTime checks whether LastRestartTime has changed.
func inconsistentRestartTime(old, new *metav1.Time) bool {
	if old == nil && new == nil {
		return false
	}
	if old == nil || new == nil {
		return true
	}
	return !old.Equal(new)
}

func inconsistentComponentStatus(oldRoleStatus, newRoleStatus workloadsv1alpha2.RoleInstanceComponentStatus) bool {
	return oldRoleStatus.Size != newRoleStatus.Size ||
		oldRoleStatus.Name != newRoleStatus.Name ||
		oldRoleStatus.ReadyReplicas != newRoleStatus.ReadyReplicas ||
		oldRoleStatus.UpdatedReadyReplicas != newRoleStatus.UpdatedReadyReplicas ||
		oldRoleStatus.AvailableReplicas != newRoleStatus.AvailableReplicas ||
		oldRoleStatus.UpdatedReplicas != newRoleStatus.UpdatedReplicas
}

func inconsistentCondition(oldConditions, newConditions []workloadsv1alpha2.RoleInstanceCondition) bool {
	if len(oldConditions) != len(newConditions) {
		return true
	}
	oldConditionMap := make(map[workloadsv1alpha2.RoleInstanceConditionType]workloadsv1alpha2.RoleInstanceCondition, len(oldConditions))
	for _, c := range oldConditions {
		oldConditionMap[c.Type] = c
	}
	for _, c := range newConditions {
		old, ok := oldConditionMap[c.Type]
		if !ok {
			return true
		}
		if old.Status != c.Status ||
			old.Message != c.Message ||
			old.Reason != c.Reason {
			return true
		}
	}
	return false
}

func (r *realStatusUpdater) updateStatus(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus, pods ...*v1.Pod) error {
	ownedTypes := controllerOwnedConditionTypes()
	// Try the informer first; the optimistic RV patch detects a stale snapshot.
	// After any conflict all retries must read the API, not the same stale cache.
	useAPI := false
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		clone := &workloadsv1alpha2.RoleInstance{}
		reader := client.Reader(r.Client)
		cachedRead := !useAPI || r.apiReader == nil
		if !cachedRead {
			reader = r.apiReader
		}
		useAPI = true
		err := reader.Get(ctx, types.NamespacedName{Namespace: instance.Namespace, Name: instance.Name}, clone)
		if apierrors.IsNotFound(err) && cachedRead && r.apiReader != nil {
			reader = r.apiReader
			cachedRead = false
			err = reader.Get(ctx, client.ObjectKeyFromObject(instance), clone)
		}
		if err != nil {
			if apierrors.IsNotFound(err) {
				// Instance has been deleted; nothing to update.
				return nil
			}
			return err
		}
		if cachedRead && r.apiReader != nil &&
			(clone.UID != instance.UID || clone.DeletionTimestamp != nil ||
				clone.Generation != newStatus.ObservedGeneration || clone.Status.ObservedGeneration > newStatus.ObservedGeneration) {
			reader = r.apiReader
			if err := reader.Get(ctx, client.ObjectKeyFromObject(instance), clone); err != nil {
				return client.IgnoreNotFound(err)
			}
		}
		before := clone.DeepCopy()
		if clone.UID != instance.UID || clone.DeletionTimestamp != nil {
			return nil
		}
		if clone.Generation != newStatus.ObservedGeneration {
			// Do not acknowledge a newer spec with stale aggregate status. Pod
			// updates have already happened, however: preserve their baselines
			// so the expected image-change restart is not mistaken for a crash.
			changed, err := preserveAppliedUpdateBaselines(ctx, reader, instance, clone, newStatus, pods)
			if err != nil || !changed {
				return err
			}
			return r.Status().Patch(ctx, clone, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
		}
		if clone.Status.ObservedGeneration > newStatus.ObservedGeneration {
			return nil
		}
		// Snapshot the live custom conditions before overwriting clone.Status.
		// These are conditions not owned by this controller (e.g. RoleInstanceCustomReady
		// written by readiness/*, RoleInstanceInPlaceUpdateReady written by inplaceupdate/*).
		// They may have been updated concurrently between when we fetched instance at the
		// top of Reconcile and now, so we must carry them forward rather than silently
		// rolling them back via the precomputed newStatus.
		liveCustomConditions := make([]workloadsv1alpha2.RoleInstanceCondition, 0)
		for _, c := range clone.Status.Conditions {
			if !ownedTypes.Has(c.Type) {
				liveCustomConditions = append(liveCustomConditions, c)
			}
		}
		// calculateDiffs already committed the list/count/time before Delete.
		// Preserve that fresh authority, including an empty clear or count reset.
		liveStatus := clone.Status.DeepCopy()
		clone.Status = *newStatus.DeepCopy()
		clone.Status.Conditions = slices.DeleteFunc(clone.Status.Conditions, func(condition workloadsv1alpha2.RoleInstanceCondition) bool {
			return !ownedTypes.Has(condition.Type)
		})
		clone.Status.Conditions = append(clone.Status.Conditions, liveCustomConditions...)
		clone.Status.RetiringPods = liveStatus.RetiringPods
		clone.Status.RestartCount = liveStatus.RestartCount
		clone.Status.LastRestartTime = liveStatus.LastRestartTime
		clone.Status.GroupRestartCount = liveStatus.GroupRestartCount
		clone.Status.GroupRestartRevision = liveStatus.GroupRestartRevision
		clone.Status.RestartBudgetExhausted = liveStatus.RestartBudgetExhausted
		clone.Status.Conditions = projectRetirement(clone.Status.Conditions, liveStatus.RetiringPods)
		// Ready was computed from the informer snapshot, but the readiness gate is
		// owned by the in-place-update controller and only the live value is
		// authoritative. Publishing Ready=True next to a newer gate=False would both
		// violate the gate contract and roll back the writer's own Ready=False.
		downgradeReadyForLiveGate(clone.Status.Conditions)
		synccontrol.EnforceRestartBudgetConditions(clone)
		// Do not resurrect old baselines cleared when the list was recorded;
		// keep genuinely new baselines recorded by the existing Update path.
		for name, baselines := range clone.Status.InPlaceUpdateContainerBaselines {
			if _, exists := liveStatus.InPlaceUpdateContainerBaselines[name]; !exists &&
				maps.Equal(baselines, instance.Status.InPlaceUpdateContainerBaselines[name]) {
				delete(clone.Status.InPlaceUpdateContainerBaselines, name)
			}
		}
		return r.Status().Patch(ctx, clone, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	})
}

// preserveAppliedUpdateBaselines carries only bookkeeping for verified Pod
// updates across an RI spec change. It never copies stale aggregate status,
// overwrites a newer baseline, or restores allowances during retirement.
func preserveAppliedUpdateBaselines(ctx context.Context, reader client.Reader,
	instance, live *workloadsv1alpha2.RoleInstance, computed *workloadsv1alpha2.RoleInstanceStatus, pods []*v1.Pod) (bool, error) {
	if len(live.Status.RetiringPods) > 0 {
		return false, nil
	}
	changed := false
	for _, pod := range pods {
		baseline := computed.InPlaceUpdateContainerBaselines[pod.Name]
		previous := instance.Status.InPlaceUpdateContainerBaselines[pod.Name]
		if pod.UID == "" || maps.Equal(baseline, previous) {
			continue
		}
		current := &v1.Pod{}
		if err := reader.Get(ctx, client.ObjectKeyFromObject(pod), current); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return false, err
		}
		if current.UID != pod.UID || !metav1.IsControlledBy(current, live) ||
			current.DeletionTimestamp != nil || !instanceutils.EqualToRevisionHash("", current, computed.UpdateRevision) {
			continue
		}
		for name, value := range baseline {
			old, existed := previous[name]
			if existed && value == old {
				continue
			}
			existing, present := live.Status.InPlaceUpdateContainerBaselines[pod.Name][name]
			if present && (!existed || existing != old) {
				continue
			}
			if live.Status.InPlaceUpdateContainerBaselines == nil {
				live.Status.InPlaceUpdateContainerBaselines = make(map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline)
			}
			if live.Status.InPlaceUpdateContainerBaselines[pod.Name] == nil {
				live.Status.InPlaceUpdateContainerBaselines[pod.Name] = make(map[string]workloadsv1alpha2.ContainerUpdateBaseline)
			}
			live.Status.InPlaceUpdateContainerBaselines[pod.Name][name] = value
			changed = true
		}
	}
	return changed, nil
}

func (r *realStatusUpdater) updatePodsLifeCycle(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, pods []*v1.Pod) error {
	podsChan := make(chan *v1.Pod, len(pods))
	for _, p := range pods {
		podsChan <- p
	}
	inInplaceUpdating := isInstanceInInplaceUpdatingPhase(instance)
	markPodNotReady := inInplaceUpdating
	if getInstanceReadyPolicy(instance) == workloadsv1alpha2.RoleInstanceReadyOnAllPodReady {
		allPodRuntimeReady := r.allPodsRuntimeReady(ctx, instance, pods)
		markPodNotReady = inInplaceUpdating || !allPodRuntimeReady
	}
	_, err := instanceutils.DoItSlowly(len(pods), initialBatchSize, func() error {
		pod := <-podsChan
		if updated, gotPod, updateErr := r.lifecycleControl.UpdatePodLifecycle(instance, pod, markPodNotReady); updateErr != nil {
			return updateErr
		} else if updated {
			instanceutils.ResourceVersionExpectations.Expect(gotPod)
		}
		return nil
	})
	return err
}

func isInstanceInInplaceUpdatingPhase(instance *workloadsv1alpha2.RoleInstance) bool {
	if !instanceutils.ContainsReadinessGate(instance, workloadsv1alpha2.RoleInstanceInPlaceUpdateReady) {
		return false
	}
	for _, condition := range instance.Status.Conditions {
		if condition.Type == workloadsv1alpha2.RoleInstanceInPlaceUpdateReady {
			return condition.Status == v1.ConditionFalse
		}
	}
	return true
}

func getInstanceReadyPolicy(instance *workloadsv1alpha2.RoleInstance) workloadsv1alpha2.RoleInstanceReadyPolicyType {
	policy := instance.Spec.ReadyPolicy
	if policy == "" {
		policy = workloadsv1alpha2.RoleInstanceReadyOnAllPodReady
	}
	return policy
}

// allPodsRuntimeReady checks whether all existing pods in the instance are runtime-ready.
// It uses only the already-fetched filteredPods to avoid fetching pods that have not yet
// been created (e.g. components whose creation is gated on ordered startup dependencies).
// This prevents a false "not-ready" lifecycle mark on existing pods when some components
// haven't started yet due to startAfter ordering.
func (r *realStatusUpdater) allPodsRuntimeReady(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, pods []*v1.Pod) bool {
	logger := log.FromContext(ctx)
	for _, pod := range pods {
		if !instanceutils.IsPodRuntimeReady(pod, 0) {
			logger.Info("pod runtime not ready", "pod", klog.KObj(pod))
			return false
		}
	}
	return true
}
