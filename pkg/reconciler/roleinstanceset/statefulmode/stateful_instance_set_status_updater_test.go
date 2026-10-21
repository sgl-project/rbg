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

package statefulmode

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instancelisters "sigs.k8s.io/rbgs/client-go/listers/workloads/v1alpha2"
)

func TestPersistRolloutReplacementState(t *testing.T) {
	for _, name := range []string{"success", "stale version", "write error", "field pruned", "deleting"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			set := newRevisionTestSet("nginx:1.0")
			rolloutStatusExpectations.Delete(set)
			t.Cleanup(func() { rolloutStatusExpectations.Delete(set) })
			set.Status.ReadyReplicas = 3
			set.Status.CurrentRevision = testOldRev
			patches := 0
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(set).WithObjects(set).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						patches++
						if subresource != "status" {
							t.Fatalf("patched subresource %q, want status", subresource)
						}
						switch name {
						case "write error":
							return errors.New("status storage unavailable")
						case "field pruned":
							// An old CRD can accept the patch but drop the new field.
							return c.Get(ctx, client.ObjectKeyFromObject(obj), obj)
						default:
							return c.SubResource(subresource).Patch(ctx, obj, patch, opts...)
						}
					},
				}).Build()
			if err := c.Get(ctx, client.ObjectKeyFromObject(set), set); err != nil {
				t.Fatal(err)
			}
			original := set.DeepCopy()
			if name == "stale version" {
				current := set.DeepCopy()
				current.Annotations = map[string]string{"changed": "true"}
				if err := c.Update(ctx, current); err != nil {
					t.Fatal(err)
				}
			}
			if name == "deleting" {
				now := metav1.Now()
				set.DeletionTimestamp = &now
			}
			state := &workloadsv1alpha2.RolloutReplacementState{UpdateRevision: testUpdateRev, Replicas: 1}
			updater := NewRealStatefulInstanceSetStatusUpdater(c, nil)
			err := updater.PersistRolloutReplacementState(ctx, set, state)
			if (err == nil) != (name == "success") {
				t.Fatalf("persistence error=%v for %s", err, name)
			}
			if name == "deleting" && patches != 0 {
				t.Fatal("attempted to persist replacement intent on a deleting set")
			}
			if patches > 1 {
				t.Fatal("retried a stale replacement-intent write")
			}
			if name == "field pruned" && !strings.Contains(err.Error(), "upgrade the RoleInstanceSet CRD") {
				t.Fatalf("missing CRD upgrade guidance: %v", err)
			}
			if errors.Is(err, errRolloutSpecChanged) != (name == "stale version") {
				t.Fatalf("spec-change error=%v for %s", err, name)
			}
			stored := &workloadsv1alpha2.RoleInstanceSet{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(set), stored); err != nil {
				t.Fatal(err)
			}
			wantStatus := original.Status.DeepCopy()
			if name == "success" {
				wantStatus.RolloutReplacementState = state
				if set.ResourceVersion != stored.ResourceVersion || !reflect.DeepEqual(set.Status.RolloutReplacementState, state) {
					t.Fatal("successful persistence did not refresh the reconcile snapshot")
				}
			} else if set.Status.RolloutReplacementState != nil {
				t.Fatal("failed persistence changed the reconcile snapshot")
			}
			if !reflect.DeepEqual(stored.Status, *wantStatus) {
				t.Fatalf("stored status=%+v, want %+v", stored.Status, *wantStatus)
			}
			assertRolloutStatusExpectation(t, original, stored, name == "success")
		})
	}
}

func TestStatusUpdateDoesNotOverwriteChangedRolloutScope(t *testing.T) {
	for _, change := range []string{"metadata only", "set UID", "generation", "replacement state"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			cached := newRevisionTestSet("nginx:1.0")
			cached.ResourceVersion = "1"
			latest := cached.DeepCopy()
			latest.ResourceVersion = "2"
			switch change {
			case "set UID":
				latest.UID = "recreated-set"
			case "generation":
				latest.Generation++
			case "replacement state":
				latest.Status.RolloutReplacementState = &workloadsv1alpha2.RolloutReplacementState{UpdateRevision: testUpdateRev, Replicas: 1}
			}
			updates := 0
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(latest).WithObjects(latest).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceUpdate: func(ctx context.Context, c client.Client, subresource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						updates++
						return c.SubResource(subresource).Update(ctx, obj, opts...)
					},
				}).Build()
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, nil)
			if err := indexer.Add(latest); err != nil {
				t.Fatal(err)
			}
			updater := NewRealStatefulInstanceSetStatusUpdater(c, instancelisters.NewRoleInstanceSetLister(indexer))
			desired := cached.Status.DeepCopy()
			desired.ReadyReplicas = 1
			err := updater.UpdateInstanceSetStatus(ctx, cached, desired)
			if change == "metadata only" {
				if err != nil || updates != 2 {
					t.Fatalf("safe conflict retry: error=%v, writes=%d, want success after two writes", err, updates)
				}
			} else if err != nil || updates != 1 {
				t.Fatalf("stale status write: error=%v, writes=%d, want to discard after one write", err, updates)
			}
			stored := &workloadsv1alpha2.RoleInstanceSet{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(latest), stored); err != nil {
				t.Fatal(err)
			}
			wantStatus := latest.Status
			if change == "metadata only" {
				wantStatus = *desired
			} else if cached.Status.ReadyReplicas != 0 {
				t.Fatal("discarded write mutated the reconcile snapshot")
			}
			if !reflect.DeepEqual(stored.Status, wantStatus) {
				t.Fatalf("overwrote status across %s change: got %+v, want %+v", change, stored.Status, wantStatus)
			}
		})
	}
}

func TestPersistRolloutReplacementStateRejectsChangedSet(t *testing.T) {
	for _, change := range []string{"set UID", "generation", "deleting"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			set := newRevisionTestSet("nginx:1.0")
			set.ResourceVersion = "1"
			state := &workloadsv1alpha2.RolloutReplacementState{UpdateRevision: testUpdateRev, Replicas: 1}
			rolloutStatusExpectations.Delete(set)
			t.Cleanup(func() { rolloutStatusExpectations.Delete(set) })
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(set).WithObjects(set).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, c client.Client, _ string, obj client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
						if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
							return err
						}
						updated := obj.(*workloadsv1alpha2.RoleInstanceSet)
						updated.Status.RolloutReplacementState = state
						switch change {
						case "set UID":
							updated.UID = "recreated-set"
						case "generation":
							updated.Generation++
						case "deleting":
							now := metav1.Now()
							updated.DeletionTimestamp = &now
						}
						return nil
					},
				}).Build()
			updater := NewRealStatefulInstanceSetStatusUpdater(c, nil)
			if err := updater.PersistRolloutReplacementState(ctx, set, state); !errors.Is(err, errRolloutSpecChanged) {
				t.Fatalf("expected spec-change interruption for %s, got %v", change, err)
			}
			if set.Status.RolloutReplacementState != nil || set.ResourceVersion != "1" {
				t.Fatal("rejected intent write mutated the reconcile snapshot")
			}
			if satisfied, _ := rolloutStatusExpectations.IsSatisfied(set); !satisfied {
				t.Fatal("rejected intent write registered a cache expectation")
			}
		})
	}
}

func TestStatusUpdateTracksChangedRolloutStateResourceVersion(t *testing.T) {
	for _, change := range []string{"write state", "clear state", "counts only"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			set := newRevisionTestSet("nginx:1.0")
			set.ResourceVersion = "1"
			if change == "clear state" {
				set.Status.RolloutReplacementState = &workloadsv1alpha2.RolloutReplacementState{UpdateRevision: testUpdateRev, Replicas: 1}
			}
			rolloutStatusExpectations.Delete(set)
			t.Cleanup(func() { rolloutStatusExpectations.Delete(set) })
			original := set.DeepCopy()
			desired := set.Status.DeepCopy()
			desired.ReadyReplicas = 1
			if change == "write state" {
				desired.RolloutReplacementState = &workloadsv1alpha2.RolloutReplacementState{UpdateRevision: testUpdateRev, Replicas: 1}
			} else {
				desired.RolloutReplacementState = nil
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(set).WithObjects(set).Build()
			updater := NewRealStatefulInstanceSetStatusUpdater(c, nil)
			if err := updater.UpdateInstanceSetStatus(ctx, set, desired); err != nil {
				t.Fatal(err)
			}
			stored := &workloadsv1alpha2.RoleInstanceSet{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(set), stored); err != nil {
				t.Fatal(err)
			}
			if set.ResourceVersion != stored.ResourceVersion || set.ResourceVersion == original.ResourceVersion {
				t.Fatalf("status write returned resourceVersion %q, stored %q, original %q", set.ResourceVersion, stored.ResourceVersion, original.ResourceVersion)
			}
			assertRolloutStatusExpectation(t, original, stored, change != "counts only")
		})
	}
}

func assertRolloutStatusExpectation(t *testing.T, cached, stored *workloadsv1alpha2.RoleInstanceSet, wantPending bool) {
	t.Helper()
	if satisfied, _ := rolloutStatusExpectations.IsSatisfied(cached); satisfied == wantPending {
		t.Fatalf("cached resourceVersion %q satisfied=%t, want pending=%t", cached.ResourceVersion, satisfied, wantPending)
	}
	if satisfied, _ := rolloutStatusExpectations.IsSatisfied(stored); !satisfied {
		t.Fatalf("returned resourceVersion %q did not satisfy the status expectation", stored.ResourceVersion)
	}
}
