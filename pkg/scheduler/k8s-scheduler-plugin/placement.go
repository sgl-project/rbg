/*
Copyright 2026 The RBG Authors.

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

package kubeschedulerplugin

import (
	"context"
	"sync"

	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scheduler/common"
)

var _ common.PlacementScheduler = (*GangScheduler)(nil)

// ReconcilePlacement preserves KEP-430 gang-only behavior. Topology-aware
// scheduling is explicitly unsupported on scheduler-plugins because its PodGroup has
// no network topology fields.
func (m *GangScheduler) ReconcilePlacement(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	plan *common.PlacementPlan,
	runtimeController *builder.TypedBuilder[reconcile.Request],
	watchedWorkload *sync.Map,
	apiReader client.Reader,
) (*common.PlacementRenderResult, error) {
	if plan == nil {
		return nil, m.ReconcilePodGroup(ctx, rbg, nil, runtimeController, watchedWorkload, apiReader)
	}
	if plan.HasTopology() {
		return nil, common.NewSchedulerUnsupportedError("scheduler-plugins does not support topology-aware scheduling")
	}
	root := plan.Root
	if groups := plan.TopLevelGroups(); len(groups) == 1 {
		root = groups[0]
	}
	if err := m.ReconcilePodGroup(ctx, rbg, root.Gang, runtimeController, watchedWorkload, apiReader); err != nil {
		return nil, err
	}
	return &common.PlacementRenderResult{}, nil
}

// InjectPlacementSchedulingFields delegates to the KEP-430 injector for gang-only
// plans. A topology plan never reaches injection because ReconcilePlacement rejects
// it and the controller gates role creation.
func (m *GangScheduler) InjectPlacementSchedulingFields(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
	plan *common.PlacementPlan,
	pts *coreapplyv1.PodTemplateSpecApplyConfiguration,
) {
	if plan == nil || plan.HasTopology() {
		return
	}
	root := plan.Root
	if groups := plan.TopLevelGroups(); len(groups) == 1 {
		root = groups[0]
	}
	m.InjectPodSchedulingFields(rbg, role, root.Gang, pts)
}
