package reconciler

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	appsapplyv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	metaapplyv1 "k8s.io/client-go/applyconfigurations/meta/v1"
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

func (r *StatefulSetReconciler) Reconciler(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
) error {
	if err := r.reconcileStatefulSet(ctx, rbg, role); err != nil {
		return err
	}

	return r.reconcileHeadlessService(ctx, rbg, role)
}

func (r *StatefulSetReconciler) reconcileStatefulSet(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
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

	stsApplyConfig, err := r.constructStatefulSetApplyConfiguration(ctx, rbg, role, oldSts)
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

	equal, err := semanticallyEqualStatefulSet(oldSts, newSts, false)
	if err != nil {
		logger.Info(fmt.Sprintf("sts not equal, diff: %s", err.Error()))
	}

	stsUpdated := !equal
	partition, replicas, err := r.rollingUpdateParameters(ctx, role, oldSts, stsUpdated)
	if err != nil {
		return err
	}

	if equal && partition == *oldSts.Spec.UpdateStrategy.RollingUpdate.Partition &&
		*oldSts.Spec.Replicas == *role.Replicas {
		logger.Info("sts equal, skip reconcile")
		return nil
	}

	stsApplyConfig = stsApplyConfig.WithSpec(
		stsApplyConfig.Spec.WithReplicas(replicas).
			WithUpdateStrategy(
				appsapplyv1.StatefulSetUpdateStrategy().
					WithType(appsv1.StatefulSetUpdateStrategyType(role.RolloutStrategy.Type)).
					WithRollingUpdate(
						appsapplyv1.RollingUpdateStatefulSetStrategy().
							WithMaxUnavailable(role.RolloutStrategy.RollingUpdate.MaxUnavailable).
							WithPartition(partition),
					),
			),
	)

	if err := utils.PatchObjectApplyConfiguration(ctx, r.client, stsApplyConfig, utils.PatchSpec); err != nil {
		logger.Error(err, "Failed to patch statefulset apply configuration")
		return err
	}

	return nil
}

func (r *StatefulSetReconciler) rollingUpdateParameters(
	ctx context.Context,
	role *workloadsv1alpha1.RoleSpec, sts *appsv1.StatefulSet, stsUpdated bool,
) (int32, int32, error) {
	logger := log.FromContext(ctx)
	roleReplicas := *role.Replicas

	// Case 1:
	// If sts not created yet, all partitions should be updated,
	// replicas should not change.
	if sts == nil || sts.UID == "" {
		return 0, roleReplicas, nil
	}

	stsReplicas := *sts.Spec.Replicas
	maxSurge, err := intstr.GetScaledValueFromIntOrPercent(
		&role.RolloutStrategy.RollingUpdate.MaxSurge,
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

	// Case 2:
	// Indicates a new rolling update here.
	if stsUpdated {
		partition, replicas := min(roleReplicas, stsReplicas), wantReplicas(roleReplicas)
		// Processing scaling up/down first prior to rolling update.
		logger.V(1).Info(fmt.Sprintf("case 2: rolling update started. partition %d, replicas: %d", partition, replicas))
		return partition, replicas, nil
	}

	partition := *sts.Spec.UpdateStrategy.RollingUpdate.Partition
	rollingUpdateCompleted := partition == 0 && stsReplicas == roleReplicas
	// Case 3:
	// In normal cases, return the values directly.
	if rollingUpdateCompleted {
		logger.V(1).Info("case 3: rolling update completed.")
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
	// Case 4:
	// Replicas changed during rolling update.
	if replicasUpdated {
		partition = min(partition, burstReplicas)
		replicas := wantReplicas(roleUnreadyReplicas)
		logger.V(1).Info(
			fmt.Sprintf(
				"case 4: Replicas changed during rolling update. partition %d, replicas: %d", partition, replicas,
			),
		)
		return partition, replicas, nil
	}

	// Case 5:
	// Calculating the Partition during rolling update, no leaderWorkerSet updates happens.
	rollingStep, err := intstr.GetScaledValueFromIntOrPercent(
		&role.RolloutStrategy.RollingUpdate.MaxUnavailable, int(roleReplicas), false,
	)
	if err != nil {
		return 0, 0, err
	}
	// Make sure that we always respect the maxUnavailable, or
	// we'll violate it when reclaiming bursted replicas.
	rollingStep += maxSurge - (int(burstReplicas) - int(stsReplicas))
	partition = rollingUpdatePartition(ctx, states, stsReplicas, int32(rollingStep), partition)
	replicas := wantReplicas(roleUnreadyReplicas)
	logger.V(1).Info(
		fmt.Sprintf(
			"case 5: Calculating the Partition during rolling update. partition %d, replicas: %d", partition, replicas,
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

	states := make([]replicaState, *sts.Spec.Replicas)
	sortedPods := make([]corev1.Pod, *sts.Spec.Replicas)

	podSelector := client.MatchingLabels(sts.Labels)
	var podList corev1.PodList
	if err := r.client.List(ctx, &podList, podSelector, client.InNamespace(sts.Namespace)); err != nil {
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

	highestRevision, err := r.getHighestRevision(ctx, sts)
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

func (r *StatefulSetReconciler) reconcileHeadlessService(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("start to reconciling headless service")

	sts := &appsv1.StatefulSet{}
	err := r.client.Get(ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, sts)
	if err != nil {
		return fmt.Errorf("get sts error, skip reconcile svc. error:  %s", err.Error())
	}

	svcApplyConfig := r.constructServiceApplyConfiguration(ctx, rbg, role, sts)
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(svcApplyConfig)
	if err != nil {
		logger.Error(err, "Converting obj apply configuration to json.")
		return err
	}

	newSvc := &corev1.Service{}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(obj, newSvc); err != nil {
		return fmt.Errorf("convert svcApplyConfig to svc error: %s", err.Error())
	}

	oldSvc := &corev1.Service{}
	err = r.client.Get(ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, oldSvc)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	equal, err := SemanticallyEqualService(oldSvc, newSvc)
	if equal {
		logger.V(1).Info("svc equal, skip reconcile")
		return nil
	}

	logger.V(1).Info(fmt.Sprintf("svc not equal, diff: %s", err.Error()))

	if err := utils.PatchObjectApplyConfiguration(ctx, r.client, svcApplyConfig, utils.PatchSpec); err != nil {
		logger.Error(err, "Failed to patch svc apply configuration")
		return err
	}

	return nil
}

func (r *StatefulSetReconciler) constructStatefulSetApplyConfiguration(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
	oldSts *appsv1.StatefulSet,
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

	// construct statefulset apply configuration
	statefulSetConfig := appsapplyv1.StatefulSet(rbg.GetWorkloadName(role), rbg.Namespace).
		WithSpec(
			appsapplyv1.StatefulSetSpec().
				WithServiceName(rbg.GetWorkloadName(role)).
				WithReplicas(*role.Replicas).
				WithTemplate(podTemplateApplyConfiguration).
				WithPodManagementPolicy(appsv1.ParallelPodManagement).
				WithSelector(
					metaapplyv1.LabelSelector().
						WithMatchLabels(matchLabels),
				),
		).
		WithAnnotations(rbg.GetCommonAnnotationsFromRole(role)).
		WithLabels(matchLabels).
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

func (r *StatefulSetReconciler) constructServiceApplyConfiguration(
	_ context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
	sts *appsv1.StatefulSet,
) *coreapplyv1.ServiceApplyConfiguration {
	selectMap := map[string]string{
		workloadsv1alpha1.SetNameLabelKey: rbg.Name,
		workloadsv1alpha1.SetRoleLabelKey: role.Name,
	}
	serviceConfig := coreapplyv1.Service(rbg.GetWorkloadName(role), rbg.Namespace).
		WithSpec(
			coreapplyv1.ServiceSpec().
				WithClusterIP("None").
				WithSelector(selectMap).
				WithPublishNotReadyAddresses(true),
		).
		WithLabels(rbg.GetCommonLabelsFromRole(role)).
		WithAnnotations(rbg.GetCommonAnnotationsFromRole(role)).
		WithOwnerReferences(
			metaapplyv1.OwnerReference().
				WithAPIVersion(sts.APIVersion).
				WithKind(sts.Kind).
				WithName(sts.Name).
				WithUID(sts.GetUID()).
				WithBlockOwnerDeletion(true),
		)
	return serviceConfig
}

func (r *StatefulSetReconciler) ConstructRoleStatus(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
) (workloadsv1alpha1.RoleStatus, bool, error) {
	updateStatus := false
	sts := &appsv1.StatefulSet{}
	if err := r.client.Get(
		ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, sts,
	); err != nil {
		return workloadsv1alpha1.RoleStatus{}, updateStatus, err
	}

	currentReplicas := *sts.Spec.Replicas
	currentReady := sts.Status.ReadyReplicas
	status, found := rbg.GetRoleStatus(role.Name)
	if !found || status.Replicas != currentReplicas || status.ReadyReplicas != currentReady {
		status = workloadsv1alpha1.RoleStatus{
			Name:          role.Name,
			Replicas:      currentReplicas,
			ReadyReplicas: currentReady,
		}
		updateStatus = true
	}
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
	return sts.Status.ReadyReplicas == *sts.Spec.Replicas, nil
}

func (r *StatefulSetReconciler) CleanupOrphanedWorkloads(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup,
) error {
	logger := log.FromContext(ctx)
	// list sts managed by rbg
	stsList := &appsv1.StatefulSetList{}
	if err := r.client.List(
		context.Background(), stsList, client.InNamespace(rbg.Namespace),
		client.MatchingLabels(
			map[string]string{
				workloadsv1alpha1.SetNameLabelKey: rbg.Name,
			},
		),
	); err != nil {
		return err
	}

	for _, sts := range stsList.Items {
		if !v1.IsControlledBy(&sts, rbg) {
			continue
		}
		found := false
		for _, role := range rbg.Spec.Roles {
			if role.Workload.Kind == "StatefulSet" && rbg.GetWorkloadName(&role) == sts.Name {
				found = true
				break
			}
		}
		if !found {
			if err := r.client.Delete(ctx, &sts); err != nil {
				return fmt.Errorf("delete sts %s error: %s", sts.Name, err.Error())
			}
			// The deletion of headless services depends on its own reference
			logger.Info("delete sts", "sts", sts.Name)
		}
	}
	return nil
}

func (r *StatefulSetReconciler) getHighestRevision(
	ctx context.Context, sts *appsv1.StatefulSet,
) (*appsv1.ControllerRevision, error) {
	selector, err := v1.LabelSelectorAsSelector(
		&v1.LabelSelector{
			MatchLabels: sts.Labels,
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
	logger := log.FromContext(ctx)
	if rbg == nil || role == nil {
		return nil
	}

	stsName := rbg.GetWorkloadName(role)
	var sts appsv1.StatefulSet
	err := r.client.Get(ctx, types.NamespacedName{Name: stsName, Namespace: rbg.Namespace}, &sts)
	// if sts is not found, skip delete sts
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	logger.Info(fmt.Sprintf("Recreate sts workload, delete sts %s", stsName))
	if err := r.client.Delete(ctx, &sts); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	// wait new sts create
	var retErr error
	err = wait.PollUntilContextTimeout(
		ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
			var newSts appsv1.StatefulSet
			retErr = r.client.Get(ctx, types.NamespacedName{Name: stsName, Namespace: rbg.Namespace}, &newSts)
			if retErr != nil {
				if apierrors.IsNotFound(retErr) {
					return false, nil
				}
				return false, retErr
			}
			return true, nil
		},
	)

	if err != nil {
		logger.Error(retErr, "wait new sts creating error")
		return retErr
	}

	return nil
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
	if oldStatus.Replicas != newStatus.Replicas {
		return false, fmt.Errorf("status.replicas not equal, old: %v, new: %v", oldStatus.Replicas, newStatus.Replicas)
	}

	if oldStatus.ReadyReplicas != newStatus.ReadyReplicas {
		return false, fmt.Errorf(
			"status.ReadyReplicas not equal, old: %v, new: %v", oldStatus.ReadyReplicas, newStatus.ReadyReplicas,
		)
	}
	return true, nil

}

func SemanticallyEqualService(svc1, svc2 *corev1.Service) (bool, error) {
	if svc1 == nil || svc2 == nil {
		if svc1 != svc2 {
			return false, fmt.Errorf("object is nil")
		} else {
			return true, nil
		}
	}

	if equal, err := objectMetaEqual(svc1.ObjectMeta, svc2.ObjectMeta); !equal {
		return false, fmt.Errorf("objectMeta not equal: %s", err.Error())
	}

	if !reflect.DeepEqual(svc1.Spec.Selector, svc2.Spec.Selector) {
		return false, fmt.Errorf("selector not equal, old: %v, new: %v", svc1.Spec.Selector, svc2.Spec.Selector)
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
				MaxUnavailable: intstr.FromInt32(1),
				MaxSurge:       intstr.FromInt32(0),
			},
		}, nil
	}

	maxSurge, err := intstr.GetScaledValueFromIntOrPercent(&rollingStrategy.RollingUpdate.MaxSurge, replicas, true)
	if err != nil {
		return nil, err
	}
	maxAvailable, err := intstr.GetScaledValueFromIntOrPercent(
		&rollingStrategy.RollingUpdate.MaxUnavailable, replicas, false,
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
