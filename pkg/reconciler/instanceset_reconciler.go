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
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha1client "sigs.k8s.io/rbgs/client-go/applyconfiguration/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type InstanceSetReconciler struct {
	scheme *runtime.Scheme
	client client.Client
}

var _ WorkloadReconciler = &InstanceSetReconciler{}

func NewInstanceSetReconciler(scheme *runtime.Scheme, client client.Client) *InstanceSetReconciler {
	return &InstanceSetReconciler{
		scheme: scheme,
		client: client,
	}
}

func (r *InstanceSetReconciler) Validate(
	ctx context.Context, role *workloadsv1alpha1.RoleSpec) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("start to validate role declaration")
	if len(role.Components) > 0 {
		if role.TemplateSource.Template != nil || role.LeaderWorkerSet != nil {
			return fmt.Errorf("when 'components' field is set, 'template' and 'leaderWorkerSet' fields must not be set")
		}
	} else if role.TemplateSource.Template == nil {
		if role.LeaderWorkerSet == nil {
			return fmt.Errorf("either 'template' or 'leaderWorkerSet' field must be provided")
		}
		if role.LeaderWorkerSet.PatchLeaderTemplate == nil || role.LeaderWorkerSet.PatchWorkerTemplate == nil {
			return fmt.Errorf("both 'patchLeaderTemplate' and 'patchWorkerTemplate' fields must be provided when 'template' field not set")
		}
	}

	if len(role.Labels[workloadsv1alpha1.RBGInstancePatternLabelKey]) == 0 {
		return fmt.Errorf("currently role.labels[instance.rolebasedgroup.workloads.x-k8s.io/pattern] is required")
	}

	if role.Labels[workloadsv1alpha1.RBGInstancePatternLabelKey] !=
		string(workloadsv1alpha1.DeploymentInstancePattern) {
		return fmt.Errorf("currently only 'Deployment' pattern is supported")
	}

	return nil
}

func (r *InstanceSetReconciler) Reconciler(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
	rollingUpdateStrategy *workloadsv1alpha1.RollingUpdate, revisionKey string,
) error {
	if err := r.reconcileInstanceSet(ctx, rbg, role, rollingUpdateStrategy, revisionKey); err != nil {
		return err
	}
	return NewServiceReconciler(r.client).reconcileHeadlessService(ctx, rbg, role)
}

func (r *InstanceSetReconciler) reconcileInstanceSet(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
	rollingUpdateStrategy *workloadsv1alpha1.RollingUpdate, revisionKey string,
) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("start to reconciling instanceset workload")

	rollingStrategy, err := validateRolloutStrategy(role.RolloutStrategy, int(*role.Replicas))
	if err != nil {
		logger.Error(err, "Invalid rollout strategy")
		return err
	}
	role.RolloutStrategy = rollingStrategy

	oldInstanceSet := &workloadsv1alpha1.InstanceSet{}
	err = r.client.Get(ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, oldInstanceSet)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	instanceSetApplyConfig, err := r.constructInstanceSetApplyConfiguration(ctx, rbg, role, rollingUpdateStrategy, revisionKey)
	if err != nil {
		logger.Error(err, "Failed to construct instanceSet apply configuration")
		return err
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(instanceSetApplyConfig)
	if err != nil {
		logger.Error(err, "Converting obj apply configuration to json.")
		return err
	}

	newInstanceSet := &workloadsv1alpha1.InstanceSet{}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(obj, newInstanceSet); err != nil {
		return fmt.Errorf("convert instanceSet ApplyConfig to instanceSet error: %s", err.Error())
	}

	roleHashKey := fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, role.Name)
	revisionHashEqual := oldInstanceSet.Labels[roleHashKey] == newInstanceSet.Labels[roleHashKey]
	if !revisionHashEqual {
		logger.Info(
			fmt.Sprintf(
				"instanceSet hash not equal, old: %s, new: %s",
				oldInstanceSet.Labels[roleHashKey], newInstanceSet.Labels[roleHashKey],
			),
		)
	}

	if err := utils.PatchObjectApplyConfiguration(ctx, r.client, instanceSetApplyConfig, utils.PatchSpec); err != nil {
		logger.Error(err, "Failed to patch instanceSet apply configuration")
		return err
	}

	return nil
}

func (r *InstanceSetReconciler) constructInstanceSetApplyConfiguration(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
	rollingUpdateStrategy *workloadsv1alpha1.RollingUpdate,
	revisionKey string,
) (*workloadsv1alpha1client.InstanceSetApplyConfiguration, error) {
	matchLabels := rbg.GetCommonLabelsFromRole(role)
	// set revision label
	instanceSetLabel := maps.Clone(matchLabels)
	instanceSetLabel[fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, role.Name)] = revisionKey

	// 1. construct instance configuration
	var restartPolicy workloadsv1alpha1.InstanceRestartPolicyType
	if role.RestartPolicy == "None" {
		restartPolicy = workloadsv1alpha1.NoneInstanceRestartPolicy
	} else {
		// if role has RecreateRBGOnPodRestart or RecreateRoleInstanceOnPodRestart policy,
		// set RecreateInstanceOnPodRestart for lws
		// it's safe to do so since
		// 1. RecreateInstanceOnPodRestart is the default restart policy for lws
		// 2. RecreateRBGOnPodRestart will delete lws if pod recreated or containers restarted
		restartPolicy = workloadsv1alpha1.RecreateInstanceOnPodRestart
	}
	instanceTemplateConfig := workloadsv1alpha1client.InstanceTemplate().
		WithRestartPolicy(restartPolicy)
	var constructErr error
	switch {
	case len(role.Components) > 0:
		instanceSetLabel[workloadsv1alpha1.RBGRoleTemplateTypeLabelKey] = string(workloadsv1alpha1.ComponentsTemplateType)
		constructErr = r.constructInstanceTemplateByComponents(ctx, rbg, role, matchLabels, instanceTemplateConfig)
	case role.LeaderWorkerSet != nil:
		instanceSetLabel[workloadsv1alpha1.RBGRoleTemplateTypeLabelKey] = string(workloadsv1alpha1.LeaderWorkerSetTemplateType)
		constructErr = r.constructInstanceTemplateByLWS(ctx, rbg, role, matchLabels, instanceTemplateConfig)
	case role.TemplateSource.Template != nil:
		instanceSetLabel[workloadsv1alpha1.RBGRoleTemplateTypeLabelKey] = string(workloadsv1alpha1.PodTemplateTemplateType)
		constructErr = r.constructInstanceTemplateByTemplate(ctx, rbg, role, matchLabels, instanceTemplateConfig)
	default:
		constructErr = fmt.Errorf("no valid template configuration found for role %s", role.Name)
	}

	if constructErr != nil {
		return nil, constructErr
	}

	// 2. construct instanceset configuration
	instanceSetConfig := workloadsv1alpha1client.InstanceSet(rbg.GetWorkloadName(role), rbg.Namespace).
		WithSpec(
			workloadsv1alpha1client.InstanceSetSpec().
				WithReplicas(*role.Replicas).
				WithInstanceTemplate(instanceTemplateConfig).
				WithMinReadySeconds(role.MinReadySeconds),
		).
		WithAnnotations(labels.Merge(maps.Clone(role.Annotations), rbg.GetCommonAnnotationsFromRole(role))).
		WithLabels(labels.Merge(maps.Clone(role.Labels), instanceSetLabel)).
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
			rollingUpdate.Type = workloadsv1alpha1.InPlaceIfPossibleUpdateStrategyType
		}
		updateStrategyConfig := workloadsv1alpha1client.InstanceSetUpdateStrategy().
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
				workloadsv1alpha1client.InPlaceUpdateStrategy().WithGracePeriodSeconds(rollingUpdate.InPlaceUpdateStrategy.GracePeriodSeconds),
			)
		}

		instanceSetConfig = instanceSetConfig.WithSpec(
			instanceSetConfig.Spec.WithUpdateStrategy(updateStrategyConfig))
	}

	if rollingUpdateStrategy != nil {
		if instanceSetConfig.Spec.UpdateStrategy == nil {
			instanceSetConfig = instanceSetConfig.WithSpec(
				instanceSetConfig.Spec.WithUpdateStrategy(
					workloadsv1alpha1client.InstanceSetUpdateStrategy(),
				),
			)
		}

		updateStrategy := instanceSetConfig.Spec.UpdateStrategy
		if rollingUpdateStrategy.Partition != nil {
			updateStrategy = updateStrategy.WithPartition(*rollingUpdateStrategy.Partition)
		}
		if rollingUpdateStrategy.MaxUnavailable != nil {
			updateStrategy = updateStrategy.WithMaxUnavailable(*rollingUpdateStrategy.MaxUnavailable)
		}
		instanceSetConfig = instanceSetConfig.WithSpec(
			instanceSetConfig.Spec.WithUpdateStrategy(updateStrategy),
		)
	}

	return instanceSetConfig, nil
}

func (r *InstanceSetReconciler) constructInstanceTemplateByComponents(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
	matchLabels map[string]string,
	instanceTemplateConfig *workloadsv1alpha1client.InstanceTemplateApplyConfiguration,
) error {
	podReconciler := NewPodReconciler(r.scheme, r.client)
	for _, component := range role.Components {
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
		instanceTemplateConfig.WithComponents(workloadsv1alpha1client.InstanceComponent().
			WithName(component.Name).
			WithServiceName(svcName).
			WithSize(*component.Size).
			WithTemplate(podTemplateApplyConfiguration.WithLabels(map[string]string{
				workloadsv1alpha1.RBGComponentSizeLabelKey: fmt.Sprintf("%d", *component.Size),
			})))
	}
	return nil
}

func (r *InstanceSetReconciler) constructInstanceTemplateByLWS(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
	matchLabels map[string]string,
	instanceTemplateConfig *workloadsv1alpha1client.InstanceTemplateApplyConfiguration,
) error {
	logger := log.FromContext(ctx)
	leaderWorkerSet := role.LeaderWorkerSet
	leaderTemp, err := patchPodTemplate(role.TemplateSource.Template, leaderWorkerSet.PatchLeaderTemplate)
	if err != nil {
		logger.Error(err, "patch leader podTemplate failed", "rbg", keyOfRbg(rbg))
		return err
	}

	leaderPodReconciler := NewPodReconciler(r.scheme, r.client)
	leaderPodReconciler.SetInjectors([]string{"config", "sidecar", "common_env", "lws_env"})
	leaderTemplateApplyCfg, err := leaderPodReconciler.ConstructPodTemplateSpecApplyConfiguration(
		ctx, rbg, role, matchLabels, *leaderTemp,
	)
	if err != nil {
		logger.Error(err, "patch Construct PodTemplateSpecApplyConfiguration failed", "rbg", keyOfRbg(rbg))
		return err
	}

	// workerTemplate
	workerTemp, err := patchPodTemplate(role.TemplateSource.Template, leaderWorkerSet.PatchWorkerTemplate)
	if err != nil {
		logger.Error(err, "patch worker podTemplate failed", "rbg", keyOfRbg(rbg))
		return err
	}

	workerPodReconciler := NewPodReconciler(r.scheme, r.client)
	// workerTemplate do not need to inject sidecar
	workerPodReconciler.SetInjectors([]string{"config", "common_env", "lws_env"})
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

	workerSize := utils.NonZeroValue(*leaderWorkerSet.Size - 1)
	instanceTemplateConfig.WithComponents(
		workloadsv1alpha1client.InstanceComponent().
			WithName("leader").
			WithServiceName(svcName).
			WithSize(1).
			WithTemplate(leaderTemplateApplyCfg.WithLabels(map[string]string{
				workloadsv1alpha1.RBGComponentNameLabelKey: "leader",
				workloadsv1alpha1.RBGComponentSizeLabelKey: fmt.Sprintf("%d", *leaderWorkerSet.Size),
			})),
		workloadsv1alpha1client.InstanceComponent().
			WithName("worker").
			WithSize(workerSize).
			WithTemplate(workerTemplateApplyCfg.WithLabels(map[string]string{
				workloadsv1alpha1.RBGComponentNameLabelKey: "worker",
				workloadsv1alpha1.RBGComponentSizeLabelKey: fmt.Sprintf("%d", *leaderWorkerSet.Size),
			})))
	return nil
}

func (r *InstanceSetReconciler) constructInstanceTemplateByTemplate(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
	matchLabels map[string]string,
	instanceTemplateConfig *workloadsv1alpha1client.InstanceTemplateApplyConfiguration,
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

	instanceTemplateConfig.WithComponents(workloadsv1alpha1client.InstanceComponent().
		WithName(role.Name).
		WithServiceName(svcName).
		WithTemplate(podTemplateApplyConfiguration.WithLabels(map[string]string{
			workloadsv1alpha1.RBGComponentSizeLabelKey: "1",
		})).
		WithSize(1))
	return nil
}

func (r *InstanceSetReconciler) ConstructRoleStatus(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
) (workloadsv1alpha1.RoleStatus, bool, error) {
	instanceSet := &workloadsv1alpha1.InstanceSet{}
	if err := r.client.Get(
		ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, instanceSet,
	); err != nil {
		return workloadsv1alpha1.RoleStatus{Name: role.Name}, false, err
	}

	if instanceSet.Status.ObservedGeneration < instanceSet.Generation {
		err := fmt.Errorf("instanceSet generation not equal to observed generation")
		return workloadsv1alpha1.RoleStatus{Name: role.Name}, false, err
	}

	status, updateStatus := ConstructRoleStatue(rbg, role,
		*instanceSet.Spec.Replicas,
		instanceSet.Status.ReadyReplicas,
		instanceSet.Status.UpdatedReplicas)
	return status, updateStatus, nil
}

func (r *InstanceSetReconciler) CheckWorkloadReady(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
) (bool, error) {
	instanceSet := &workloadsv1alpha1.InstanceSet{}
	if err := r.client.Get(
		ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, instanceSet,
	); err != nil {
		return false, err
	}

	// We don't check ready if workload is rolling update if maxSkew is set.
	if utils.RoleInMaxSkewCoordination(rbg, role.Name) &&
		instanceSet.Status.CurrentRevision != instanceSet.Status.UpdateRevision {
		return true, nil
	}
	return instanceSet.Status.ReadyReplicas == *instanceSet.Spec.Replicas, nil
}

func (r *InstanceSetReconciler) CleanupOrphanedWorkloads(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup,
) error {
	return CleanupOrphanedObjs(ctx, r.client, rbg, schema.GroupVersionKind{
		Group:   "workloads.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "InstanceSet"})
}

func (r *InstanceSetReconciler) RecreateWorkload(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
) error {
	return RecreateObj(ctx, r.client, rbg, role, schema.GroupVersionKind{
		Group:   "workloads.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "InstanceSet"})
}
