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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type CleanupStep struct {
	*RoleBasedGroupReconciler
}

func (c *CleanupStep) Name() string { return "cleanup" }
func (c *CleanupStep) Execute(ctx context.Context, data *ReconcileData) (ctrl.Result, error) {
	// delete orphan roles
	rbg := data.GetRBG()
	if err := c.deleteOrphanRoles(ctx, rbg); err != nil {
		c.recorder.Eventf(
			rbg, corev1.EventTypeWarning, "delete role error",
			"Failed to delete roles for %s: %v", rbg.Name, err,
		)
		return ctrl.Result{}, err
	}

	// delete expired controllerRevision
	if _, err := utils.CleanExpiredRevision(ctx, c.client, rbg); err != nil {
		c.recorder.Eventf(
			rbg, corev1.EventTypeWarning, "delete expired revision error",
			"Failed to delete expired revision for %s: %v", rbg.Name, err,
		)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}
