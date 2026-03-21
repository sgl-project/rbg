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
//   - Controller-computed types (Ready, AllPodsReady, FailedScale, FailedUpdate) are
//     always generated fresh each reconcile and must win over the live object.
//   - RoleInstanceInPlaceUpdateReady and RoleInstanceCustomReady are written by
//     external controllers, but setInstanceConditions always copies them from
//     instance.Status into newStatus (via getInstanceInplaceUpdateReadyCondition and
//     the custom-condition passthrough). They are therefore already present in
//     newStatus.Conditions, so they must be listed here to prevent a duplicate entry
//     being appended from the live clone.
func controllerOwnedConditionTypes() sets.Set[workloadsv1alpha2.RoleInstanceConditionType] {
	return sets.New[workloadsv1alpha2.RoleInstanceConditionType](
		// Computed by this controller on every reconcile.
		workloadsv1alpha2.RoleInstanceReady,
		workloadsv1alpha2.RoleInstanceAllPodsReady,
		workloadsv1alpha2.RoleInstanceFailedScale,
		workloadsv1alpha2.RoleInstanceFailedUpdate,
		// Written externally but always copied into newStatus by setInstanceConditions,
		// so they are already present and must not be appended again from the live clone.
		workloadsv1alpha2.RoleInstanceInPlaceUpdateReady,
		workloadsv1alpha2.RoleInstanceCustomReady,
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
	}
	r.setInstanceConditions(instance, newStatus, conditions)
}

func (r *realStatusUpdater) setInstanceConditions(instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus,
	newInnerConditions []workloadsv1alpha2.RoleInstanceCondition) {
	conditions := make([]workloadsv1alpha2.RoleInstanceCondition, 0, len(instance.Status.Conditions)+1)
	conditions = append(conditions, newInnerConditions...)
	podsReadyCondition := r.getInstancePodsReadyCondition(instance)
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
	var customInstanceConditions []workloadsv1alpha2.RoleInstanceCondition
	for _, c := range instance.Status.Conditions {
		if !innerConditionTypes.Has(c.Type) {
			customInstanceConditions = append(customInstanceConditions, c)
		}
	}
	conditions = append(conditions, customInstanceConditions...)
	newStatus.Conditions = conditions
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
	if containsInstanceInplaceUpdateReadinessGates(instance) {
		if inplace != nil {
			if inplace.Status == v1.ConditionFalse {
				instanceReady.Reason = "InplaceUpdateNotReady"
				instanceReady.Message = "NotReady"
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
		instanceReady.Message = "NotReady"
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
		newStatus.UpdateRevision != oldStatus.UpdateRevision {
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
	return inconsistentCondition(oldStatus.Conditions, newStatus.Conditions)
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
		clone.Status = *newStatus
		clone.Status.Conditions = append(clone.Status.Conditions, liveCustomConditions...)
		return r.Status().Update(ctx, clone)
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
		allPodRuntimeReady := r.allPodsRuntimeReady(ctx, instance)
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

func (r *realStatusUpdater) allPodsRuntimeReady(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) bool {
	logger := log.FromContext(ctx)
	pods, err := r.getExpectOwnedPods(instance)
	if err != nil {
		return false
	}
	for _, pod := range pods {
		if !instanceutils.IsPodRuntimeReady(pod, 0) {
			logger.Info("pod runtime not ready", "pod", klog.KObj(pod))
			return false
		}
	}
	return true
}
