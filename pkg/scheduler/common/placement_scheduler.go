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

package common

import (
	"context"
	"sync"

	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// PlacementRenderResult reports how a PlacementPlan was compiled.
type PlacementRenderResult struct {
	// PreferredAbsorbed is true when the dialect could not anchor the preferred
	// topology level and used generic topology scoring instead.
	PreferredAbsorbed bool
}

// PlacementScheduler is the KEP-473 scheduler compiler contract. It consumes the
// scheduler-independent PlacementPlan and is the only component allowed to render
// physical PodGroups and emit pod bindings. Implementations that cannot compile a
// valid plan without semantic loss return an error rather than approximating it.
type PlacementScheduler interface {
	ReconcilePlacement(
		ctx context.Context,
		rbg *workloadsv1alpha2.RoleBasedGroup,
		plan *PlacementPlan,
		runtimeController *builder.TypedBuilder[reconcile.Request],
		watchedWorkload *sync.Map,
		apiReader client.Reader,
	) (*PlacementRenderResult, error)

	InjectPlacementSchedulingFields(
		rbg *workloadsv1alpha2.RoleBasedGroup,
		role *workloadsv1alpha2.RoleSpec,
		plan *PlacementPlan,
		pts *coreapplyv1.PodTemplateSpecApplyConfiguration,
	)
}
