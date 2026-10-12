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
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestResolvePlacementPlanGangOnlyPreservesOneGroup(t *testing.T) {
	rbg := placementRBG()
	ctx := placementContext(t, rbg, nil)

	plan, err := ResolvePlacementPlan(ctx, fakeClient(t, rbg, nil).Build(), rbg, &GangStrategy{
		Roles: sets.New("prefill", "decode"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.HasTopology() {
		t.Fatal("unexpected topology")
	}
	if len(plan.TopLevelGroups()) != 1 {
		t.Fatalf("expected one top-level group, got %d", len(plan.TopLevelGroups()))
	}
	group := plan.TopLevelGroups()[0]
	if group.Gang == nil {
		t.Fatal("expected gang attribute")
	}
	if len(group.Scope.PartitionBy) != 0 {
		t.Fatalf("expected no partition, got %q", group.Scope.PartitionBy)
	}
}

func TestResolvePlacementPlanMergesEqualGangAndTopologyScopes(t *testing.T) {
	rbg := placementRBG()
	policy := placementPolicy([]workloadsv1alpha2.CoordinatedPolicyRule{{
		Name:  "pd",
		Roles: []string{"decode", "prefill"},
		Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
			Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
				Gang: &workloadsv1alpha2.GangSchedulingStrategy{},
				TopologyConstraint: &workloadsv1alpha2.TopologyConstraint{
					Pack: &workloadsv1alpha2.TopologyPackConstraint{
						Required: ptrTo("block"),
					},
				},
			},
		},
	}})
	ctx := placementContext(t, rbg, policy)

	plan, err := ResolvePlacementPlan(ctx, fakeClient(t, rbg, policy).Build(), rbg, &GangStrategy{
		Roles: sets.New("prefill", "decode"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.TopLevelGroups()) != 1 {
		t.Fatalf("expected one merged group, got %d", len(plan.TopLevelGroups()))
	}
	group := plan.TopLevelGroups()[0]
	if group.Gang == nil || group.Topology == nil {
		t.Fatal("expected gang and topology on the same group")
	}
}

func TestResolvePlacementPlanRejectsPartialOverlap(t *testing.T) {
	rbg := placementRBG()
	policy := placementPolicy([]workloadsv1alpha2.CoordinatedPolicyRule{{
		Name:  "pd",
		Roles: []string{"prefill", "decode"},
		Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
			Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
				TopologyConstraint: &workloadsv1alpha2.TopologyConstraint{
					Pack: &workloadsv1alpha2.TopologyPackConstraint{Required: ptrTo("block")},
				},
			},
		},
	}})
	ctx := placementContext(t, rbg, policy)
	gang := &GangStrategy{Roles: sets.New("prefill", "router")}

	_, err := ResolvePlacementPlan(ctx, fakeClient(t, rbg, policy).Build(), rbg, gang)
	if !IsIncompatiblePlacementGroups(err) {
		t.Fatalf("expected IncompatiblePlacementGroups, got %v", err)
	}
}

func TestResolvePlacementPlanRoleConstraintBecomesPerInstanceChild(t *testing.T) {
	rbg := placementRBG()
	rbg.Spec.Roles[0].InstanceTopologyConstraint = &workloadsv1alpha2.TopologyConstraint{
		Pack: &workloadsv1alpha2.TopologyPackConstraint{Required: ptrTo("rack")},
	}
	policy := placementPolicy([]workloadsv1alpha2.CoordinatedPolicyRule{{
		Name:  "pd",
		Roles: []string{"prefill", "decode"},
		Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
			Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
				TopologyConstraint: &workloadsv1alpha2.TopologyConstraint{
					Pack: &workloadsv1alpha2.TopologyPackConstraint{Required: ptrTo("block")},
				},
			},
		},
	}})
	ctx := placementContext(t, rbg, policy)

	plan, err := ResolvePlacementPlan(ctx, fakeClient(t, rbg, policy).Build(), rbg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.TopLevelGroups()) != 1 {
		t.Fatalf("expected one parent, got %d", len(plan.TopLevelGroups()))
	}
	parent := plan.TopLevelGroups()[0]
	if parent.Name != "p-pd" {
		t.Fatalf("expected policy placement name, got %q", parent.Name)
	}
	if len(parent.Children) != 1 {
		t.Fatalf("expected one child, got %d", len(parent.Children))
	}
	child := parent.Children[0]
	if child.Name != "r-prefill" {
		t.Fatalf("expected role placement name, got %q", child.Name)
	}
	if !slices.Equal(child.Scope.PartitionBy, PartitionByRoleInstance) {
		t.Fatalf("expected per-instance child, got %q", child.Scope.PartitionBy)
	}
	if len(child.Scope.Roles) != 1 || child.Scope.Roles[0] != "prefill" {
		t.Fatalf("expected prefill child, got %v", child.Scope.Roles)
	}
}

func TestResolvePlacementPlanRejectsDifferentTopologyNames(t *testing.T) {
	rbg := placementRBG()
	rbg.Spec.Roles[0].InstanceTopologyConstraint = &workloadsv1alpha2.TopologyConstraint{
		TopologyName: ptrTo("a"),
	}
	policy := placementPolicy([]workloadsv1alpha2.CoordinatedPolicyRule{{
		Name:  "pd",
		Roles: []string{"prefill", "decode"},
		Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
			Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
				TopologyConstraint: &workloadsv1alpha2.TopologyConstraint{
					TopologyName: ptrTo("b"),
				},
			},
		},
	}})
	ctx := placementContext(t, rbg, policy)
	_, err := ResolvePlacementPlan(ctx, fakeClient(t, rbg, policy).Build(), rbg, nil)
	if !IsTopologyTranslationError(err) {
		t.Fatalf("expected TopologyTranslationError, got %v", err)
	}
	if reason, ok := TopologyTranslationReason(err); !ok || reason != TopologyReasonIncompatibleTopologyNames {
		t.Fatalf("expected IncompatibleTopologyNames reason, got reason=%q ok=%v err=%v", reason, ok, err)
	}
}

func placementRBG() *workloadsv1alpha2.RoleBasedGroup {
	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "infer", Namespace: "default"},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{Name: "prefill", Replicas: ptrTo(int32(2))},
				{Name: "decode", Replicas: ptrTo(int32(2))},
				{Name: "router", Replicas: ptrTo(int32(1))},
			},
		},
	}
}

func placementPolicy(rules []workloadsv1alpha2.CoordinatedPolicyRule) *workloadsv1alpha2.CoordinatedPolicy {
	return &workloadsv1alpha2.CoordinatedPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "infer", Namespace: "default"},
		Spec:       workloadsv1alpha2.CoordinatedPolicySpec{Policies: rules},
	}
}

func placementContext(t *testing.T, rbg *workloadsv1alpha2.RoleBasedGroup, policy *workloadsv1alpha2.CoordinatedPolicy) context.Context {
	t.Helper()
	return context.Background()
}

func fakeClient(t *testing.T, rbg *workloadsv1alpha2.RoleBasedGroup, policy *workloadsv1alpha2.CoordinatedPolicy) *fake.ClientBuilder {
	t.Helper()
	sch := runtime.NewScheme()
	if err := workloadsv1alpha2.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	builder := fake.NewClientBuilder().WithScheme(sch).WithObjects(rbg)
	if policy != nil {
		builder = builder.WithObjects(policy)
	}
	return builder
}

func ptrTo[T any](value T) *T {
	return &value
}

func TestResolvePlacementPlanExpandsWholeGroupGangScope(t *testing.T) {
	rbg := placementRBG()
	plan, err := ResolvePlacementPlan(
		context.Background(),
		fakeClient(t, rbg, nil).Build(),
		rbg,
		&GangStrategy{Roles: sets.New[string]()},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.TopLevelGroups()) != 1 {
		t.Fatalf("expected one top-level group, got %d", len(plan.TopLevelGroups()))
	}
	group := plan.TopLevelGroups()[0]
	want := []string{"decode", "prefill", "router"}
	if !slices.Equal(group.Scope.Roles, want) {
		t.Fatalf("expected whole-group roles %v, got %v", want, group.Scope.Roles)
	}
	if group.ID == "" {
		t.Fatal("expected a non-synthetic root ID")
	}
}

func TestResolvePlacementPlanUnknownRoleReturnsTopologyTranslationError(t *testing.T) {
	rbg := placementRBG()
	policy := placementPolicy([]workloadsv1alpha2.CoordinatedPolicyRule{{
		Name:  "bad",
		Roles: []string{"missing"},
		Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
			Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
				TopologyConstraint: &workloadsv1alpha2.TopologyConstraint{
					Pack: &workloadsv1alpha2.TopologyPackConstraint{Required: ptrTo("rack")},
				},
			},
		},
	}})
	_, err := ResolvePlacementPlan(
		context.Background(),
		fakeClient(t, rbg, policy).Build(),
		rbg,
		nil,
	)
	if !IsTopologyTranslationError(err) {
		t.Fatalf("expected TopologyTranslationError, got %v", err)
	}
}

func TestResolvePlacementPlanTerminatingPolicyMatchingUIDStaysAuthoritative(t *testing.T) {
	rbg := placementRBG()
	rbg.UID = "rbg-uid-active"
	policy := placementPolicy([]workloadsv1alpha2.CoordinatedPolicyRule{{
		Name:  "pd",
		Roles: []string{"prefill", "decode"},
		Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
			Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
				TopologyConstraint: &workloadsv1alpha2.TopologyConstraint{
					Pack: &workloadsv1alpha2.TopologyPackConstraint{Required: ptrTo("block")},
				},
			},
		},
	}})
	policy.Annotations = map[string]string{
		constants.CoordinatedPolicyTopologyRBGUIDAnnotation: string(rbg.UID),
	}
	policy.Finalizers = []string{constants.CoordinatedPolicyTopologyFinalizer}
	now := metav1.Now()
	policy.DeletionTimestamp = &now

	plan, err := ResolvePlacementPlan(
		context.Background(),
		fakeClient(t, rbg, policy).Build(),
		rbg,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.HasTopology() {
		t.Fatal("expected the frozen terminating policy to remain authoritative")
	}
}

func TestResolvePlacementPlanIgnoresStaleTerminatingPolicy(t *testing.T) {
	rbg := placementRBG()
	rbg.UID = "rbg-uid-new"
	policy := placementPolicy([]workloadsv1alpha2.CoordinatedPolicyRule{{
		Name:  "pd",
		Roles: []string{"prefill", "decode"},
		Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
			Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
				TopologyConstraint: &workloadsv1alpha2.TopologyConstraint{
					Pack: &workloadsv1alpha2.TopologyPackConstraint{Required: ptrTo("block")},
				},
			},
		},
	}})
	policy.Annotations = map[string]string{
		constants.CoordinatedPolicyTopologyRBGUIDAnnotation: "rbg-uid-old",
	}
	policy.Finalizers = []string{constants.CoordinatedPolicyTopologyFinalizer}
	now := metav1.Now()
	policy.DeletionTimestamp = &now

	plan, err := ResolvePlacementPlan(
		context.Background(),
		fakeClient(t, rbg, policy).Build(),
		rbg,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan != nil && plan.HasTopology() {
		t.Fatal("expected the stale terminating policy to be ignored")
	}
}

func TestTopologyContractID(t *testing.T) {
	nonePlan := &PlacementPlan{Root: &PlacementGroup{
		Gang:  &GangStrategy{Roles: sets.New("prefill")},
		Scope: PlacementScope{Roles: []string{"prefill"}},
	}}
	if got := nonePlan.TopologyContractID(); got != "none" {
		t.Fatalf("expected no-topology contract ID \"none\", got %q", got)
	}
	if got := (*PlacementPlan)(nil).TopologyContractID(); got != "none" {
		t.Fatalf("expected nil plan contract ID \"none\", got %q", got)
	}

	topology := &workloadsv1alpha2.TopologyConstraint{
		Pack: &workloadsv1alpha2.TopologyPackConstraint{
			Required:  ptrTo("block"),
			Preferred: ptrTo("rack"),
		},
	}
	first := &PlacementPlan{Root: &PlacementGroup{
		Scope:    PlacementScope{Roles: []string{"decode", "prefill"}},
		Topology: topology,
	}}
	second := &PlacementPlan{Root: &PlacementGroup{
		Scope:    PlacementScope{Roles: []string{"decode", "prefill"}},
		Topology: topology,
	}}
	if first.TopologyContractID() != second.TopologyContractID() {
		t.Fatal("expected equal topology declarations to produce the same contract ID")
	}

	changed := &PlacementPlan{Root: &PlacementGroup{
		Scope: PlacementScope{Roles: []string{"decode", "prefill"}},
		Topology: &workloadsv1alpha2.TopologyConstraint{
			Pack: &workloadsv1alpha2.TopologyPackConstraint{Required: ptrTo("rack")},
		},
	}}
	if first.TopologyContractID() == changed.TopologyContractID() {
		t.Fatal("expected different topology declarations to produce different contract IDs")
	}
}
