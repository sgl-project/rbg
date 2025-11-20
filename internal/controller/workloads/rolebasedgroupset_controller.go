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
	"sort"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
)

// RoleBasedGroupSetReconciler reconciles a RoleBasedGroupSet object
type RoleBasedGroupSetReconciler struct {
	client    client.Client
	apiReader client.Reader
	scheme    *runtime.Scheme
	recorder  record.EventRecorder
}

func NewRoleBasedGroupSetReconciler(mgr ctrl.Manager) *RoleBasedGroupSetReconciler {
	return &RoleBasedGroupSetReconciler{
		client:    mgr.GetClient(),
		apiReader: mgr.GetAPIReader(),
		scheme:    mgr.GetScheme(),
		recorder:  mgr.GetEventRecorderFor("rbgset-controller"),
	}
}

// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupsets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupsets/finalizers,verbs=update

// Reconcile is the main reconciliation logic for RoleBasedGroupSet
func (r *RoleBasedGroupSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("rbgset", req.NamespacedName)
	ctx = ctrl.LoggerInto(ctx, logger)
	logger.Info("Start to reconcile rbgset")

	// 1. Fetch the RoleBasedGroupSet instance.
	rbgset := &workloadsv1alpha1.RoleBasedGroupSet{}
	if err := r.client.Get(ctx, req.NamespacedName, rbgset); err != nil {
		// Ignore not-found errors, which can happen after an object has been deleted.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !rbgset.DeletionTimestamp.IsZero() {
		logger.Info("rbgset is deleting, skip reconcile")
		return ctrl.Result{}, nil
	}

	// 2. List all child RoleBasedGroup instances currently associated with this RoleBasedGroupSet.
	var rbglist workloadsv1alpha1.RoleBasedGroupList
	selector, _ := labels.Parse(fmt.Sprintf("%s=%s", workloadsv1alpha1.SetRBGSetNameLabelKey, rbgset.Name))
	if err := r.client.List(
		ctx, &rbglist, client.InNamespace(rbgset.Namespace), client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		logger.Error(err, "Failed to list child RoleBasedGroups")
		return ctrl.Result{}, err
	}

	// 3. Calculate the difference between the desired state and the current state to determine which RBGs to create or delete.
	// Map existing RBGs by their index label for efficient lookup.
	existingRBGs := make(map[int]*workloadsv1alpha1.RoleBasedGroup)
	var rbgsToDelete []*workloadsv1alpha1.RoleBasedGroup
	for i := range rbglist.Items {
		rbg := &rbglist.Items[i]
		indexStr, ok := rbg.Labels[workloadsv1alpha1.SetRBGIndexLabelKey]
		if !ok {
			logger.Info("Found RoleBasedGroup with missing index label, marking for deletion", "rbgName", rbg.Name)
			rbgsToDelete = append(rbgsToDelete, rbg)
			continue
		}
		index, err := strconv.Atoi(indexStr)
		if err != nil {
			logger.Error(
				err, "Failed to parse index label for RoleBasedGroup, marking for deletion", "rbgName", rbg.Name,
			)
			rbgsToDelete = append(rbgsToDelete, rbg)
			continue
		}
		existingRBGs[index] = rbg
	}

	desiredReplicas := int(*rbgset.Spec.Replicas)
	var rbgsToCreate []*workloadsv1alpha1.RoleBasedGroup

	// Determine which RBGs need to be created.
	for i := 0; i < desiredReplicas; i++ {
		if _, exists := existingRBGs[i]; !exists {
			rbg := newRBGForSet(rbgset, i)
			rbgsToCreate = append(rbgsToCreate, rbg)
		}
	}

	// Determine which RBGs need to be deleted (e.g., index is out of bounds of desired replicas).
	for index, rbg := range existingRBGs {
		if index >= desiredReplicas {
			rbgsToDelete = append(rbgsToDelete, rbg)
		}
	}

	// 4. Perform operations in optimized order.
	// First, scale down to avoid unnecessary updates on RBGs that will be deleted.
	if len(rbgsToDelete) > 0 {
		logger.Info(
			fmt.Sprintf("Scaling down RoleBasedGroups, %d -> %d", len(existingRBGs), desiredReplicas), "count",
			len(rbgsToDelete),
		)
		if err := r.scaleDown(ctx, rbgsToDelete); err != nil {
			logger.Error(err, "Failed to scale down")
			return ctrl.Result{}, err
		}
	}

	// Filter out RBGs that are going to be deleted from update candidates
	rbgsToDeleteMap := make(map[string]bool)
	for _, rbg := range rbgsToDelete {
		rbgsToDeleteMap[rbg.Name] = true
	}

	// Check for updates needed on existing RBGs that won't be deleted
	var rbgsToUpdate []*workloadsv1alpha1.RoleBasedGroup
	for _, rbg := range existingRBGs {
		// Skip RBGs that are being deleted
		if rbgsToDeleteMap[rbg.Name] {
			continue
		}
		if r.needsUpdate(rbgset, rbg) {
			rbgsToUpdate = append(rbgsToUpdate, rbg)
		}
	}

	// Then, update existing RBGs that remain
	if len(rbgsToUpdate) > 0 {
		logger.Info("Updating existing RoleBasedGroups", "count", len(rbgsToUpdate))
		if err := r.updateExistingRBGs(ctx, rbgset, rbgsToUpdate); err != nil {
			logger.Error(err, "Failed to update existing RBGs")
			return ctrl.Result{}, err
		}
	}

	// Finally, scale up to create new RBGs
	if len(rbgsToCreate) > 0 {
		logger.Info(
			fmt.Sprintf("Scaling up RoleBasedGroups, %d -> %d", len(existingRBGs), desiredReplicas), "count",
			len(rbgsToCreate),
		)
		if err := r.scaleUp(ctx, rbgset, rbgsToCreate); err != nil {
			logger.Error(err, "Failed to scale up")
			// Returning an error will trigger a requeue.
			return ctrl.Result{}, err
		}
	}

	// 5. Update the status after all operations are complete.
	// After scaling, re-list the children to ensure the status is accurate.
	if err := r.client.List(
		ctx, &rbglist, client.InNamespace(rbgset.Namespace), client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		logger.Error(err, "Failed to re-list child RoleBasedGroups for status update")
		return ctrl.Result{}, err
	}
	if err := r.updateStatus(ctx, rbgset, &rbglist); err != nil {
		logger.Error(err, "Failed to update RoleBasedGroupSet status")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully reconciled rbgset")
	return ctrl.Result{}, nil
}

// scaleUp concurrently creates a given set of RoleBasedGroup instances.
func (r *RoleBasedGroupSetReconciler) scaleUp(
	ctx context.Context, rbgset *workloadsv1alpha1.RoleBasedGroupSet, rbgsToCreate []*workloadsv1alpha1.RoleBasedGroup,
) error {
	logger := log.FromContext(ctx)
	// TODO: we need to enhance it by following the way:
	// https://github.com/openkruise/kruise/blob/master/pkg/controller/statefulset/stateful_set_control.go#L478
	allErrs := make([]error, 0, len(rbgsToCreate))
	for _, rbg := range rbgsToCreate {
		// Set the owner reference.
		if err := controllerutil.SetControllerReference(rbgset, rbg, r.scheme); err != nil {
			allErrs = append(allErrs, fmt.Errorf("failed to set controller reference for rbg %s: %w", rbg.Name, err))
			continue
		}

		// Already created not need to continue
		got := &workloadsv1alpha1.RoleBasedGroup{}
		if err := r.client.Get(
			ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, got,
		); err == nil {
			continue
		}

		if err := r.client.Create(ctx, rbg); err != nil {
			// If it already exists, ignore the error. This ensures idempotency,
			// e.g., if the previous reconcile was interrupted after a successful creation.
			if !apierrors.IsAlreadyExists(err) {
				allErrs = append(allErrs, fmt.Errorf("failed to create RoleBasedGroup %s: %w", rbg.Name, err))
			} else {
				logger.V(1).Info("RoleBasedGroup has been created", "name", rbg.Name)
			}
		} else {
			logger.Info("Successfully created RoleBasedGroup", "name", rbg.Name)
		}

	}

	// Aggregate all errors.
	return utilerrors.NewAggregate(allErrs)
}

// scaleDown concurrently deletes a given set of RoleBasedGroup instances.
func (r *RoleBasedGroupSetReconciler) scaleDown(
	ctx context.Context, rbgsToDelete []*workloadsv1alpha1.RoleBasedGroup,
) error {
	logger := log.FromContext(ctx)
	allErrs := make([]error, 0, len(rbgsToDelete))
	for _, rbg := range rbgsToDelete {
		if err := r.client.Delete(ctx, rbg); err != nil {
			// If the resource is not found, it's considered a success, ensuring idempotency.
			if !apierrors.IsNotFound(err) {
				allErrs = append(allErrs, fmt.Errorf("failed to delete RoleBasedGroup %s: %w", rbg.Name, err))
			}
		} else {
			logger.Info("Successfully deleted RoleBasedGroup", "name", rbg.Name)
		}
	}

	return utilerrors.NewAggregate(allErrs)
}

// updateStatus updates the status of the RoleBasedGroupSet.
func (r *RoleBasedGroupSetReconciler) updateStatus(
	ctx context.Context, rbgset *workloadsv1alpha1.RoleBasedGroupSet, rbglist *workloadsv1alpha1.RoleBasedGroupList,
) error {
	logger := log.FromContext(ctx)

	// Create a deep copy of the status to modify.
	newStatus := *rbgset.Status.DeepCopy()
	newStatus.Replicas = int32(len(rbglist.Items))

	// Calculate the number of ready replicas.
	readyReplicas := 0
	for _, rbg := range rbglist.Items {
		if meta.IsStatusConditionTrue(rbg.Status.Conditions, string(workloadsv1alpha1.RoleBasedGroupReady)) {
			readyReplicas++
		}
	}
	newStatus.ReadyReplicas = int32(readyReplicas)

	// Update the Condition.
	desiredReplicas := *rbgset.Spec.Replicas
	var condition metav1.Condition
	if newStatus.ReadyReplicas >= desiredReplicas {
		condition = metav1.Condition{
			Type:               string(workloadsv1alpha1.RoleBasedGroupSetReady),
			Status:             metav1.ConditionTrue,
			Reason:             "AllReplicasReady",
			Message:            "All RoleBasedGroup replicas are ready.",
			LastTransitionTime: metav1.Now(),
		}
	} else {
		condition = metav1.Condition{
			Type:   string(workloadsv1alpha1.RoleBasedGroupSetReady),
			Status: metav1.ConditionFalse,
			Reason: "ReplicasNotReady",
			Message: fmt.Sprintf(
				"Waiting for replicas to be ready (%d/%d)", newStatus.ReadyReplicas, desiredReplicas,
			),
			LastTransitionTime: metav1.Now(),
		}
	}
	// Use apimeta.SetStatusCondition to safely set or update the condition. It correctly handles the LastTransitionTime.
	meta.SetStatusCondition(&newStatus.Conditions, condition)

	// Only update the status if it has changed to avoid unnecessary API calls.
	if reflect.DeepEqual(rbgset.Status, newStatus) {
		return nil
	}

	// Use RetryOnConflict to handle potential conflicts during status updates.
	return retry.RetryOnConflict(
		retry.DefaultRetry, func() error {
			// On each retry, get the latest version of the rbgset object.
			latestRBGSet := &workloadsv1alpha1.RoleBasedGroupSet{}
			if err := r.client.Get(
				ctx, types.NamespacedName{Name: rbgset.Name, Namespace: rbgset.Namespace}, latestRBGSet,
			); err != nil {
				return err
			}

			// Apply the status changes to the latest object.
			latestRBGSet.Status = newStatus

			err := r.client.Status().Update(ctx, latestRBGSet)
			if err == nil {
				logger.Info(
					"Successfully updated RoleBasedGroupSet status",
					"replicas", newStatus.Replicas, "readyReplicas", newStatus.ReadyReplicas,
				)
			}
			return err
		},
	)
}

// rolesEqual compares two role slices by sorting them by name first.
func (r *RoleBasedGroupSetReconciler) rolesEqual(
	roles1, roles2 []workloadsv1alpha1.RoleSpec,
) bool {
	if len(roles1) != len(roles2) {
		return false
	}

	// Create copies to avoid modifying the original slices
	sortedRoles1 := make([]workloadsv1alpha1.RoleSpec, len(roles1))
	sortedRoles2 := make([]workloadsv1alpha1.RoleSpec, len(roles2))
	copy(sortedRoles1, roles1)
	copy(sortedRoles2, roles2)

	// Sort both slices by role name
	sort.Slice(
		sortedRoles1, func(i, j int) bool {
			return sortedRoles1[i].Name < sortedRoles1[j].Name
		},
	)
	sort.Slice(
		sortedRoles2, func(i, j int) bool {
			return sortedRoles2[i].Name < sortedRoles2[j].Name
		},
	)

	// Compare the sorted slices
	return reflect.DeepEqual(sortedRoles1, sortedRoles2)
}

// needsUpdate checks if a child RBG needs to be updated based on changes in the parent RBGSet.
func (r *RoleBasedGroupSetReconciler) needsUpdate(
	rbgset *workloadsv1alpha1.RoleBasedGroupSet, rbg *workloadsv1alpha1.RoleBasedGroup,
) bool {
	// Check if the template spec has changed using order-insensitive comparison
	if !r.rolesEqual(rbg.Spec.Roles, rbgset.Spec.Template.Roles) {
		return true
	}

	// Check if annotations need to be propagated
	return r.needsAnnotationUpdate(rbgset, rbg)
}

// needsAnnotationUpdate checks if RBG annotations need to be updated to match RBGSet annotations.
func (r *RoleBasedGroupSetReconciler) needsAnnotationUpdate(
	rbgset *workloadsv1alpha1.RoleBasedGroupSet, rbg *workloadsv1alpha1.RoleBasedGroup,
) bool {
	// Check exclusive topology annotation
	setExclusiveKey, setHasExclusive := rbgset.Annotations[workloadsv1alpha1.ExclusiveKeyAnnotationKey]
	rbgExclusiveKey, rbgHasExclusive := rbg.Annotations[workloadsv1alpha1.ExclusiveKeyAnnotationKey]

	// If RBGSet has the annotation but RBG doesn't, or they have different values
	if setHasExclusive {
		if !rbgHasExclusive || setExclusiveKey != rbgExclusiveKey {
			return true
		}
	} else if rbgHasExclusive {
		// If RBGSet doesn't have the annotation but RBG does, remove it
		return true
	}

	// Add other annotation checks here as needed
	return false
}

// updateExistingRBGs updates existing RoleBasedGroup instances to match the current template.
func (r *RoleBasedGroupSetReconciler) updateExistingRBGs(
	ctx context.Context, rbgset *workloadsv1alpha1.RoleBasedGroupSet, rbgsToUpdate []*workloadsv1alpha1.RoleBasedGroup,
) error {
	logger := log.FromContext(ctx)
	allErrs := make([]error, 0, len(rbgsToUpdate))

	for _, rbg := range rbgsToUpdate {

		// Use retry mechanism to handle potential conflicts
		err := retry.RetryOnConflict(
			retry.DefaultRetry, func() error {
				// Get the latest version of the RBG
				latestRBG := &workloadsv1alpha1.RoleBasedGroup{}
				if err := r.client.Get(
					ctx, types.NamespacedName{
						Name:      rbg.Name,
						Namespace: rbg.Namespace,
					}, latestRBG,
				); err != nil {
					return err
				}

				// Update the spec from template
				latestRBG.Spec.Roles = rbgset.Spec.Template.Roles

				// Update annotations
				r.updateRBGAnnotations(rbgset, latestRBG)

				// Perform the update
				return r.client.Update(ctx, latestRBG)
			},
		)

		if err != nil {
			allErrs = append(allErrs,
				fmt.Errorf("failed to update RoleBasedGroup %s: %w", rbg.Name, err))
		} else {
			logger.Info("Successfully updated RoleBasedGroup", "name", rbg.Name)
		}

	}

	// Aggregate all concurrent errors
	return utilerrors.NewAggregate(allErrs)
}

// updateRBGAnnotations updates the RBG annotations to match the RBGSet annotations.
func (r *RoleBasedGroupSetReconciler) updateRBGAnnotations(
	rbgset *workloadsv1alpha1.RoleBasedGroupSet, rbg *workloadsv1alpha1.RoleBasedGroup,
) {
	if rbg.Annotations == nil {
		rbg.Annotations = make(map[string]string)
	}

	// Handle exclusive topology annotation
	if exclusiveKey, found := rbgset.Annotations[workloadsv1alpha1.ExclusiveKeyAnnotationKey]; found {
		rbg.Annotations[workloadsv1alpha1.ExclusiveKeyAnnotationKey] = exclusiveKey
	} else {
		// Remove the annotation if it exists in RBG but not in RBGSet
		delete(rbg.Annotations, workloadsv1alpha1.ExclusiveKeyAnnotationKey)
	}
}

// newRBGForSet creates a new RoleBasedGroup object based on the set's template.
func newRBGForSet(rbgset *workloadsv1alpha1.RoleBasedGroupSet, index int) *workloadsv1alpha1.RoleBasedGroup {
	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: rbgset.Namespace,
			Name:      fmt.Sprintf("%s-%d", rbgset.Name, index),
			Labels: map[string]string{
				workloadsv1alpha1.SetRBGSetNameLabelKey: rbgset.Name,
				workloadsv1alpha1.SetRBGIndexLabelKey:   fmt.Sprintf("%d", index),
			},
			// The OwnerReference will be set in the scaleUp function.
		},
		Spec: workloadsv1alpha1.RoleBasedGroupSpec{
			PodGroupPolicy: rbgset.Spec.Template.PodGroupPolicy,
			Roles:          rbgset.Spec.Template.Roles,
		},
	}
	// Copy annotations from RBGSet to child RBG
	if exclusiveKey, found := rbgset.Annotations[workloadsv1alpha1.ExclusiveKeyAnnotationKey]; found {
		if rbg.Annotations == nil {
			rbg.Annotations = make(map[string]string)
		}
		rbg.Annotations[workloadsv1alpha1.ExclusiveKeyAnnotationKey] = exclusiveKey
	}

	return rbg
}

// SetupWithManager sets up the controller with the Manager.
func (r *RoleBasedGroupSetReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&workloadsv1alpha1.RoleBasedGroupSet{}).
		Owns(&workloadsv1alpha1.RoleBasedGroup{}).
		Named("rbgset-controller").
		Complete(r)
}

// CheckCrdExists checks if the specified Custom Resource Definition (CRD) exists in the Kubernetes cluster.
func (r *RoleBasedGroupSetReconciler) CheckCrdExists() error {
	return utils.CheckCrdExists(r.apiReader, "rolebasedgroupsets.workloads.x-k8s.io")
}
