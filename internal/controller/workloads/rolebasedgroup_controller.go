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
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	metaapplyv1 "k8s.io/client-go/applyconfigurations/meta/v1"
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

	// Step 1: Init RBGReconcileData to hold all reconciliation reconcileData
	// Inject event function to avoid passing rbg and recorder through all steps
	eventFunc := func(eventType, reason, message string) {
		r.recorder.Event(rbg, eventType, reason, message)
	}
	reconcileData := reconciler.NewRBGReconcileData(rbg, eventFunc)

	// Step 2: Sort roles by dependency and init RoleData list
	if err := r.initRoleData(ctx, rbg, reconcileData); err != nil {
		return ctrl.Result{}, err
	}

	// Step 3: Build global config for all roles
	if err := r.buildGlobalConfig(ctx, reconcileData); err != nil {
		reconcileData.WarningEvent("FailedBuildConfig", err.Error())
		return ctrl.Result{}, err
	}

	// Step 4: Process PodGroup
	podGroupManager := scheduler.NewPodGroupScheduler(r.client)
	if err := podGroupManager.Reconcile(ctx, reconcileData, runtimeController, &watchedWorkload, r.apiReader); err != nil {
		reconcileData.WarningEvent(FailedCreatePodGroup, err.Error())
		return ctrl.Result{}, err
	}

	// Step 5: Construct role statuses
	needUpdateStatus, err := r.constructRoleStatuses(ctx, reconcileData)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Update status if needed
	if needUpdateStatus {
		if err := r.updateRBGStatus(ctx, rbg, reconcileData.GetRoleStatuses()); err != nil {
			reconcileData.WarningEventf(FailedUpdateStatus, "Failed to update status for %s: %v", reconcileData.Metadata().Name, err)
			return ctrl.Result{}, err
		}
	}

	// Step 6: Process coordination requirements (rolling update strategy, etc.)
	if err := r.processCoordination(reconcileData); err != nil {
		return ctrl.Result{}, err
	}

	// Step 7: Reconcile roles by dependency level
	if err := r.reconcileRoles(ctx, reconcileData); err != nil {
		return ctrl.Result{}, err
	}

	// Step 8: Delete orphan roles
	if err := r.deleteOrphanRoles(ctx, reconcileData); err != nil {
		reconcileData.WarningEventf("delete role error", "Failed to delete roles for %s: %v", reconcileData.Metadata().Name, err)
		return ctrl.Result{}, err
	}

	// Step 9: Delete expired controllerRevision
	metadata := reconcileData.Metadata()
	if _, err := utils.CleanExpiredRevision(ctx, r.client, metadata.Name, metadata.Namespace, metadata.UID); err != nil {
		reconcileData.WarningEventf("delete expired revision error", "Failed to delete expired revision for %s: %v", metadata.Name, err)
		return ctrl.Result{}, err
	}

	reconcileData.NormalEvent(Succeed, "ReconcileSucceed")
	return ctrl.Result{}, nil
}

// initRoleData sorts roles by dependency and initializes RoleData list in data.
func (r *RoleBasedGroupReconciler) initRoleData(ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, data *reconciler.RBGReconcileData) error {
	// Process revisions and get revision hash map
	revisionHash, err := r.handleRevisions(ctx, rbg, data)
	if err != nil {
		return err
	}

	dependencyManager := dependency.NewDefaultDependencyManager(r.scheme, r.client)
	sortedRoles, err := dependencyManager.SortRoles(ctx, rbg)
	if err != nil {
		data.WarningEvent(InvalidRoleDependency, err.Error())
		return err
	}

	// Build RoleData list with dependency level
	metadata := data.Metadata()

	// Get exclusive topology key and generate group unique hash
	exclusiveTopologyKey, _ := rbg.GetExclusiveKey()

	// Determine pod group label key based on policy
	podGroupLabelKey := ""
	if rbg.Spec.PodGroupPolicy != nil {
		if rbg.Spec.PodGroupPolicy.KubeScheduling != nil {
			podGroupLabelKey = scheduler.KubePodGroupLabelKey
		} else if rbg.Spec.PodGroupPolicy.VolcanoScheduling != nil {
			podGroupLabelKey = scheduler.VolcanoPodGroupAnnotationKey
		}
	}

	for level, roleList := range sortedRoles {
		for _, role := range roleList {
			roleData := &reconciler.RoleData{
				Spec:                 role,
				DependencyLevel:      level,
				ExpectedRevisionHash: revisionHash[role.Name],
				WorkloadName:         metadata.GetWorkloadName(role),
				OwnerInfo: reconciler.OwnerInfo{
					Name:      metadata.Name,
					Namespace: metadata.Namespace,
					UID:       metadata.UID,
				},
				ExclusiveTopologyKey: exclusiveTopologyKey,
				PodGroupKey:          podGroupLabelKey,
			}
			data.AddRole(roleData)
		}
	}

	return nil
}

// buildGlobalConfig builds global config ConfigMap for all roles.
// This ConfigMap contains cluster-wide information used by all roles for service discovery.
func (r *RoleBasedGroupReconciler) buildGlobalConfig(ctx context.Context, data *reconciler.RBGReconcileData) error {
	logger := log.FromContext(ctx)
	allRoles := data.GetRoles()
	if len(allRoles) == 0 {
		return nil
	}

	metadata := data.Metadata()

	// Build config using ConfigBuilder
	builder := &ConfigBuilder{
		Client:    r.client,
		GroupName: metadata.Name,
		Roles:     allRoles,
	}

	const (
		configKey = "config.yaml"
	)

	configData, err := builder.Build()
	if err != nil {
		return fmt.Errorf("failed to build config: %w", err)
	}

	// Create ConfigMap name using the RBG name
	configMapName := fmt.Sprintf("%s-config", metadata.Name)

	cmApplyConfig := coreapplyv1.ConfigMap(configMapName, metadata.Namespace).
		WithData(
			map[string]string{
				configKey: string(configData),
			},
		).
		WithOwnerReferences(
			metaapplyv1.OwnerReference().
				WithAPIVersion(workloadsv1alpha1.GroupVersion.String()).
				WithKind(workloadsv1alpha1.RoleBasedGroupKind).
				WithName(metadata.Name).
				WithUID(metadata.UID).
				WithBlockOwnerDeletion(true).
				WithController(true),
		)

	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cmApplyConfig)
	if err != nil {
		logger.Error(err, "Converting obj apply configuration to json.")
		return err
	}
	newConfigmap := &corev1.ConfigMap{}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(obj, newConfigmap); err != nil {
		return fmt.Errorf("convert ConfigmapApplyConfig error: %s", err.Error())
	}

	oldConfigmap := &corev1.ConfigMap{}
	err = r.client.Get(
		ctx, types.NamespacedName{Name: configMapName, Namespace: metadata.Namespace}, oldConfigmap,
	)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	equal, diff := SemanticallyEqualConfigmap(oldConfigmap, newConfigmap)
	if equal {
		logger.V(1).Info("configmap equal, skip reconcile")
	} else {
		logger.V(1).Info(fmt.Sprintf("configmap not equal, diff: %s", diff))
		if err := utils.PatchObjectApplyConfiguration(ctx, r.client, cmApplyConfig, utils.PatchSpec); err != nil {
			logger.Error(err, "Failed to patch ConfigMap")
			return err
		}
	}

	return nil
}

// constructRoleStatuses constructs status for each role and returns whether status update is needed.
func (r *RoleBasedGroupReconciler) constructRoleStatuses(ctx context.Context, data *reconciler.RBGReconcileData) (bool, error) {
	needUpdateStatus := false
	metadata := data.Metadata()
	logger := log.FromContext(ctx).WithValues("rbg", metadata.Name)

	for _, roleData := range data.GetRoles() {
		roleSpec := roleData.Spec
		logger = logger.WithValues("role", roleSpec.Name)
		roleCtx := log.IntoContext(ctx, logger)

		// Dynamic watch custom CRD
		dynamicWatchCustomCRD(roleCtx, roleSpec.Workload.Kind)

		// Create reconciler for validation and status construction
		workloadReconciler, err := reconciler.NewWorkloadReconciler(roleSpec.Workload, r.scheme, r.client)
		if err != nil {
			logger.Error(err, "Failed to create workload reconciler")
			data.WarningEventf(FailedReconcileWorkload, "Failed to reconcile role %s, err: %v", roleSpec.Name, err)
			return false, err
		}

		// Validate role using roleData
		if err := workloadReconciler.Validate(ctx, roleData); err != nil {
			logger.Error(err, "Failed to validate role declaration")
			data.WarningEventf(FailedReconcileWorkload, "Failed to validate role %s declaration, err: %v", roleSpec.Name, err)
			return false, err
		}

		roleStatus, updateRoleStatus, err := workloadReconciler.ConstructRoleStatus(roleCtx, roleData)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				data.WarningEventf(FailedReconcileWorkload, "Failed to construct role %s status: %v", roleSpec.Name, err)
				return false, err
			}
		}

		roleData.Status = &roleStatus
		if updateRoleStatus {
			needUpdateStatus = true
		}
	}

	return needUpdateStatus, nil
}

// processCoordination processes all coordination requirements for the RBG.
// This includes rolling update strategy calculation and future coordination policies.
func (r *RoleBasedGroupReconciler) processCoordination(data *reconciler.RBGReconcileData) error {
	// Calculate rolling update strategy for all coordination specifications
	if err := r.calculateRollingUpdateStrategies(data); err != nil {
		data.WarningEventf(FailedUpdateStatus, "Failed to calculate rolling update strategy for %s: %v", data.Metadata().Name, err)
		return err
	}

	// TODO: Add more coordination policies here as coordinationRequirements expands
	// e.g., scaling coordination, failure handling coordination, etc.

	return nil
}

// calculateRollingUpdateStrategies calculates rolling update strategy for each role.
func (r *RoleBasedGroupReconciler) calculateRollingUpdateStrategies(data *reconciler.RBGReconcileData) error {
	rollingUpdateStrategies, err := r.CalculateRollingUpdateForAllCoordination(data)
	if err != nil {
		return err
	}

	// Update rolling update strategy to each RoleData
	for _, roleData := range data.GetRoles() {
		if strategy, ok := rollingUpdateStrategies[roleData.Spec.Name]; ok {
			roleData.RollingUpdateStrategy = &strategy
		}
	}

	return nil
}

// reconcileRoles reconciles all roles by dependency level.
func (r *RoleBasedGroupReconciler) reconcileRoles(ctx context.Context, data *reconciler.RBGReconcileData) error {
	metadata := data.Metadata()
	logger := log.FromContext(ctx).WithValues("rbg", metadata.Name)

	dependencyManager := dependency.NewDefaultDependencyManager(r.scheme, r.client)

	// Process roles by dependency level
	for _, roleDataList := range data.GetRolesByDependencyLevel() {
		var errs error

		for _, roleData := range roleDataList {
			roleSpec := roleData.Spec
			logger = logger.WithValues("roleSpec", roleSpec.Name)
			roleCtx := log.IntoContext(ctx, logger)

			// Check dependencies first
			ready, err := dependencyManager.CheckDependencyReady(roleCtx, data, roleSpec)
			if err != nil {
				data.WarningEvent(FailedCheckRoleDependency, err.Error())
				return err
			}
			if !ready {
				err := fmt.Errorf("dependencies not met for roleSpec '%s'", roleSpec.Name)
				data.WarningEvent(DependencyNotMet, err.Error())
				return err
			}

			// Create reconciler for this roleSpec
			workloadReconciler, err := reconciler.NewWorkloadReconciler(roleSpec.Workload, r.scheme, r.client)
			if err != nil || reflect2.IsNil(workloadReconciler) {
				if err == nil {
					err = fmt.Errorf("workload reconciler not found")
				}
				logger.Error(err, "Failed to get workload reconciler")
				data.WarningEventf(FailedReconcileWorkload, "Failed to reconcile roleSpec %s: %v", roleSpec.Name, err)
				errs = stderrors.Join(errs, err)
				continue
			}

			if err := workloadReconciler.Reconciler(roleCtx, roleData); err != nil {
				logger.Error(err, "Failed to reconcile workload")
				data.WarningEventf(FailedReconcileWorkload, "Failed to reconcile roleSpec %s: %v", roleSpec.Name, err)
				errs = stderrors.Join(errs, err)
				continue
			}

			if err := r.ReconcileScalingAdapter(roleCtx, roleData); err != nil {
				logger.Error(err, "Failed to reconcile scaling adapter")
				data.WarningEventf(FailedCreateScalingAdapter, "Failed to reconcile scaling adapter for roleSpec %s: %v", roleSpec.Name, err)
				errs = stderrors.Join(errs, err)
				continue
			}
		}

		if errs != nil {
			return errs
		}
	}

	return nil
}

func (r *RoleBasedGroupReconciler) handleRevisions(ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, data *reconciler.RBGReconcileData) (map[string]string, error) {
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
			data.WarningEvent(FailedCreateRevision, "Failed create revision for RoleBasedGroup")
			return nil, err
		} else {
			logger.Info(fmt.Sprintf("Create revision [%s] successfully", expectedRevision.Name))
			data.NormalEvent(SucceedCreateRevision, "Successful create revision for RoleBasedGroup")
		}
	}

	expectedRolesRevisionHash, err := utils.GetRolesRevisionHash(expectedRevision)
	if err != nil {
		logger.Error(err, "Failed to get roles revision hash")
		return nil, err
	}

	return expectedRolesRevisionHash, nil
}

func (r *RoleBasedGroupReconciler) deleteOrphanRoles(ctx context.Context, data *reconciler.RBGReconcileData) error {
	errs := make([]error, 0)
	roles := data.GetRoles()

	deployRecon := reconciler.NewDeploymentReconciler(r.scheme, r.client)
	if err := deployRecon.CleanupOrphanedWorkloads(ctx, roles); err != nil {
		errs = append(errs, err)
	}

	stsRecon := reconciler.NewStatefulSetReconciler(r.scheme, r.client)
	if err := stsRecon.CleanupOrphanedWorkloads(ctx, roles); err != nil {
		errs = append(errs, err)
	}

	lwsRecon := reconciler.NewLeaderWorkerSetReconciler(r.scheme, r.client)
	if err := lwsRecon.CleanupOrphanedWorkloads(ctx, roles); err != nil {
		errs = append(errs, err)
	}

	instanceSetRecon := reconciler.NewInstanceSetReconciler(r.scheme, r.client)
	if err := instanceSetRecon.CleanupOrphanedWorkloads(ctx, roles); err != nil {
		errs = append(errs, err)
	}

	if err := r.CleanupOrphanedScalingAdapters(ctx, data); err != nil {
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
	ctx context.Context, roleData *reconciler.RoleData,
) error {
	logger := log.FromContext(ctx)
	roleName := roleData.Spec.Name
	ownerInfo := roleData.OwnerInfo
	roleScalingAdapterName := scale.GenerateScalingAdapterName(ownerInfo.Name, roleName)
	rbgScalingAdapter := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{}
	err := r.client.Get(
		ctx, types.NamespacedName{Name: roleScalingAdapterName, Namespace: ownerInfo.Namespace}, rbgScalingAdapter,
	)
	if err == nil {
		// scalingAdapter exists
		// clean scalingAdapter when user update rbg.spec.role.scalingAdapter.enable to false
		if !scale.IsScalingAdapterEnable(roleData.Spec) {
			logger.Info("delete scalingAdapter", "scalingAdapter", rbgScalingAdapter.Name)
			return r.client.Delete(ctx, rbgScalingAdapter)
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		// failed to check scaling adapter exists
		return err
	}
	if !scale.IsScalingAdapterEnable(roleData.Spec) {
		return nil
	}

	// scalingAdapter not found
	rbgScalingAdapter = &workloadsv1alpha1.RoleBasedGroupScalingAdapter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleScalingAdapterName,
			Namespace: ownerInfo.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         workloadsv1alpha1.GroupVersion.String(),
					Kind:               workloadsv1alpha1.RoleBasedGroupKind,
					Name:               ownerInfo.Name,
					UID:                ownerInfo.UID,
					BlockOwnerDeletion: ptr.To(true),
				},
			},
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: ownerInfo.Name,
				workloadsv1alpha1.SetRoleLabelKey: roleName,
			},
		},
		Spec: workloadsv1alpha1.RoleBasedGroupScalingAdapterSpec{
			ScaleTargetRef: &workloadsv1alpha1.AdapterScaleTargetRef{
				Name: ownerInfo.Name,
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
	ctx context.Context, data *reconciler.RBGReconcileData,
) error {
	logger := log.FromContext(ctx)
	metadata := data.Metadata()

	// list scalingAdapter managed by rbg
	scalingAdapterList := &workloadsv1alpha1.RoleBasedGroupScalingAdapterList{}
	if err := r.client.List(
		context.Background(), scalingAdapterList, client.InNamespace(metadata.Namespace),
		client.MatchingLabels(
			map[string]string{
				workloadsv1alpha1.SetNameLabelKey: metadata.Name,
			},
		),
	); err != nil {
		return err
	}

	for _, scalingAdapter := range scalingAdapterList.Items {
		// Check if controlled by this RBG
		isControlled := false
		for _, owner := range scalingAdapter.OwnerReferences {
			if owner.UID == metadata.UID {
				isControlled = true
				break
			}
		}
		if !isControlled {
			continue
		}

		scaleTargetRef := scalingAdapter.Spec.ScaleTargetRef
		if scaleTargetRef == nil || scaleTargetRef.Name != metadata.Name {
			continue
		}

		found := false
		for _, role := range data.GetRoles() {
			if role.Spec.Name == scaleTargetRef.Role {
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

func (r *RoleBasedGroupReconciler) CalculateRollingUpdateForAllCoordination(
	data *reconciler.RBGReconcileData) (map[string]workloadsv1alpha1.RollingUpdate, error) {
	coordinationRequirements := data.GetCoordinationRequirements()
	var strategyRollingUpdate map[string]workloadsv1alpha1.RollingUpdate
	for _, coordination := range coordinationRequirements {
		strategyByCoord, err := r.calculateRollingUpdateForCoordination(data, &coordination)
		if err != nil {
			return nil, err
		}
		strategyRollingUpdate = mergeStrategyRollingUpdate(strategyByCoord, strategyRollingUpdate)
	}
	return strategyRollingUpdate, nil
}

func (r *RoleBasedGroupReconciler) calculateRollingUpdateForCoordination(
	data *reconciler.RBGReconcileData,
	coordination *workloadsv1alpha1.Coordination,
) (map[string]workloadsv1alpha1.RollingUpdate, error) {
	roleStatuses := data.GetRoleStatuses()
	if coordination == nil ||
		coordination.Strategy == nil ||
		coordination.Strategy.RollingUpdate == nil ||
		len(coordination.Roles) <= 1 {
		return nil, nil
	}

	// The roles that are under controlled by this coordination.
	coordinationRoles := sets.New[string]()
	for _, roleName := range coordination.Roles {
		if replicas := data.GetRoleSpecs(roleName); replicas > 0 {
			coordinationRoles.Insert(roleName)
		}
	}

	// Initialize the maxUnavailable and partition.
	strategyRollingUpdate := make(map[string]workloadsv1alpha1.RollingUpdate, len(data.GetRoles()))
	for _, role := range data.GetRoles() {

		roleSpec := role.Spec
		if !coordinationRoles.Has(roleSpec.Name) {
			continue
		}
		strategy := workloadsv1alpha1.RollingUpdate{}
		if roleSpec.RolloutStrategy != nil && roleSpec.RolloutStrategy.RollingUpdate != nil {
			strategy = *(roleSpec.RolloutStrategy.RollingUpdate.DeepCopy())
		}
		if coordination.Strategy.RollingUpdate.MaxUnavailable != nil {
			strategy.MaxUnavailable = ptr.To(intstr.FromString(*coordination.Strategy.RollingUpdate.MaxUnavailable))
		}
		if coordination.Strategy.RollingUpdate.Partition != nil {
			partitionPercent := intstr.FromString(*coordination.Strategy.RollingUpdate.Partition)
			partition, err := utils.CalculatePartitionReplicas(&partitionPercent, roleSpec.Replicas)
			if err != nil {
				return nil, err
			}
			strategy.Partition = func(a int32) *intstr.IntOrString {
				return ptr.To(intstr.FromInt32(a))
			}(int32(partition))
		}
		strategyRollingUpdate[roleSpec.Name] = strategy
	}

	// Calculate the next rolling target based on maxSkew.
	nextRollingTarget := CalculateNextRollingTarget(data.GetRoles(), coordination, roleStatuses, coordinationRoles)

	// Calculate the partition based one the next rolling target.
	for _, role := range data.GetRoles() {
		roleSpec := role.Spec
		if !coordinationRoles.Has(roleSpec.Name) {
			continue
		}
		updatedTarget, ok := nextRollingTarget[roleSpec.Name]
		if !ok {
			continue
		}
		strategy := strategyRollingUpdate[roleSpec.Name]

		// finalPartition is the user-settings target partition that should be respected by any coordination.
		finalPartition := int32(0)
		if strategy.Partition != nil {
			finalPartitionInt, err := intstr.GetScaledValueFromIntOrPercent(strategy.Partition, int(*roleSpec.Replicas), true)
			if err != nil {
				return nil, err
			}
			finalPartition = int32(finalPartitionInt)
		}

		// stepPartition is the calculated partition based on the maxSkew.
		stepPartition := max(*roleSpec.Replicas-updatedTarget, 0)

		if stepPartition > finalPartition {
			strategy.Partition = ptr.To(intstr.FromInt32(stepPartition))
		} else {
			strategy.Partition = ptr.To(intstr.FromInt32(finalPartition))
		}
		strategyRollingUpdate[roleSpec.Name] = strategy
	}
	return strategyRollingUpdate, nil
}

func CalculateNextRollingTarget(roleDatas []*reconciler.RoleData,
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
	for _, role := range roleDatas {
		roleName := role.Spec.Name

		if !coordinationRoles.Has(role.Spec.Name) {
			continue
		}
		desiredReplicas[roleName] = *role.Spec.Replicas
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
