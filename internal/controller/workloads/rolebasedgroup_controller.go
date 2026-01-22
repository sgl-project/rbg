/*
Copyright 2025.

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

package workloads

import (
	"context"
	stderrors "errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/modern-go/reflect2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/coordination/coordinationscaling"
	"sigs.k8s.io/rbgs/pkg/dependency"
	"sigs.k8s.io/rbgs/pkg/reconciler"
	"sigs.k8s.io/rbgs/pkg/scale"
	"sigs.k8s.io/rbgs/pkg/scheduler"
	"sigs.k8s.io/rbgs/pkg/utils"
	schev1alpha1 "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
	volcanoschedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

var (
	runtimeController *builder.TypedBuilder[reconcile.Request]
	watchedWorkload   sync.Map
)

func init() {
	watchedWorkload = sync.Map{}
}

// RoleBasedGroupReconciler reconciles a RoleBasedGroup object
type RoleBasedGroupReconciler struct {
	client    client.Client
	apiReader client.Reader
	scheme    *runtime.Scheme
	recorder  record.EventRecorder
}

func NewRoleBasedGroupReconciler(mgr ctrl.Manager) *RoleBasedGroupReconciler {
	return &RoleBasedGroupReconciler{
		client:    mgr.GetClient(),
		apiReader: mgr.GetAPIReader(),
		scheme:    mgr.GetScheme(),
		recorder:  mgr.GetEventRecorderFor("RoleBasedGroup"),
	}
}

// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroups/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=controllerrevisions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=controllerrevisions/status,verbs=get;update;patch

func (r *RoleBasedGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the RoleBasedGroup instance
	rbg := &workloadsv1alpha1.RoleBasedGroup{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: req.Name, Namespace: req.Namespace}, rbg); err != nil {
		r.recorder.Eventf(
			rbg, corev1.EventTypeWarning, FailedGetRBG,
			"Failed to get rbg, err: %s", err.Error(),
		)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if rbg.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx).WithValues("rbg", klog.KObj(rbg))
	ctx = ctrl.LoggerInto(ctx, logger)
	logger.Info("Start reconciling")
	start := time.Now()
	defer func() {
		logger.Info("Finished reconciling", "duration", time.Since(start))
	}()

	// Process revisions
	expectedRolesRevisionHash, err := r.handleRevisions(ctx, rbg)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Process roles in dependency order
	dependencyManager := dependency.NewDefaultDependencyManager(r.scheme, r.client)
	sortedRoles, err := dependencyManager.SortRoles(ctx, rbg)
	if err != nil {
		r.recorder.Event(rbg, corev1.EventTypeWarning, InvalidRoleDependency, err.Error())
		return ctrl.Result{}, err
	}

	// Process PodGroup
	podGroupManager := scheduler.NewPodGroupScheduler(r.client)
	if err := podGroupManager.Reconcile(ctx, rbg, runtimeController, &watchedWorkload, r.apiReader); err != nil {
		r.recorder.Event(rbg, corev1.EventTypeWarning, FailedCreatePodGroup, err.Error())
		return ctrl.Result{}, err
	}

	var updateStatus bool
	roleStatuses := make([]workloadsv1alpha1.RoleStatus, 0, len(rbg.Spec.Roles))
	roleReconciler := make(map[string]reconciler.WorkloadReconciler)
	for _, role := range rbg.Spec.Roles {
		logger := log.FromContext(ctx)
		roleCtx := log.IntoContext(ctx, logger.WithValues("role", role.Name))
		// first check whether watch lws cr
		dynamicWatchCustomCRD(roleCtx, role.Workload.Kind)

		reconciler, err := reconciler.NewWorkloadReconciler(role.Workload, r.scheme, r.client)
		if err != nil {
			logger.Error(err, "Failed to create workload reconciler")
			r.recorder.Eventf(
				rbg, corev1.EventTypeWarning, FailedReconcileWorkload,
				"Failed to reconcile role %s, err: %v", role.Name, err,
			)
			return ctrl.Result{}, err
		}

		if err := reconciler.Validate(ctx, &role); err != nil {
			logger.Error(err, "Failed to validate role declaration")
			r.recorder.Eventf(
				rbg, corev1.EventTypeWarning, FailedReconcileWorkload,
				"Failed to validate role %s declaration, err: %v", role.Name, err,
			)
			return ctrl.Result{}, err
		}

		roleReconciler[role.Name] = reconciler

		roleStatus, updateRoleStatus, err := reconciler.ConstructRoleStatus(roleCtx, rbg, &role)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				r.recorder.Eventf(
					rbg, corev1.EventTypeWarning, FailedReconcileWorkload,
					"Failed to construct role %s status: %v", role.Name, err,
				)
				return ctrl.Result{}, err
			}
			logger.V(1).Info("Workload not found for role, using empty status", "role", role.Name)
		}
		updateStatus = updateStatus || updateRoleStatus
		roleStatuses = append(roleStatuses, roleStatus)
	}

	// Update the status based on the observed role statuses.
	if updateStatus {
		if err := r.updateRBGStatus(ctx, rbg, roleStatuses); err != nil {
			r.recorder.Eventf(
				rbg, corev1.EventTypeWarning, FailedUpdateStatus,
				"Failed to update status for %s: %v", rbg.Name, err,
			)
			return ctrl.Result{}, err
		}
	}

	// Calculate coordination strategies for scaling and rolling update
	// TODO: This is a simple method consolidation for now. The coordination logic will be
	// refactored later to improve extensibility and readability.
	scalingTargets, rollingUpdateStrategies, err := r.calculateCoordinationStrategies(ctx, rbg, roleStatuses)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Reconcile roles, do create/update actions for roles.
	for _, roleList := range sortedRoles {
		var errs error

		for _, role := range roleList {
			logger := log.FromContext(ctx)
			roleCtx := log.IntoContext(ctx, logger.WithValues("role", role.Name))

			// Check dependencies first
			ready, err := dependencyManager.CheckDependencyReady(roleCtx, rbg, role)
			if err != nil {
				r.recorder.Event(rbg, corev1.EventTypeWarning, FailedCheckRoleDependency, err.Error())
				return ctrl.Result{}, err
			}
			if !ready {
				err := fmt.Errorf("dependencies not met for role '%s'", role.Name)
				r.recorder.Event(rbg, corev1.EventTypeWarning, DependencyNotMet, err.Error())
				return ctrl.Result{}, err
			}

			reconciler, ok := roleReconciler[role.Name]
			if !ok || reflect2.IsNil(reconciler) {
				err = fmt.Errorf("workload reconciler not found")
				logger.Error(err, "Failed to get workload reconciler")
				r.recorder.Eventf(
					rbg, corev1.EventTypeWarning, FailedReconcileWorkload,
					"Failed to reconcile role %s: %v", role.Name, err,
				)
				errs = stderrors.Join(errs, err)
				continue
			}

			var rollingUpdateStrategy *workloadsv1alpha1.RollingUpdate
			if rollout, ok := rollingUpdateStrategies[role.Name]; ok {
				rollingUpdateStrategy = &rollout
			}

			// Apply coordination scaling target replicas if available
			roleToReconcile := role
			if targetReplicas, ok := scalingTargets[role.Name]; ok {
				// Always apply the target replicas from coordination scaling
				// This handles both scale-up scenarios
				roleToReconcile = role.DeepCopy()
				roleToReconcile.Replicas = ptr.To(targetReplicas)
				if role.Replicas != nil && targetReplicas != *role.Replicas {
					logger.Info("Applying coordination scaling", "role", role.Name, "original", *role.Replicas, "target", targetReplicas)
				}
			}

			if err := reconciler.Reconciler(roleCtx, rbg, roleToReconcile, rollingUpdateStrategy, expectedRolesRevisionHash[role.Name]); err != nil {
				logger.Error(err, "Failed to reconcile workload")
				r.recorder.Eventf(
					rbg, corev1.EventTypeWarning, FailedReconcileWorkload,
					"Failed to reconcile role %s: %v", role.Name, err,
				)
				errs = stderrors.Join(errs, err)
				continue
			}

			if err := r.ReconcileScalingAdapter(roleCtx, rbg, role); err != nil {
				logger.Error(err, "Failed to reconcile scaling adapter")
				r.recorder.Eventf(
					rbg, corev1.EventTypeWarning, FailedCreateScalingAdapter,
					"Failed to reconcile scaling adapter for role %s: %v", role.Name, err,
				)
				errs = stderrors.Join(errs, err)
				continue
			}
		}

		if errs != nil {
			return ctrl.Result{}, errs
		}
	}

	// delete orphan roles
	if err := r.deleteOrphanRoles(ctx, rbg); err != nil {
		r.recorder.Eventf(
			rbg, corev1.EventTypeWarning, "delete role error",
			"Failed to delete roles for %s: %v", rbg.Name, err,
		)
		return ctrl.Result{}, err
	}

	// delete expired controllerRevision
	if _, err := utils.CleanExpiredRevision(ctx, r.client, rbg); err != nil {
		r.recorder.Eventf(
			rbg, corev1.EventTypeWarning, "delete expired revision error",
			"Failed to delete expired revision for %s: %v", rbg.Name, err,
		)
		return ctrl.Result{}, err
	}

	r.recorder.Event(rbg, corev1.EventTypeNormal, Succeed, "ReconcileSucceed")
	return ctrl.Result{}, nil
}

func (r *RoleBasedGroupReconciler) handleRevisions(ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup) (map[string]string, error) {
	logger := log.FromContext(ctx)

	currentRevision, err := r.getCurrentRevision(ctx, rbg)
	if err != nil {
		logger.Error(err, "Failed get or create revision")
		return nil, err
	}

	expectedRevision, err := utils.NewRevision(ctx, r.client, rbg, currentRevision)
	if err != nil {
		return nil, err
	}

	if !utils.EqualRevision(currentRevision, expectedRevision) {
		logger.Info("Current revision need to be updated")
		if err := r.client.Create(ctx, expectedRevision); err != nil {
			logger.Error(err, fmt.Sprintf("Failed to create revision %v", expectedRevision))
			r.recorder.Event(rbg, corev1.EventTypeWarning, FailedCreateRevision, "Failed create revision for RoleBasedGroup")
			return nil, err
		} else {
			logger.Info(fmt.Sprintf("Create revision [%s] successfully", expectedRevision.Name))
			r.recorder.Event(rbg, corev1.EventTypeNormal, SucceedCreateRevision, "Successful create revision for RoleBasedGroup")
		}
	}

	expectedRolesRevisionHash, err := utils.GetRolesRevisionHash(expectedRevision)
	if err != nil {
		logger.Error(err, "Failed to get roles revision hash")
		return nil, err
	}

	return expectedRolesRevisionHash, nil
}

func (r *RoleBasedGroupReconciler) deleteOrphanRoles(ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup) error {
	errs := make([]error, 0)
	deployRecon := reconciler.NewDeploymentReconciler(r.scheme, r.client)
	if err := deployRecon.CleanupOrphanedWorkloads(ctx, rbg); err != nil {
		errs = append(errs, err)
	}

	stsRecon := reconciler.NewStatefulSetReconciler(r.scheme, r.client)
	if err := stsRecon.CleanupOrphanedWorkloads(ctx, rbg); err != nil {
		errs = append(errs, err)
	}

	lwsRecon := reconciler.NewLeaderWorkerSetReconciler(r.scheme, r.client)
	if err := lwsRecon.CleanupOrphanedWorkloads(ctx, rbg); err != nil {
		errs = append(errs, err)
	}

	instanceSetRecon := reconciler.NewInstanceSetReconciler(r.scheme, r.client)
	if err := instanceSetRecon.CleanupOrphanedWorkloads(ctx, rbg); err != nil {
		errs = append(errs, err)
	}

	if err := r.CleanupOrphanedScalingAdapters(ctx, rbg); err != nil {
		errs = append(errs, err)
	}

	return errors.NewAggregate(errs)
}

func (r *RoleBasedGroupReconciler) updateRBGStatus(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, roleStatuses []workloadsv1alpha1.RoleStatus,
) error {
	// update ready condition
	var rbgReady = true
	statusMap := make(map[string]workloadsv1alpha1.RoleStatus, len(roleStatuses))
	for _, rs := range roleStatuses {
		statusMap[rs.Name] = rs
	}
	for _, role := range rbg.Spec.Roles {
		if rs, ok := statusMap[role.Name]; !ok ||
			role.Replicas == nil ||
			*role.Replicas != rs.Replicas ||
			rs.Replicas != rs.ReadyReplicas {
			rbgReady = false
			break
		}
	}

	var readyCondition metav1.Condition
	if rbgReady {
		readyCondition = metav1.Condition{
			Type:               string(workloadsv1alpha1.RoleBasedGroupReady),
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "AllRolesReady",
			Message:            "All roles are ready",
		}
	} else {
		readyCondition = metav1.Condition{
			Type:               string(workloadsv1alpha1.RoleBasedGroupReady),
			Status:             metav1.ConditionFalse,
			LastTransitionTime: metav1.Now(),
			Reason:             "RoleNotReady",
			Message:            "Not all role ready",
		}
	}

	setCondition(rbg, readyCondition)
	rbg.Status.ObservedGeneration = rbg.Generation

	// update role status
	for i := range roleStatuses {
		found := false
		for j, oldStatus := range rbg.Status.RoleStatuses {
			// if found, update
			if roleStatuses[i].Name == oldStatus.Name {
				found = true
				if roleStatuses[i].Replicas != oldStatus.Replicas || roleStatuses[i].ReadyReplicas != oldStatus.ReadyReplicas {
					rbg.Status.RoleStatuses[j] = roleStatuses[i]
				}
				break
			}
		}
		if !found {
			rbg.Status.RoleStatuses = append(rbg.Status.RoleStatuses, roleStatuses[i])
		}
	}

	// update rbg status
	rbgApplyConfig := ToRBGApplyConfigurationForStatus(rbg)

	return utils.PatchObjectApplyConfiguration(ctx, r.client, rbgApplyConfig, utils.PatchStatus)

}

func (r *RoleBasedGroupReconciler) ReconcileScalingAdapter(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, roleSpec *workloadsv1alpha1.RoleSpec,
) error {
	logger := log.FromContext(ctx)
	roleName := roleSpec.Name
	roleScalingAdapterName := scale.GenerateScalingAdapterName(rbg.Name, roleName)
	rbgScalingAdapter := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{}
	err := r.client.Get(
		ctx, types.NamespacedName{Name: roleScalingAdapterName, Namespace: rbg.Namespace}, rbgScalingAdapter,
	)
	if err == nil {
		// scalingAdapter exists
		// clean scalingAdapter when user update rbg.spec.role.scalingAdapter.enable to false
		if !scale.IsScalingAdapterEnable(roleSpec) {
			logger.Info("delete scalingAdapter", "scalingAdapter", rbgScalingAdapter.Name)
			return r.client.Delete(ctx, rbgScalingAdapter)
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		// failed to check scaling adapter exists
		return err
	}
	if !scale.IsScalingAdapterEnable(roleSpec) {
		return nil
	}

	// scalingAdapter not found
	rbgScalingAdapter = &workloadsv1alpha1.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleScalingAdapterName,
			Namespace: rbg.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         rbg.APIVersion,
					Kind:               rbg.Kind,
					Name:               rbg.Name,
					UID:                rbg.UID,
					BlockOwnerDeletion: ptr.To(true),
				},
			},
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: rbg.Name,
				workloadsv1alpha1.SetRoleLabelKey: roleName,
			},
		},
		Spec: workloadsv1alpha1.RoleBasedGroupScalingAdapterSpec{
			ScaleTargetRef: &workloadsv1alpha1.AdapterScaleTargetRef{
				Name: rbg.Name,
				Role: roleName,
			},
		},
	}

	return r.client.Create(ctx, rbgScalingAdapter)
}
func (r *RoleBasedGroupReconciler) getCurrentRevision(ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup) (*appsv1.ControllerRevision, error) {
	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: map[string]string{
			workloadsv1alpha1.SetNameLabelKey: rbg.Name,
		},
	})
	if err != nil {
		return nil, err
	}
	revisions, err := utils.ListRevisions(ctx, r.client, rbg, selector)
	if err != nil {
		return nil, err
	}
	revision := utils.GetHighestRevision(revisions)
	return revision, nil
}

func (r *RoleBasedGroupReconciler) CleanupOrphanedScalingAdapters(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup,
) error {
	logger := log.FromContext(ctx)
	// list scalingAdapter managed by rbg
	scalingAdapterList := &workloadsv1alpha1.RoleBasedGroupScalingAdapterList{}
	if err := r.client.List(
		context.Background(), scalingAdapterList, client.InNamespace(rbg.Namespace),
		client.MatchingLabels(
			map[string]string{
				workloadsv1alpha1.SetNameLabelKey: rbg.Name,
			},
		),
	); err != nil {
		return err
	}

	for _, scalingAdapter := range scalingAdapterList.Items {
		if !scale.IsScalingAdapterManagedByRBG(&scalingAdapter, rbg) {
			continue
		}
		scaleTargetRef := scalingAdapter.Spec.ScaleTargetRef
		if scaleTargetRef == nil || scaleTargetRef.Name != rbg.Name {
			continue
		}

		found := false
		for _, role := range rbg.Spec.Roles {
			if role.Name == scaleTargetRef.Role {
				found = true
				break
			}
		}
		if !found {
			logger.Info("delete scalingAdapter", "scalingAdapter", scalingAdapter.Name)
			if err := r.client.Delete(ctx, &scalingAdapter); err != nil {
				return fmt.Errorf("delete scalingAdapter %s error: %s", scalingAdapter.Name, err.Error())
			}
		}
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RoleBasedGroupReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	runtimeController = ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&workloadsv1alpha1.RoleBasedGroup{}, builder.WithPredicates(RBGPredicate())).
		Owns(&appsv1.StatefulSet{}, builder.WithPredicates(WorkloadPredicate())).
		Owns(&appsv1.Deployment{}, builder.WithPredicates(WorkloadPredicate())).
		Owns(&workloadsv1alpha1.InstanceSet{}, builder.WithPredicates(WorkloadPredicate())).
		Owns(&corev1.Service{}).
		Named("workloads-rolebasedgroup")

	err := utils.CheckCrdExists(r.apiReader, utils.LwsCrdName)
	if err == nil {
		watchedWorkload.LoadOrStore(utils.LwsCrdName, struct{}{})
		runtimeController.Owns(&lwsv1.LeaderWorkerSet{}, builder.WithPredicates(WorkloadPredicate()))
	}
	err = utils.CheckCrdExists(r.apiReader, scheduler.KubePodGroupCrdName)
	if err == nil {
		watchedWorkload.LoadOrStore(scheduler.KubePodGroupCrdName, struct{}{})
		runtimeController.Owns(&schev1alpha1.PodGroup{})
	}
	err = utils.CheckCrdExists(r.apiReader, scheduler.VolcanoPodGroupCrdName)
	if err == nil {
		watchedWorkload.LoadOrStore(scheduler.VolcanoPodGroupCrdName, struct{}{})
		runtimeController.Owns(&volcanoschedulingv1beta1.PodGroup{})
	}

	return runtimeController.Complete(r)
}

// CheckCrdExists checks if the specified Custom Resource Definition (CRD) exists in the Kubernetes cluster.
func (r *RoleBasedGroupReconciler) CheckCrdExists() error {
	crds := []string{
		"rolebasedgroups.workloads.x-k8s.io",
		"clusterengineruntimeprofiles.workloads.x-k8s.io",
	}

	for _, crd := range crds {
		if err := utils.CheckCrdExists(r.apiReader, crd); err != nil {
			return err
		}
	}
	return nil
}

// calculateCoordinationStrategies calculates coordination scaling targets and rolling update strategies.
func (r *RoleBasedGroupReconciler) calculateCoordinationStrategies(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	roleStatuses []workloadsv1alpha1.RoleStatus,
) (map[string]int32, map[string]workloadsv1alpha1.RollingUpdate, error) {
	// Calculate target replicas for coordination scaling
	scalingTargets, err := r.CalculateScalingForAllCoordination(ctx, rbg, roleStatuses)
	if err != nil {
		r.recorder.Eventf(
			rbg, corev1.EventTypeWarning, FailedCalculateScaling,
			"Failed to calculate scaling targets for %s: %v", rbg.Name, err,
		)
		return nil, nil, err
	}

	// Calculate the rolling update strategy for all coordination specifications in rbg.
	rollingUpdateStrategies, err := r.CalculateRollingUpdateForAllCoordination(rbg, roleStatuses)
	if err != nil {
		r.recorder.Eventf(
			rbg, corev1.EventTypeWarning, FailedUpdateStatus,
			"Failed to update status for %s: %v", rbg.Name, err,
		)
		return nil, nil, err
	}

	return scalingTargets, rollingUpdateStrategies, nil
}

// CalculateScalingForAllCoordination calculates target replicas for each role based on coordination scaling strategies.
func (r *RoleBasedGroupReconciler) CalculateScalingForAllCoordination(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	roleStatuses []workloadsv1alpha1.RoleStatus,
) (map[string]int32, error) {
	logger := log.FromContext(ctx)
	if rbg == nil {
		return nil, fmt.Errorf("rbg is nil")
	}

	result := make(map[string]int32)
	processedRoles := make(map[string]bool)

	for _, coordination := range rbg.Spec.CoordinationRequirements {
		// Skip if no scaling strategy
		if coordination.Strategy == nil || coordination.Strategy.Scaling == nil {
			logger.V(2).Info("Skipping coordination without scaling strategy", "coordination", coordination.Name)
			continue
		}

		maxSkewStr := "100%"
		if coordination.Strategy.Scaling.MaxSkew != nil {
			maxSkewStr = *coordination.Strategy.Scaling.MaxSkew
		}
		logger.V(1).Info("Processing coordination scaling", "coordination", coordination.Name, "roles", coordination.Roles, "maxSkew", maxSkewStr)

		// Create scaler
		scaler, err := coordinationscaling.NewCoordinationScaler(&coordination)
		if err != nil {
			return nil, fmt.Errorf("failed to create coordination scaler for %s: %w", coordination.Name, err)
		}

		// Build role states for this coordination
		roleStates := make(map[string]coordinationscaling.RoleScalingState)
		for _, roleName := range coordination.Roles {
			// Get desired replicas from spec
			desired := utils.GetRoleReplicas(rbg, roleName)

			// Get current and ready replicas from status
			var current, ready int32
			for _, status := range roleStatuses {
				if status.Name == roleName {
					current = status.Replicas
					ready = status.ReadyReplicas
					break
				}
			}

			// Query scheduled replicas from pods
			scheduled, err := r.getScheduledReplicas(ctx, rbg, roleName)
			if err != nil {
				return nil, fmt.Errorf("failed to query scheduled replicas for role %s: %w", roleName, err)
			}

			roleStates[roleName] = coordinationscaling.RoleScalingState{
				RoleName:          roleName,
				DesiredReplicas:   desired,
				CurrentReplicas:   current,
				ScheduledReplicas: scheduled,
				ReadyReplicas:     ready,
			}
		}

		// Calculate target replicas
		targets, err := scaler.CalculateTargetReplicas(roleStates)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate target replicas for coordination %s: %w", coordination.Name, err)
		}

		// Merge results, taking the minimum target for roles in multiple coordinations
		for roleName, target := range targets {
			if processedRoles[roleName] {
				// Role already processed by another coordination, take minimum
				if existing := result[roleName]; target < existing {
					logger.V(1).Info("Taking minimum target for role in multiple coordinations", "role", roleName, "previous", existing, "new", target)
					result[roleName] = target
				}
			} else {
				result[roleName] = target
				processedRoles[roleName] = true
			}
		}
	}

	return result, nil
}

// getScheduledReplicas queries the number of scheduled pods (with nodeName) for a given role.
func (r *RoleBasedGroupReconciler) getScheduledReplicas(
	ctx context.Context,
	rbg *workloadsv1alpha1.RoleBasedGroup,
	roleName string,
) (int32, error) {
	podList := &corev1.PodList{}
	labelSelector := client.MatchingLabels{
		workloadsv1alpha1.SetNameLabelKey: rbg.Name,
		workloadsv1alpha1.SetRoleLabelKey: roleName,
	}

	if err := r.client.List(ctx, podList, client.InNamespace(rbg.Namespace), labelSelector); err != nil {
		return 0, err
	}

	var scheduled int32
	for _, pod := range podList.Items {
		if pod.Spec.NodeName != "" {
			scheduled++
		}
	}

	return scheduled, nil
}

func (r *RoleBasedGroupReconciler) CalculateRollingUpdateForAllCoordination(
	rbg *workloadsv1alpha1.RoleBasedGroup,
	roleStatuses []workloadsv1alpha1.RoleStatus,
) (map[string]workloadsv1alpha1.RollingUpdate, error) {
	var strategyRollingUpdate map[string]workloadsv1alpha1.RollingUpdate
	for _, coordination := range rbg.Spec.CoordinationRequirements {
		strategyByCoord, err := r.calculateRollingUpdateForCoordination(rbg, &coordination, roleStatuses)
		if err != nil {
			return nil, err
		}
		strategyRollingUpdate = mergeStrategyRollingUpdate(strategyByCoord, strategyRollingUpdate)
	}
	return strategyRollingUpdate, nil
}

func (r *RoleBasedGroupReconciler) calculateRollingUpdateForCoordination(
	rbg *workloadsv1alpha1.RoleBasedGroup,
	coordination *workloadsv1alpha1.Coordination,
	roleStatuses []workloadsv1alpha1.RoleStatus,
) (map[string]workloadsv1alpha1.RollingUpdate, error) {
	if coordination == nil ||
		coordination.Strategy == nil ||
		coordination.Strategy.RollingUpdate == nil ||
		len(coordination.Roles) <= 1 {
		return nil, nil
	}

	// The roles that are under controlled by this coordination.
	coordinationRoles := sets.New[string]()
	for _, role := range coordination.Roles {
		if replicas := utils.GetRoleReplicas(rbg, role); replicas > 0 {
			coordinationRoles.Insert(role)
		}
	}

	// Initialize the maxUnavailable and partition.
	strategyRollingUpdate := make(map[string]workloadsv1alpha1.RollingUpdate, len(rbg.Spec.Roles))
	for _, role := range rbg.Spec.Roles {
		if !coordinationRoles.Has(role.Name) {
			continue
		}
		strategy := workloadsv1alpha1.RollingUpdate{}
		if role.RolloutStrategy != nil && role.RolloutStrategy.RollingUpdate != nil {
			strategy = *(role.RolloutStrategy.RollingUpdate.DeepCopy())
		}
		if coordination.Strategy.RollingUpdate.MaxUnavailable != nil {
			strategy.MaxUnavailable = ptr.To(intstr.FromString(*coordination.Strategy.RollingUpdate.MaxUnavailable))
		}
		if coordination.Strategy.RollingUpdate.Partition != nil {
			partitionPercent := intstr.FromString(*coordination.Strategy.RollingUpdate.Partition)
			partition, err := utils.CalculatePartitionReplicas(&partitionPercent, role.Replicas)
			if err != nil {
				return nil, err
			}
			strategy.Partition = func(a int32) *intstr.IntOrString {
				return ptr.To(intstr.FromInt32(a))
			}(int32(partition))
		}
		strategyRollingUpdate[role.Name] = strategy
	}

	// Calculate the next rolling target based on maxSkew.
	nextRollingTarget := CalculateNextRollingTarget(rbg, coordination, roleStatuses, coordinationRoles)

	// Calculate the partition based one the next rolling target.
	for _, role := range rbg.Spec.Roles {
		if !coordinationRoles.Has(role.Name) {
			continue
		}
		updatedTarget, ok := nextRollingTarget[role.Name]
		if !ok {
			continue
		}
		strategy := strategyRollingUpdate[role.Name]

		// finalPartition is the user-settings target partition that should be respected by any coordination.
		finalPartition := int32(0)
		if strategy.Partition != nil && role.Replicas != nil {
			finalPartitionInt, err := intstr.GetScaledValueFromIntOrPercent(strategy.Partition, int(*role.Replicas), true)
			if err != nil {
				return nil, err
			}
			finalPartition = int32(finalPartitionInt)
		}

		// stepPartition is the calculated partition based on the maxSkew.
		if role.Replicas == nil {
			continue
		}
		stepPartition := max(*role.Replicas-updatedTarget, 0)

		if stepPartition > finalPartition {
			strategy.Partition = ptr.To(intstr.FromInt32(stepPartition))
		} else {
			strategy.Partition = ptr.To(intstr.FromInt32(finalPartition))
		}
		strategyRollingUpdate[role.Name] = strategy
	}
	return strategyRollingUpdate, nil
}

func CalculateNextRollingTarget(rbg *workloadsv1alpha1.RoleBasedGroup,
	coordination *workloadsv1alpha1.Coordination,
	roleStatuses []workloadsv1alpha1.RoleStatus,
	coordinationRoles sets.Set[string],
) map[string]int32 {

	// single role no need coordination for skew, just return
	if coordination.Strategy.RollingUpdate.MaxSkew == nil || len(coordination.Roles) <= 1 {
		return nil
	}

	var (
		desiredReplicas = make(map[string]int32, len(coordination.Roles))
		updatedReplicas = make(map[string]int32, len(coordination.Roles))
		readyReplicas   = make(map[string]int32, len(coordination.Roles))
	)

	// initialize the desired & updated replicas of each role
	for _, role := range rbg.Spec.Roles {
		if !coordinationRoles.Has(role.Name) {
			continue
		}
		desiredReplicas[role.Name] = *role.Replicas
	}
	for _, status := range roleStatuses {
		if !coordinationRoles.Has(status.Name) {
			continue
		}
		readyReplicas[status.Name] = status.ReadyReplicas
		updatedReplicas[status.Name] = status.UpdatedReplicas
	}
	return calculateNextRollingTarget(coordination.Strategy.RollingUpdate.MaxSkew, coordinationRoles, desiredReplicas, updatedReplicas, readyReplicas)
}

func calculateNextRollingTarget(
	maxSkewPercent *string,
	coordinationRoles sets.Set[string],
	desiredReplicas, updatedReplicas, readyReplicas map[string]int32,
) map[string]int32 {

	// Get the fastest and slowest role in this coordination roles, to ensure the skew between them is not larger than maxSkew.
	fastestRole, slowestRole := getFastestAndSlowestRole(coordinationRoles, desiredReplicas, updatedReplicas)
	if fastestRole == "" || slowestRole == "" {
		return nil
	}

	// Initialize the rolling target for each role.
	rollingTarget := make(map[string]int32, coordinationRoles.Len())
	for role := range coordinationRoles {
		rollingTarget[role] = updatedReplicas[role]
	}

	// Get the max skew with absolute value, we assume the maxSkew must >= 1 so that cannot block the entire upgrade.
	maxSkew, _ := utils.ParseIntStrAsNonZero(intstr.FromString(*maxSkewPercent), desiredReplicas[slowestRole])

	// The following codes calculate the next rolling target step for the slowest role under the constraint of maxSkew condition.
	// We choose the fastest role as the reference, so we can get the balance threshold between the fastest and the slowest.
	lowerBound, upperBound := calculateCoordinationUpdatedReplicasBound(intstr.FromString(*maxSkewPercent), updatedReplicas[fastestRole], desiredReplicas[fastestRole], desiredReplicas[slowestRole])
	balanceThreshold := (lowerBound + upperBound + 1) >> 1
	distance2Balance := max(balanceThreshold-updatedReplicas[slowestRole], 0)

	// Due to the synchronization between rbg and workloads, the rbg may not get the least updatedReplicas correctly here.
	// We add maxSkew/2 to the next rolling step to make sure the skew is not too large.
	nextRollingStep := max(distance2Balance, maxSkew>>1)

	// If balance reached and the fastest role is ready, we add 1 to the next rolling step to make sure the slowest role is updated.
	if readyReplicas[fastestRole] == desiredReplicas[fastestRole] {
		nextRollingStep = max(nextRollingStep, 1)
	}

	// The next rolling target = current rolling target + next rolling step.
	// `upperBound + 1` is used for limiting the maximum rolling steps in some corner cases.
	rollingTarget[slowestRole] = min(updatedReplicas[slowestRole]+nextRollingStep, upperBound+1)
	return rollingTarget
}

func getFastestAndSlowestRole(coordinationRoles sets.Set[string], desiredReplicas, updatedReplicas map[string]int32) (string, string) {
	if coordinationRoles.Len() <= 1 {
		return "", ""
	}
	rollingRatio := make(map[string]float64, coordinationRoles.Len())
	for role := range coordinationRoles {
		roleUpdatedRatio := float64(updatedReplicas[role]) / float64(desiredReplicas[role])
		rollingRatio[role] = roleUpdatedRatio
	}
	roles := coordinationRoles.UnsortedList()
	sort.Slice(roles, func(i, j int) bool {
		if utils.ABSFloat64(rollingRatio[roles[i]]-rollingRatio[roles[j]]) > 1e-6 {
			return rollingRatio[roles[i]] < rollingRatio[roles[j]]
		}
		return desiredReplicas[roles[i]] > desiredReplicas[roles[j]]
	})
	return roles[coordinationRoles.Len()-1], roles[0]
}

func mergeStrategyRollingUpdate(strategiesA, strategiesB map[string]workloadsv1alpha1.RollingUpdate) map[string]workloadsv1alpha1.RollingUpdate {
	merged := make(map[string]workloadsv1alpha1.RollingUpdate, len(strategiesA))
	for role, strategyA := range strategiesA {
		merged[role] = strategyA
	}
	for role, strategyB := range strategiesB {
		strategyA, ok := merged[role]
		if !ok {
			merged[role] = strategyB
			continue
		}
		maxUnavailableA, _ := intstr.GetScaledValueFromIntOrPercent(strategyA.MaxUnavailable, 100, true)
		maxUnavailableB, _ := intstr.GetScaledValueFromIntOrPercent(strategyB.MaxUnavailable, 100, true)
		if maxUnavailableA > maxUnavailableB {
			strategyA.MaxUnavailable = strategyB.MaxUnavailable
		}

		partitionA, partitionB := 0, 0
		if strategyA.Partition != nil {
			partitionA, _ = intstr.GetScaledValueFromIntOrPercent(strategyA.Partition, 100, true)
		}
		if strategyB.Partition != nil {
			partitionB, _ = intstr.GetScaledValueFromIntOrPercent(strategyB.Partition, 100, true)
		}
		if partitionA < partitionB {
			strategyA.Partition = strategyB.Partition
		}
		merged[role] = strategyA
	}
	return merged
}

// calculateCoordinationUpdatedReplicasBound calculate the updated replicas bound for the request role based on the reference role.
// Explanation:
// a = updated replicas of the given reference role
// x = updated replicas of the role we want to calculate
// b = desired replicas of the given reference role
// d = desired replicas of the role we want to calculate
// s = allowed percentage max-skew
//
// So we should have the following condition and result:
// a/b - x/d <= s/100
// x/d - a/b <= s/100
// => (100*a*d - s*b*d) / (100*b) <= x <= (100*a*d + s*b*d) / (100*b)
func calculateCoordinationUpdatedReplicasBound(maxSkew intstr.IntOrString, refUpdated, refDesired, requestDesired int32) (int32, int32) {
	if refDesired == 0 {
		return 0, 0
	}
	// We present maxSkew as a percentage using fraction, e.g., 10/100 for 10%.
	maxSkewPercent, _ := intstr.GetScaledValueFromIntOrPercent(&maxSkew, 100, true)

	// using int64 to avoid overflow and to maintain precision
	a := int64(refUpdated)
	b := int64(refDesired)
	d := int64(requestDesired)
	s := int64(maxSkewPercent)

	// handle the case where `A % B != 0`, we use rounding to the nearest integer to get the result.
	lower := math.Round(float64(max(100*a*d-s*b*d, 0)) / float64(100*b))
	upper := math.Round(float64(max(s*b*d+100*a*d, 0)) / float64(100*b))
	return int32(lower), int32(upper)
}

func RBGPredicate() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			_, ok := e.Object.(*workloadsv1alpha1.RoleBasedGroup)
			if ok {
				ctrl.Log.Info("enqueue: rbg create event", "rbg", klog.KObj(e.Object))
				return true
			}
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRbg, ok1 := e.ObjectOld.(*workloadsv1alpha1.RoleBasedGroup)
			newRbg, ok2 := e.ObjectNew.(*workloadsv1alpha1.RoleBasedGroup)
			if ok1 && ok2 {
				if !reflect.DeepEqual(oldRbg.Spec, newRbg.Spec) {
					ctrl.Log.Info("enqueue: rbg update event", "rbg", klog.KObj(e.ObjectOld))
					return true
				}
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			_, ok := e.Object.(*workloadsv1alpha1.RoleBasedGroup)
			if ok {
				ctrl.Log.Info("enqueue: rbg delete event", "rbg", klog.KObj(e.Object))
				return true
			}
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}
}

func WorkloadPredicate() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			// ignore workload create event
			return false
		},
		UpdateFunc: func(e event.TypedUpdateEvent[client.Object]) bool {
			ctrl.Log.V(1).Info(
				fmt.Sprintf(
					"enter workload.onUpdateFunc, %s/%s, type: %T",
					e.ObjectNew.GetNamespace(), e.ObjectNew.GetName(), e.ObjectNew,
				),
			)
			// Defensive check for nil objects
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return false
			}

			// Check validity of OwnerReferences for both old and new objects
			targetGVK := utils.GetRbgGVK()
			if !hasValidOwnerRef(e.ObjectOld, targetGVK) ||
				!hasValidOwnerRef(e.ObjectNew, targetGVK) {
				return false
			}

			// Check if the workload needs to be reconciled
			equal, err := reconciler.WorkloadEqual(e.ObjectOld, e.ObjectNew)
			if err != nil {
				ctrl.Log.Info(
					"enqueue: workload update event",
					"rbg", klog.KObj(e.ObjectOld), "diff", err.Error(),
				)
			}
			return !equal
		},
		DeleteFunc: func(e event.TypedDeleteEvent[client.Object]) bool {
			// Ignore objects without valid OwnerReferences
			if e.Object == nil || !hasValidOwnerRef(e.Object, utils.GetRbgGVK()) {
				return false
			}

			ctrl.Log.Info("enqueue: workload delete event", "rbg", klog.KObj(e.Object))
			return true
		},
		GenericFunc: func(e event.TypedGenericEvent[client.Object]) bool {
			return false
		},
	}
}

// hasValidOwnerRef checks if the object has valid OwnerReferences matching target GVK
// Returns true only when:
// 1. Object has non-empty OwnerReferences
// 2. At least one OwnerReference matches target GroupVersionKind
func hasValidOwnerRef(obj client.Object, targetGVK schema.GroupVersionKind) bool {
	refs := obj.GetOwnerReferences()
	if len(refs) == 0 {
		return false
	}
	return utils.CheckOwnerReference(refs, targetGVK)
}

func dynamicWatchCustomCRD(ctx context.Context, kind string) {
	logger := log.FromContext(ctx)
	switch kind {
	case utils.GetLwsGVK().Kind:
		_, lwsExist := watchedWorkload.Load(utils.LwsCrdName)
		if !lwsExist {
			watchedWorkload.LoadOrStore(utils.LwsCrdName, struct{}{})
			runtimeController.Owns(&lwsv1.LeaderWorkerSet{}, builder.WithPredicates(WorkloadPredicate()))
			logger.Info("rbgs controller watch LeaderWorkerSet CRD")
		}
	case utils.GetInstanceSetGVK().Kind:
		_, instanceSetExist := watchedWorkload.Load(utils.InstanceSetCrdName)
		if !instanceSetExist {
			watchedWorkload.LoadOrStore(utils.InstanceSetCrdName, struct{}{})
			runtimeController.Owns(&workloadsv1alpha1.InstanceSet{}, builder.WithPredicates(WorkloadPredicate()))
			logger.Info("rbgs controller watch InstanceSet CRD")
		}
	}
}
