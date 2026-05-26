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

			newStatus := &workloadsv1alpha2.RoleInstanceStatus{}
			_, err := ctrl.updatePod(context.Background(), instance, newStatus, updateRevision, revisions, pod)
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

func TestRecordInPlaceUpdateBaselines(t *testing.T) {
	t.Run("records baselines for all containers", func(t *testing.T) {
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
			Status: v1.PodStatus{
				ContainerStatuses: []v1.ContainerStatus{
					{Name: "main", RestartCount: 0},
					{Name: "sidecar", RestartCount: 3},
				},
			},
		}
		newStatus := &workloadsv1alpha2.RoleInstanceStatus{}
		recordInPlaceUpdateBaselines(pod, newStatus)

		if newStatus.InPlaceUpdateContainerRestartCounts == nil {
			t.Fatal("expected baselines to be set")
		}
		baselines := newStatus.InPlaceUpdateContainerRestartCounts["pod-0"]
		if baselines == nil {
			t.Fatal("expected baselines for pod-0")
		}
		if baselines["main"] != 0 {
			t.Errorf("expected main baseline=0, got %d", baselines["main"])
		}
		if baselines["sidecar"] != 3 {
			t.Errorf("expected sidecar baseline=3, got %d", baselines["sidecar"])
		}
	})

	t.Run("overwrites existing baselines for same pod", func(t *testing.T) {
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-0"},
			Status: v1.PodStatus{
				ContainerStatuses: []v1.ContainerStatus{
					{Name: "main", RestartCount: 2},
				},
			},
		}
		newStatus := &workloadsv1alpha2.RoleInstanceStatus{
			InPlaceUpdateContainerRestartCounts: map[string]map[string]int32{
				"pod-0": {"main": 0},
			},
		}
		recordInPlaceUpdateBaselines(pod, newStatus)

		if newStatus.InPlaceUpdateContainerRestartCounts["pod-0"]["main"] != 2 {
			t.Errorf("expected main baseline=2 after overwrite, got %d",
				newStatus.InPlaceUpdateContainerRestartCounts["pod-0"]["main"])
		}
	})

	t.Run("preserves baselines for other pods", func(t *testing.T) {
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pod-1"},
			Status: v1.PodStatus{
				ContainerStatuses: []v1.ContainerStatus{
					{Name: "main", RestartCount: 0},
				},
			},
		}
		newStatus := &workloadsv1alpha2.RoleInstanceStatus{
			InPlaceUpdateContainerRestartCounts: map[string]map[string]int32{
				"pod-0": {"main": 5},
			},
		}
		recordInPlaceUpdateBaselines(pod, newStatus)

		if newStatus.InPlaceUpdateContainerRestartCounts["pod-0"]["main"] != 5 {
			t.Error("baselines for pod-0 should be preserved")
		}
		if newStatus.InPlaceUpdateContainerRestartCounts["pod-1"]["main"] != 0 {
			t.Error("baselines for pod-1 should be recorded")
		}
	})
}

func TestUpdatePodRecordsBaselines(t *testing.T) {
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

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-pod",
			Namespace:       "default",
			UID:             "pod-uid",
			ResourceVersion: "1",
			Labels: map[string]string{
				apps.ControllerRevisionHashLabelKey: "rev-abc123",
			},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				{Name: "inference", RestartCount: 2},
				{Name: "router", RestartCount: 0},
			},
		},
	}
	t.Cleanup(func() {
		instanceutil.ResourceVersionExpectations.Delete(pod)
	})

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()

	ctrl := &realControl{
		Client: fakeClient,
		inplaceControl: &fakeInplaceControl{
			updateFn: func(_ context.Context, _ *v1.Pod, _, _ *apps.ControllerRevision, _ *podinplaceupdate.UpdateOptions) podinplaceupdate.UpdateResult {
				return podinplaceupdate.UpdateResult{InPlaceUpdate: true, NewResourceVersion: "999"}
			},
		},
		recorder:         record.NewFakeRecorder(10),
		lifecycleControl: &fakeLifecycleControl{},
	}

	revisions := []*apps.ControllerRevision{
		{ObjectMeta: metav1.ObjectMeta{Name: "rev-abc123", Namespace: "default"}},
	}
	updateRevision := &apps.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: "rev-def456", Namespace: "default"},
	}

	newStatus := &workloadsv1alpha2.RoleInstanceStatus{}
	_, err := ctrl.updatePod(context.Background(), instance, newStatus, updateRevision, revisions, pod)
	if err != nil {
		t.Fatalf("updatePod returned unexpected error: %v", err)
	}

	// Verify baselines were recorded
	if newStatus.InPlaceUpdateContainerRestartCounts == nil {
		t.Fatal("expected baselines to be recorded after successful in-place update")
	}
	baselines := newStatus.InPlaceUpdateContainerRestartCounts["test-pod"]
	if baselines == nil {
		t.Fatal("expected baselines for test-pod")
	}
	if baselines["inference"] != 2 {
		t.Errorf("expected inference baseline=2, got %d", baselines["inference"])
	}
	if baselines["router"] != 0 {
		t.Errorf("expected router baseline=0, got %d", baselines["router"])
	}
}
