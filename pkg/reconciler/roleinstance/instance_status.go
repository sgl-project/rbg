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
	inplaceutil "sigs.k8s.io/rbgs/pkg/inplace/instance"
	instancecore "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/core"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/lifecycle"
	instanceutils "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

var initialBatchSize = 3

// StatusUpdater is interface for updating CloneSet status.
type StatusUpdater interface {
	UpdateInstanceStatus(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus, pods []*v1.Pod) error
}

func newStatusUpdater(c client.Client) StatusUpdater {
	return &realStatusUpdater{Client: c, lifecycleControl: lifecycle.New(c)}
}

type realStatusUpdater struct {
	client.Client
	lifecycleControl lifecycle.Interface
}

func (r *realStatusUpdater) UpdateInstanceStatus(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus, pods []*v1.Pod) error {
	conditions := make([]workloadsv1alpha2.RoleInstanceCondition, 0, len(newStatus.Conditions))
	conditions = append(conditions, newStatus.Conditions...)
	r.calculateStatus(instance, newStatus, conditions, pods)
	var updateStatusErr error
	if r.inconsistentStatus(instance, newStatus) {
		updateStatusErr = r.updateStatus(ctx, instance, newStatus)
	}
	updatePodsConditionErr := r.updatePodsLifeCycle(ctx, instance, pods)
	return utilerrors.NewAggregate([]error{updateStatusErr, updatePodsConditionErr})
}

// controllerOwnedConditionTypes 固定 writer 归属；gate 始终以冲突重试时的最新对象为准。
// 已废弃的条件也保留在此集合，避免从旧对象重新合入。
func controllerOwnedConditionTypes() sets.Set[workloadsv1alpha2.RoleInstanceConditionType] {
	return sets.New[workloadsv1alpha2.RoleInstanceConditionType](
		// 聚合条件和重启条件由 RI controller 写入。
		workloadsv1alpha2.RoleInstanceReady,
		workloadsv1alpha2.RoleInstanceAllPodsReady,
		workloadsv1alpha2.RoleInstanceFailedScale,
		workloadsv1alpha2.RoleInstanceFailedUpdate,
		workloadsv1alpha2.RoleInstanceRestarting,
		workloadsv1alpha2.RoleInstanceConditionType("RollingUpdateInProgress"),
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
	clone := instance.DeepCopy()
	clone.Status.Conditions = nil
	for _, c := range instance.Status.Conditions {
		if c.Type == workloadsv1alpha2.RoleInstanceFailedScale || c.Type == workloadsv1alpha2.RoleInstanceFailedUpdate || c.Type == "RollingUpdateInProgress" {
			continue
		}
		if c.Type == workloadsv1alpha2.RoleInstanceAllPodsReady && getInstanceReadyPolicy(instance) == workloadsv1alpha2.RoleInstanceReadyPolicyTypeNone {
			continue
		}
		inplaceutil.SetRoleInstanceCondition(clone, c)
	}
	propagatedRestart := false
	for _, c := range newInnerConditions {
		if c.Type == workloadsv1alpha2.RoleInstanceRestarting {
			inplaceutil.SetRoleInstanceCondition(clone, c)
			propagatedRestart = true
		}
	}
	podsReadyCondition := r.getInstancePodsReadyCondition(instance)
	if podsReadyCondition != nil {
		inplaceutil.SetRoleInstanceCondition(clone, *podsReadyCondition)
	}
	ready := getInstanceReadyCondition(clone, podsReadyCondition, r.getInstanceInplaceUpdateReadyCondition(clone))
	inplaceutil.SetRoleInstanceCondition(clone, ready)
	// 保持原清除时机：本轮没有传播活动重启且原操作性健康条件已满足。
	controlReady, _ := inplaceutil.GetRoleInstanceControlReadiness(clone)
	restarting := inplaceutil.GetRoleInstanceCondition(clone, workloadsv1alpha2.RoleInstanceRestarting)
	if restarting == nil || (!propagatedRestart && controlReady) {
		inplaceutil.SetRoleInstanceCondition(clone, workloadsv1alpha2.RoleInstanceCondition{
			Type: workloadsv1alpha2.RoleInstanceRestarting, Status: v1.ConditionFalse,
			Reason: workloadsv1alpha2.RoleInstanceReasonNoRestartInProgress,
		})
	}
	newStatus.Conditions = clone.Status.Conditions
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
		readyCondition.Reason = workloadsv1alpha2.RoleInstanceReasonPodReadFailed
		readyCondition.Message = err.Error()
		return readyCondition
	}
	for _, pod := range pods {
		if !coreControl.IsPodUpdateReady(pod, 0) {
			readyCondition.Message = fmt.Sprintf("instance need all pods to be ready, "+
				"but %v is in notready condition", klog.KObj(pod))
			readyCondition.Reason = workloadsv1alpha2.RoleInstanceReasonAtLeastOnePodNotReady
			return readyCondition
		}
	}
	readyCondition.Status = v1.ConditionTrue
	readyCondition.Reason = workloadsv1alpha2.RoleInstanceReasonPodsReady
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
			// A recreated instance can encounter a same-name Pod left behind
			// by its previous UID while garbage collection is still pending.
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
		Reason:             workloadsv1alpha2.RoleInstanceReasonReadinessGatesNotReady,
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
	for _, gate := range instance.Spec.ReadinessGates {
		condition := inplaceutil.GetRoleInstanceCondition(instance, gate.ConditionType)
		if gate.ConditionType == workloadsv1alpha2.RoleInstanceInPlaceUpdateReady {
			condition = inplace
		}
		if condition == nil || condition.Status != v1.ConditionTrue {
			instanceReady.Reason = workloadsv1alpha2.RoleInstanceReasonReadinessGatesNotReady
			if gate.ConditionType == workloadsv1alpha2.RoleInstanceInPlaceUpdateReady && condition != nil {
				instanceReady.Reason = workloadsv1alpha2.RoleInstanceReasonInPlaceUpdateNotReady
			}
			instanceReady.Message = fmt.Sprintf("readiness gate %q is not True", gate.ConditionType)
			return instanceReady
		}
	}
	if getInstanceReadyPolicy(instance) != workloadsv1alpha2.RoleInstanceReadyPolicyTypeNone && (podsReady == nil || podsReady.Status != v1.ConditionTrue) {
		instanceReady.Reason = workloadsv1alpha2.RoleInstanceReasonPodsNotReady
		instanceReady.Message = "NotReady"
		return instanceReady
	}
	instanceReady.Status = v1.ConditionTrue
	instanceReady.Reason = workloadsv1alpha2.RoleInstanceReasonReady
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

// restartTrackingChanged detects whether updateRestartTracking ran this reconcile.
// It returns true when newLastRestartTime differs from BOTH the informer-cached
// value and the live API value, indicating an intentional write by updateRestartTracking
// (which sets metav1.Now(), producing a timestamp that differs from both stale sources).
func restartTrackingChanged(newLastRestartTime, instanceLastRestartTime, liveLastRestartTime *metav1.Time) bool {
	return newLastRestartTime != nil &&
		(instanceLastRestartTime == nil ||
			!newLastRestartTime.Equal(instanceLastRestartTime)) &&
		(liveLastRestartTime == nil ||
			!newLastRestartTime.Equal(liveLastRestartTime))
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
			old.Reason != c.Reason ||
			(old.LastTransitionTime.IsZero() && !c.LastTransitionTime.IsZero()) {
			return true
		}
	}
	return false
}

func (r *realStatusUpdater) updateStatus(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus) error {
	ownedTypes := controllerOwnedConditionTypes()
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		clone := &workloadsv1alpha2.RoleInstance{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: instance.Namespace, Name: instance.Name}, clone); err != nil {
			if apierrors.IsNotFound(err) {
				// Instance has been deleted; nothing to update.
				return nil
			}
			return err
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
		// Snapshot live restart tracking before overwriting clone.Status.
		// The informer cache may be stale: a previous reconcile may have already
		// persisted a different RestartCount/LastRestartTime. We use the live API
		// value (clone) as the source of truth and only overwrite when this
		// reconcile intentionally modified restart tracking (via updateRestartTracking).
		liveRestartCount := clone.Status.RestartCount
		liveLastRestartTime := clone.Status.LastRestartTime
		// Detect whether updateRestartTracking ran this reconcile. It sets
		// LastRestartTime = metav1.Now(), producing a timestamp that differs from
		// BOTH the informer's value AND the live API value. In contrast,
		// syncRestartTrackingFromAPI copies the API's timestamp, which equals the
		// live value — so the double-check below correctly identifies that case as
		// "not changed" and preserves the live (potentially reset) count.
		restartTrackingModified := restartTrackingChanged(newStatus.LastRestartTime, instance.Status.LastRestartTime, liveLastRestartTime)
		liveConditions := clone.Status.Conditions
		clone.Status = *newStatus.DeepCopy()
		clone.Status.Conditions = nil
		for _, c := range liveCustomConditions {
			inplaceutil.SetRoleInstanceCondition(clone, c)
		}
		for _, c := range newStatus.Conditions {
			if ownedTypes.Has(c.Type) && c.Type != workloadsv1alpha2.RoleInstanceFailedScale && c.Type != workloadsv1alpha2.RoleInstanceFailedUpdate && c.Type != "RollingUpdateInProgress" {
				for _, old := range liveConditions {
					if old.Type == c.Type && old.Status == c.Status && !old.LastTransitionTime.IsZero() {
						c.LastTransitionTime = old.LastTransitionTime
						break
					}
				}
				inplaceutil.SetRoleInstanceCondition(clone, c)
			}
		}
		// 合并最新 gate 后重新求 AND，不能持久化与 gate 矛盾的 Ready。
		ready := getInstanceReadyCondition(clone,
			inplaceutil.GetRoleInstanceCondition(clone, workloadsv1alpha2.RoleInstanceAllPodsReady),
			r.getInstanceInplaceUpdateReadyCondition(clone))
		inplaceutil.SetRoleInstanceCondition(clone, ready)
		controlReady, _ := inplaceutil.GetRoleInstanceControlReadiness(clone)
		if !controlReady {
			for _, c := range liveConditions {
				if c.Type == workloadsv1alpha2.RoleInstanceRestarting && c.Status == v1.ConditionTrue {
					inplaceutil.SetRoleInstanceCondition(clone, c)
					break
				}
			}
		}
		if restartTrackingModified {
			// updateRestartTracking ran this reconcile — trust newStatus values.
			// This covers both increments (count goes up) and resets (count goes
			// down after a stable period). The timestamp is always brand-new.
		} else {
			// Restart tracking was NOT modified by updateRestartTracking this
			// reconcile. Preserve the live API values to prevent a stale informer
			// (or a syncRestartTrackingFromAPI pass-through) from clobbering a
			// reset that was persisted by a prior reconcile.
			clone.Status.RestartCount = liveRestartCount
			clone.Status.LastRestartTime = liveLastRestartTime
		}
		if err := r.Status().Update(ctx, clone); err != nil {
			return err
		}
		newStatus.Conditions = clone.Status.Conditions
		return nil
	})
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
