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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	rbg := topologyFinalizerRBG()
	c, r := finalizerTestReconciler(t, rbg, topologyFinalizerPolicy("block"))

	if err := r.ensureCoordinatedPolicyTopologyFinalizer(context.Background(), rbg); err != nil {
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
	if policy.Annotations[workloadsv1alpha2.CoordinatedPolicyTopologyRBGUIDAnnotation] != string(rbg.UID) {
		t.Fatalf("expected protected RBG UID annotation, got %#v", policy.Annotations)
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

	// The active RBG still has the recorded UID, so the policy remains protected.
	if err := r.releaseCoordinatedPolicyTopologyFinalizerByKey(context.Background(), key.Namespace, key.Name); err != nil {
		t.Fatal(err)
	}
	policy = &v1alpha2.CoordinatedPolicy{}
	if err := c.Get(context.Background(), key, policy); err != nil {
		t.Fatal(err)
	}
	if !controllerutil.ContainsFinalizer(policy, workloadsv1alpha2.CoordinatedPolicyTopologyFinalizer) {
		t.Fatal("expected the finalizer to remain while the recorded RBG UID is active")
	}

	// Recreating an RBG with the same name does not satisfy the old lifecycle's
	// UID-keyed release condition, but the old UID is gone, so the stale policy is
	// released and a replacement policy can be created.
	if err := c.Delete(context.Background(), rbg); err != nil {
		t.Fatal(err)
	}
	recreatedRBG := topologyFinalizerRBG()
	recreatedRBG.UID = "rbg-uid-2"
	if err := c.Create(context.Background(), recreatedRBG); err != nil {
		t.Fatal(err)
	}
	if err := r.releaseCoordinatedPolicyTopologyFinalizerByKey(context.Background(), key.Namespace, key.Name); err != nil {
		t.Fatal(err)
	}
	// With the last finalizer removed, the API server completes deletion of the stale
	// terminating policy and the replacement can reuse the name.
	policy = &v1alpha2.CoordinatedPolicy{}
	if err := c.Get(context.Background(), key, policy); !apierrors.IsNotFound(err) {
		t.Fatalf("expected the stale terminating policy to be deleted, got %v", err)
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
	if recreated.Annotations[workloadsv1alpha2.CoordinatedPolicyTopologyRBGUIDAnnotation] != string(recreatedRBG.UID) {
		t.Fatalf("expected recreated policy to record the new RBG UID, got %#v", recreated.Annotations)
	}
}

func TestEnsureReleasesStaleTerminatingPolicyForRecreatedRBG(t *testing.T) {
	rbg := topologyFinalizerRBG()
	rbg.UID = "rbg-uid-new"
	policy := topologyFinalizerPolicy("block")
	policy.Annotations = map[string]string{
		workloadsv1alpha2.CoordinatedPolicyTopologyRBGUIDAnnotation: "rbg-uid-old",
	}
	policy.Finalizers = []string{workloadsv1alpha2.CoordinatedPolicyTopologyFinalizer}
	now := metav1.Now()
	policy.DeletionTimestamp = &now

	c, r := finalizerTestReconciler(t, rbg, policy)
	if err := r.ensureCoordinatedPolicyTopologyFinalizer(context.Background(), rbg); err != nil {
		t.Fatal(err)
	}

	// Removing the finalizer lets the API server complete deletion of the stale
	// terminating policy, freeing the name for a replacement.
	policy = &v1alpha2.CoordinatedPolicy{}
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "rbg"}, policy)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected the stale terminating policy to be deleted, got %v", err)
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
