package reconciler

import (
	"context"
	"fmt"
	"maps"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
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
		if role.Template != nil || role.LeaderWorkerSet != nil {
			return fmt.Errorf("when 'components' field is set, 'template' and 'leaderWorkerSet' fields must not be set")
		}
	} else if role.Template == nil {
		if role.LeaderWorkerSet == nil {
			return fmt.Errorf("either 'template' or 'leaderWorkerSet' field must be provided")
		}
		if role.LeaderWorkerSet.PatchLeaderTemplate == nil || role.LeaderWorkerSet.PatchWorkerTemplate == nil {
			return fmt.Errorf("both 'patchLeaderTemplate' and 'patchWorkerTemplate' fields must be provided when 'template' field not set")
		}
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
	logger := log.FromContext(ctx)
	matchLabels := rbg.GetCommonLabelsFromRole(role)

	// set revision label
	instanceSetLabel := maps.Clone(matchLabels)
	instanceSetLabel[fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, role.Name)] = revisionKey

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

	// construct instance template
	instanceTemplateConfig := workloadsv1alpha1client.InstanceTemplate().
		WithRestartPolicy(restartPolicy)

	// construct comment
	podReconciler := NewPodReconciler(r.scheme, r.client)
	if len(role.Components) > 0 {
		for _, component := range role.Components {
			podTemplateApplyConfiguration, err := podReconciler.ConstructPodTemplateSpecApplyConfiguration(
				ctx, rbg, role, maps.Clone(matchLabels), component.Template)
			if err != nil {
				return nil, err
			}
			// construct service name
			svcName := component.ServiceName
			if svcName == "" {
				svcName, err = utils.GetCompatibleHeadlessServiceName(ctx, r.client, rbg, role)
				if err != nil {
					return nil, err
				}
			}
			instanceTemplateConfig.WithComponents(workloadsv1alpha1client.InstanceComponent().
				WithName(component.Name).
				WithServiceName(svcName).
				WithSize(*component.Size).
				WithTemplate(podTemplateApplyConfiguration))
		}
	} else if role.LeaderWorkerSet != nil {
		leaderWorkerSet := role.LeaderWorkerSet
		leaderTemp, err := patchPodTemplate(role.Template, leaderWorkerSet.PatchLeaderTemplate)
		if err != nil {
			logger.Error(err, "patch leader podTemplate failed", "rbg", keyOfRbg(rbg))
			return nil, err
		}
		leaderTemplateApplyCfg, err := podReconciler.ConstructPodTemplateSpecApplyConfiguration(
			ctx, rbg, role, rbg.GetCommonLabelsFromRole(role), *leaderTemp,
		)
		if err != nil {
			logger.Error(err, "patch Construct PodTemplateSpecApplyConfiguration failed", "rbg", keyOfRbg(rbg))
			return nil, err
		}
		// workerTemplate
		workerTemp, err := patchPodTemplate(role.Template, leaderWorkerSet.PatchWorkerTemplate)
		if err != nil {
			logger.Error(err, "patch worker podTemplate failed", "rbg", keyOfRbg(rbg))
			return nil, err
		}
		workerPodReconciler := NewPodReconciler(r.scheme, r.client)
		// workerTemplate do not need to inject sidecar
		workerPodReconciler.SetInjectors([]string{"config", "common_env", "lws_env"})
		workerTemplateApplyCfg, err := workerPodReconciler.ConstructPodTemplateSpecApplyConfiguration(
			ctx, rbg, role, rbg.GetCommonLabelsFromRole(role), *workerTemp,
		)
		if err != nil {
			logger.Error(err, "patch Construct PodTemplateSpecApplyConfiguration failed", "rbg", keyOfRbg(rbg))
			return nil, err
		}
		svcName, err := utils.GetCompatibleHeadlessServiceName(ctx, r.client, rbg, role)
		if err != nil {
			return nil, err
		}
		instanceTemplateConfig.WithComponents(
			workloadsv1alpha1client.InstanceComponent().
				WithName("leader").
				WithServiceName(svcName).
				WithSize(1).
				WithTemplate(leaderTemplateApplyCfg.WithLabels(map[string]string{
					workloadsv1alpha1.SetLWSComponentLabelKey: fmt.Sprintf("%s", workloadsv1alpha1.LeaderLwsComponentType),
				})),
			workloadsv1alpha1client.InstanceComponent().
				WithName("worker").
				WithSize(*leaderWorkerSet.Size).
				WithTemplate(workerTemplateApplyCfg.WithLabels(map[string]string{
					workloadsv1alpha1.SetLWSComponentLabelKey: fmt.Sprintf("%s", workloadsv1alpha1.WorkerLwsComponentType),
				})))
	} else if role.Template != nil {
		svcName, err := utils.GetCompatibleHeadlessServiceName(ctx, r.client, rbg, role)
		if err != nil {
			return nil, err
		}
		podTemplateApplyConfiguration, err := podReconciler.ConstructPodTemplateSpecApplyConfiguration(
			ctx, rbg, role, maps.Clone(matchLabels),
		)
		if err != nil {
			return nil, err
		}
		instanceTemplateConfig.WithComponents(workloadsv1alpha1client.InstanceComponent().
			WithName(role.Name).
			WithServiceName(svcName).
			WithTemplate(podTemplateApplyConfiguration).
			WithSize(1))
	}

	// construct instanceset apply configuration
	instanceSetConfig := workloadsv1alpha1client.InstanceSet(rbg.GetWorkloadName(role), rbg.Namespace).
		WithSpec(
			workloadsv1alpha1client.InstanceSetSpec().
				WithReplicas(*role.Replicas).
				WithInstanceTemplate(instanceTemplateConfig).
				WithMinReadySeconds(role.MinReadySeconds),
		).
		WithAnnotations(rbg.GetCommonAnnotationsFromRole(role)).
		WithLabels(instanceSetLabel).
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
			WithPartition(intstr.FromInt32(*rollingUpdate.Partition)).
			WithPaused(rollingUpdate.Paused).
			WithMaxUnavailable(rollingUpdate.MaxUnavailable).
			WithMaxSurge(rollingUpdate.MaxSurge)

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

		instanceSetConfig = instanceSetConfig.WithSpec(
			instanceSetConfig.Spec.WithUpdateStrategy(
				instanceSetConfig.Spec.UpdateStrategy.
					WithPartition(intstr.FromInt32(*rollingUpdateStrategy.Partition)).
					WithMaxUnavailable(rollingUpdateStrategy.MaxUnavailable),
			),
		)
	}

	return instanceSetConfig, nil
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

	updateStatus := false
	currentReplicas := *instanceSet.Spec.Replicas
	currentReady := instanceSet.Status.ReadyReplicas
	updatedReplicas := instanceSet.Status.UpdatedReplicas
	status, found := rbg.GetRoleStatus(role.Name)
	if !found || status.Replicas != currentReplicas ||
		status.ReadyReplicas != currentReady ||
		status.UpdatedReplicas != updatedReplicas {
		status = workloadsv1alpha1.RoleStatus{
			Name:            role.Name,
			Replicas:        currentReplicas,
			ReadyReplicas:   currentReady,
			UpdatedReplicas: updatedReplicas,
		}
		updateStatus = true
	}
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
	logger := log.FromContext(ctx)
	// list instanceSet managed by rbg
	instanceSetList := &workloadsv1alpha1.InstanceSetList{}
	if err := r.client.List(
		context.Background(), instanceSetList, client.InNamespace(rbg.Namespace),
		client.MatchingLabels(
			map[string]string{
				workloadsv1alpha1.SetNameLabelKey: rbg.Name,
			},
		),
	); err != nil {
		return err
	}

	for _, instanceSet := range instanceSetList.Items {
		if !v1.IsControlledBy(&instanceSet, rbg) {
			continue
		}
		found := false
		for _, role := range rbg.Spec.Roles {
			if role.Workload.Kind == "InstanceSet" && rbg.GetWorkloadName(&role) == instanceSet.Name {
				found = true
				break
			}
		}
		if !found {
			if err := r.client.Delete(ctx, &instanceSet); err != nil {
				return fmt.Errorf("delete instanceSet %s error: %s", instanceSet.Name, err.Error())
			}
			// The deletion of headless services depends on its own reference
			logger.Info("delete instanceSet", "instanceSet", instanceSet.Name)
		}
	}
	return nil
}

func (r *InstanceSetReconciler) RecreateWorkload(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
) error {
	logger := log.FromContext(ctx)
	if rbg == nil || role == nil {
		return nil
	}

	instanceSetName := rbg.GetWorkloadName(role)
	var instanceSet workloadsv1alpha1.InstanceSet
	err := r.client.Get(ctx, types.NamespacedName{Name: instanceSetName, Namespace: rbg.Namespace}, &instanceSet)
	// if instanceSet is not found, skip delete instanceSet
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	logger.Info(fmt.Sprintf("Recreate instanceSet workload, delete instanceSet %s", instanceSetName))
	if err := r.client.Delete(ctx, &instanceSet); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	// wait new instanceSet create
	var retErr error
	err = wait.PollUntilContextTimeout(
		ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
			var newInstanceSet workloadsv1alpha1.InstanceSet
			retErr = r.client.Get(ctx, types.NamespacedName{Name: instanceSetName, Namespace: rbg.Namespace}, &newInstanceSet)
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
		logger.Error(retErr, "wait new InstanceSet creating error")
		return retErr
	}

	return nil
}
