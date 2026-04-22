/*
Copyright 2025 The RBG Authors.

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
	"fmt"
	"reflect"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	metaapplyv1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	applyconfiguration "sigs.k8s.io/rbgs/client-go/applyconfiguration/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scale"
	"sigs.k8s.io/rbgs/pkg/utils"
)

// RoleBasedGroupScalingAdapterReconciler reconciles a RoleBasedGroupScalingAdapter object
type RoleBasedGroupScalingAdapterReconciler struct {
	client    client.Client
	apiReader client.Reader
	scheme    *runtime.Scheme
	recorder  record.EventRecorder
}

func NewRoleBasedGroupScalingAdapterReconciler(mgr ctrl.Manager) *RoleBasedGroupScalingAdapterReconciler {
	return &RoleBasedGroupScalingAdapterReconciler{
		client:    mgr.GetClient(),
		apiReader: mgr.GetAPIReader(),
		scheme:    mgr.GetScheme(),
		recorder:  mgr.GetEventRecorderFor("RoleBasedGroupScalingAdapter"),
	}
}

// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupscalingadapters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupscalingadapters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupscalingadapters/finalizers,verbs=update
func (r *RoleBasedGroupScalingAdapterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the RoleBasedGroupScalingAdapter instance.
	// DeepCopy to avoid mutating the informer cache when modifying conditions.
	cached := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
	if err := r.client.Get(
		ctx, types.NamespacedName{Name: req.Name, Namespace: req.Namespace}, cached,
	); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	rbgScalingAdapter := cached.DeepCopy()
	logger := log.FromContext(ctx).WithValues("rbg-scaling-adapter", klog.KObj(rbgScalingAdapter))
	ctx = ctrl.LoggerInto(ctx, logger)

	// TODO: this adapter's lifecycle is binding to RBG object to make it easy to management.
	if rbgScalingAdapter.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	if rbgScalingAdapter.Spec.ScaleTargetRef == nil {
		return ctrl.Result{}, errors.New("RoleBasedGroupScalingAdapter.Spec.ScaleTargetRef is nil")
	}

	logger.Info("Start reconciling")
	rbgScalingAdapterName := rbgScalingAdapter.Name
	rbgName := rbgScalingAdapter.Spec.ScaleTargetRef.Name
	targetRoleName := rbgScalingAdapter.Spec.ScaleTargetRef.Role

	// check scale target exist
	var (
		getTargetRoleErr error
		targetRole       *workloadsv1alpha2.RoleSpec
	)
	rbg, err := r.GetTargetRbgFromAdapter(ctx, rbgScalingAdapter)
	if err != nil {
		getTargetRoleErr = errors.Wrapf(err, "Failed to get rbg %s:", rbgName)
	} else {
		targetRole, err = rbg.GetRole(targetRoleName)
		if err != nil {
			getTargetRoleErr = errors.Wrapf(err, "Failed to get role %s in rbg %s:", targetRoleName, rbgName)
		}
	}

	if !scale.IsScalingAdapterManagedByRBG(rbgScalingAdapter, rbg) {
		logger.Info(
			"Skip to reconcile the scaling adapter which is not managed by RBG-controller", "rbgScalingAdapterName",
			rbgScalingAdapter.Name,
		)
		return ctrl.Result{}, nil
	}

	// check scale target exist failed, update phase to unbound
	if getTargetRoleErr != nil {
		r.recorder.Eventf(
			rbgScalingAdapter, corev1.EventTypeWarning, FailedGetRBGRole,
			"Failed to get scale target role: %v", getTargetRoleErr,
		)
		if rbgScalingAdapter.Status.Phase != constants.AdapterPhaseNotBound {
			rbgScalingAdapterApplyConfig := ToRoleBasedGroupScalingAdapterApplyConfiguration(rbgScalingAdapter).
				WithStatus(ToRoleBasedGroupScalingAdapterStatusApplyConfiguration(rbgScalingAdapter.Status, false).WithPhase(constants.AdapterPhaseNotBound))
			if err := utils.PatchObjectApplyConfiguration(
				ctx, r.client, rbgScalingAdapterApplyConfig, utils.PatchStatus,
			); err != nil {
				logger.Error(err, "Failed to update status", "rbgScalingAdapterName", rbgScalingAdapterName)
			}
		}
		// The Watches() on RBG enables event-driven reconciliation for bound adapters.
		// This RequeueAfter is retained for unbound adapters whose target RBG does not
		// yet exist (no watch events generated for non-existent objects).
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// add owner reference
	if !rbgScalingAdapter.ContainsRBGOwner(rbg) {
		if err := r.UpdateAdapterOwnerReference(ctx, rbgScalingAdapter, rbg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}

	// check scale target exist succeed, init adapter status with phase bound, selector and initial replicas
	if rbgScalingAdapter.Status.Phase != constants.AdapterPhaseBound {
		spec := ToRoleBasedGroupScalingAdapterSpecApplyConfiguration(rbgScalingAdapter.Spec)
		if targetRole.Replicas != nil {
			spec = spec.WithReplicas(*targetRole.Replicas)
		}
		rbgScalingAdapterSpecApplyConfig := ToRoleBasedGroupScalingAdapterApplyConfiguration(rbgScalingAdapter).
			WithSpec(spec)

		if err := utils.PatchObjectApplyConfiguration(
			ctx, r.client, rbgScalingAdapterSpecApplyConfig, utils.PatchSpec,
		); err != nil {
			logger.Error(err, "Failed to init spec.replicas", "rbgScalingAdapterName", rbgScalingAdapterName)
			return ctrl.Result{}, err
		}

		selector, err := r.extractLabelSelectorDefault(rbg, targetRole)
		if err != nil {
			return ctrl.Result{}, err
		}

		status := ToRoleBasedGroupScalingAdapterStatusApplyConfiguration(rbgScalingAdapter.Status, false)
		if targetRole.Replicas != nil {
			status = status.WithReplicas(*targetRole.Replicas)
		}
		roleStatus, found := rbg.GetRoleStatus(targetRoleName)
		if found {
			status = status.WithReadyReplicas(roleStatus.ReadyReplicas)
		}
		rbgScalingAdapterStatusApplyConfig := ToRoleBasedGroupScalingAdapterApplyConfiguration(rbgScalingAdapter).
			WithStatus(
				status.WithPhase(constants.AdapterPhaseBound).WithSelector(selector),
			)

		if err := utils.PatchObjectApplyConfiguration(
			ctx, r.client, rbgScalingAdapterStatusApplyConfig, utils.PatchStatus,
		); err != nil {
			logger.Error(err, "Failed to update status", "rbgScalingAdapterName", rbgScalingAdapterName)
			return ctrl.Result{}, err
		}
		r.recorder.Eventf(
			rbgScalingAdapter, corev1.EventTypeNormal, SuccessfulBound,
			"Succeed to find scale target role [%s] of rbg [%s]", targetRoleName, rbgName,
		)
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}

	// Sync readyReplicas from RBG role status
	roleStatus, found := rbg.GetRoleStatus(targetRoleName)
	if found {
		if rbgScalingAdapter.Status.ReadyReplicas == nil || *rbgScalingAdapter.Status.ReadyReplicas != roleStatus.ReadyReplicas {
			rbgScalingAdapterApplyConfig := ToRoleBasedGroupScalingAdapterApplyConfiguration(rbgScalingAdapter).
				WithStatus(
					ToRoleBasedGroupScalingAdapterStatusApplyConfiguration(rbgScalingAdapter.Status, false).
						WithReadyReplicas(roleStatus.ReadyReplicas),
				)
			if err := utils.PatchObjectApplyConfiguration(ctx, r.client, rbgScalingAdapterApplyConfig, utils.PatchStatus); err != nil {
				logger.Error(err, "Failed to update readyReplicas")
				return ctrl.Result{}, err
			}
			rbgScalingAdapter.Status.ReadyReplicas = ptr.To(roleStatus.ReadyReplicas)
		}
	}

	desiredReplicas, currentReplicas := rbgScalingAdapter.Spec.Replicas, targetRole.Replicas
	if desiredReplicas == nil || currentReplicas == nil ||
		*rbgScalingAdapter.Spec.Replicas == *targetRole.Replicas {
		if err := r.clearScaleDownDeferredCondition(ctx, rbgScalingAdapter); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Gate scale-down for partition-based workloads during rolling update.
	// StatefulSet/LWS delete pods from highest ordinals on scale-down, which are
	// the already-updated pods (ordinal >= partition). Deferring scale-down until
	// the rollout completes prevents rollout progress destruction.
	if deferred, result, err := r.deferScaleDownIfNeeded(
		ctx, rbgScalingAdapter, rbg, targetRole, targetRoleName,
		*desiredReplicas, *currentReplicas,
	); deferred || err != nil {
		return result, err
	}

	// Clear condition in memory; the SSA status patch below persists the change
	// in a single API call together with the replica/lastScaleTime update.
	apimeta.RemoveStatusCondition(&rbgScalingAdapter.Status.Conditions,
		workloadsv1alpha2.AdapterConditionScaleDownDeferred)

	return r.scaleRole(ctx, rbgScalingAdapter, rbg, targetRoleName, *desiredReplicas, *currentReplicas)
}

// patchAdapterStatus persists the RBGSA status using SSA, consistent with all
// other status writes in this controller.
func (r *RoleBasedGroupScalingAdapterReconciler) patchAdapterStatus(
	ctx context.Context, rbgScalingAdapter *workloadsv1alpha2.RoleBasedGroupScalingAdapter,
) error {
	applyConfig := ToRoleBasedGroupScalingAdapterApplyConfiguration(rbgScalingAdapter).
		WithStatus(ToRoleBasedGroupScalingAdapterStatusApplyConfiguration(rbgScalingAdapter.Status, false))
	return utils.PatchObjectApplyConfiguration(ctx, r.client, applyConfig, utils.PatchStatus)
}

// shouldDeferScaleDown returns true if the role's ScaleDownPolicy is
// explicitly set to DeferDuringRollout. When unset, defaults to Unrestricted
// (same behavior as before this feature was introduced).
func shouldDeferScaleDown(role *workloadsv1alpha2.RoleSpec) bool {
	if role.ScalingAdapter == nil || role.ScalingAdapter.ScaleDownPolicy == nil {
		return false // default: unrestricted (backward compatible)
	}
	return *role.ScalingAdapter.ScaleDownPolicy == workloadsv1alpha2.ScaleDownPolicyDeferDuringRollout
}

// clearScaleDownDeferredCondition removes the ScaleDownDeferred condition if present
// and persists the change via SSA status patch.
func (r *RoleBasedGroupScalingAdapterReconciler) clearScaleDownDeferredCondition(
	ctx context.Context, rbgScalingAdapter *workloadsv1alpha2.RoleBasedGroupScalingAdapter,
) error {
	if apimeta.FindStatusCondition(rbgScalingAdapter.Status.Conditions,
		workloadsv1alpha2.AdapterConditionScaleDownDeferred) == nil {
		return nil
	}
	apimeta.RemoveStatusCondition(&rbgScalingAdapter.Status.Conditions,
		workloadsv1alpha2.AdapterConditionScaleDownDeferred)
	return r.patchAdapterStatus(ctx, rbgScalingAdapter)
}

// deferScaleDownIfNeeded checks whether a scale-down should be deferred because
// a partition-based rolling update is in progress. Returns (true, result, nil)
// if the scale-down was deferred, (false, {}, nil) if it should proceed.
func (r *RoleBasedGroupScalingAdapterReconciler) deferScaleDownIfNeeded(
	ctx context.Context,
	rbgScalingAdapter *workloadsv1alpha2.RoleBasedGroupScalingAdapter,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	targetRole *workloadsv1alpha2.RoleSpec,
	targetRoleName string,
	desiredReplicas, currentReplicas int32,
) (bool, ctrl.Result, error) {
	if desiredReplicas >= currentReplicas ||
		!workloadsv1alpha2.IsStatefulRole(targetRole) ||
		!shouldDeferScaleDown(targetRole) {
		return false, ctrl.Result{}, nil
	}

	roleStatus, found := rbg.GetRoleStatus(targetRoleName)
	if !found || roleStatus.UpdatedReplicas >= roleStatus.Replicas {
		return false, ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx)
	msg := fmt.Sprintf(
		"Scale-down to %d replicas deferred (current: %d): partition-based rolling update in progress for role %s (updated %d/%d)",
		desiredReplicas, currentReplicas, targetRoleName,
		roleStatus.UpdatedReplicas, roleStatus.Replicas)

	logger.Info("Deferring scale-down during partition-based rollout",
		"desired", desiredReplicas, "current", currentReplicas,
		"updatedReplicas", roleStatus.UpdatedReplicas, "replicas", roleStatus.Replicas)

	// Only emit the event on first deferral, not on every requeue.
	isFirstDeferral := apimeta.FindStatusCondition(rbgScalingAdapter.Status.Conditions,
		workloadsv1alpha2.AdapterConditionScaleDownDeferred) == nil

	apimeta.SetStatusCondition(&rbgScalingAdapter.Status.Conditions, metav1.Condition{
		Type:               workloadsv1alpha2.AdapterConditionScaleDownDeferred,
		Status:             metav1.ConditionTrue,
		Reason:             "RollingUpdateInProgress",
		ObservedGeneration: rbgScalingAdapter.Generation,
		Message:            msg,
	})
	if err := r.patchAdapterStatus(ctx, rbgScalingAdapter); err != nil {
		return true, ctrl.Result{}, err
	}

	if isFirstDeferral {
		r.recorder.Eventf(rbgScalingAdapter, corev1.EventTypeWarning, ScaleDownDeferred, msg)
	}
	return true, ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// scaleRole performs the actual replica update on the RBG and patches the RBGSA status.
func (r *RoleBasedGroupScalingAdapterReconciler) scaleRole(
	ctx context.Context,
	rbgScalingAdapter *workloadsv1alpha2.RoleBasedGroupScalingAdapter,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	targetRoleName string,
	desiredReplicas, currentReplicas int32,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	rbgName := rbg.Name

	logger.Info("Start scaling", "desired replicas", desiredReplicas, "current replicas", currentReplicas)

	if err := r.updateRoleReplicas(ctx, rbg, targetRoleName, &desiredReplicas); err != nil {
		r.recorder.Eventf(
			rbgScalingAdapter, corev1.EventTypeWarning, FailedScale,
			"Failed to scale target role [%s] of rbg [%s] from %v to %v replicas: %v",
			targetRoleName, rbgName, currentReplicas, desiredReplicas, err,
		)
		return ctrl.Result{}, err
	}
	rbgScalingAdapterApplyConfig := ToRoleBasedGroupScalingAdapterApplyConfiguration(rbgScalingAdapter).
		WithStatus(
			ToRoleBasedGroupScalingAdapterStatusApplyConfiguration(rbgScalingAdapter.Status, true).WithReplicas(desiredReplicas),
		)
	if err := utils.PatchObjectApplyConfiguration(
		ctx, r.client, rbgScalingAdapterApplyConfig, utils.PatchStatus,
	); err != nil {
		logger.Error(err, "Failed to update status for %s", rbgScalingAdapter.Name)
		return ctrl.Result{}, err
	}

	logger.Info("Scale successfully", "old replicas", currentReplicas, "new replicas", desiredReplicas)
	r.recorder.Eventf(
		rbgScalingAdapter, corev1.EventTypeNormal, SuccessfulScale,
		"Succeed to scale target role [%s] of rbg [%s] from %v to %v replicas",
		targetRoleName, rbgName, currentReplicas, desiredReplicas,
	)

	return ctrl.Result{}, nil
}

func (r *RoleBasedGroupScalingAdapterReconciler) UpdateAdapterOwnerReference(
	ctx context.Context,
	rbgScalingAdapter *workloadsv1alpha2.RoleBasedGroupScalingAdapter,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) error {
	rbgGKV := utils.GetRbgGVK()
	rbgScalingAdapterApplyConfig := ToRoleBasedGroupScalingAdapterApplyConfiguration(rbgScalingAdapter).WithOwnerReferences(
		metaapplyv1.OwnerReference().
			WithAPIVersion(rbgGKV.GroupVersion().String()).
			WithKind(rbgGKV.Kind).
			WithName(rbg.Name).
			WithUID(rbg.GetUID()).
			WithBlockOwnerDeletion(true),
	)
	return utils.PatchObjectApplyConfiguration(ctx, r.client, rbgScalingAdapterApplyConfig, utils.PatchSpec)
}

func ToRoleBasedGroupScalingAdapterApplyConfiguration(rbgScalingAdapter *workloadsv1alpha2.RoleBasedGroupScalingAdapter) *applyconfiguration.RoleBasedGroupScalingAdapterApplyConfiguration {
	if rbgScalingAdapter == nil {
		return nil
	}
	gkv := utils.GetRbgScalingAdapterGVK()
	rbgScalingAdapterApplyConfig := applyconfiguration.RoleBasedGroupScalingAdapter(rbgScalingAdapter.Name, rbgScalingAdapter.Namespace).
		WithKind(gkv.Kind).
		WithAPIVersion(gkv.GroupVersion().String()).
		WithSpec(ToRoleBasedGroupScalingAdapterSpecApplyConfiguration(rbgScalingAdapter.Spec))
	return rbgScalingAdapterApplyConfig
}

func ToRoleBasedGroupScalingAdapterSpecApplyConfiguration(spec workloadsv1alpha2.RoleBasedGroupScalingAdapterSpec) *applyconfiguration.RoleBasedGroupScalingAdapterSpecApplyConfiguration {
	specApplyConfig := applyconfiguration.RoleBasedGroupScalingAdapterSpec().
		WithScaleTargetRef(
			applyconfiguration.AdapterScaleTargetRef().
				WithName(spec.ScaleTargetRef.Name).
				WithRole(spec.ScaleTargetRef.Role),
		)
	if spec.Replicas != nil {
		specApplyConfig = specApplyConfig.WithReplicas(*spec.Replicas)
	}
	return specApplyConfig
}

func ToRoleBasedGroupScalingAdapterStatusApplyConfiguration(status workloadsv1alpha2.RoleBasedGroupScalingAdapterStatus, scale bool) *applyconfiguration.RoleBasedGroupScalingAdapterStatusApplyConfiguration {
	statusApplyConfig := applyconfiguration.RoleBasedGroupScalingAdapterStatus().
		WithPhase(status.Phase).
		WithSelector(status.Selector)
	if status.Replicas != nil {
		statusApplyConfig = statusApplyConfig.WithReplicas(*status.Replicas)
	}
	if status.ReadyReplicas != nil {
		statusApplyConfig = statusApplyConfig.WithReadyReplicas(*status.ReadyReplicas)
	}
	if status.LastScaleTime != nil {
		statusApplyConfig = statusApplyConfig.WithLastScaleTime(*status.LastScaleTime)
	}
	if scale {
		statusApplyConfig = statusApplyConfig.WithLastScaleTime(metav1.Now())
	}
	// Always include conditions so SSA clears conditions that were previously set.
	statusApplyConfig = statusApplyConfig.WithConditions(
		ToConditionApplyConfigurations(status.Conditions)...)
	return statusApplyConfig
}

// SetupWithManager sets up the controller with the Manager.
func (r *RoleBasedGroupScalingAdapterReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&workloadsv1alpha2.RoleBasedGroupScalingAdapter{}, builder.WithPredicates(RBGScalingAdapterPredicate())).
		Watches(
			&workloadsv1alpha2.RoleBasedGroup{},
			handler.EnqueueRequestsFromMapFunc(r.mapRBGToScalingAdapters),
			builder.WithPredicates(RBGRoleStatusPredicate()),
		).
		Named("workloads-rolebasedgroup-scalingadapter").
		Complete(r)
}

func (r *RoleBasedGroupScalingAdapterReconciler) mapRBGToScalingAdapters(ctx context.Context, obj client.Object) []reconcile.Request {
	rbg, ok := obj.(*workloadsv1alpha2.RoleBasedGroup)
	if !ok {
		return nil
	}

	adapterList := &workloadsv1alpha2.RoleBasedGroupScalingAdapterList{}
	if err := r.client.List(ctx, adapterList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name},
	); err != nil {
		log.FromContext(ctx).Error(err, "failed to list scaling adapters for RBG", "rbg", klog.KObj(rbg))
		return nil
	}

	requests := make([]reconcile.Request, 0, len(adapterList.Items))
	for _, adapter := range adapterList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      adapter.Name,
				Namespace: adapter.Namespace,
			},
		})
	}
	return requests
}

// CheckCrdExists checks if the specified Custom Resource Definition (CRD) exists in the Kubernetes cluster.
func (r *RoleBasedGroupScalingAdapterReconciler) CheckCrdExists() error {
	crds := []string{
		"rolebasedgroupscalingadapters.workloads.x-k8s.io",
	}

	for _, crd := range crds {
		if err := utils.CheckCrdExists(r.apiReader, crd); err != nil {
			return err
		}
	}
	return nil
}

func RBGScalingAdapterPredicate() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			_, ok := e.Object.(*workloadsv1alpha2.RoleBasedGroupScalingAdapter)
			if ok {
				ctrl.Log.Info("enqueue: rbg scalingAdapter create event", "rbg", klog.KObj(e.Object))
				return true
			}
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRbg, ok1 := e.ObjectOld.(*workloadsv1alpha2.RoleBasedGroupScalingAdapter)
			newRbg, ok2 := e.ObjectNew.(*workloadsv1alpha2.RoleBasedGroupScalingAdapter)
			if ok1 && ok2 {
				if !reflect.DeepEqual(oldRbg.Spec, newRbg.Spec) {
					ctrl.Log.Info("enqueue: rbg scalingAdapter update event", "rbg", klog.KObj(e.ObjectOld))
					return true
				}
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			_, ok := e.Object.(*workloadsv1alpha2.RoleBasedGroupScalingAdapter)
			if ok {
				ctrl.Log.Info("enqueue: rbg scalingAdapter delete event", "rbg", klog.KObj(e.Object))
				return true
			}
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}
}

func RBGRoleStatusPredicate() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRBG, ok1 := e.ObjectOld.(*workloadsv1alpha2.RoleBasedGroup)
			newRBG, ok2 := e.ObjectNew.(*workloadsv1alpha2.RoleBasedGroup)
			if ok1 && ok2 {
				return !reflect.DeepEqual(oldRBG.Status.RoleStatuses, newRBG.Status.RoleStatuses)
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}
}

func (r *RoleBasedGroupScalingAdapterReconciler) GetTargetRbgFromAdapter(
	ctx context.Context, rbgScalingAdapter *workloadsv1alpha2.RoleBasedGroupScalingAdapter,
) (*workloadsv1alpha2.RoleBasedGroup, error) {
	name := rbgScalingAdapter.Spec.ScaleTargetRef.Name
	namespace := rbgScalingAdapter.Namespace

	rbg := &workloadsv1alpha2.RoleBasedGroup{}
	if err := r.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, rbg); err != nil {
		return nil, err
	}
	return rbg, nil
}

func (r *RoleBasedGroupScalingAdapterReconciler) updateRoleReplicas(
	ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup, targetRoleName string, newReplicas *int32,
) error {
	return retry.RetryOnConflict(
		retry.DefaultBackoff, func() error {
			for index, role := range rbg.Spec.Roles {
				if role.Name == targetRoleName {
					role.Replicas = newReplicas
					rbg.Spec.Roles[index] = role
					break
				}
			}
			if err := r.client.Update(ctx, rbg); err != nil {
				if apierrors.IsConflict(err) {
					if err := r.client.Get(
						ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, rbg,
					); err != nil {
						return err
					}
				}
				return err
			}
			return nil
		},
	)
}

// extractLabelSelectorDefault extracts a LabelSelector string from the given role's scale subresource.
func (r *RoleBasedGroupScalingAdapterReconciler) extractLabelSelectorDefault(
	rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec,
) (string, error) {
	apiVersion, kind := role.Workload.APIVersion, role.Workload.Kind

	targetGV, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return "", err
	}

	gvk := schema.GroupVersionKind{
		Group:   targetGV.Group,
		Version: targetGV.Version,
		Kind:    kind,
	}

	// Get the scale subresource
	scaleObj := &unstructured.Unstructured{}
	scaleObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    kind,
	})
	scaleObj.SetNamespace(rbg.Namespace)
	scaleObj.SetName(rbg.GetWorkloadName(role))

	if err := r.client.Get(
		context.TODO(),
		client.ObjectKey{Namespace: rbg.Namespace, Name: rbg.GetWorkloadName(role)}, scaleObj,
	); err != nil {
		return "", fmt.Errorf("failed to get workload: %v", err)
	}

	// Try to get selector from status
	// For LeaderWorkerSet: use status.hpaPodSelector
	// For InstanceSet/StatefulSet/Deployment: use status.labelSelector
	selectorField := "labelSelector"
	if kind == "LeaderWorkerSet" {
		selectorField = "hpaPodSelector"
	}
	selectorStr, _, err := unstructured.NestedString(scaleObj.Object, "status", selectorField)
	if err != nil {
		return "", fmt.Errorf("failed to get selectore field in status: %v", err)
	}

	if kind == "RoleInstanceSet" && role.IsLeaderWorkerPattern() {
		selectorStr += fmt.Sprintf(",%s=0", constants.ComponentIndexLabelKey)
	}

	return selectorStr, nil
}
