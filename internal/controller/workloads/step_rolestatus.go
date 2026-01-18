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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/reconciler"
)

// role_status_step.go
type RoleStatusStep struct {
	*RoleBasedGroupReconciler
}

func (r *RoleStatusStep) Name() string { return "roleStatus" }

func (r *RoleStatusStep) Execute(ctx context.Context, data *ReconcileData) (ctrl.Result, error) {
	rbg := data.GetRBG()

	var updateStatus bool
	roleStatuses := make([]workloadsv1alpha1.RoleStatus, 0, len(rbg.Spec.Roles))
	data.SetRoleReconcilers(make(map[string]reconciler.WorkloadReconciler))
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

		data.roleReconciler[role.Name] = reconciler

		roleStatus, updateRoleStatus, err := reconciler.ConstructRoleStatus(roleCtx, rbg, &role)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				r.recorder.Eventf(
					rbg, corev1.EventTypeWarning, FailedReconcileWorkload,
					"Failed to construct role %s status: %v", role.Name, err,
				)
				return ctrl.Result{}, err
			}
		}
		updateStatus = updateStatus || updateRoleStatus
		roleStatuses = append(roleStatuses, roleStatus)
	}

	data.SetRoleStatuses(roleStatuses)
	// data.needUpdateStatus = &updateStatus

	// Update the status based on the observed role statuses.
	if updateStatus {
		if err := r.updateRBGStatus(ctx, rbg, data.GetRoleStatuses()); err != nil {
			r.recorder.Eventf(
				rbg, corev1.EventTypeWarning, FailedUpdateStatus,
				"Failed to update status for %s: %v", rbg.Name, err,
			)
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}
