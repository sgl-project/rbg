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

package lifecycle

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestHookControlEnterAndCleanup(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "default"},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				instance, workloadsv1alpha2.GroupVersion.WithKind("RoleInstance"))},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, pod).Build()
	if err := c.Get(ctx, client.ObjectKeyFromObject(instance), instance); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
		t.Fatal(err)
	}
	control := NewHookControl(c)
	hook := &workloadsv1alpha2.RoleInstanceLifecycleHook{
		SignalLabels: map[string]string{"example.com/update": "true"},
	}
	lifecycle := &workloadsv1alpha2.RoleInstanceLifecycle{PreUpdate: hook}

	if err := control.EnterHook(ctx, instance, constants.RoleInstanceHookStatePreUpdate, hook, []*corev1.Pod{pod}); err != nil {
		t.Fatalf("EnterHook() error = %v", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(instance), instance); err != nil {
		t.Fatal(err)
	}

	if got := instance.Labels[constants.RoleInstanceLifecycleHookStateKey]; got != string(constants.RoleInstanceHookStatePreUpdate) {
		t.Fatalf("hook state = %q, want %q", got, constants.RoleInstanceHookStatePreUpdate)
	}
	if got := instance.Labels["example.com/update"]; got != "true" {
		t.Fatalf("RoleInstance signal label = %q, want true", got)
	}
	if got := instance.Annotations[constants.RoleInstanceLifecycleHookSignalLabelKeys]; got != "example.com/update" {
		t.Fatalf("signal key record = %q, want example.com/update", got)
	}
	storedPod := &corev1.Pod{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(pod), storedPod); err != nil {
		t.Fatal(err)
	}
	if got := storedPod.Labels["example.com/update"]; got != "true" {
		t.Fatalf("Pod mirror = %q, want true", got)
	}

	storedInstance := &workloadsv1alpha2.RoleInstance{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(instance), storedInstance); err != nil {
		t.Fatal(err)
	}
	delete(storedInstance.Labels, "example.com/update")
	if err := c.Update(ctx, storedInstance); err != nil {
		t.Fatal(err)
	}
	if err := control.SyncRuntimeLabels(ctx, storedInstance, lifecycle, []*corev1.Pod{storedPod}); err != nil {
		t.Fatalf("SyncRuntimeLabels() error = %v", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(storedPod), storedPod); err != nil {
		t.Fatal(err)
	}
	if value, exists := storedPod.Labels["example.com/update"]; exists {
		t.Fatalf("Pod mirror = %q, want absent", value)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(storedInstance), storedInstance); err != nil {
		t.Fatal(err)
	}
	if value, exists := storedInstance.Annotations[constants.RoleInstanceLifecycleHookSignalLabelKeys]; exists {
		t.Fatalf("signal key record = %q, want absent", value)
	}
}

func TestHookControlPreDeleteFinalizer(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "default"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()
	control := NewHookControl(c)

	added, err := control.EnsurePreDeleteFinalizer(ctx, instance)
	if err != nil {
		t.Fatalf("EnsurePreDeleteFinalizer() error = %v", err)
	}
	if !added || !contains(instance, constants.RoleInstancePreDeleteFinalizer) {
		t.Fatalf("EnsurePreDeleteFinalizer() added = %v, finalizers = %v", added, instance.Finalizers)
	}
	removed, err := control.RemovePreDeleteFinalizer(ctx, instance)
	if err != nil {
		t.Fatalf("RemovePreDeleteFinalizer() error = %v", err)
	}
	if !removed || contains(instance, constants.RoleInstancePreDeleteFinalizer) {
		t.Fatalf("RemovePreDeleteFinalizer() removed = %v, finalizers = %v", removed, instance.Finalizers)
	}
}

// A plain merge patch replaces the whole finalizer array, so the patch must
// carry the resourceVersion and conflict when another writer changed it.
func TestHookControlFinalizerPatchConflictsOnConcurrentWriter(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	stored := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "default"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stored).Build()
	control := NewHookControl(c)

	// Install our finalizer first.
	if err := c.Get(ctx, client.ObjectKeyFromObject(stored), stored); err != nil {
		t.Fatal(err)
	}
	if _, err := control.EnsurePreDeleteFinalizer(ctx, stored); err != nil {
		t.Fatalf("EnsurePreDeleteFinalizer() error = %v", err)
	}

	// Another writer adds a finalizer after our read.
	stale := stored.DeepCopy()
	live := &workloadsv1alpha2.RoleInstance{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(stored), live); err != nil {
		t.Fatal(err)
	}
	live.Finalizers = append(live.Finalizers, "other.example.com/backup")
	if err := c.Update(ctx, live); err != nil {
		t.Fatal(err)
	}

	// Removing our finalizer from the stale view must conflict instead of
	// replacing the array and dropping the other writer's finalizer.
	_, err := control.RemovePreDeleteFinalizer(ctx, stale)
	if err == nil {
		t.Fatal("RemovePreDeleteFinalizer() must conflict when the object changed concurrently")
	}

	// After re-reading, the removal keeps the other finalizer.
	fresh := &workloadsv1alpha2.RoleInstance{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(stored), fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := control.RemovePreDeleteFinalizer(ctx, fresh); err != nil {
		t.Fatalf("RemovePreDeleteFinalizer() after re-read error = %v", err)
	}
	final := &workloadsv1alpha2.RoleInstance{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(stored), final); err != nil {
		t.Fatal(err)
	}
	if !contains(final, "other.example.com/backup") {
		t.Fatalf("concurrent finalizer was dropped: %v", final.Finalizers)
	}
}

func TestPendingSignalsRequiresLabelPresence(t *testing.T) {
	hook := &workloadsv1alpha2.RoleInstanceLifecycleHook{
		SignalLabels: map[string]string{"example.com/drain": ""},
	}
	released := &workloadsv1alpha2.RoleInstance{}
	if !HookReleased(released, hook) {
		t.Fatal("an absent label must release an empty-valued signal")
	}
	pending := &workloadsv1alpha2.RoleInstance{}
	pending.Labels = map[string]string{"example.com/drain": ""}
	if got := PendingSignals(pending, hook); len(got) != 1 || got[0] != "example.com/drain" {
		t.Fatalf("PendingSignals() = %v, want [example.com/drain]", got)
	}
}

func TestHookControlMirrorsEmptyValuedSignal(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "default"},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				instance, workloadsv1alpha2.GroupVersion.WithKind("RoleInstance"))},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, pod).Build()
	if err := c.Get(ctx, client.ObjectKeyFromObject(instance), instance); err != nil {
		t.Fatal(err)
	}

	hook := &workloadsv1alpha2.RoleInstanceLifecycleHook{
		SignalLabels: map[string]string{"example.com/drain": ""},
	}
	control := NewHookControl(c)
	if err := control.EnterHook(
		ctx, instance, constants.RoleInstanceHookStatePreUpdate, hook, []*corev1.Pod{pod}); err != nil {
		t.Fatalf("EnterHook() error = %v", err)
	}

	storedPod := &corev1.Pod{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(pod), storedPod); err != nil {
		t.Fatal(err)
	}
	if value, exists := storedPod.Labels["example.com/drain"]; !exists || value != "" {
		t.Fatalf("Pod mirror = (%q, %v), want a present label with an empty value", value, exists)
	}
}

func contains(obj metav1.Object, finalizer string) bool {
	for _, candidate := range obj.GetFinalizers() {
		if candidate == finalizer {
			return true
		}
	}
	return false
}
