package reconciler

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	appsapplyv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	metaapplyv1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type DeploymentReconciler struct {
	scheme *runtime.Scheme
	client client.Client
}

var _ WorkloadReconciler = &DeploymentReconciler{}

func NewDeploymentReconciler(scheme *runtime.Scheme, client client.Client) *DeploymentReconciler {
	return &DeploymentReconciler{scheme: scheme, client: client}
}

func (r *DeploymentReconciler) Validate(
	ctx context.Context, role *workloadsv1alpha1.RoleSpec) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("start to validate role declaration")
	if role.Template == nil && !role.UsesRoleTemplate() {
		return fmt.Errorf("either 'template' or 'templateRef' is required when use %s as workload", role.Workload.String())
	}

	return nil
}

func (r *DeploymentReconciler) Reconciler(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
	rollingUpdateStrategy *workloadsv1alpha1.RollingUpdate, revisionKey string) error {
	logger := log.FromContext(ctx)
	logger.V(1).Info("start to reconciling deployment workload")

	oldDeploy := &appsv1.Deployment{}
	err := r.client.Get(ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, oldDeploy)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	deployApplyConfig, err := r.constructDeployApplyConfiguration(ctx, rbg, role, oldDeploy, rollingUpdateStrategy, revisionKey)
	if err != nil {
		logger.Error(err, "Failed to construct deployment apply configuration")
		return err
	}
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deployApplyConfig)
	if err != nil {
		logger.Error(err, "Converting obj apply configuration to json.")
		return err
	}
	newDeploy := &appsv1.Deployment{}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(obj, newDeploy); err != nil {
		return fmt.Errorf("convert deployApplyConfig to deployment error: %s", err.Error())
	}

	// the err value was used to pass the differences between the old and new objects,
	// not to indicate an actual processing error.
	semanticallyEqual, err := semanticallyEqualDeployment(oldDeploy, newDeploy, false)
	if err != nil {
		logger.Info(fmt.Sprintf("deployment not equal, diff: %s", err.Error()))
	}

	roleHashKey := fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, role.Name)
	revisionHashEqual := newDeploy.Labels[roleHashKey] == oldDeploy.Labels[roleHashKey]
	if !revisionHashEqual {
		logger.Info(fmt.Sprintf("deployment hash not equal, old: %s, new: %s",
			oldDeploy.Labels[roleHashKey],
			newDeploy.Labels[roleHashKey]))
	}
	if semanticallyEqual && revisionHashEqual {
		logger.Info("deployment equal, skip reconcile")
		return nil
	}

	if err := utils.PatchObjectApplyConfiguration(ctx, r.client, deployApplyConfig, utils.PatchSpec); err != nil {
		logger.Error(err, "Failed to patch deployment apply configuration")
		return err
	}
	return nil
}

func (r *DeploymentReconciler) constructDeployApplyConfiguration(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
	oldDeploy *appsv1.Deployment,
	rollingUpdateStrategy *workloadsv1alpha1.RollingUpdate,
	revisionKey string,
) (*appsapplyv1.DeploymentApplyConfiguration, error) {
	matchLabels := rbg.GetCommonLabelsFromRole(role)
	if oldDeploy.UID != "" {
		// do not update selector when workload exists
		matchLabels = oldDeploy.Spec.Selector.MatchLabels
	}

	podReconciler := NewPodReconciler(r.scheme, r.client)
	podTemplateApplyConfiguration, err := podReconciler.ConstructPodTemplateSpecApplyConfiguration(
		ctx, rbg, role, maps.Clone(matchLabels),
	)
	if err != nil {
		return nil, err
	}
	deployLabel := maps.Clone(matchLabels)
	deployLabel[fmt.Sprintf(workloadsv1alpha1.RoleRevisionLabelKeyFmt, role.Name)] = revisionKey

	// construct deployment apply configuration
	deployConfig := appsapplyv1.Deployment(rbg.GetWorkloadName(role), rbg.Namespace).
		WithSpec(
			appsapplyv1.DeploymentSpec().
				WithReplicas(*role.Replicas).
				WithTemplate(podTemplateApplyConfiguration).
				WithSelector(
					metaapplyv1.LabelSelector().
						WithMatchLabels(matchLabels),
				),
		).
		WithAnnotations(labels.Merge(maps.Clone(role.Annotations), rbg.GetCommonAnnotationsFromRole(role))).
		WithLabels(labels.Merge(maps.Clone(role.Labels), deployLabel)).
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
		rollingUpdate := appsapplyv1.RollingUpdateDeployment()
		if role.RolloutStrategy.RollingUpdate.MaxSurge != nil {
			rollingUpdate = rollingUpdate.WithMaxSurge(*role.RolloutStrategy.RollingUpdate.MaxSurge)
		}
		if role.RolloutStrategy.RollingUpdate.MaxUnavailable != nil {
			rollingUpdate = rollingUpdate.WithMaxUnavailable(*role.RolloutStrategy.RollingUpdate.MaxUnavailable)
		}

		deployConfig = deployConfig.WithSpec(
			deployConfig.Spec.WithStrategy(
				appsapplyv1.DeploymentStrategy().
					WithType(appsv1.DeploymentStrategyType(role.RolloutStrategy.Type)).
					WithRollingUpdate(rollingUpdate),
			),
		)
	}
	if rollingUpdateStrategy != nil {
		if deployConfig.Spec.Strategy == nil {
			deployConfig = deployConfig.WithSpec(
				deployConfig.Spec.WithStrategy(
					appsapplyv1.DeploymentStrategy(),
				),
			)
		}
		if deployConfig.Spec.Strategy.RollingUpdate == nil {
			deployConfig = deployConfig.WithSpec(
				deployConfig.Spec.WithStrategy(
					deployConfig.Spec.Strategy.WithRollingUpdate(
						appsapplyv1.RollingUpdateDeployment(),
					),
				),
			)
		}

		rollingUpdate := appsapplyv1.RollingUpdateDeployment()
		if rollingUpdateStrategy.MaxUnavailable != nil {
			rollingUpdate = rollingUpdate.WithMaxUnavailable(*rollingUpdateStrategy.MaxUnavailable)
		}

		deployConfig = deployConfig.WithSpec(
			deployConfig.Spec.WithStrategy(
				deployConfig.Spec.Strategy.WithRollingUpdate(rollingUpdate),
			),
		)
	}
	return deployConfig, nil

}

func (r *DeploymentReconciler) ConstructRoleStatus(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
) (workloadsv1alpha1.RoleStatus, bool, error) {
	updateStatus := false
	deploy := &appsv1.Deployment{}
	if err := r.client.Get(
		ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, deploy,
	); err != nil {
		return workloadsv1alpha1.RoleStatus{Name: role.Name}, false, err
	}

	if deploy.Status.ObservedGeneration < deploy.Generation {
		err := fmt.Errorf("role(%s) workload generation not equal to observed generation", role.Name)
		return workloadsv1alpha1.RoleStatus{Name: role.Name}, false, err
	}

	currentReplicas := *deploy.Spec.Replicas
	currentReady := deploy.Status.ReadyReplicas
	updatedReplicas := deploy.Status.UpdatedReplicas
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

func (r *DeploymentReconciler) CheckWorkloadReady(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
) (bool, error) {
	deploy := &appsv1.Deployment{}
	if err := r.client.Get(
		ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, deploy,
	); err != nil {
		return false, err
	}

	// We don't check ready if workload is rolling update if maxSkew is set.
	if utils.RoleInMaxSkewCoordination(rbg, role.Name) &&
		deploy.Status.UpdatedReplicas != deploy.Status.Replicas {
		return true, nil
	}
	return deploy.Status.ReadyReplicas == *deploy.Spec.Replicas, nil
}

func (r *DeploymentReconciler) CleanupOrphanedWorkloads(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup,
) error {
	logger := log.FromContext(ctx)
	// list deploy managed by rbg
	deployList := &appsv1.DeploymentList{}
	if err := r.client.List(
		context.Background(), deployList, client.InNamespace(rbg.Namespace),
		client.MatchingLabels(
			map[string]string{
				workloadsv1alpha1.SetNameLabelKey: rbg.Name,
			},
		),
	); err != nil {
		return err
	}

	for _, deploy := range deployList.Items {
		if !metav1.IsControlledBy(&deploy, rbg) {
			continue
		}
		found := false
		for _, role := range rbg.Spec.Roles {
			if role.Workload.Kind == "Deployment" && rbg.GetWorkloadName(&role) == deploy.Name {
				found = true
				break
			}
		}
		if !found {
			logger.Info("delete deploy", "deploy", deploy.Name)
			if err := r.client.Delete(ctx, &deploy); err != nil {
				return fmt.Errorf("delete sts %s error: %s", deploy.Name, err.Error())
			}
		}
	}
	return nil
}

func (r *DeploymentReconciler) RecreateWorkload(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
) error {
	logger := log.FromContext(ctx)
	if rbg == nil || role == nil {
		return nil
	}

	deployName := rbg.GetWorkloadName(role)
	var deploy appsv1.Deployment
	err := r.client.Get(ctx, types.NamespacedName{Name: deployName, Namespace: rbg.Namespace}, &deploy)
	// if deploy is not found, skip delete deploy
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if deploy.UID == "" {
		return nil
	}

	logger.Info(fmt.Sprintf("Recreate deployment workload, delete deployment %s", deployName))
	if err := r.client.Delete(ctx, &deploy); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	// wait new deployment create
	var retErr error
	err = wait.PollUntilContextTimeout(
		ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
			var newDeploy appsv1.Deployment
			retErr = r.client.Get(ctx, types.NamespacedName{Name: deployName, Namespace: rbg.Namespace}, &newDeploy)
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
		logger.Error(retErr, "wait new deployment creating error")
		return retErr
	}

	return nil
}

func semanticallyEqualDeployment(oldDeploy, newDeploy *appsv1.Deployment, checkStatus bool) (bool, error) {
	if oldDeploy == nil || oldDeploy.UID == "" {
		return false, errors.New("old deployment not exist")
	}
	if newDeploy == nil {
		return false, fmt.Errorf("new deployment is nil")
	}

	if equal, err := objectMetaEqual(oldDeploy.ObjectMeta, newDeploy.ObjectMeta); !equal {
		return false, fmt.Errorf("objectMeta not equal: %s", err.Error())
	}

	if equal, err := deploymentSpecEqual(oldDeploy.Spec, newDeploy.Spec); !equal {
		return false, fmt.Errorf("spec not equal: %s", err.Error())
	}

	if checkStatus {
		if equal, err := deploymentStatusEqual(oldDeploy.Status, newDeploy.Status); !equal {
			return false, fmt.Errorf("status not equal: %s", err.Error())
		}
	}

	return true, nil
}

func deploymentSpecEqual(spec1, spec2 appsv1.DeploymentSpec) (bool, error) {
	if spec1.Replicas != nil && spec2.Replicas != nil {
		if *spec1.Replicas != *spec2.Replicas {
			return false, fmt.Errorf(
				"replicas not equal, old: %d, new: %d", *spec1.Replicas,
				*spec2.Replicas,
			)
		}
	}

	if !reflect.DeepEqual(spec1.Selector, spec2.Selector) {
		return false, fmt.Errorf("selector not equal, old: %v, new: %v", spec1.Selector, spec2.Selector)
	}

	if equal, err := podTemplateSpecEqual(spec1.Template, spec2.Template); !equal {
		return false, fmt.Errorf("podTemplateSpec not equal, %s", err.Error())
	}

	return true, nil
}

func deploymentStatusEqual(oldStatus, newStatus appsv1.DeploymentStatus) (bool, error) {
	if !reflect.DeepEqual(oldStatus, newStatus) {
		return false, fmt.Errorf("status not equal")
	}
	return true, nil
}
