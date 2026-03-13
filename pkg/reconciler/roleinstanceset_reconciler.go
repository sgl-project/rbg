package reconciler

import (
	"context"
	"fmt"
	"maps"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	metaapplyv1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	workloadsv1alpha2client "sigs.k8s.io/rbgs/client-go/applyconfiguration/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/constants"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type RoleInstanceSetReconciler struct {
	scheme *runtime.Scheme
	client client.Client
}

var _ WorkloadReconciler = &RoleInstanceSetReconciler{}

func NewRoleInstanceSetReconciler(scheme *runtime.Scheme, client client.Client) *RoleInstanceSetReconciler {
	return &RoleInstanceSetReconciler{
		scheme: scheme,
		client: client,
	}
}

func (r *RoleInstanceSetReconciler) Validate(
	ctx context.Context, role *workloadsv1alpha2.RoleSpec) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("start to validate role declaration")

	return nil
}

func (r *RoleInstanceSetReconciler) Reconciler(
	ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec,
	rollingUpdateStrategy *workloadsv1alpha2.RollingUpdate, revisionKey string,
) error {
	if err := r.reconcileRoleInstanceSet(ctx, rbg, role, rollingUpdateStrategy, revisionKey); err != nil {
		return err
	}
	return NewServiceReconciler(r.client).reconcileHeadlessService(ctx, rbg, role)
}

func (r *RoleInstanceSetReconciler) reconcileRoleInstanceSet(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec,
	rollingUpdateStrategy *workloadsv1alpha2.RollingUpdate, revisionKey string,
) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("start to reconciling roleinstanceset workload")

	rollingStrategy, err := validateRolloutStrategy(role.RolloutStrategy, int(*role.Replicas))
	if err != nil {
		logger.Error(err, "Invalid rollout strategy")
		return err
	}
	role.RolloutStrategy = rollingStrategy

	oldRoleInstanceSet := &workloadsv1alpha2.RoleInstanceSet{}
	err = r.client.Get(ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, oldRoleInstanceSet)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	roleInstanceSetApplyConfig, err := r.constructRoleInstanceSetApplyConfiguration(ctx, rbg, role, rollingUpdateStrategy, revisionKey)
	if err != nil {
		logger.Error(err, "Failed to construct roleInstanceSet apply configuration")
		return err
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(roleInstanceSetApplyConfig)
	if err != nil {
		logger.Error(err, "Converting obj apply configuration to json.")
		return err
	}

	newRoleInstanceSet := &workloadsv1alpha2.RoleInstanceSet{}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(obj, newRoleInstanceSet); err != nil {
		return fmt.Errorf("convert roleInstanceSet ApplyConfig to roleInstanceSet error: %s", err.Error())
	}

	roleHashKey := fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name)
	revisionHashEqual := oldRoleInstanceSet.Labels[roleHashKey] == newRoleInstanceSet.Labels[roleHashKey]
	if !revisionHashEqual {
		logger.Info(
			fmt.Sprintf(
				"roleInstanceSet hash not equal, old: %s, new: %s",
				oldRoleInstanceSet.Labels[roleHashKey], newRoleInstanceSet.Labels[roleHashKey],
			),
		)
	}

	if err := utils.PatchObjectApplyConfiguration(ctx, r.client, roleInstanceSetApplyConfig, utils.PatchSpec); err != nil {
		logger.Error(err, "Failed to patch roleInstanceSet apply configuration")
		return err
	}

	return nil
}

func (r *RoleInstanceSetReconciler) constructRoleInstanceSetApplyConfiguration(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
	rollingUpdateStrategy *workloadsv1alpha2.RollingUpdate,
	revisionKey string,
) (*workloadsv1alpha2client.RoleInstanceSetApplyConfiguration, error) {
	matchLabels := rbg.GetCommonLabelsFromRole(role)
	// set revision label
	roleInstanceSetLabel := maps.Clone(matchLabels)
	roleInstanceSetLabel[fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name)] = revisionKey

	// set default instance pattern annotation to Stateful only if not explicitly set in role.Annotations
	roleInstanceSetAnnotation := maps.Clone(rbg.GetCommonAnnotationsFromRole(role))
	if role.Annotations[constants.RoleInstancePatternKey] == "" {
		roleInstanceSetAnnotation[constants.RoleInstancePatternKey] = string(constants.StatefulPattern)
	}

	// 1. construct role instance configuration
	var restartPolicy workloadsv1alpha2.RoleInstanceRestartPolicyType
	if role.RestartPolicy == "None" {
		restartPolicy = workloadsv1alpha2.NoneRoleInstanceRestartPolicy
	} else {
		// if role has RecreateRBGOnPodRestart or RecreateRoleInstanceOnPodRestart policy,
		// set RecreateRoleInstanceOnPodRestart for lws
		// it's safe to do so since
		// 1. RecreateRoleInstanceOnPodRestart is the default restart policy for lws
		// 2. RecreateRBGOnPodRestart will delete lws if pod recreated or containers restarted
		restartPolicy = workloadsv1alpha2.RoleInstanceRestartPolicyType(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart)
	}
	roleInstanceTemplateConfig := workloadsv1alpha2client.RoleInstanceTemplate().
		WithRestartPolicy(restartPolicy)
	var constructErr error
	switch {
	case role.GetStandalonePattern() != nil:
		roleInstanceSetLabel[constants.RoleTypeLabelKey] = string(constants.PodTemplateTemplateType)
		constructErr = r.constructRoleInstanceTemplateFromStandalonePattern(ctx, rbg, role, matchLabels, roleInstanceTemplateConfig)
	case role.GetLeaderWorkerPattern() != nil:
		roleInstanceSetLabel[constants.RoleTypeLabelKey] = string(constants.LeaderWorkerSetTemplateType)
		constructErr = r.constructRoleInstanceTemplateByLeaderWorkerPattern(ctx, rbg, role, matchLabels, roleInstanceTemplateConfig)
	case role.GetCustomComponentsPattern() != nil:
		roleInstanceSetLabel[constants.RoleTypeLabelKey] = string(constants.ComponentsTemplateType)
		constructErr = r.constructRoleInstanceTemplateByCustomComponentsPattern(ctx, rbg, role, matchLabels, roleInstanceTemplateConfig)

	default:
		constructErr = fmt.Errorf("no valid pattern found for role %s", role.Name)
	}

	if constructErr != nil {
		return nil, constructErr
	}

	// 2. construct roleinstanceset configuration
	roleInstanceSetConfig := workloadsv1alpha2client.RoleInstanceSet(rbg.GetWorkloadName(role), rbg.Namespace).
		WithSpec(
			workloadsv1alpha2client.RoleInstanceSetSpec().
				WithSelector(metaapplyv1.LabelSelector().WithMatchLabels(maps.Clone(matchLabels))).
				WithReplicas(*role.Replicas).
				WithRoleInstanceTemplate(roleInstanceTemplateConfig).
				WithMinReadySeconds(role.MinReadySeconds),
		).
		WithAnnotations(labels.Merge(maps.Clone(role.Annotations), roleInstanceSetAnnotation)).
		WithLabels(labels.Merge(maps.Clone(role.Labels), roleInstanceSetLabel)).
		WithOwnerReferences(
			metaapplyv1.OwnerReference().
				WithAPIVersion(rbg.APIVersion).
				WithKind(rbg.Kind).
				WithName(rbg.Name).
				WithUID(rbg.GetUID()).
				WithBlockOwnerDeletion(true).
				WithController(true),
		)

	if role.RolloutStrategy != nil && role.RolloutStrategy.RollingUpdate != nil {
		rollingUpdate := role.RolloutStrategy.RollingUpdate
		if rollingUpdate.Type == "" {
			rollingUpdate.Type = workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType
		}
		updateStrategyConfig := workloadsv1alpha2client.RoleInstanceSetUpdateStrategy().
			WithType(rollingUpdate.Type).
			WithPaused(rollingUpdate.Paused)
		if rollingUpdate.Partition != nil {
			updateStrategyConfig = updateStrategyConfig.WithPartition(*rollingUpdate.Partition)
		}
		if rollingUpdate.MaxUnavailable != nil {
			updateStrategyConfig = updateStrategyConfig.WithMaxUnavailable(*rollingUpdate.MaxUnavailable)
		}
		if rollingUpdate.MaxSurge != nil {
			updateStrategyConfig = updateStrategyConfig.WithMaxSurge(*rollingUpdate.MaxSurge)
		}

		if rollingUpdate.InPlaceUpdateStrategy != nil {
			updateStrategyConfig = updateStrategyConfig.WithInPlaceUpdateStrategy(
				workloadsv1alpha2client.RoleInstanceSetInPlaceUpdateStrategy().WithGracePeriodSeconds(rollingUpdate.InPlaceUpdateStrategy.GracePeriodSeconds),
			)
		}

		roleInstanceSetConfig = roleInstanceSetConfig.WithSpec(
			roleInstanceSetConfig.Spec.WithUpdateStrategy(updateStrategyConfig))
	}

	if rollingUpdateStrategy != nil {
		if roleInstanceSetConfig.Spec.UpdateStrategy == nil {
			roleInstanceSetConfig = roleInstanceSetConfig.WithSpec(
				roleInstanceSetConfig.Spec.WithUpdateStrategy(
					workloadsv1alpha2client.RoleInstanceSetUpdateStrategy(),
				),
			)
		}

		updateStrategy := roleInstanceSetConfig.Spec.UpdateStrategy
		if rollingUpdateStrategy.Partition != nil {
			updateStrategy = updateStrategy.WithPartition(*rollingUpdateStrategy.Partition)
		}
		if rollingUpdateStrategy.MaxUnavailable != nil {
			updateStrategy = updateStrategy.WithMaxUnavailable(*rollingUpdateStrategy.MaxUnavailable)
		}
		roleInstanceSetConfig = roleInstanceSetConfig.WithSpec(
			roleInstanceSetConfig.Spec.WithUpdateStrategy(updateStrategy),
		)
	}

	return roleInstanceSetConfig, nil
}

func (r *RoleInstanceSetReconciler) constructRoleInstanceTemplateByCustomComponentsPattern(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
	matchLabels map[string]string,
	roleInstanceTemplateConfig *workloadsv1alpha2client.RoleInstanceTemplateApplyConfiguration,
) error {
	podReconciler := NewPodReconciler(r.scheme, r.client)
	for _, component := range role.GetCustomComponentsPattern().Components {
		podTemplateApplyConfiguration, err := podReconciler.ConstructPodTemplateSpecApplyConfiguration(
			ctx, rbg, role, maps.Clone(matchLabels), component.Template)
		if err != nil {
			return err
		}
		// construct service name
		svcName := component.ServiceName
		if svcName == "" {
			svcName, err = utils.GetCompatibleHeadlessServiceName(ctx, r.client, rbg, role)
			if err != nil {
				return err
			}
		}
		roleInstanceTemplateConfig.
			WithRestartPolicy(workloadsv1alpha2.NoneRoleInstanceRestartPolicy).
			WithComponents(workloadsv1alpha2client.RoleInstanceComponent().
				WithName(component.Name).
				WithServiceName(svcName).
				WithSize(*component.Size).
				WithTemplate(podTemplateApplyConfiguration.WithLabels(map[string]string{
					constants.ComponentSizeLabelKey: fmt.Sprintf("%d", *component.Size),
				})))
	}
	return nil
}

func (r *RoleInstanceSetReconciler) constructRoleInstanceTemplateByLeaderWorkerPattern(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
	matchLabels map[string]string,
	roleInstanceTemplateConfig *workloadsv1alpha2client.RoleInstanceTemplateApplyConfiguration,
) error {
	logger := log.FromContext(ctx)
	lwp := role.GetLeaderWorkerPattern()
	if lwp == nil {
		return fmt.Errorf("leaderWorkerPattern is nil")
	}
	leaderTemp, err := patchPodTemplate(role.GetTemplate(), lwp.LeaderTemplatePatch)
	if err != nil {
		logger.Error(err, "patch leader podTemplate failed", "rbg", keyOfRbg(rbg))
		return err
	}

	leaderPodReconciler := NewPodReconciler(r.scheme, r.client)
	leaderPodReconciler.SetInjectors([]string{"config", "sidecar", "common_env", "lwp_env"})
	leaderTemplateApplyCfg, err := leaderPodReconciler.ConstructPodTemplateSpecApplyConfiguration(
		ctx, rbg, role, matchLabels, *leaderTemp,
	)
	if err != nil {
		logger.Error(err, "patch Construct PodTemplateSpecApplyConfiguration failed", "rbg", keyOfRbg(rbg))
		return err
	}

	// workerTemplate
	workerTemp, err := patchPodTemplate(role.GetTemplate(), lwp.WorkerTemplatePatch)
	if err != nil {
		logger.Error(err, "patch worker podTemplate failed", "rbg", keyOfRbg(rbg))
		return err
	}

	workerPodReconciler := NewPodReconciler(r.scheme, r.client)
	// workerTemplate do not need to inject sidecar
	workerPodReconciler.SetInjectors([]string{"config", "common_env", "lwp_env"})
	workerTemplateApplyCfg, err := workerPodReconciler.ConstructPodTemplateSpecApplyConfiguration(
		ctx, rbg, role, matchLabels, *workerTemp,
	)
	if err != nil {
		logger.Error(err, "patch Construct PodTemplateSpecApplyConfiguration failed", "rbg", keyOfRbg(rbg))
		return err
	}

	svcName, err := utils.GetCompatibleHeadlessServiceName(ctx, r.client, rbg, role)
	if err != nil {
		return err
	}

	workerSize := utils.NonZeroValue(*lwp.Size - 1)
	roleInstanceTemplateConfig.
		WithRestartPolicy(workloadsv1alpha2.RoleInstanceRestartPolicyRecreateOnPodRestart).
		WithComponents(
			workloadsv1alpha2client.RoleInstanceComponent().
				WithName("leader").
				WithServiceName(svcName).
				WithSize(1).
				WithTemplate(leaderTemplateApplyCfg.WithLabels(map[string]string{
					constants.ComponentNameLabelKey: "leader",
					constants.ComponentSizeLabelKey: fmt.Sprintf("%d", *lwp.Size),
				})),
			workloadsv1alpha2client.RoleInstanceComponent().
				WithName("worker").
				WithSize(workerSize).
				WithTemplate(workerTemplateApplyCfg.WithLabels(map[string]string{
					constants.ComponentNameLabelKey: "worker",
					constants.ComponentSizeLabelKey: fmt.Sprintf("%d", *lwp.Size),
				})))
	return nil
}

func (r *RoleInstanceSetReconciler) constructRoleInstanceTemplateFromStandalonePattern(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
	matchLabels map[string]string,
	roleInstanceTemplateConfig *workloadsv1alpha2client.RoleInstanceTemplateApplyConfiguration,
) error {
	svcName, err := utils.GetCompatibleHeadlessServiceName(ctx, r.client, rbg, role)
	if err != nil {
		return err
	}

	podReconciler := NewPodReconciler(r.scheme, r.client)
	podTemplateApplyConfiguration, err := podReconciler.ConstructPodTemplateSpecApplyConfiguration(
		ctx, rbg, role, maps.Clone(matchLabels),
	)
	if err != nil {
		return err
	}

	roleInstanceTemplateConfig.
		WithRestartPolicy(workloadsv1alpha2.NoneRoleInstanceRestartPolicy).
		WithComponents(workloadsv1alpha2client.RoleInstanceComponent().
			WithName(role.Name).
			WithServiceName(svcName).
			WithTemplate(podTemplateApplyConfiguration.WithLabels(map[string]string{
				constants.ComponentSizeLabelKey: "1",
			})).
			WithSize(1))
	return nil
}

func (r *RoleInstanceSetReconciler) ConstructRoleStatus(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
) (workloadsv1alpha2.RoleStatus, bool, error) {
	roleInstanceSet := &workloadsv1alpha2.RoleInstanceSet{}
	if err := r.client.Get(
		ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, roleInstanceSet,
	); err != nil {
		return workloadsv1alpha2.RoleStatus{Name: role.Name}, false, err
	}

	if roleInstanceSet.Status.ObservedGeneration < roleInstanceSet.Generation {
		err := fmt.Errorf("roleInstanceSet generation not equal to observed generation")
		return workloadsv1alpha2.RoleStatus{Name: role.Name}, false, err
	}

	status, updateStatus := ConstructRoleStatue(rbg, role,
		*roleInstanceSet.Spec.Replicas,
		roleInstanceSet.Status.ReadyReplicas,
		roleInstanceSet.Status.UpdatedReplicas)
	return status, updateStatus, nil
}

func (r *RoleInstanceSetReconciler) CheckWorkloadReady(
	ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec,
) (bool, error) {
	roleInstanceSet := &workloadsv1alpha2.RoleInstanceSet{}
	if err := r.client.Get(
		ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, roleInstanceSet,
	); err != nil {
		return false, err
	}

	// We don't check ready if workload is rolling update if maxSkew is set.
	if utils.RoleInMaxSkewCoordinationV2(rbg, role.Name) &&
		roleInstanceSet.Status.CurrentRevision != roleInstanceSet.Status.UpdateRevision {
		return true, nil
	}
	return roleInstanceSet.Status.ReadyReplicas == *roleInstanceSet.Spec.Replicas, nil
}

func (r *RoleInstanceSetReconciler) CleanupOrphanedWorkloads(
	ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup,
) error {
	return CleanupOrphanedObjs(ctx, r.client, rbg, schema.GroupVersionKind{
		Group:   "workloads.x-k8s.io",
		Version: "v1alpha2",
		Kind:    "RoleInstanceSet"})
}

func (r *RoleInstanceSetReconciler) RecreateWorkload(
	ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec,
) error {
	return RecreateObj(ctx, r.client, rbg, role, schema.GroupVersionKind{
		Group:   "workloads.x-k8s.io",
		Version: "v1alpha2",
		Kind:    "RoleInstanceSet"})
}
