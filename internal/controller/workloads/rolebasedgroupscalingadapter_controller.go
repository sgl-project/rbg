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
	"fmt"
	"reflect"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	metaapplyv1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
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
	// Fetch the RoleBasedGroupScalingAdapter instance
	rbgScalingAdapter := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{}
	if err := r.client.Get(
		ctx, types.NamespacedName{Name: req.Name, Namespace: req.Namespace}, rbgScalingAdapter,
	); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	logger := log.FromContext(ctx).WithValues("rbg-scaling-adapter", klog.KObj(rbgScalingAdapter))
	ctx = ctrl.LoggerInto(ctx, logger)

	// TODO: this adapter's lifecycle is binding to RBG object to make it easy to management.
	if rbgScalingAdapter.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	logger.Info("Start reconciling")
	rbgScalingAdapterName := rbgScalingAdapter.Name
	rbgName := rbgScalingAdapter.Spec.ScaleTargetRef.Name
	targetRoleName := rbgScalingAdapter.Spec.ScaleTargetRef.Role

	// check scale target exist
	var (
		getTargetRoleErr error
		targetRole       *workloadsv1alpha1.RoleSpec
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
			rbgScalingAdapter, corev1.EventTypeNormal, FailedGetRBGRole,
			"Failed to get scale target role: %v", getTargetRoleErr,
		)
		if rbgScalingAdapter.Status.Phase != workloadsv1alpha1.AdapterPhaseNotBound {
			rbgScalingAdapterApplyConfig := utils.RoleBasedGroupScalingAdapter(rbgScalingAdapter).
				WithStatus(utils.RbgScalingAdapterStatus(rbgScalingAdapter.Status).WithPhase(workloadsv1alpha1.AdapterPhaseNotBound))
			if err := utils.PatchObjectApplyConfiguration(
				ctx, r.client, rbgScalingAdapterApplyConfig, utils.PatchStatus,
			); err != nil {
				logger.Error(err, "Failed to update status for %s", rbgScalingAdapterName)
			}
		}
		// TODO: currently reconcile unbound adapter by a default reconcile interval, need to implement a rbg event-driven manager
		return ctrl.Result{RequeueAfter: 10}, nil
	}

	// add owner reference
	if !rbgScalingAdapter.ContainsRBGOwner(rbg) {
		if err := r.UpdateAdapterOwnerReference(ctx, rbgScalingAdapter, rbg); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 1}, nil
	}

	// check scale target exist succeed, init adapter status with phase bound, selector and initial replicas
	if rbgScalingAdapter.Status.Phase != workloadsv1alpha1.AdapterPhaseBound {
		rbgScalingAdapterSpecApplyConfig := utils.RoleBasedGroupScalingAdapter(rbgScalingAdapter).
			WithSpec(utils.RbgScalingAdapterSpec(rbgScalingAdapter.Spec).WithReplicas(targetRole.Replicas))

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

		rbgScalingAdapterStatusApplyConfig := utils.RoleBasedGroupScalingAdapter(rbgScalingAdapter).
			WithStatus(
				utils.RbgScalingAdapterStatus(rbgScalingAdapter.Status).
					WithReplicas(targetRole.Replicas, false).
					WithPhase(workloadsv1alpha1.AdapterPhaseBound).WithSelector(selector),
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
		return ctrl.Result{RequeueAfter: 1}, nil
	}

	desiredReplicas, currentReplicas := rbgScalingAdapter.Spec.Replicas, targetRole.Replicas
	if desiredReplicas == nil || currentReplicas == nil ||
		*rbgScalingAdapter.Spec.Replicas == *targetRole.Replicas {
		// nothing to do
		return ctrl.Result{}, nil
	}

	logger.Info("Start scaling", "desired replicas", *desiredReplicas, "current replicas", *currentReplicas)

	// scale role
	if err := r.updateRoleReplicas(ctx, rbg, targetRoleName, desiredReplicas); err != nil {
		r.recorder.Eventf(
			rbgScalingAdapter, corev1.EventTypeNormal, FailedScale,
			"Failed to scale target role [%s] of rbg [%s] from %v to %v replicas: %v",
			targetRoleName, rbgName, *currentReplicas, *desiredReplicas, err,
		)
		return ctrl.Result{}, err
	}
	rbgScalingAdapterApplyConfig := utils.RoleBasedGroupScalingAdapter(rbgScalingAdapter).
		WithStatus(utils.RbgScalingAdapterStatus(rbgScalingAdapter.Status).WithReplicas(desiredReplicas, true))
	if err := utils.PatchObjectApplyConfiguration(
		ctx, r.client, rbgScalingAdapterApplyConfig, utils.PatchStatus,
	); err != nil {
		logger.Error(err, "Failed to update status for %s", rbgScalingAdapterName)
		return ctrl.Result{}, err
	}

	logger.Info("Scale successfully", "old replicas", *desiredReplicas, "new replicas", *currentReplicas)
	r.recorder.Eventf(
		rbgScalingAdapter, corev1.EventTypeNormal, SuccessfulScale,
		"Succeed to scale target role [%s] of rbg [%s] from %v to %v replicas",
		targetRoleName, rbgName, *currentReplicas, *desiredReplicas,
	)

	return ctrl.Result{}, nil
}

func (r *RoleBasedGroupScalingAdapterReconciler) UpdateAdapterOwnerReference(
	ctx context.Context,
	rbgScalingAdapter *workloadsv1alpha1.RoleBasedGroupScalingAdapter,
	rbg *workloadsv1alpha1.RoleBasedGroup,
) error {
	rbgScalingAdapterApplyConfig := utils.RoleBasedGroupScalingAdapter(rbgScalingAdapter).WithOwnerReferences(
		metaapplyv1.OwnerReference().
			WithAPIVersion(rbg.APIVersion).
			WithKind(rbg.Kind).
			WithName(rbg.Name).
			WithUID(rbg.GetUID()).
			WithBlockOwnerDeletion(true),
	)
	return utils.PatchObjectApplyConfiguration(ctx, r.client, rbgScalingAdapterApplyConfig, utils.PatchSpec)
}

// SetupWithManager sets up the controller with the Manager.
func (r *RoleBasedGroupScalingAdapterReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&workloadsv1alpha1.RoleBasedGroupScalingAdapter{}, builder.WithPredicates(RBGScalingAdapterPredicate())).
		Named("workloads-rolebasedgroup-scalingadapter").
		Complete(r)
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
			_, ok := e.Object.(*workloadsv1alpha1.RoleBasedGroupScalingAdapter)
			if ok {
				ctrl.Log.Info("enqueue: rbg scalingAdapter create event", "rbg", klog.KObj(e.Object))
				return true
			}
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRbg, ok1 := e.ObjectOld.(*workloadsv1alpha1.RoleBasedGroupScalingAdapter)
			newRbg, ok2 := e.ObjectNew.(*workloadsv1alpha1.RoleBasedGroupScalingAdapter)
			if ok1 && ok2 {
				if !reflect.DeepEqual(oldRbg.Spec, newRbg.Spec) {
					ctrl.Log.Info("enqueue: rbg scalingAdapter update event", "rbg", klog.KObj(e.ObjectOld))
					return true
				}
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			_, ok := e.Object.(*workloadsv1alpha1.RoleBasedGroupScalingAdapter)
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

func (r *RoleBasedGroupScalingAdapterReconciler) GetTargetRbgFromAdapter(
	ctx context.Context, rbgScalingAdapter *workloadsv1alpha1.RoleBasedGroupScalingAdapter,
) (*workloadsv1alpha1.RoleBasedGroup, error) {
	name := rbgScalingAdapter.Spec.ScaleTargetRef.Name
	namespace := rbgScalingAdapter.Namespace

	rbg := &workloadsv1alpha1.RoleBasedGroup{}
	if err := r.client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, rbg); err != nil {
		return nil, err
	}
	return rbg, nil
}

func (r *RoleBasedGroupScalingAdapterReconciler) updateRoleReplicas(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, targetRoleName string, newReplicas *int32,
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

// extractLabelSelectorDefault extracts a LabelSelector string from the given role object.
func (r *RoleBasedGroupScalingAdapterReconciler) extractLabelSelectorDefault(
	rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
) (string, error) {
	apiVersion, kind := role.Workload.APIVersion, role.Workload.Kind
	if kind == "LeaderWorkerSet" {
		// For lws role, we extract leader statefulset selector
		apiVersion, kind = "apps/v1", "StatefulSet"
	}

	targetGV, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return "", err
	}

	gvk := schema.GroupVersionKind{
		Group:   targetGV.Group,
		Version: targetGV.Version,
		Kind:    kind,
	}
	roleObj := &unstructured.Unstructured{}
	roleObj.SetGroupVersionKind(gvk)
	roleObj.SetNamespace(rbg.Namespace)
	roleObj.SetName(rbg.GetWorkloadName(role))

	if err := r.client.Get(
		context.TODO(),
		client.ObjectKey{Namespace: rbg.Namespace, Name: rbg.GetWorkloadName(role)}, roleObj,
	); err != nil {
		return "", err
	}
	// Retrieve the selector string from the Scale object's 'spec' field.
	selectorMap, found, err := unstructured.NestedMap(roleObj.Object, "spec", "selector")
	if err != nil {
		return "", fmt.Errorf("failed to get 'spec.selector' from scale: %v", err)
	}
	if !found {
		return "", fmt.Errorf("the 'spec.selector' field was not found in the scale object")
	}

	selector := &metav1.LabelSelector{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(selectorMap, selector)
	if err != nil {
		return "", fmt.Errorf("failed to convert 'spec.selector' to LabelSelector: %v", err)
	}
	pairs := make([]string, 0, len(selector.MatchLabels))
	for k, v := range selector.MatchLabels {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, v))
	}

	return strings.Join(pairs, ","), nil
}
