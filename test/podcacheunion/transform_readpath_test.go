//go:build podcacheunion

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

package podcacheunion

import (
	"context"
	"reflect"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	clientfeatures "k8s.io/client-go/features"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

// The operator's instanceutil import replaces client-go's environment-based
// gates. Enabling WatchListClient requires setting the effective gate, and the
// test must restore its initial value when running the streaming test suite.
func TestFeatureGateSetOverridesProductionImport(t *testing.T) {
	before := clientfeatures.FeatureGates().Enabled(clientfeatures.WatchListClient)
	t.Cleanup(func() {
		if err := configureWatchListClient(before); err != nil {
			t.Error(err)
		}
	})
	if err := configureWatchListClient(false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBE_FEATURE_WatchListClient", "true")
	if clientfeatures.FeatureGates().Enabled(clientfeatures.WatchListClient) {
		t.Fatal("environment variable unexpectedly changed the production feature gate")
	}
	if err := configureWatchListClient(true); err != nil {
		t.Fatal(err)
	}
}

// TestAPITransformProductionReadPaths drives the production code behind the
// KEP's two read-path claims through a transform cache:
//
//   - getOwnedPods (instance_reconciler.go) lists through
//     instanceutil.GetActiveAndInactivePods with the ownerReference-UID field
//     index. An owned Pod whose admission labels were all removed must still be
//     returned whole, and an unrelated Pod must never be.
//   - getExistingPod (instance_scale.go) is a cached Get followed by
//     metav1.IsControlledBy. A same-name unrelated Pod must be found as a stub
//     that the RoleInstance does not control, so the name conflict is reported
//     from the cache without an API read.
func TestAPITransformProductionReadPaths(t *testing.T) {
	ns := namespace(t)
	root := roleInstance(t, ns)
	owned := ownedPod(ns, "owned-active", root)
	create(t, owned)
	stripped := ownedPod(ns, "owned-stripped", root)
	create(t, stripped)
	stripped = updateLabels(t, stripped, map[string]string{"keep": "original"})
	// A Pod that will occupy the name the RoleInstance wants next. It has a
	// non-RBG controller and no admission label, so it becomes a stub.
	squatter := pod(ns, "instance-main-0", map[string]string{"app": "other"})
	squatter.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "other", UID: "other-uid", Controller: ptr.To(true)}}
	create(t, squatter)
	// Unrelated noise in the same namespace.
	create(t, pod(ns, "noise", map[string]string{"app": "noise"}))

	cch, _, ctx, cancel := newTransformCache(t, ns, StubIdentityLabelsOwners, true)
	runCache(t, cch, ctx, cancel)
	waitSync(t, cch, ctx)

	// getOwnedPods: the production List helper with the owner index option.
	opts := &client.ListOptions{Namespace: ns, FieldSelector: fields.SelectorFromSet(fields.Set{"ownerRefUID": string(root.UID)})}
	active, inactive, err := instanceutil.GetActiveAndInactivePods(ctx, cch, opts)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range append(active, inactive...) {
		if isStub(p) {
			t.Fatalf("owner-index List returned the stub %s", p.Name)
		}
		names = append(names, p.Name)
	}
	sort.Strings(names)
	if want := []string{"owned-active", "owned-stripped"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("getOwnedPods path returned %v, want %v: an owned Pod without admission labels must not be lost", names, want)
	}

	// getExistingPod: cached Get, then the controller check the create loop makes.
	existing := &corev1.Pod{}
	if err := cch.Get(ctx, client.ObjectKey{Namespace: ns, Name: squatter.Name}, existing); err != nil {
		t.Fatalf("cached Get of the occupied name: %v", err)
	}
	if !isStub(existing) {
		t.Fatalf("occupying Pod cached whole, want a stub: %+v", existing)
	}
	if metav1.IsControlledBy(existing, root) {
		t.Fatal("stub claims RoleInstance control; the name conflict would be missed")
	}
	if ref := metav1.GetControllerOf(existing); ref == nil || ref.Kind != "ReplicaSet" {
		t.Fatalf("stub lost the foreign controller reference: %+v", existing.OwnerReferences)
	}
	// The same check for a name the RoleInstance does own resolves from the cache too.
	mine := &corev1.Pod{}
	if err := cch.Get(ctx, client.ObjectKeyFromObject(stripped), mine); err != nil {
		t.Fatal(err)
	}
	if isStub(mine) || !metav1.IsControlledBy(mine, &workloadsv1alpha2.RoleInstance{ObjectMeta: metav1.ObjectMeta{UID: root.UID}}) {
		t.Fatalf("owned Pod without admission labels not cached whole and controlled: %+v", mine)
	}
	if err := apiClient.Delete(context.Background(), squatter); err != nil {
		t.Fatal(err)
	}
}
