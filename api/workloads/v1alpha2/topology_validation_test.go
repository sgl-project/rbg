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

package v1alpha2

import (
	"context"
	"strings"
	"testing"
)

func TestValidateTopologyConstraintRejectsEmptyAndMissingPack(t *testing.T) {
	tests := []struct {
		name       string
		constraint *TopologyConstraint
	}{
		{name: "empty object", constraint: &TopologyConstraint{}},
		{name: "topology name only", constraint: &TopologyConstraint{TopologyName: ptrString("kai")}},
		{name: "empty pack", constraint: &TopologyConstraint{Pack: &TopologyPackConstraint{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTopologyConstraint("spec.roles[0].instanceTopologyConstraint", tt.constraint)
			if err == nil || !strings.Contains(err.Error(), "at least one of required or preferred") {
				t.Fatalf("expected empty topology error, got %v", err)
			}
		})
	}
}

func TestValidateRoleTopologyImmutability(t *testing.T) {
	rbgWithTopology := func() *RoleBasedGroup {
		return &RoleBasedGroup{Spec: RoleBasedGroupSpec{Roles: []RoleSpec{
			{Name: "router"},
			{Name: "prefill", InstanceTopologyConstraint: topologyConstraint("rack")},
		}}}
	}

	t.Run("constraint change is rejected", func(t *testing.T) {
		oldRBG := rbgWithTopology()
		newRBG := oldRBG.DeepCopy()
		newRBG.Spec.Roles[1].InstanceTopologyConstraint = topologyConstraint("block")
		if err := ValidateRoleTopologyImmutability(oldRBG, newRBG); err == nil {
			t.Fatal("expected topology change to be rejected")
		}
	})

	t.Run("constraint removal is rejected", func(t *testing.T) {
		oldRBG := rbgWithTopology()
		newRBG := oldRBG.DeepCopy()
		newRBG.Spec.Roles[1].InstanceTopologyConstraint = nil
		if err := ValidateRoleTopologyImmutability(oldRBG, newRBG); err == nil {
			t.Fatal("expected topology removal to be rejected")
		}
	})

	t.Run("first constraint cannot be added", func(t *testing.T) {
		oldRBG := rbgWithTopology()
		newRBG := oldRBG.DeepCopy()
		newRBG.Spec.Roles[0].InstanceTopologyConstraint = topologyConstraint("rack")
		if err := ValidateRoleTopologyImmutability(oldRBG, newRBG); err == nil {
			t.Fatal("expected topology addition to be rejected")
		}
	})

	t.Run("topology-free role changes are allowed", func(t *testing.T) {
		oldRBG := rbgWithTopology()
		newRBG := oldRBG.DeepCopy()
		newRBG.Spec.Roles = newRBG.Spec.Roles[1:]
		if err := ValidateRoleTopologyImmutability(oldRBG, newRBG); err != nil {
			t.Fatalf("expected topology-free role deletion to be allowed, got %v", err)
		}
	})
}

func TestValidateCoordinatedPolicyTopologyRejectsInvalidRules(t *testing.T) {
	policy := &CoordinatedPolicy{Spec: CoordinatedPolicySpec{Policies: []CoordinatedPolicyRule{
		{
			Name:  "same",
			Roles: []string{"prefill"},
			Strategy: CoordinatedPolicyStrategy{Scheduling: &SchedulingCoordinationStrategy{
				TopologyConstraint: topologyConstraint("rack"),
			}},
		},
		{
			Name:  "same",
			Roles: []string{"decode"},
			Strategy: CoordinatedPolicyStrategy{Scheduling: &SchedulingCoordinationStrategy{
				TopologyConstraint: topologyConstraint("rack"),
			}},
		},
	}}}

	err := ValidateCoordinatedPolicyTopology(policy)
	if err == nil || !strings.Contains(err.Error(), "duplicate topology rule name") {
		t.Fatalf("expected duplicate rule-name error, got %v", err)
	}
}

func TestValidateCoordinatedPolicyTopologyImmutability(t *testing.T) {
	oldPolicy := topologyPolicy("pd", []string{"prefill", "decode"})

	t.Run("rule removal is rejected", func(t *testing.T) {
		newPolicy := oldPolicy.DeepCopy()
		newPolicy.Spec.Policies = nil
		if err := ValidateCoordinatedPolicyTopologyImmutability(oldPolicy, newPolicy); err == nil {
			t.Fatal("expected rule removal to be rejected")
		}
	})

	t.Run("role membership change is rejected", func(t *testing.T) {
		newPolicy := oldPolicy.DeepCopy()
		newPolicy.Spec.Policies[0].Roles = []string{"prefill"}
		if err := ValidateCoordinatedPolicyTopologyImmutability(oldPolicy, newPolicy); err == nil {
			t.Fatal("expected role membership change to be rejected")
		}
	})

	t.Run("new topology rule is rejected", func(t *testing.T) {
		newPolicy := oldPolicy.DeepCopy()
		newPolicy.Spec.Policies = append(newPolicy.Spec.Policies, CoordinatedPolicyRule{
			Name:  "router-topology",
			Roles: []string{"router"},
			Strategy: CoordinatedPolicyStrategy{Scheduling: &SchedulingCoordinationStrategy{
				TopologyConstraint: topologyConstraint("rack"),
			}},
		})
		if err := ValidateCoordinatedPolicyTopologyImmutability(oldPolicy, newPolicy); err == nil {
			t.Fatal("expected topology rule addition to be rejected")
		}
	})
}

func TestValidateRoleBasedGroupSetTopologyImmutability(t *testing.T) {
	oldRBGS := &RoleBasedGroupSet{Spec: RoleBasedGroupSetSpec{
		GroupTemplate: RoleBasedGroupTemplateSpec{Spec: RoleBasedGroupSpec{Roles: []RoleSpec{
			{Name: "router"},
			{Name: "prefill", InstanceTopologyConstraint: topologyConstraint("rack")},
		}}},
	}}

	t.Run("template topology change is rejected", func(t *testing.T) {
		newRBGS := oldRBGS.DeepCopy()
		newRBGS.Spec.GroupTemplate.Spec.Roles[1].InstanceTopologyConstraint = topologyConstraint("block")
		if err := validateTopologyImmutability(oldRBGS, newRBGS); err == nil {
			t.Fatal("expected template topology change to be rejected")
		}
	})

	t.Run("first template topology cannot be added", func(t *testing.T) {
		newRBGS := oldRBGS.DeepCopy()
		newRBGS.Spec.GroupTemplate.Spec.Roles[0].InstanceTopologyConstraint = topologyConstraint("rack")
		if err := validateTopologyImmutability(oldRBGS, newRBGS); err == nil {
			t.Fatal("expected template topology addition to be rejected")
		}
	})

	t.Run("topology-free template role deletion is allowed", func(t *testing.T) {
		newRBGS := oldRBGS.DeepCopy()
		newRBGS.Spec.GroupTemplate.Spec.Roles = newRBGS.Spec.GroupTemplate.Spec.Roles[1:]
		if err := validateTopologyImmutability(oldRBGS, newRBGS); err != nil {
			t.Fatalf("expected topology-free template role deletion to be allowed, got %v", err)
		}
	})
}

func topologyConstraint(level string) *TopologyConstraint {
	return &TopologyConstraint{Pack: &TopologyPackConstraint{Required: ptrString(level)}}
}

func topologyPolicy(name string, roles []string) *CoordinatedPolicy {
	return &CoordinatedPolicy{Spec: CoordinatedPolicySpec{Policies: []CoordinatedPolicyRule{{
		Name:  name,
		Roles: roles,
		Strategy: CoordinatedPolicyStrategy{Scheduling: &SchedulingCoordinationStrategy{
			TopologyConstraint: topologyConstraint("block"),
		}},
	}}}}
}

func ptrString(value string) *string { return &value }

func TestAdmissionValidatorsRejectLifecycleTopologyChanges(t *testing.T) {
	oldRBG := &RoleBasedGroup{Spec: RoleBasedGroupSpec{Roles: []RoleSpec{{
		Name:                       "prefill",
		InstanceTopologyConstraint: topologyConstraint("rack"),
	}}}}
	changedRBG := oldRBG.DeepCopy()
	changedRBG.Spec.Roles[0].InstanceTopologyConstraint = topologyConstraint("block")
	rbgValidator := &RoleBasedGroupValidator{}
	if _, err := rbgValidator.ValidateUpdate(context.Background(), oldRBG, changedRBG); err == nil {
		t.Fatal("expected RoleBasedGroup admission to reject topology change")
	}

	oldPolicy := topologyPolicy("pd", []string{"prefill", "decode"})
	changedPolicy := oldPolicy.DeepCopy()
	changedPolicy.Spec.Policies[0].Roles = []string{"prefill"}
	policyValidator := &CoordinatedPolicyValidator{}
	if _, err := policyValidator.ValidateUpdate(context.Background(), oldPolicy, changedPolicy); err == nil {
		t.Fatal("expected CoordinatedPolicy admission to reject topology role change")
	}

	oldRBGS := &RoleBasedGroupSet{Spec: RoleBasedGroupSetSpec{
		GroupTemplate: RoleBasedGroupTemplateSpec{Spec: RoleBasedGroupSpec{Roles: []RoleSpec{{
			Name:                       "prefill",
			InstanceTopologyConstraint: topologyConstraint("rack"),
		}}}},
	}}
	changedRBGS := oldRBGS.DeepCopy()
	changedRBGS.Spec.GroupTemplate.Spec.Roles[0].InstanceTopologyConstraint = topologyConstraint("block")
	rbgsValidator := &RoleBasedGroupSetValidator{}
	if _, err := rbgsValidator.ValidateUpdate(context.Background(), oldRBGS, changedRBGS); err == nil {
		t.Fatal("expected RoleBasedGroupSet admission to reject template topology change")
	}
}
