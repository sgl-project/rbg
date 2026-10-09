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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/constants"
	v1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestCoordinatedPolicyTopologyFinalizerLifecycle(t *testing.T) {
	c, r := finalizerTestReconciler(t, topologyFinalizerRBG(), topologyFinalizerPolicy("block"))

	if err := r.ensureCoordinatedPolicyTopologyFinalizer(context.Background(), topologyFinalizerRBG()); err != nil {
		t.Fatal(err)
	}
	policy := &v1alpha2.CoordinatedPolicy{}
	key := types.NamespacedName{Namespace: "default", Name: "rbg"}
	if err := c.Get(context.Background(), key, policy); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(policy, workloadsv1alpha2.CoordinatedPolicyTopologyFinalizer) {
		t.Fatalf("expected topology finalizer on %#v", policy.Finalizers)
	}

	if err := c.Delete(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	policy = &v1alpha2.CoordinatedPolicy{}
	if err := c.Get(context.Background(), key, policy); err != nil {
		t.Fatalf("expected finalizer to block deletion: %v", err)
	}
	if policy.DeletionTimestamp.IsZero() {
		t.Fatal("expected deletion timestamp while finalizer blocks deletion")
	}

	if err := c.Delete(context.Background(), topologyFinalizerRBG()); err != nil {
		t.Fatal(err)
	}
	if err := r.releaseCoordinatedPolicyTopologyFinalizerByKey(context.Background(), key.Namespace, key.Name); err != nil {
		t.Fatal(err)
	}

	// With the old RBG gone, the same name can be reused with a different topology.
	recreatedRBG := topologyFinalizerRBG()
	recreatedRBG.UID = "rbg-uid-2"
	if err := c.Create(context.Background(), recreatedRBG); err != nil {
		t.Fatal(err)
	}
	recreated := topologyFinalizerPolicy("rack")
	if err := c.Create(context.Background(), recreated); err != nil {
		t.Fatal(err)
	}
	if err := r.ensureCoordinatedPolicyTopologyFinalizer(context.Background(), recreatedRBG); err != nil {
		t.Fatal(err)
	}
	recreated = &v1alpha2.CoordinatedPolicy{}
	if err := c.Get(context.Background(), key, recreated); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(recreated, workloadsv1alpha2.CoordinatedPolicyTopologyFinalizer) {
		t.Fatalf("expected recreated policy to be protected, got %#v", recreated.Finalizers)
	}
}

func TestCoordinatedPolicyWithoutTopologyGetsNoFinalizer(t *testing.T) {
	rbg := topologyFinalizerRBG()
	policy := &v1alpha2.CoordinatedPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: rbg.Name, Namespace: rbg.Namespace},
		Spec: v1alpha2.CoordinatedPolicySpec{Policies: []v1alpha2.CoordinatedPolicyRule{{
			Name:  "scaling",
			Roles: []string{"prefill"},
		}}},
	}
	_, r := finalizerTestReconciler(t, rbg, policy)

	if err := r.ensureCoordinatedPolicyTopologyFinalizer(context.Background(), rbg); err != nil {
		t.Fatal(err)
	}
	policy = &v1alpha2.CoordinatedPolicy{}
	if err := r.client.Get(
		context.Background(), types.NamespacedName{Namespace: rbg.Namespace, Name: rbg.Name}, policy); err != nil {
		t.Fatal(err)
	}
	if controllerutil.ContainsFinalizer(policy, workloadsv1alpha2.CoordinatedPolicyTopologyFinalizer) {
		t.Fatalf("expected no topology finalizer, got %#v", policy.Finalizers)
	}
}

func finalizerTestReconciler(
	t *testing.T,
	rbg *v1alpha2.RoleBasedGroup,
	policy *v1alpha2.CoordinatedPolicy,
) (client.Client, *RoleBasedGroupReconciler) {
	t.Helper()
	testScheme := runtime.NewScheme()
	if err := v1alpha2.AddToScheme(testScheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(rbg, policy).Build()
	return c, &RoleBasedGroupReconciler{client: c}
}

func topologyFinalizerRBG() *v1alpha2.RoleBasedGroup {
	return &v1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default", UID: "rbg-uid"},
		Spec: v1alpha2.RoleBasedGroupSpec{Roles: []v1alpha2.RoleSpec{
			{Name: "prefill", Replicas: ptr.To(int32(1))},
		}},
	}
}

func topologyFinalizerPolicy(level string) *v1alpha2.CoordinatedPolicy {
	return &v1alpha2.CoordinatedPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
		Spec: v1alpha2.CoordinatedPolicySpec{Policies: []v1alpha2.CoordinatedPolicyRule{{
			Name:  "pd",
			Roles: []string{"prefill"},
			Strategy: v1alpha2.CoordinatedPolicyStrategy{Scheduling: &v1alpha2.SchedulingCoordinationStrategy{
				TopologyConstraint: &v1alpha2.TopologyConstraint{
					Pack: &v1alpha2.TopologyPackConstraint{Required: ptr.To(level)},
				},
			}},
		}}},
	}
}
