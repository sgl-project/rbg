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

	"github.com/modern-go/reflect2"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

type ReconcileRolesStep struct {
	*RoleBasedGroupReconciler
}

func (r *ReconcileRolesStep) Name() string { return "reconcileRoles" }
func (r *ReconcileRolesStep) Execute(ctx context.Context, data *ReconcileData) (ctrl.Result, error) {
	// Reconcile roles, do create/update actions for roles.
	sortedRoles := data.GetSortedRoles()
	rbg := data.GetRBG()
	dependencyManager := data.GetDependencyManager()
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

			reconciler, ok := data.GetRoleReconcilers()[role.Name]
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
			rollingUpdateStrategies := data.GetRollingUpdateStrategies()
			if rollout, ok := rollingUpdateStrategies[role.Name]; ok {
				rollingUpdateStrategy = &rollout
			}

			expectedRolesRevisionHash := data.GetExpectedRolesRevisionHash()
			if err := reconciler.Reconciler(roleCtx, rbg, role, rollingUpdateStrategy, expectedRolesRevisionHash[role.Name]); err != nil {
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
	return ctrl.Result{}, nil
}
