package instance

import (
	"context"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	instancecore "sigs.k8s.io/rbgs/pkg/reconciler/instance/core"
	"sigs.k8s.io/rbgs/pkg/reconciler/instance/lifecycle"
	instanceutils "sigs.k8s.io/rbgs/pkg/reconciler/instance/utils"
)

var initialBatchSize = 3

// StatusUpdater is interface for updating CloneSet status.
type StatusUpdater interface {
	UpdateInstanceStatus(ctx context.Context, instance *v1alpha1.Instance, newStatus *v1alpha1.InstanceStatus, pods []*v1.Pod) error
}

func newStatusUpdater(c client.Client) StatusUpdater {
	return &realStatusUpdater{Client: c, lifecycleControl: lifecycle.New(c)}
}

type realStatusUpdater struct {
	client.Client
	lifecycleControl lifecycle.Interface
}

func (r *realStatusUpdater) UpdateInstanceStatus(ctx context.Context, instance *v1alpha1.Instance, newStatus *v1alpha1.InstanceStatus, pods []*v1.Pod) error {
	conditions := make([]v1alpha1.InstanceCondition, 0, len(newStatus.Conditions))
	conditions = append(conditions, newStatus.Conditions...)
	r.calculateStatus(instance, newStatus, conditions, pods)
	var updateStatusErr error
	if r.inconsistentStatus(instance, newStatus) {
		updateStatusErr = r.updateStatus(ctx, instance, newStatus)
	}
	updatePodsConditionErr := r.updatePodsLifeCycle(ctx, instance, pods)
	return utilerrors.NewAggregate([]error{updateStatusErr, updatePodsConditionErr})
}

func (r *realStatusUpdater) calculateStatus(instance *v1alpha1.Instance, newStatus *v1alpha1.InstanceStatus,
	conditions []v1alpha1.InstanceCondition, pods []*v1.Pod) {
	coreControl := instancecore.New(instance)
	componentStatuses := make(map[string]v1alpha1.ComponentStatus)

	for _, pod := range pods {
		componentStatus := componentStatuses[instanceutils.GetPodComponentName(pod)]
		componentStatus.Replicas++
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
	newStatus.ComponentStatuses = make([]v1alpha1.ComponentStatus, len(instance.Spec.Components))
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

func (r *realStatusUpdater) setInstanceConditions(instance *v1alpha1.Instance, newStatus *v1alpha1.InstanceStatus,
	newInnerConditions []v1alpha1.InstanceCondition) {
	conditions := make([]v1alpha1.InstanceCondition, 0, len(instance.Status.Conditions)+1)
	conditions = append(conditions, newInnerConditions...)
	podsReadyCondition := r.getInstancePodsReadyCondition(instance)
	conditions = append(conditions, getInstanceReadyCondition(
		instance,
		podsReadyCondition,
		r.getInstanceInplaceUpdateReadyCondition(instance),
	))
	if instance.Spec.ReadyPolicy == v1alpha1.InstanceReadyOnAllPodReady && podsReadyCondition != nil {
		conditions = append(conditions, *podsReadyCondition)
	}
	innerConditionTypes := sets.New[v1alpha1.InstanceConditionType]()
	for _, c := range conditions {
		innerConditionTypes.Insert(c.Type)
	}
	var customInstanceConditions []v1alpha1.InstanceCondition
	for _, c := range instance.Status.Conditions {
		if !innerConditionTypes.Has(c.Type) {
			customInstanceConditions = append(customInstanceConditions, c)
		}
	}
	conditions = append(conditions, customInstanceConditions...)
	newStatus.Conditions = conditions
}

func (r *realStatusUpdater) getInstancePodsReadyCondition(instance *v1alpha1.Instance) *v1alpha1.InstanceCondition {
	policy := instance.Spec.ReadyPolicy
	if policy == "" {
		policy = v1alpha1.InstanceReadyOnAllPodReady
	}
	switch policy {
	case v1alpha1.InstanceReadyOnAllPodReady:
		return r.getAllReadyCondition(instance)
	case v1alpha1.InstanceReadyPolicyTypeNone:
		return nil
	default:
		return r.getAllReadyCondition(instance)
	}
}

func (r *realStatusUpdater) getAllReadyCondition(instance *v1alpha1.Instance) *v1alpha1.InstanceCondition {
	readyCondition := &v1alpha1.InstanceCondition{
		Type:               v1alpha1.InstanceAllPodsReady,
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

func (r *realStatusUpdater) getExpectOwnedPods(instance *v1alpha1.Instance) ([]*v1.Pod, error) {
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

func (r *realStatusUpdater) getInstanceInplaceUpdateReadyCondition(instance *v1alpha1.Instance) *v1alpha1.InstanceCondition {
	if !containsInstanceInplaceUpdateReadinessGates(instance) {
		return nil
	}
	for _, condition := range instance.Status.Conditions {
		if condition.Type == v1alpha1.InstanceInPlaceUpdateReady {
			return &condition
		}
	}
	return &v1alpha1.InstanceCondition{
		Type:               v1alpha1.InstanceInPlaceUpdateReady,
		Status:             v1.ConditionFalse,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}
}

func containsInstanceInplaceUpdateReadinessGates(instance *v1alpha1.Instance) bool {
	for _, gate := range instance.Spec.ReadinessGates {
		if gate.ConditionType == v1alpha1.InstanceInPlaceUpdateReady {
			return true
		}
	}
	return false
}

func getInstanceReadyCondition(instance *v1alpha1.Instance, podsReady, inplace *v1alpha1.InstanceCondition) v1alpha1.InstanceCondition {
	instanceReady := v1alpha1.InstanceCondition{
		Type:               v1alpha1.InstanceReady,
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
				v1alpha1.InstanceInPlaceUpdateReady)
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

func (r *realStatusUpdater) inconsistentStatus(instance *v1alpha1.Instance, newStatus *v1alpha1.InstanceStatus) bool {
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

func inconsistentComponentStatus(oldRoleStatus, newRoleStatus v1alpha1.ComponentStatus) bool {
	return oldRoleStatus.Replicas != newRoleStatus.Replicas ||
		oldRoleStatus.Name != newRoleStatus.Name ||
		oldRoleStatus.ReadyReplicas != newRoleStatus.ReadyReplicas ||
		oldRoleStatus.UpdatedReadyReplicas != newRoleStatus.UpdatedReadyReplicas ||
		oldRoleStatus.AvailableReplicas != newRoleStatus.AvailableReplicas ||
		oldRoleStatus.UpdatedReplicas != newRoleStatus.UpdatedReplicas
}

func inconsistentCondition(oldConditions, newConditions []v1alpha1.InstanceCondition) bool {
	if len(oldConditions) != len(newConditions) {
		return true
	}
	oldConditionMap := make(map[v1alpha1.InstanceConditionType]v1alpha1.InstanceCondition, len(oldConditions))
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

func (r *realStatusUpdater) updateStatus(ctx context.Context, instance *v1alpha1.Instance, newStatus *v1alpha1.InstanceStatus) error {
	instance.Status = *newStatus
	return r.Status().Update(ctx, instance)
}

func (r *realStatusUpdater) updatePodsLifeCycle(ctx context.Context, instance *v1alpha1.Instance, pods []*v1.Pod) error {
	podsChan := make(chan *v1.Pod, len(pods))
	for _, p := range pods {
		podsChan <- p
	}
	inInplaceUpdating := isInstanceInInplaceUpdatingPhase(instance)
	markPodNotReady := inInplaceUpdating
	if getInstanceReadyPolicy(instance) == v1alpha1.InstanceReadyOnAllPodReady {
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

func isInstanceInInplaceUpdatingPhase(instance *v1alpha1.Instance) bool {
	if !instanceutils.ContainsReadinessGate(instance, v1alpha1.InstanceInPlaceUpdateReady) {
		return false
	}
	for _, condition := range instance.Status.Conditions {
		if condition.Type == v1alpha1.InstanceInPlaceUpdateReady {
			return condition.Status == v1.ConditionFalse
		}
	}
	return true
}

func getInstanceReadyPolicy(instance *v1alpha1.Instance) v1alpha1.InstanceReadyPolicyType {
	policy := instance.Spec.ReadyPolicy
	if policy == "" {
		policy = v1alpha1.InstanceReadyOnAllPodReady
	}
	return policy
}

func (r *realStatusUpdater) allPodsRuntimeReady(ctx context.Context, instance *v1alpha1.Instance) bool {
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
