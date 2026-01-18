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
)

type DependencyStep struct {
	*RoleBasedGroupReconciler
}

func (s *DependencyStep) Name() string { return "dependency" }

func (s *DependencyStep) Execute(ctx context.Context, data *ReconcileData) (ctrl.Result, error) {
	dependencyManager := data.GetDependencyManager()
	sortedRoles, err := dependencyManager.SortRoles(ctx, data.GetRBG())
	if err != nil {
		s.recorder.Event(data.GetRBG(), corev1.EventTypeWarning, InvalidRoleDependency, err.Error())
		return ctrl.Result{}, err
	}
	data.SetSortedRoles(sortedRoles)
	return ctrl.Result{}, nil
}
