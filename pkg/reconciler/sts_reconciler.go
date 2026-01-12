package reconciler

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	appsapplyv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	metaapplyv1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type StatefulSetReconciler struct {
	scheme *runtime.Scheme
	client client.Client
}

var _ WorkloadReconciler = &StatefulSetReconciler{}

func NewStatefulSetReconciler(scheme *runtime.Scheme, client client.Client) *StatefulSetReconciler {
	return &StatefulSetReconciler{
		scheme: scheme,
		client: client,
	}
}

func (r *StatefulSetReconciler) Validate(
	ctx context.Context, role *workloadsv1alpha1.RoleSpec) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("start to validate role declaration")
	if role.TemplateSource.Template == nil && !role.UsesRoleTemplate() {
		return fmt.Errorf("either 'template' or 'templateRef' is required when use %s as workload", role.Workload.String())
	}

	return nil
}

func (r *StatefulSetReconciler) Reconciler(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
	rollingUpdateStrategy *workloadsv1alpha1.RollingUpdate, revisionKey string,
) error {
	if err := r.reconcileStatefulSet(ctx, rbg, role, rollingUpdateStrategy, revisionKey); err != nil {
		return err
	}

	return NewServiceReconciler(r.client).reconcileHeadlessService(ctx, rbg, role)
}

func (r *StatefulSetReconciler) reconcileStatefulSet(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
	rollingUpdateStrategy *workloadsv1alpha1.RollingUpdate, revisionKey string,
) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("start to reconciling sts workload")

	rollingStrategy, err := validateRolloutStrategy(role.RolloutStrategy, int(*role.Replicas))
	if err != nil {
		logger.Error(err, "Invalid rollout strategy")
		return err
	}
	role.RolloutStrategy = rollingStrategy

	oldSts := &appsv1.StatefulSet{}
	err = r.client.Get(ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, oldSts)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	stsApplyConfig, err := r.constructStatefulSetApplyConfiguration(ctx, rbg, role, oldSts, revisionKey)
	if err != nil {
		logger.Error(err, "Failed to construct statefulset apply configuration")
		return err
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(stsApplyConfig)
	if err != nil {
		logger.Error(err, "Converting obj apply configuration to json.")
		return err
	}

	newSts := &appsv1.StatefulSet{}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(obj, newSts); err != nil {
		return fmt.Errorf("convert stsApplyConfig to sts error: %s", err.Error())
	}

	// the err value was used to pass the differences between the old and new objects,
	// not to indicate an actual processing error.
	semanticallyEqual, err := semanticallyEqualStatefulSet(oldSts, newSts, false)
	if err != nil {
		logger.Info(fmt.Sprintf("sts not equal, diff: %s", err.Error()))
	}

	roleHashKey := fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, role.Name)
	revisionHashEqual := newSts.Labels[roleHashKey] == oldSts.Labels[roleHashKey]
	if !revisionHashEqual {
		logger.Info(
			fmt.Sprintf(
				"sts hash not equal, old: %s, new: %s",
				oldSts.Labels[roleHashKey], newSts.Labels[roleHashKey],
			),
		)
	}

	stsUpdated := !semanticallyEqual || !revisionHashEqual
	partition, replicas, err := r.rollingUpdateParameters(ctx, role, oldSts, stsUpdated, rollingUpdateStrategy)
	if err != nil {
		return err
	}

	if semanticallyEqual && revisionHashEqual && partition == *oldSts.Spec.UpdateStrategy.RollingUpdate.Partition &&
		*oldSts.Spec.Replicas == *role.Replicas {
		logger.Info("sts equal, skip reconcile")
		return nil
	}

	rollingUpdate := appsapplyv1.RollingUpdateStatefulSetStrategy().WithPartition(partition)
	if role.RolloutStrategy.RollingUpdate.MaxUnavailable != nil {
		rollingUpdate = rollingUpdate.WithMaxUnavailable(*role.RolloutStrategy.RollingUpdate.MaxUnavailable)
	}

	stsApplyConfig = stsApplyConfig.WithSpec(
		stsApplyConfig.Spec.WithReplicas(replicas).
			WithUpdateStrategy(
				appsapplyv1.StatefulSetUpdateStrategy().
					WithType(appsv1.StatefulSetUpdateStrategyType(role.RolloutStrategy.Type)).
					WithRollingUpdate(rollingUpdate),
			),
	)

	if err := utils.PatchObjectApplyConfiguration(ctx, r.client, stsApplyConfig, utils.PatchSpec); err != nil {
		logger.Error(err, "Failed to patch statefulset apply configuration")
		return err
	}

	return nil
}

// Rolling update will always wait for the former replica to be ready then process the next one,
// Possible scenarios for Partition:
//   - When sts is under creation, partition is always 0 because pods are created in parallel, rolling update is not
//     relevant here.
//   - When sts is in rolling update, the partition will start from the last index to the index 0 processing in
//     maxUnavailable step.
//   - When sts is in rolling update, and Replicas increases, we'll delay the rolling update until the scaling up is
//     done, Partition will not change, new replicas are created using the new template from the get-go.
//   - When sts is rolling update, and Replicas decreases, the partition will not change until new Replicas < Partition,
//     in which case Partition will be reset to the new Replicas value.
//   - When sts is ready for a rolling update and Replicas increases at the same time, we'll delay the rolling update
//     until the scaling up is done.
//   - When sts is ready for a rolling update and Replicas decreases at the same time, we'll start the rolling update
//     together with scaling down.
//
// At rest, Partition should always be zero.
//
// For Replicas:
//   - When rolling update, Replicas is equal to (spec.Replicas+maxSurge)
//   - Otherwise, Replicas is equal to spec.Replicas
//   - One exception here is when unready replicas of leaderWorkerSet is equal to MaxSurge,
//     we should reclaim the extra replicas gradually to accommodate for the new replicas.

func (r *StatefulSetReconciler) rollingUpdateParameters(
	ctx context.Context, role *workloadsv1alpha1.RoleSpec,
	sts *appsv1.StatefulSet, stsUpdated bool,
	coordinationRollout *workloadsv1alpha1.RollingUpdate,
) (stsPartition int32, replicas int32, err error) {
	logger := log.FromContext(ctx)
	roleReplicas := *role.Replicas

	defer func() {
		// Limit the replicas with less than partition will not be updated.
		var partition int
		partition, err = intstr.GetScaledValueFromIntOrPercent(role.RolloutStrategy.RollingUpdate.Partition, int(*role.Replicas), true)
		if err != nil {
			return
		}
		stsPartition = max(stsPartition, int32(partition))

	}()

	// Case 1:
	// If sts not created yet, all partitions should be updated,
	// replicas should not change.
	if sts == nil || sts.UID == "" {
		return 0, roleReplicas, nil
	}

	// Case 2:
	// If coordination enabled, maxSurge will not be considered.
	if coordinationRollout != nil && coordinationRollout.Partition != nil {
		partition, err := intstr.GetScaledValueFromIntOrPercent(coordinationRollout.Partition, int(*role.Replicas), true)
		if err != nil {
			return 0, 0, err
		}

		return int32(partition), roleReplicas, nil
	}

	stsReplicas := *sts.Spec.Replicas
	maxSurge, err := intstr.GetScaledValueFromIntOrPercent(
		role.RolloutStrategy.RollingUpdate.MaxSurge,
		int(roleReplicas), true,
	)
	if err != nil {
		return 0, 0, err
	}
	// No need to burst more than the replicas.
	if maxSurge > int(roleReplicas) {
		maxSurge = int(roleReplicas)
	}
	burstReplicas := roleReplicas + int32(maxSurge)

	// wantReplicas calculates the final replicas if needed.
	wantReplicas := func(unreadyReplicas int32) int32 {
		if unreadyReplicas <= int32(maxSurge) {
			// When we have n unready replicas and n bursted replicas, we should
			// start to release the burst replica gradually for the accommodation of
			// the unready ones.
			finalReplicas := roleReplicas + utils.NonZeroValue(unreadyReplicas-1)
			logger.Info(fmt.Sprintf("deleting surge replica %s-%d", role.Name, finalReplicas))
			return finalReplicas
		}
		return burstReplicas
	}

	// Case 3:
	// Indicates a new rolling update here.
	if stsUpdated {
		partition, replicas := min(roleReplicas, stsReplicas), wantReplicas(roleReplicas)
		// Processing scaling up/down first prior to rolling update.
		logger.V(1).Info(fmt.Sprintf("case 3: rolling update started. partition %d, replicas: %d", partition, replicas))
		return partition, replicas, nil
	}

	partition := *sts.Spec.UpdateStrategy.RollingUpdate.Partition
	rollingUpdateCompleted := partition == 0 && stsReplicas == roleReplicas
	// Case 4:
	// In normal cases, return the values directly.
	if rollingUpdateCompleted {
		logger.V(1).Info("case 4: rolling update completed.")
		return 0, roleReplicas, nil
	}

	states, err := r.getReplicaStates(ctx, sts)
	if err != nil {
		return 0, 0, err
	}
	roleUnreadyReplicas := calculateRoleUnreadyReplicas(states, roleReplicas)

	originalRoleReplicas, err := strconv.Atoi(sts.Annotations[workloadsv1alpha1.RoleSizeAnnotationKey])
	if err != nil {
		return 0, 0, err
	}
	replicasUpdated := originalRoleReplicas != int(*role.Replicas)
	// Case 5:
	// Replicas changed during rolling update.
	if replicasUpdated {
		partition = min(partition, burstReplicas)
		replicas := wantReplicas(roleUnreadyReplicas)
		logger.V(1).Info(
			fmt.Sprintf(
				"case 5: Replicas changed during rolling update. partition %d, replicas: %d", partition, replicas,
			),
		)
		return partition, replicas, nil
	}

	// Case 6:
	// Calculating the Partition during rolling update, no leaderWorkerSet updates happens.
	rollingStep, err := intstr.GetScaledValueFromIntOrPercent(
		role.RolloutStrategy.RollingUpdate.MaxUnavailable, int(roleReplicas), false,
	)
	if err != nil {
		return 0, 0, err
	}
	// Make sure that we always respect the maxUnavailable, or
	// we'll violate it when reclaiming bursted replicas.
	rollingStep += maxSurge - (int(burstReplicas) - int(stsReplicas))
	partition = rollingUpdatePartition(ctx, states, stsReplicas, int32(rollingStep), partition)
	replicas = wantReplicas(roleUnreadyReplicas)
	logger.V(1).Info(
		fmt.Sprintf(
			"case 6: Calculating the Partition during rolling update. partition %d, replicas: %d", partition, replicas,
		),
	)
	return partition, replicas, nil
}

func calculateRoleUnreadyReplicas(states []replicaState, roleReplicas int32) int32 {
	var unreadyCount int32
	for idx := int32(0); idx < roleReplicas; idx++ {
		if idx >= int32(len(states)) || !states[idx].ready || !states[idx].updated {
			unreadyCount++
		}
	}
	return unreadyCount
}

type replicaState struct {
	updated bool
	ready   bool
}

func (r *StatefulSetReconciler) getReplicaStates(ctx context.Context, sts *appsv1.StatefulSet) ([]replicaState, error) {
	logger := log.FromContext(ctx)
	if sts == nil || sts.UID == "" {
		return nil, fmt.Errorf("statefulset has not been created")
	}

	states := make([]replicaState, *sts.Spec.Replicas)
	sortedPods := make([]corev1.Pod, *sts.Spec.Replicas)

	podSelector := sts.Spec.Selector.MatchLabels
	var podList corev1.PodList
	if err := r.client.List(
		ctx, &podList, client.MatchingLabels(podSelector), client.InNamespace(sts.Namespace),
	); err != nil {
		return nil, err
	}
	for i, pod := range podList.Items {
		idx, err := strconv.Atoi(pod.Labels["apps.kubernetes.io/pod-index"])
		if err != nil {
			continue
		}
		if idx >= int(*sts.Spec.Replicas) {
			continue
		}
		sortedPods[idx] = podList.Items[i]
	}

	highestRevision, err := r.getHighestRevision(ctx, sts, podSelector)
	if err != nil {
		logger.Error(fmt.Errorf("get sts highest controller revision error"), "sts", sts.Name)
		return nil, err
	}
	if highestRevision == nil {
		return nil, fmt.Errorf("sts %s has no revision", sts.Name)
	}

	for idx := int32(0); idx < *sts.Spec.Replicas; idx++ {
		nominatedName := fmt.Sprintf("%s-%d", sts.Name, idx)
		// It can happen that the leader pod or the worker statefulset hasn't created yet
		// or under rebuilding, which also indicates not ready.
		if nominatedName != sortedPods[idx].Name {
			states[idx] = replicaState{
				ready:   false,
				updated: false,
			}
			continue
		}

		podReady := utils.PodRunningAndReady(sortedPods[idx])
		states[idx] = replicaState{
			ready:   podReady,
			updated: sortedPods[idx].Labels["controller-revision-hash"] == highestRevision.Name,
		}
	}
	return states, nil
}

func rollingUpdatePartition(
	ctx context.Context, states []replicaState, stsReplicas int32, rollingStep int32, currentPartition int32,
) int32 {
	logger := log.FromContext(ctx)

	continuousReadyReplicas := calculateContinuousReadyReplicas(states)

	// Update up to rollingStep replicas at once.
	rollingStepPartition := utils.NonZeroValue(stsReplicas - continuousReadyReplicas - rollingStep)

	// rollingStepPartition calculation above disregards the state of replicas with idx<rollingStepPartition.
	// To prevent violating the maxUnavailable, we have to account for these replicas
	// and increase the partition if some are not ready.
	var unavailable int32
	for idx := 0; idx < int(rollingStepPartition); idx++ {
		if !states[idx].ready {
			unavailable++
		}
	}
	var partition = rollingStepPartition + unavailable
	logger.V(1).Info(
		"Calculating partition parameters", "partition", partition,
		"continuousReady", continuousReadyReplicas, "rollingStep", rollingStep, "unavailable", unavailable,
	)

	// Reduce the partition if replicas are continuously not ready. It is safe since updating these replicas does not
	// impact the availability of the LWS. This is important to prevent update from getting stuck in case
	// maxUnavailable is already violated
	// (for example, all replicas are not ready when rolling update is started).
	// Note that we never drop the partition below rollingStepPartition.
	for idx := min(partition, stsReplicas-1); idx >= rollingStepPartition; idx-- {
		if !states[idx].ready || states[idx].updated {
			partition = idx
		} else {
			break
		}
	}

	// That means Partition moves in one direction to make it simple.
	return min(partition, currentPartition)
}

func calculateContinuousReadyReplicas(states []replicaState) int32 {
	// Count ready replicas at tail (from last index down)
	var continuousReadyCount int32
	for idx := len(states) - 1; idx >= 0; idx-- {
		if !states[idx].ready || !states[idx].updated {
			break
		}
		continuousReadyCount++
	}
	return continuousReadyCount
}

func (r *StatefulSetReconciler) constructStatefulSetApplyConfiguration(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
	oldSts *appsv1.StatefulSet,
	revisionKey string,
) (*appsapplyv1.StatefulSetApplyConfiguration, error) {
	matchLabels := rbg.GetCommonLabelsFromRole(role)
	if oldSts.UID != "" {
		// do not update selector when workload exists
		matchLabels = oldSts.Spec.Selector.MatchLabels
	}

	podReconciler := NewPodReconciler(r.scheme, r.client)
	podTemplateApplyConfiguration, err := podReconciler.ConstructPodTemplateSpecApplyConfiguration(
		ctx, rbg, role, maps.Clone(matchLabels),
	)
	if err != nil {
		return nil, err
	}
	stsLabel := maps.Clone(matchLabels)
	stsLabel[fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, role.Name)] = revisionKey

	svcName, err := utils.GetCompatibleHeadlessServiceName(ctx, r.client, rbg, role)
	if err != nil {
		return nil, err
	}
	// construct statefulset apply configuration
	statefulSetConfig := appsapplyv1.StatefulSet(rbg.GetWorkloadName(role), rbg.Namespace).
		WithSpec(
			appsapplyv1.StatefulSetSpec().
				// WithServiceName(rbg.GetWorkloadName(role)).
				WithServiceName(svcName).
				WithReplicas(*role.Replicas).
				WithTemplate(podTemplateApplyConfiguration).
				WithMinReadySeconds(role.MinReadySeconds).
				WithPodManagementPolicy(appsv1.ParallelPodManagement).
				WithSelector(
					metaapplyv1.LabelSelector().
						WithMatchLabels(matchLabels),
				),
		).
		WithAnnotations(labels.Merge(maps.Clone(role.Annotations), rbg.GetCommonAnnotationsFromRole(role))).
		WithLabels(labels.Merge(maps.Clone(role.Labels), stsLabel)).
		WithOwnerReferences(
			metaapplyv1.OwnerReference().
				WithAPIVersion(rbg.APIVersion).
				WithKind(rbg.Kind).
				WithName(rbg.Name).
				WithUID(rbg.GetUID()).
				WithBlockOwnerDeletion(true).
				WithController(true),
		)
	return statefulSetConfig, nil
}

func (r *StatefulSetReconciler) ConstructRoleStatus(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
) (workloadsv1alpha1.RoleStatus, bool, error) {
	sts := &appsv1.StatefulSet{}
	if err := r.client.Get(
		ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, sts,
	); err != nil {
		return workloadsv1alpha1.RoleStatus{Name: role.Name}, false, err
	}

	if sts.Status.ObservedGeneration < sts.Generation {
		err := fmt.Errorf("sts generation not equal to observed generation")
		return workloadsv1alpha1.RoleStatus{Name: role.Name}, false, err
	}

	status, updateStatus := ConstructRoleStatue(rbg, role,
		*sts.Spec.Replicas,
		sts.Status.ReadyReplicas,
		sts.Status.UpdatedReplicas)
	return status, updateStatus, nil
}

func (r *StatefulSetReconciler) CheckWorkloadReady(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
) (bool, error) {
	sts := &appsv1.StatefulSet{}
	if err := r.client.Get(
		ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, sts,
	); err != nil {
		return false, err
	}

	// We don't check ready if workload is rolling update if maxSkew is set.
	if utils.RoleInMaxSkewCoordination(rbg, role.Name) &&
		sts.Status.CurrentRevision != sts.Status.UpdateRevision {
		return true, nil
	}
	return sts.Status.ReadyReplicas == *sts.Spec.Replicas, nil
}

func (r *StatefulSetReconciler) CleanupOrphanedWorkloads(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup,
) error {
	return CleanupOrphanedObjs(ctx, r.client, rbg, schema.GroupVersionKind{
		Group:   "apps",
		Version: "v1",
		Kind:    "StatefulSet"})
}

func (r *StatefulSetReconciler) getHighestRevision(
	ctx context.Context, sts *appsv1.StatefulSet, roleCommonLabels map[string]string,
) (*appsv1.ControllerRevision, error) {
	selector, err := v1.LabelSelectorAsSelector(
		&v1.LabelSelector{
			MatchLabels: roleCommonLabels,
		},
	)
	if err != nil {
		return nil, err
	}
	revisions, err := utils.ListRevisions(ctx, r.client, sts, selector)
	if err != nil {
		return nil, err
	}
	return utils.GetHighestRevision(revisions), nil
}

func (r *StatefulSetReconciler) RecreateWorkload(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
) error {
	return RecreateObj(ctx, r.client, rbg, role, schema.GroupVersionKind{
		Group:   "apps",
		Version: "v1",
		Kind:    "StatefulSet"})
}

func semanticallyEqualStatefulSet(oldSts, newSts *appsv1.StatefulSet, checkStatus bool) (bool, error) {
	if oldSts == nil || oldSts.UID == "" {
		return false, errors.New("old sts not exist")
	}
	if newSts == nil {
		return false, fmt.Errorf("new sts is nil")
	}

	if equal, err := objectMetaEqual(oldSts.ObjectMeta, newSts.ObjectMeta); !equal {
		return false, fmt.Errorf("objectMeta not equal: %s", err.Error())
	}

	if equal, err := statefulSetSpecEqual(oldSts.Spec, newSts.Spec); !equal {
		return false, fmt.Errorf("spec not equal: %s", err.Error())
	}

	if checkStatus {
		if equal, err := statefulSetStatusEqual(oldSts.Status, newSts.Status); !equal {
			return false, fmt.Errorf("status not equal: %s", err.Error())
		}
	}
	return true, nil
}

func statefulSetSpecEqual(spec1, spec2 appsv1.StatefulSetSpec) (bool, error) {
	if !reflect.DeepEqual(spec1.Selector, spec2.Selector) {
		return false, fmt.Errorf("selector not equal, old: %v, new: %v", spec1.Selector, spec2.Selector)
	}

	if spec1.ServiceName != spec2.ServiceName {
		return false, fmt.Errorf("serviceName not equal, old: %s, new: %s", spec1.ServiceName, spec2.ServiceName)
	}

	if equal, err := podTemplateSpecEqual(spec1.Template, spec2.Template); !equal {
		return false, fmt.Errorf("podTemplateSpec not equal, %s", err.Error())
	}

	return true, nil
}

func statefulSetStatusEqual(oldStatus, newStatus appsv1.StatefulSetStatus) (bool, error) {
	if !reflect.DeepEqual(oldStatus, newStatus) {
		return false, fmt.Errorf("status not equal")
	}
	return true, nil
}

func validateRolloutStrategy(
	rollingStrategy *workloadsv1alpha1.RolloutStrategy, replicas int,
) (*workloadsv1alpha1.RolloutStrategy, error) {
	if rollingStrategy == nil || rollingStrategy.RollingUpdate == nil {
		return &workloadsv1alpha1.RolloutStrategy{
			Type: workloadsv1alpha1.RollingUpdateStrategyType,
			RollingUpdate: &workloadsv1alpha1.RollingUpdate{
				MaxUnavailable: ptr.To(intstr.FromInt32(1)),
				MaxSurge:       ptr.To(intstr.FromInt32(0)),
				Partition:      ptr.To(intstr.FromInt32(0)),
			},
		}, nil
	}

	if rollingStrategy.RollingUpdate.Partition == nil {
		rollingStrategy.RollingUpdate.Partition = ptr.To(intstr.FromInt32(0))
	}

	maxSurge, err := intstr.GetScaledValueFromIntOrPercent(rollingStrategy.RollingUpdate.MaxSurge, replicas, true)
	if err != nil {
		return nil, err
	}
	maxAvailable, err := intstr.GetScaledValueFromIntOrPercent(
		rollingStrategy.RollingUpdate.MaxUnavailable, replicas, false,
	)
	if err != nil {
		return nil, err
	}
	if maxAvailable == 0 && maxSurge == 0 {
		return nil, fmt.Errorf(
			"RollingUpdate is invalid: " +
				"rolloutStrategy.rollingUpdate.maxUnavailable may not be 0 when maxSurge is 0",
		)
	}

	return rollingStrategy, nil
}
