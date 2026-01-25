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

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type RevisionStep struct {
	// client client.Client
	*RoleBasedGroupReconciler
}

func (s *RevisionStep) Name() string { return "revision" }

func (s *RevisionStep) Execute(ctx context.Context, data *ReconcileData) (ctrl.Result, error) {
	revisionHash, err := s.handleRevisions(ctx, data.GetRBG())
	if err != nil {
		return ctrl.Result{}, err
	}
	// data.expectedRolesRevisionHash = revisionHash
	data.SetExpectedRolesRevisionHash(revisionHash)
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
