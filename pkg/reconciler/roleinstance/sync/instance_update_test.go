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

package sync

import (
	"context"
	"testing"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	podinplaceupdate "sigs.k8s.io/rbgs/pkg/inplace/pod/inplaceupdate"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

// fakeInplaceControl implements inplaceupdate.Interface for testing.
type fakeInplaceControl struct {
	updateFn func(ctx context.Context, pod *v1.Pod, oldRevision, newRevision *apps.ControllerRevision, opts *podinplaceupdate.UpdateOptions) podinplaceupdate.UpdateResult
}

func (f *fakeInplaceControl) CanUpdateInPlace(_ context.Context, _, _ *apps.ControllerRevision, _ *podinplaceupdate.UpdateOptions) bool {
	return false
}

func (f *fakeInplaceControl) Refresh(_ context.Context, _ *v1.Pod, _ *podinplaceupdate.UpdateOptions) podinplaceupdate.RefreshResult {
	return podinplaceupdate.RefreshResult{}
}

func (f *fakeInplaceControl) Update(ctx context.Context, pod *v1.Pod, oldRevision, newRevision *apps.ControllerRevision, opts *podinplaceupdate.UpdateOptions) podinplaceupdate.UpdateResult {
	if f.updateFn != nil {
		return f.updateFn(ctx, pod, oldRevision, newRevision, opts)
	}
	return podinplaceupdate.UpdateResult{}
}

// fakeLifecycleControl implements lifecycle.Interface for testing.
type fakeLifecycleControl struct{}

func (f *fakeLifecycleControl) UpdatePodLifecycle(_ *workloadsv1alpha2.RoleInstance, _ *v1.Pod, _ bool) (bool, *v1.Pod, error) {
	return false, nil, nil
}

func TestUpdatePod(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = v1.AddToScheme(scheme)
	_ = apps.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance",
			Namespace: "default",
			UID:       "test-uid",
		},
	}

	tests := []struct {
		name             string
		podRevisionHash  string
		revisionNames    []string
		updateFn         func(ctx context.Context, pod *v1.Pod, oldRevision, newRevision *apps.ControllerRevision, opts *podinplaceupdate.UpdateOptions) podinplaceupdate.UpdateResult
		expectPodDeleted bool
	}{
		{
			name:            "nil oldRevision falls back to recreate",
			podRevisionHash: "nonexistent-revision",
			revisionNames:   []string{"rev-abc123", "rev-def456"},
			updateFn: func(_ context.Context, _ *v1.Pod, _, _ *apps.ControllerRevision, _ *podinplaceupdate.UpdateOptions) podinplaceupdate.UpdateResult {
				t.Fatal("inplaceControl.Update should not be called when oldRevision is nil")
				return podinplaceupdate.UpdateResult{}
			},
			expectPodDeleted: true,
		},
		{
			name:            "matching revision, in-place update succeeds",
			podRevisionHash: "rev-abc123",
			revisionNames:   []string{"rev-abc123", "rev-def456"},
			updateFn: func(_ context.Context, _ *v1.Pod, oldRevision, _ *apps.ControllerRevision, _ *podinplaceupdate.UpdateOptions) podinplaceupdate.UpdateResult {
				if oldRevision == nil {
					t.Fatal("oldRevision should not be nil when a matching revision exists")
				}
				return podinplaceupdate.UpdateResult{InPlaceUpdate: true, NewResourceVersion: "999"}
			},
			expectPodDeleted: false,
		},
		{
			name:            "matching revision, in-place not possible falls back to recreate",
			podRevisionHash: "rev-abc123",
			revisionNames:   []string{"rev-abc123", "rev-def456"},
			updateFn: func(_ context.Context, _ *v1.Pod, _ *apps.ControllerRevision, _ *apps.ControllerRevision, _ *podinplaceupdate.UpdateOptions) podinplaceupdate.UpdateResult {
				return podinplaceupdate.UpdateResult{InPlaceUpdate: false}
			},
			expectPodDeleted: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test-pod",
					Namespace:       "default",
					UID:             types.UID(tt.name),
					ResourceVersion: "1",
					Labels: map[string]string{
						apps.ControllerRevisionHashLabelKey: tt.podRevisionHash,
					},
				},
			}
			t.Cleanup(func() {
				instanceutil.ResourceVersionExpectations.Delete(pod)
			})
			updateRevision := &apps.ControllerRevision{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rev-def456",
					Namespace: "default",
				},
			}
			var revisions []*apps.ControllerRevision
			for _, name := range tt.revisionNames {
				revisions = append(revisions, &apps.ControllerRevision{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: "default",
					},
				})
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(pod).
				Build()

			recorder := record.NewFakeRecorder(10)

			ctrl := &realControl{
				Client:           fakeClient,
				inplaceControl:   &fakeInplaceControl{updateFn: tt.updateFn},
				recorder:         recorder,
				lifecycleControl: &fakeLifecycleControl{},
			}

			_, err := ctrl.updatePod(context.Background(), instance, updateRevision, revisions, pod)
			if err != nil {
				t.Fatalf("updatePod returned unexpected error: %v", err)
			}

			// Check if pod was deleted
			gotPod := &v1.Pod{}
			getErr := fakeClient.Get(context.Background(), client.ObjectKeyFromObject(pod), gotPod)
			podDeleted := apierrors.IsNotFound(getErr)
			if getErr != nil && !podDeleted {
				t.Fatalf("unexpected error getting pod: %v", getErr)
			}
			if podDeleted != tt.expectPodDeleted {
				t.Errorf("expected pod deleted=%v, got deleted=%v (err=%v)", tt.expectPodDeleted, podDeleted, getErr)
			}
		})
	}
}
