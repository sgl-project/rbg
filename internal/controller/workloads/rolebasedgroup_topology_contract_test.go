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

package workloads

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/constants"
	v1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scheduler/common"
)

func TestEnsureTopologyContractRecordsFirstAcceptance(t *testing.T) {
	rbg := topologyContractRBG()
	c, r := topologyContractReconciler(t, rbg)

	// Advance the stored object after rbg was fetched, mimicking a status SSA patch
	// that changes resourceVersion. The annotation write must not conflict.
	fresh := &v1alpha2.RoleBasedGroup{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(rbg), fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Annotations == nil {
		fresh.Annotations = map[string]string{}
	}
	fresh.Annotations["workloads.x-k8s.io/test-resource-version"] = "advanced"
	if err := c.Update(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}

	if err := r.ensureTopologyContract(context.Background(), rbg, nil); err != nil {
		t.Fatal(err)
	}
	if rbg.Annotations[workloadsv1alpha2.RBGTopologyContractAnnotation] != "none" {
		t.Fatalf("expected recorded contract \"none\", got %#v", rbg.Annotations)
	}

	updated := &v1alpha2.RoleBasedGroup{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(rbg), updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[workloadsv1alpha2.RBGTopologyContractAnnotation] != "none" {
		t.Fatalf("expected persisted contract annotation, got %#v", updated.Annotations)
	}
}

func TestEnsureTopologyContractRejectsLateTopology(t *testing.T) {
	rbg := topologyContractRBG()
	rbg.Annotations = map[string]string{workloadsv1alpha2.RBGTopologyContractAnnotation: "none"}
	_, r := topologyContractReconciler(t, rbg)

	late := &common.PlacementPlan{Root: &common.PlacementGroup{
		Scope: common.PlacementScope{Roles: []string{"prefill"}},
		Topology: &v1alpha2.TopologyConstraint{
			Pack: &v1alpha2.TopologyPackConstraint{Required: ptr.To("block")},
		},
	}}
	err := r.ensureTopologyContract(context.Background(), rbg, late)
	if err == nil {
		t.Fatal("expected a late topology policy to be rejected")
	}
	if !common.IsTopologyTranslationError(err) {
		t.Fatalf("expected TopologyTranslationError, got %v", err)
	}
	reason, ok := common.TopologyTranslationReason(err)
	if !ok || reason != common.TopologyReasonTopologyPolicyLate {
		t.Fatalf("expected TopologyPolicyLate reason, got reason=%q ok=%v err=%v", reason, ok, err)
	}
}

func TestEnsureTopologyContractAcceptsUnchangedContract(t *testing.T) {
	rbg := topologyContractRBG()
	plan := &common.PlacementPlan{Root: &common.PlacementGroup{
		Scope: common.PlacementScope{Roles: []string{"prefill"}},
		Topology: &v1alpha2.TopologyConstraint{
			Pack: &v1alpha2.TopologyPackConstraint{Required: ptr.To("block")},
		},
	}}
	rbg.Annotations = map[string]string{
		workloadsv1alpha2.RBGTopologyContractAnnotation: plan.TopologyContractID(),
	}
	_, r := topologyContractReconciler(t, rbg)

	if err := r.ensureTopologyContract(context.Background(), rbg, plan); err != nil {
		t.Fatal(err)
	}
}

func topologyContractRBG() *v1alpha2.RoleBasedGroup {
	return &v1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default", UID: "rbg-uid"},
		Spec: v1alpha2.RoleBasedGroupSpec{Roles: []v1alpha2.RoleSpec{
			{Name: "prefill", Replicas: ptr.To(int32(1))},
		}},
	}
}

func topologyContractReconciler(
	t *testing.T,
	rbg *v1alpha2.RoleBasedGroup,
) (client.Client, *RoleBasedGroupReconciler) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rbg).Build()
	return c, &RoleBasedGroupReconciler{client: c}
}
