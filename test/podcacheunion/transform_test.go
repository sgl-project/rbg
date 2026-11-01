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
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	rbgclient "sigs.k8s.io/rbgs/pkg/utils/client"
)

func newTransformCache(t *testing.T, ns string, stub StubFields, wrap bool) (cache.Cache, toolscache.SharedIndexInformer, context.Context, context.CancelFunc) {
	t.Helper()
	options := cache.Options{Scheme: probeScheme, ByObject: map[client.Object]cache.ByObject{
		&corev1.Pod{}: {Field: fields.OneTermEqualSelector("metadata.namespace", ns), Transform: PodCacheTransform(stub)},
	}}
	if wrap {
		options.NewInformer = WatchStreamTransformInformer(PodCacheTransform(stub))
	}
	return newCacheWithOptions(t, options)
}

func TestTransformIsIdempotent(t *testing.T) {
	for _, stub := range []StubFields{StubIdentity, StubIdentityLabelsOwners} {
		transform := PodCacheTransform(stub)
		admittedPod := pod("ns", "admitted", map[string]string{groupLabel: "g"})
		admittedPod.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "test"}}
		admittedPod.Status.Phase = corev1.PodRunning
		other := pod("ns", "other", map[string]string{"app": "x"})
		other.UID, other.ResourceVersion = "uid-other", "7"
		other.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs", UID: "uid-rs", Controller: ptr.To(true)}}
		other.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "test"}}
		now := metav1.Now()
		other.DeletionTimestamp = &now
		other.Annotations = map[string]string{"drop": "annotation"}
		other.Finalizers = []string{"drop/finalizer"}
		other.Spec.NodeName = "drop-node"
		other.Status.Phase = corev1.PodRunning
		for _, in := range []*corev1.Pod{admittedPod, other} {
			once, err := transform(in.DeepCopy())
			if err != nil {
				t.Fatal(err)
			}
			twice, err := transform(once.(*corev1.Pod).DeepCopy())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(once, twice) {
				t.Fatalf("stub=%d %s: transform is not idempotent:\nonce=%+v\ntwice=%+v", stub, in.Name, once, twice)
			}
			if once.(*corev1.Pod).ManagedFields != nil {
				t.Fatalf("stub=%d %s: managedFields retained", stub, in.Name)
			}
			want := in.DeepCopy()
			want.ManagedFields = nil
			if in == other {
				want = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: in.Name, Namespace: in.Namespace, UID: in.UID, ResourceVersion: in.ResourceVersion, DeletionTimestamp: in.DeletionTimestamp}}
				if stub == StubIdentityLabelsOwners {
					want.Labels, want.OwnerReferences = in.Labels, in.OwnerReferences
				}
			}
			if !reflect.DeepEqual(once, want) {
				t.Fatalf("stub=%d %s: unexpected retained fields: %+v", stub, in.Name, once)
			}
		}
		// DeltaFIFO never transforms a deletion tombstone; any non-Pod input
		// passes through unchanged.
		tombstone := toolscache.DeletedFinalStateUnknown{Key: "ns/other", Obj: other}
		out, err := transform(tombstone)
		if err != nil || !reflect.DeepEqual(out, tombstone) {
			t.Fatalf("stub=%d: tombstone changed: %+v, %v", stub, out, err)
		}
	}
}

// Change admission while the old watch is stopped, before delivering 410.
// Only the replacement snapshot can repair these cache entries.
func TestAPITransformRelist(t *testing.T) {
	ns := namespace(t)
	demoted := pod(ns, "demoted", map[string]string{groupLabel: "g"})
	promoted := pod(ns, "promoted", nil)
	create(t, demoted)
	create(t, promoted)
	probe := newRelistProbe()
	changed := make(chan error, 1)
	probe.afterExpiry = func() {
		demoted.Labels, promoted.Labels = nil, map[string]string{managedLabel: "true"}
		if err := apiClient.Update(context.Background(), demoted); err != nil {
			changed <- err
			return
		}
		changed <- apiClient.Update(context.Background(), promoted)
	}
	transform := PodCacheTransform(StubIdentityLabelsOwners)
	cch, _, ctx, cancel := newCacheWithOptions(t, cache.Options{
		Scheme: probeScheme, NewInformer: streamTransformRelistInformer(probe, transform),
		ByObject: map[client.Object]cache.ByObject{&corev1.Pod{}: {
			Field: fields.OneTermEqualSelector("metadata.namespace", ns), Transform: transform,
		}},
	})
	runCache(t, cch, ctx, cancel)
	waitSync(t, cch, ctx)
	eventually(t, "initial watch opened", func() bool {
		probe.mu.Lock()
		defer probe.mu.Unlock()
		return len(probe.open) == 1
	})
	trigger := time.Now()
	if n := probe.expireWatches(); n != 1 {
		t.Fatalf("expired %d watches, want 1", n)
	}
	select {
	case err := <-changed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("watch expiry did not change the snapshot")
	}
	eventually(t, "410 relist applies changed admission and resourceVersions", func() bool {
		if _, _, ok := probe.relistCompletion(trigger); !ok {
			return false
		}
		for _, want := range []*corev1.Pod{demoted, promoted} {
			got := &corev1.Pod{}
			if err := cch.Get(ctx, client.ObjectKeyFromObject(want), got); err != nil {
				return false
			}
			if got.ResourceVersion != want.ResourceVersion || isStub(got) != (want.Name == demoted.Name) {
				return false
			}
		}
		return true
	})
}

func TestAPITransformAdmissionAndStubs(t *testing.T) {
	for _, variant := range []struct {
		name string
		stub StubFields
		wrap bool
	}{{"identity", StubIdentity, false}, {"labels-owners", StubIdentityLabelsOwners, false}, {"labels-owners-stream", StubIdentityLabelsOwners, true}} {
		t.Run(variant.name, func(t *testing.T) {
			ns := namespace(t)
			root := roleInstance(t, ns)
			controllerRef := metav1.OwnerReference{APIVersion: workloadsv1alpha2.GroupVersion.String(), Kind: "RoleInstance", Name: root.Name, UID: root.UID, Controller: ptr.To(true)}
			nonControllerRef := controllerRef
			nonControllerRef.Controller = ptr.To(false)
			foreignRef := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "foreign", UID: types.UID("foreign-uid"), Controller: ptr.To(true)}
			cases := []struct {
				name   string
				labels map[string]string
				owners []metav1.OwnerReference
				full   bool
			}{
				{"group", map[string]string{groupLabel: "g"}, nil, true},
				{"instance", map[string]string{instanceLabel: "i"}, nil, true},
				{"warmup", map[string]string{warmupLabel: "w"}, nil, true},
				{"managed", map[string]string{managedLabel: "true"}, nil, true},
				// A legacy label admits a Pod even when the marker is false.
				{"group-managed-false", map[string]string{groupLabel: "g", managedLabel: "false"}, nil, true},
				{"managed-false", map[string]string{managedLabel: "false"}, nil, false},
				// Admission checks label keys: an empty value still admits.
				{"group-empty-value", map[string]string{groupLabel: ""}, nil, true},
				{"rbg-controlled-unlabeled", nil, []metav1.OwnerReference{controllerRef}, true},
				{"rbg-non-controller-ref", nil, []metav1.OwnerReference{nonControllerRef}, true},
				// An RBG reference admits a Pod even when another kind controls it.
				{"foreign-controller-rbg-ref", nil, []metav1.OwnerReference{foreignRef, nonControllerRef}, true},
				{"foreign", map[string]string{"app": "foreign"}, []metav1.OwnerReference{foreignRef}, false},
			}
			for _, c := range cases {
				p := pod(ns, c.name, c.labels)
				p.OwnerReferences = c.owners
				create(t, p)
				p.Status.Phase = corev1.PodRunning
				if err := apiClient.Status().Update(context.Background(), p); err != nil {
					t.Fatal(err)
				}
			}
			cch, _, ctx, cancel := newTransformCache(t, ns, variant.stub, variant.wrap)
			runCache(t, cch, ctx, cancel)
			waitSync(t, cch, ctx)

			for _, c := range cases {
				server := current(t, pod(ns, c.name, nil))
				cached := &corev1.Pod{}
				if err := cch.Get(ctx, client.ObjectKey{Namespace: ns, Name: c.name}, cached); err != nil {
					t.Fatalf("%s: cached Get: %v", c.name, err)
				}
				if c.full {
					if isStub(cached) || cached.ManagedFields != nil || !reflect.DeepEqual(cached.Spec, server.Spec) || !reflect.DeepEqual(cached.Status, server.Status) || !reflect.DeepEqual(cached.Labels, server.Labels) {
						t.Fatalf("%s: admitted Pod not cached whole without managedFields: %+v", c.name, cached)
					}
					continue
				}
				// A stub still answers "does this name exist, and is it ours?".
				if !reflect.DeepEqual(cached.Spec, corev1.PodSpec{}) || cached.Name != server.Name || cached.Namespace != server.Namespace || cached.UID != server.UID || cached.ResourceVersion != server.ResourceVersion || !reflect.DeepEqual(cached.Status, corev1.PodStatus{}) {
					t.Fatalf("%s: unexpected stub %+v", c.name, cached)
				}
				if metav1.IsControlledBy(cached, root) {
					t.Fatalf("%s: stub claims RoleInstance control", c.name)
				}
				var wantLabels map[string]string
				var wantOwners []metav1.OwnerReference
				if variant.stub == StubIdentityLabelsOwners {
					wantLabels, wantOwners = server.Labels, server.OwnerReferences
				}
				if !reflect.DeepEqual(cached.Labels, wantLabels) || !reflect.DeepEqual(cached.OwnerReferences, wantOwners) {
					t.Fatalf("%s: stub fields labels=%v owners=%v", c.name, cached.Labels, cached.OwnerReferences)
				}
			}

			// Every production Pod List is scoped by an RBG label selector or the
			// owner index. Label-scoped reads, deep-copying or not, never see a stub.
			for _, check := range []struct {
				selector client.ListOption
				want     []string
			}{
				{client.HasLabels{groupLabel}, []string{"group", "group-empty-value", "group-managed-false"}},
				{client.HasLabels{instanceLabel}, []string{"instance"}},
				{client.HasLabels{warmupLabel}, []string{"warmup"}},
				{client.MatchingLabels{managedLabel: "true"}, []string{"managed"}},
			} {
				for _, opts := range [][]client.ListOption{{client.InNamespace(ns), check.selector}, {client.InNamespace(ns), check.selector, rbgclient.DisableDeepCopy}} {
					list := &corev1.PodList{}
					if err := cch.List(ctx, list, opts...); err != nil {
						t.Fatal(err)
					}
					var got []string
					for i := range list.Items {
						if isStub(&list.Items[i]) {
							t.Fatalf("selector %v returned the stub %s", check.selector, list.Items[i].Name)
						}
						got = append(got, list.Items[i].Name)
					}
					sort.Strings(got)
					if !reflect.DeepEqual(got, check.want) {
						t.Fatalf("selector %v returned %v, want %v", check.selector, got, check.want)
					}
				}
			}

			// A selector on other labels evaluates a stub as it would the whole
			// Pod only when the stub keeps its labels, as the KEP's stub does.
			wantForeign := 0
			if variant.stub == StubIdentityLabelsOwners {
				wantForeign = 1
			}
			if count := countPods(cch, ctx, client.InNamespace(ns), client.MatchingLabels{"app": "foreign"}); count != wantForeign {
				t.Fatalf("app=foreign selector returned %d Pods, want %d", count, wantForeign)
			}

			// The owner index finds every Pod that references the RoleInstance,
			// whole, without admission labels. Any RBG ownerReference admits a Pod,
			// so no stub of either field set can answer an owner-index lookup.
			owned := &corev1.PodList{}
			if err := cch.List(ctx, owned, client.InNamespace(ns), client.MatchingFields{"ownerRefUID": string(root.UID)}); err != nil {
				t.Fatal(err)
			}
			var got, stubs []string
			for i := range owned.Items {
				got = append(got, owned.Items[i].Name)
				if isStub(&owned.Items[i]) {
					stubs = append(stubs, owned.Items[i].Name)
				}
			}
			sort.Strings(got)
			if want := []string{"foreign-controller-rbg-ref", "rbg-controlled-unlabeled", "rbg-non-controller-ref"}; !reflect.DeepEqual(got, want) || stubs != nil {
				t.Fatalf("owner index returned %v (stubs %v), want %v and no stubs", got, stubs, want)
			}

			// Only an unscoped List sees stubs, which is why the Pod read-path
			// audit is an alpha criterion.
			if count := countPods(cch, ctx, client.InNamespace(ns)); count != len(cases) {
				t.Fatalf("unscoped List returned %d Pods, want %d", count, len(cases))
			}
		})
	}
}

func TestAPITransformTransitionsAndEvents(t *testing.T) {
	ns := namespace(t)
	root := roleInstance(t, ns)
	cch, informer, ctx, cancel := newTransformCache(t, ns, StubIdentityLabelsOwners, true)
	var mu sync.Mutex
	var events []string
	record := func(kind string, obj interface{}) {
		p, ok := obj.(*corev1.Pod)
		if !ok {
			if tombstone, isTombstone := obj.(toolscache.DeletedFinalStateUnknown); isTombstone {
				p, _ = tombstone.Obj.(*corev1.Pod)
			}
		}
		if p == nil {
			t.Errorf("%s event with unexpected object %T", kind, obj)
			return
		}
		state := "full"
		if isStub(p) {
			state = "stub"
		}
		mu.Lock()
		events = append(events, kind+":"+p.Name+":"+state)
		mu.Unlock()
	}
	if _, err := informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { record("add", obj) },
		UpdateFunc: func(_, obj interface{}) { record("update", obj) },
		DeleteFunc: func(obj interface{}) { record("delete", obj) },
	}); err != nil {
		t.Fatal(err)
	}
	runCache(t, cch, ctx, cancel)
	waitSync(t, cch, ctx)
	state := func(name string) string {
		p := &corev1.Pod{}
		err := cch.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, p)
		switch {
		case apierrors.IsNotFound(err):
			return "absent"
		case err != nil:
			t.Fatal(err)
		case isStub(p):
			return "stub"
		}
		return "full"
	}

	unrelated := pod(ns, "transition", map[string]string{"app": "x"})
	create(t, unrelated)
	eventually(t, "unrelated Pod cached as a stub", func() bool { return state("transition") == "stub" })
	updateLabels(t, unrelated, map[string]string{"app": "x", groupLabel: "g"})
	eventually(t, "Pod admitted whole once labelled", func() bool { return state("transition") == "full" })
	updateLabels(t, unrelated, map[string]string{"app": "x"})
	eventually(t, "Pod reduced to a stub when its last admission label is removed", func() bool { return state("transition") == "stub" })

	owned := ownedPod(ns, "owned", root)
	create(t, owned)
	eventually(t, "owned Pod admitted", func() bool { return state("owned") == "full" })
	updateLabels(t, owned, map[string]string{"keep": "original"})
	eventually(t, "RBG-controlled Pod stays whole without admission labels", func() bool {
		p := &corev1.Pod{}
		if err := cch.Get(ctx, client.ObjectKeyFromObject(owned), p); err != nil {
			t.Fatal(err)
		}
		_, labelled := p.Labels[instanceLabel]
		return !isStub(p) && !labelled
	})
	if found := countPods(cch, ctx, client.InNamespace(ns), client.MatchingFields{"ownerRefUID": string(root.UID)}); found != 1 {
		t.Fatalf("owner index found %d Pods, want the unlabelled owned Pod", found)
	}

	for _, p := range []*corev1.Pod{unrelated, owned} {
		if err := apiClient.Delete(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "both deletions observed", func() bool { return state("transition") == "absent" && state("owned") == "absent" })
	eventually(t, "transition and deletion events delivered", func() bool {
		mu.Lock()
		defer mu.Unlock()
		seen := map[string]bool{}
		for _, event := range events {
			seen[event] = true
		}
		return seen["add:transition:stub"] && seen["update:transition:full"] && seen["update:transition:stub"] &&
			seen["delete:transition:stub"] && seen["delete:owned:full"]
	})
}
