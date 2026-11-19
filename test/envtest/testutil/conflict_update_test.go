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

package testutil

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	if err := workloadsv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("add workloadsv1alpha2 to scheme: %v", err)
	}
	return scheme
}

// swapClient installs c as the package client for the test and restores the
// previous one afterwards.
func swapClient(t *testing.T, c client.Client) {
	t.Helper()
	previous := K8sClient
	K8sClient = c
	t.Cleanup(func() { K8sClient = previous })
}

// TestUpdatePodStatusOnConflictRetriesAndRereads pins the retry contract
// deterministically: one injected 409 must cause a fresh read of the live
// object (not a retry of the stale resourceVersion) and a reapplication of
// the mutation.
func TestUpdatePodStatusOnConflictRetriesAndRereads(t *testing.T) {
	ctx := context.Background()
	base := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-a", Namespace: "default"},
	}

	var statusWrites int32
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(base).
		WithStatusSubresource(&corev1.Pod{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if subResourceName != "status" {
					return c.Status().Update(ctx, obj, opts...)
				}
				n := atomic.AddInt32(&statusWrites, 1)
				if n == 1 {
					return apierrors.NewConflict(
						schema.GroupResource{Group: "", Resource: "pods"},
						obj.GetName(), errors.New("the object has been modified"))
				}
				return c.Status().Update(ctx, obj, opts...)
			},
		}).Build()
	swapClient(t, c)

	// The mutation counter proves the mutation is reapplied on the reread
	// object, not carried over from the first attempt.
	var mutations int32
	err := UpdatePodStatusOnConflict(ctx, client.ObjectKeyFromObject(base), func(pod *corev1.Pod) {
		atomic.AddInt32(&mutations, 1)
		pod.Status.Phase = corev1.PodFailed
	})
	if err != nil {
		t.Fatalf("expected the conflict to be retried, got %v", err)
	}
	if got := atomic.LoadInt32(&statusWrites); got != 2 {
		t.Fatalf("expected 2 status writes (one 409, one success), got %d", got)
	}
	if got := atomic.LoadInt32(&mutations); got != 2 {
		t.Fatalf("expected the mutation to be reapplied on the retry, got %d applications", got)
	}

	got := &corev1.Pod{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(base), got); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if got.Status.Phase != corev1.PodFailed {
		t.Fatalf("phase = %q, want Failed", got.Status.Phase)
	}
}

// TestUpdatePodStatusOnConflictRefusesReplacement aborts instead of silently
// applying the mutation to a replacement object that reuses the name.
func TestUpdatePodStatusOnConflictRefusesReplacement(t *testing.T) {
	ctx := context.Background()
	base := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-a", Namespace: "default", UID: types.UID("original-uid")},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(base).
		WithStatusSubresource(&corev1.Pod{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if subResourceName != "status" {
					return c.Status().Update(ctx, obj, opts...)
				}
				// Simulate delete + same-name recreate between attempts, then
				// fail the write so the helper retries: the retry's fresh
				// read must observe the different UID and abort.
				replacement := base.DeepCopy()
				replacement.UID = types.UID("replacement-uid")
				replacement.ResourceVersion = ""
				if err := c.Delete(ctx, base); err != nil {
					return err
				}
				if err := c.Create(ctx, replacement); err != nil {
					return err
				}
				return apierrors.NewConflict(
					schema.GroupResource{Group: "", Resource: "pods"},
					obj.GetName(), errors.New("the object has been modified"))
			},
		}).Build()
	swapClient(t, c)

	err := UpdatePodStatusOnConflict(ctx, client.ObjectKeyFromObject(base), func(pod *corev1.Pod) {
		pod.Status.Phase = corev1.PodFailed
	})
	if err == nil {
		t.Fatal("expected an error when the object is replaced under the same name")
	}
	if !strings.Contains(err.Error(), "was replaced while updating") {
		t.Fatalf("expected a replacement identity error, got %v", err)
	}

	got := &corev1.Pod{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(base), got); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if got.Status.Phase != "" {
		t.Fatalf("the replacement pod must not be mutated by the fixture, phase = %q", got.Status.Phase)
	}
}

// TestUpdateRoleInstanceStatusOnConflictRetries covers the RoleInstance
// variant of the retry contract.
func TestUpdateRoleInstanceStatusOnConflictRetries(t *testing.T) {
	ctx := context.Background()
	base := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "ri-a", Namespace: "default"},
	}
	var statusWrites int32
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(base).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				n := atomic.AddInt32(&statusWrites, 1)
				if n == 1 {
					return apierrors.NewConflict(
						schema.GroupResource{Group: "workloads.x-k8s.io", Resource: "roleinstances"},
						obj.GetName(), errors.New("the object has been modified"))
				}
				return c.Status().Update(ctx, obj, opts...)
			},
		}).Build()
	swapClient(t, c)

	err := UpdateRoleInstanceStatusOnConflict(ctx, client.ObjectKeyFromObject(base), func(ri *workloadsv1alpha2.RoleInstance) {
		ri.Status.RestartCount = 7
	})
	if err != nil {
		t.Fatalf("expected the conflict to be retried, got %v", err)
	}
	if got := atomic.LoadInt32(&statusWrites); got != 2 {
		t.Fatalf("expected 2 status writes, got %d", got)
	}

	got := &workloadsv1alpha2.RoleInstance{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(base), got); err != nil {
		t.Fatalf("get roleinstance: %v", err)
	}
	if got.Status.RestartCount != 7 {
		t.Fatalf("restartCount = %d, want 7", got.Status.RestartCount)
	}
}
