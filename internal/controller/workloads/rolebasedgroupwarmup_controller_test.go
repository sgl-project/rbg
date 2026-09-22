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
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func newWarmupTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)
	return scheme
}

func newWarmupReconciler(objs ...runtime.Object) *RoleBasedGroupWarmupReconciler {
	scheme := newWarmupTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		WithStatusSubresource(&workloadsv1alpha2.RoleBasedGroupWarmup{}).
		Build()
	return &RoleBasedGroupWarmupReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(100),
	}
}

func makeWarmupPod(name, namespace, warmupName, warmupUID, nodeName string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				LabelWarmupName: warmupName,
				LabelWarmupUID:  warmupUID,
				LabelNodeName:   nodeName,
			},
		},
		Status: corev1.PodStatus{
			Phase: phase,
		},
	}
}

// ==================== buildWarmupPod Tests ====================

func TestBuildWarmupPod_ImagePreload(t *testing.T) {
	r := newWarmupReconciler()
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-warmup", Namespace: "default", UID: "uid-123"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			Tolerations: []corev1.Toleration{
				{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
			},
		},
	}

	actions := []workloadsv1alpha2.WarmupActions{
		{
			ImagePreload: &workloadsv1alpha2.ImagePreloadAction{
				Images: []string{"nginx:latest", "redis:7"},
				PullSecrets: []corev1.LocalObjectReference{
					{Name: "registry-secret"},
				},
			},
		},
	}

	pod, _ := r.buildWarmupPod(warmup, "node-1", actions)

	// Verify containers
	if len(pod.Spec.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(pod.Spec.Containers))
	}
	if pod.Spec.Containers[0].Image != "nginx:latest" {
		t.Errorf("expected image nginx:latest, got %s", pod.Spec.Containers[0].Image)
	}
	if pod.Spec.Containers[1].Image != "redis:7" {
		t.Errorf("expected image redis:7, got %s", pod.Spec.Containers[1].Image)
	}
	if pod.Spec.Containers[0].Command[2] != "exit 0" {
		t.Errorf("expected command 'exit 0', got %v", pod.Spec.Containers[0].Command)
	}
	if pod.Spec.Containers[0].ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("expected PullIfNotPresent, got %s", pod.Spec.Containers[0].ImagePullPolicy)
	}
	// Verify pullSecrets
	if len(pod.Spec.ImagePullSecrets) != 1 || pod.Spec.ImagePullSecrets[0].Name != "registry-secret" {
		t.Errorf("unexpected pull secrets: %v", pod.Spec.ImagePullSecrets)
	}
	// Verify tolerations propagated from spec
	if len(pod.Spec.Tolerations) != 1 || pod.Spec.Tolerations[0].Key != "nvidia.com/gpu" {
		t.Errorf("unexpected tolerations: %v", pod.Spec.Tolerations)
	}
	// Verify nodeSelector
	if pod.Spec.NodeSelector["kubernetes.io/hostname"] != "node-1" {
		t.Errorf("unexpected nodeSelector: %v", pod.Spec.NodeSelector)
	}
	// Verify restartPolicy
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("expected RestartPolicyNever, got %s", pod.Spec.RestartPolicy)
	}
	// Verify labels
	if pod.Labels[LabelWarmupName] != "test-warmup" {
		t.Errorf("unexpected warmup-name label: %s", pod.Labels[LabelWarmupName])
	}
	if pod.Labels[LabelNodeName] != "node-1" {
		t.Errorf("unexpected node-name label: %s", pod.Labels[LabelNodeName])
	}
}

func TestBuildWarmupPod_ImageDedup(t *testing.T) {
	r := newWarmupReconciler()
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
	}

	// Two actions from different roles both reference "nginx:latest"
	actions := []workloadsv1alpha2.WarmupActions{
		{ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{"nginx:latest", "redis:7"}}},
		{ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{"nginx:latest", "busybox:1"}}},
	}

	pod, _ := r.buildWarmupPod(warmup, "node-1", actions)

	// nginx:latest should be deduplicated
	if len(pod.Spec.Containers) != 3 {
		t.Fatalf("expected 3 containers (nginx + redis + busybox), got %d", len(pod.Spec.Containers))
	}
}

func TestBuildWarmupPod_PullSecretDedup(t *testing.T) {
	r := newWarmupReconciler()
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
	}

	actions := []workloadsv1alpha2.WarmupActions{
		{ImagePreload: &workloadsv1alpha2.ImagePreloadAction{
			Images:      []string{"img1:v1"},
			PullSecrets: []corev1.LocalObjectReference{{Name: "secret-a"}, {Name: "secret-b"}},
		}},
		{ImagePreload: &workloadsv1alpha2.ImagePreloadAction{
			Images:      []string{"img2:v1"},
			PullSecrets: []corev1.LocalObjectReference{{Name: "secret-a"}, {Name: "secret-c"}},
		}},
	}

	pod, _ := r.buildWarmupPod(warmup, "node-1", actions)

	if len(pod.Spec.ImagePullSecrets) != 3 {
		t.Fatalf("expected 3 pull secrets (a, b, c deduplicated), got %d", len(pod.Spec.ImagePullSecrets))
	}
}

func TestBuildWarmupPod_CustomizedAction(t *testing.T) {
	r := newWarmupReconciler()
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
	}

	actions := []workloadsv1alpha2.WarmupActions{
		{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{
				{Name: "user-ctr", Image: "busybox", Command: []string{"echo", "hello"}},
			},
			Volumes: []corev1.Volume{
				{Name: "vol-a", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/tmp/a"}}},
			},
		}},
	}

	pod, _ := r.buildWarmupPod(warmup, "node-1", actions)

	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(pod.Spec.Containers))
	}
	// Name is overwritten to "custom-0"
	if pod.Spec.Containers[0].Name != "custom-0" {
		t.Errorf("expected container name 'custom-0', got %s", pod.Spec.Containers[0].Name)
	}
	if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].Name != "vol-a" {
		t.Errorf("unexpected volumes: %v", pod.Spec.Volumes)
	}
}

func TestBuildWarmupPod_VolumeConflict(t *testing.T) {
	r := newWarmupReconciler()
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
	}

	// Two actions define the same volume name with different specs
	actions := []workloadsv1alpha2.WarmupActions{
		{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{{Name: "c1", Image: "busybox", Command: []string{"true"}}},
			Volumes:    []corev1.Volume{{Name: "shared", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/mnt/a"}}}},
		}},
		{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{{Name: "c2", Image: "alpine", Command: []string{"true"}}},
			Volumes:    []corev1.Volume{{Name: "shared", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/mnt/b"}}}},
		}},
	}

	pod, volumeConflict := r.buildWarmupPod(warmup, "node-1", actions)

	if !volumeConflict {
		t.Fatal("expected volume conflict to be detected")
	}
	// First-wins: volume should use /mnt/a
	if len(pod.Spec.Volumes) != 1 {
		t.Fatalf("expected 1 volume (first-wins), got %d", len(pod.Spec.Volumes))
	}
	if pod.Spec.Volumes[0].HostPath.Path != "/mnt/a" {
		t.Errorf("expected first-wins volume path /mnt/a, got %s", pod.Spec.Volumes[0].HostPath.Path)
	}
}

func TestBuildWarmupPod_ContainerHashDedup(t *testing.T) {
	r := newWarmupReconciler()
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
	}

	// Same container spec with different names should be deduped
	actions := []workloadsv1alpha2.WarmupActions{
		{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{{Name: "ctr-from-prefill", Image: "busybox", Command: []string{"echo", "warmup"}}},
		}},
		{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{{Name: "ctr-from-decode", Image: "busybox", Command: []string{"echo", "warmup"}}},
		}},
	}

	pod, _ := r.buildWarmupPod(warmup, "node-1", actions)

	// Same spec (name excluded from hash) → only 1 container
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container (deduped by hash), got %d", len(pod.Spec.Containers))
	}
}

func TestBuildWarmupPod_MixedImageAndCustom(t *testing.T) {
	r := newWarmupReconciler()
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
	}

	actions := []workloadsv1alpha2.WarmupActions{
		{
			ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{"nginx:latest"}},
			CustomizedAction: &workloadsv1alpha2.CustomizedAction{
				Containers: []corev1.Container{{Name: "custom", Image: "busybox", Command: []string{"sh", "-c", "ls"}}},
			},
		},
	}

	pod, _ := r.buildWarmupPod(warmup, "node-1", actions)

	// 1 image preload + 1 custom
	if len(pod.Spec.Containers) != 2 {
		t.Fatalf("expected 2 containers (1 preload + 1 custom), got %d", len(pod.Spec.Containers))
	}
	if pod.Spec.Containers[0].Name != "image-preload-0" {
		t.Errorf("expected first container name 'image-preload-0', got %s", pod.Spec.Containers[0].Name)
	}
	if pod.Spec.Containers[1].Name != "custom-1" {
		t.Errorf("expected second container name 'custom-1', got %s", pod.Spec.Containers[1].Name)
	}
}

// ==================== computePermanentlyFailedNodes Tests ====================

func TestComputePermanentlyFailedNodes(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	r := newWarmupReconciler()

	tests := []struct {
		name           string
		failedPods     []*corev1.Pod
		backoffLimit   *int32
		expectedFailed map[string]bool
	}{
		{
			name:           "nil backoffLimit returns empty",
			failedPods:     []*corev1.Pod{makeWarmupPod("p1", "ns", "w", "uid", "node-1", corev1.PodFailed)},
			backoffLimit:   nil,
			expectedFailed: map[string]bool{},
		},
		{
			name: "backoffLimit=0, one failure marks node permanently failed",
			failedPods: []*corev1.Pod{
				makeWarmupPod("p1", "ns", "w", "uid", "node-1", corev1.PodFailed),
			},
			backoffLimit:   ptr.To(int32(0)),
			expectedFailed: map[string]bool{"node-1": true},
		},
		{
			name: "backoffLimit=2, node with 3 failures is permanently failed",
			failedPods: []*corev1.Pod{
				makeWarmupPod("p1", "ns", "w", "uid", "node-1", corev1.PodFailed),
				makeWarmupPod("p2", "ns", "w", "uid", "node-1", corev1.PodFailed),
				makeWarmupPod("p3", "ns", "w", "uid", "node-1", corev1.PodFailed),
			},
			backoffLimit:   ptr.To(int32(2)),
			expectedFailed: map[string]bool{"node-1": true},
		},
		{
			name: "backoffLimit=2, node with 2 failures is NOT permanently failed",
			failedPods: []*corev1.Pod{
				makeWarmupPod("p1", "ns", "w", "uid", "node-1", corev1.PodFailed),
				makeWarmupPod("p2", "ns", "w", "uid", "node-1", corev1.PodFailed),
			},
			backoffLimit:   ptr.To(int32(2)),
			expectedFailed: map[string]bool{},
		},
		{
			name: "multiple nodes, some permanently failed",
			failedPods: []*corev1.Pod{
				makeWarmupPod("p1", "ns", "w", "uid", "node-1", corev1.PodFailed),
				makeWarmupPod("p2", "ns", "w", "uid", "node-1", corev1.PodFailed),
				makeWarmupPod("p3", "ns", "w", "uid", "node-2", corev1.PodFailed),
			},
			backoffLimit:   ptr.To(int32(1)),
			expectedFailed: map[string]bool{"node-1": true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := r.computePermanentlyFailedNodes(ctx, tt.failedPods, tt.backoffLimit)
			if len(result) != len(tt.expectedFailed) {
				t.Fatalf("expected %d permanently failed nodes, got %d: %v", len(tt.expectedFailed), len(result), result)
			}
			for node := range tt.expectedFailed {
				if !result[node] {
					t.Errorf("expected node %s to be permanently failed", node)
				}
			}
		})
	}
}

// ==================== collectPendingNodes Tests ====================

func TestCollectPendingNodes(t *testing.T) {
	tests := []struct {
		name                   string
		desiredNodes           map[string][]workloadsv1alpha2.WarmupActions
		activePods             []*corev1.Pod
		succeededPods          []*corev1.Pod
		permanentlyFailedNodes map[string]bool
		expectedPending        []string
	}{
		{
			name: "all nodes pending when no pods exist",
			desiredNodes: map[string][]workloadsv1alpha2.WarmupActions{
				"node-1": {{}},
				"node-2": {{}},
			},
			activePods:             nil,
			succeededPods:          nil,
			permanentlyFailedNodes: map[string]bool{},
			expectedPending:        []string{"node-1", "node-2"},
		},
		{
			name: "active pod occupies node",
			desiredNodes: map[string][]workloadsv1alpha2.WarmupActions{
				"node-1": {{}},
				"node-2": {{}},
			},
			activePods:             []*corev1.Pod{makeWarmupPod("p1", "ns", "w", "uid", "node-1", corev1.PodRunning)},
			succeededPods:          nil,
			permanentlyFailedNodes: map[string]bool{},
			expectedPending:        []string{"node-2"},
		},
		{
			name: "succeeded pod occupies node",
			desiredNodes: map[string][]workloadsv1alpha2.WarmupActions{
				"node-1": {{}},
				"node-2": {{}},
			},
			activePods:             nil,
			succeededPods:          []*corev1.Pod{makeWarmupPod("p1", "ns", "w", "uid", "node-1", corev1.PodSucceeded)},
			permanentlyFailedNodes: map[string]bool{},
			expectedPending:        []string{"node-2"},
		},
		{
			name: "permanently failed node excluded",
			desiredNodes: map[string][]workloadsv1alpha2.WarmupActions{
				"node-1": {{}},
				"node-2": {{}},
				"node-3": {{}},
			},
			activePods:             nil,
			succeededPods:          nil,
			permanentlyFailedNodes: map[string]bool{"node-2": true},
			expectedPending:        []string{"node-1", "node-3"},
		},
		{
			name: "no pending nodes when all covered",
			desiredNodes: map[string][]workloadsv1alpha2.WarmupActions{
				"node-1": {{}},
				"node-2": {{}},
			},
			activePods:             []*corev1.Pod{makeWarmupPod("p1", "ns", "w", "uid", "node-1", corev1.PodRunning)},
			succeededPods:          []*corev1.Pod{makeWarmupPod("p2", "ns", "w", "uid", "node-2", corev1.PodSucceeded)},
			permanentlyFailedNodes: map[string]bool{},
			expectedPending:        []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := collectPendingNodes(tt.desiredNodes, tt.activePods, tt.succeededPods, tt.permanentlyFailedNodes)
			if len(result) != len(tt.expectedPending) {
				t.Fatalf("expected %d pending nodes, got %d: %v", len(tt.expectedPending), len(result), result)
			}
			// Result is sorted
			for i, node := range tt.expectedPending {
				if result[i] != node {
					t.Errorf("expected pending[%d]=%s, got %s", i, node, result[i])
				}
			}
		})
	}
}

// ==================== requeueForTimeout Tests ====================

func TestRequeueForTimeout(t *testing.T) {
	r := newWarmupReconciler()
	now := metav1.Now()
	pastTime := metav1.NewTime(now.Add(-5 * time.Minute))

	tests := []struct {
		name           string
		warmup         *workloadsv1alpha2.RoleBasedGroupWarmup
		expectRequeue  bool
		expectMaxDelay bool
	}{
		{
			name: "no policies, no requeue",
			warmup: &workloadsv1alpha2.RoleBasedGroupWarmup{
				Spec:   workloadsv1alpha2.RoleBasedGroupWarmupSpec{},
				Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{StartTime: &now},
			},
			expectRequeue: false,
		},
		{
			name: "no globalTimeout, no requeue",
			warmup: &workloadsv1alpha2.RoleBasedGroupWarmup{
				Spec:   workloadsv1alpha2.RoleBasedGroupWarmupSpec{Policies: &workloadsv1alpha2.WarmupPolicies{}},
				Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{StartTime: &now},
			},
			expectRequeue: false,
		},
		{
			name: "no startTime, no requeue",
			warmup: &workloadsv1alpha2.RoleBasedGroupWarmup{
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					Policies: &workloadsv1alpha2.WarmupPolicies{GlobalTimeoutSeconds: ptr.To(int64(600))},
				},
				Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{},
			},
			expectRequeue: false,
		},
		{
			name: "completed phase, no requeue",
			warmup: &workloadsv1alpha2.RoleBasedGroupWarmup{
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					Policies: &workloadsv1alpha2.WarmupPolicies{GlobalTimeoutSeconds: ptr.To(int64(600))},
				},
				Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
					Phase:     workloadsv1alpha2.WarmupJobPhaseCompleted,
					StartTime: &now,
				},
			},
			expectRequeue: false,
		},
		{
			name: "remaining > MaxRequeueDelay, capped at MaxRequeueDelay",
			warmup: &workloadsv1alpha2.RoleBasedGroupWarmup{
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					Policies: &workloadsv1alpha2.WarmupPolicies{GlobalTimeoutSeconds: ptr.To(int64(86400))}, // 1 day
				},
				Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
					Phase:     workloadsv1alpha2.WarmupJobPhaseRunning,
					StartTime: &now,
				},
			},
			expectRequeue:  true,
			expectMaxDelay: true,
		},
		{
			name: "remaining < MaxRequeueDelay, use actual remaining",
			warmup: &workloadsv1alpha2.RoleBasedGroupWarmup{
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					Policies: &workloadsv1alpha2.WarmupPolicies{GlobalTimeoutSeconds: ptr.To(int64(600))}, // 10 min
				},
				Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
					Phase:     workloadsv1alpha2.WarmupJobPhaseRunning,
					StartTime: &pastTime, // 5 min ago → 5 min remaining
				},
			},
			expectRequeue:  true,
			expectMaxDelay: false,
		},
		{
			name: "already timed out, no requeue",
			warmup: &workloadsv1alpha2.RoleBasedGroupWarmup{
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					Policies: &workloadsv1alpha2.WarmupPolicies{GlobalTimeoutSeconds: ptr.To(int64(60))}, // 1 min
				},
				Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
					Phase:     workloadsv1alpha2.WarmupJobPhaseRunning,
					StartTime: &pastTime, // 5 min ago → already timed out
				},
			},
			expectRequeue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := r.requeueForTimeout(tt.warmup)
			if tt.expectRequeue {
				if result.RequeueAfter == 0 {
					t.Fatal("expected requeue but got zero RequeueAfter")
				}
				if tt.expectMaxDelay && result.RequeueAfter != MaxRequeueDelay {
					t.Errorf("expected MaxRequeueDelay (%v), got %v", MaxRequeueDelay, result.RequeueAfter)
				}
				if !tt.expectMaxDelay && result.RequeueAfter >= MaxRequeueDelay {
					t.Errorf("expected remaining < MaxRequeueDelay, got %v", result.RequeueAfter)
				}
			} else {
				if result.RequeueAfter != 0 {
					t.Errorf("expected no requeue, got RequeueAfter=%v", result.RequeueAfter)
				}
			}
		})
	}
}

// ==================== reconcileFinished (TTL) Tests ====================

func TestReconcileFinished_NoTTL(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Spec:       workloadsv1alpha2.RoleBasedGroupWarmupSpec{},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase: workloadsv1alpha2.WarmupJobPhaseCompleted,
		},
	}
	r := newWarmupReconciler(warmup)

	result, err := r.reconcileFinished(ctx, warmup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue when TTL not set, got %v", result.RequeueAfter)
	}
}

func TestReconcileFinished_TTLNotExpired(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	completionTime := metav1.NewTime(time.Now().Add(-30 * time.Second))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			Policies: &workloadsv1alpha2.WarmupPolicies{
				TTLSecondsAfterFinished: ptr.To(int32(120)), // 2 minutes
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase:          workloadsv1alpha2.WarmupJobPhaseCompleted,
			CompletionTime: &completionTime,
		},
	}
	r := newWarmupReconciler(warmup)

	result, err := r.reconcileFinished(ctx, warmup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// TTL not expired (120s - 30s = ~90s remaining)
	if result.RequeueAfter == 0 {
		t.Fatal("expected requeue when TTL not expired")
	}
	if result.RequeueAfter > 91*time.Second || result.RequeueAfter < 88*time.Second {
		t.Errorf("expected remaining ~90s, got %v", result.RequeueAfter)
	}
}

func TestReconcileFinished_TTLExpired(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	completionTime := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			Policies: &workloadsv1alpha2.WarmupPolicies{
				TTLSecondsAfterFinished: ptr.To(int32(60)), // 1 minute
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase:          workloadsv1alpha2.WarmupJobPhaseCompleted,
			CompletionTime: &completionTime,
		},
	}
	r := newWarmupReconciler(warmup)

	result, err := r.reconcileFinished(ctx, warmup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue after deletion, got %v", result.RequeueAfter)
	}

	// Verify CR is deleted
	deleted := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	err = r.Get(ctx, types.NamespacedName{Name: "test", Namespace: "default"}, deleted)
	if err == nil {
		t.Fatal("expected warmup CR to be deleted")
	}
}

// ==================== Reconcile (integration-level) Tests ====================

func TestReconcile_PausedSkipsTerminalPhase(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	paused := true
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			Paused: &paused,
			TargetNodes: &workloadsv1alpha2.TargetNodes{
				NodeNames:     []string{"node-1"},
				WarmupActions: workloadsv1alpha2.WarmupActions{ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{"img:v1"}}},
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase: workloadsv1alpha2.WarmupJobPhaseCompleted,
		},
	}
	r := newWarmupReconciler(warmup)

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Phase should remain Completed (not be overwritten to Paused)
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	_ = r.Get(ctx, types.NamespacedName{Name: "test", Namespace: "default"}, updated)
	if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhaseCompleted {
		t.Errorf("expected phase Completed (protected), got %s", updated.Status.Phase)
	}
}

func TestReconcile_PausedSetsPhase(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	paused := true
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			Paused: &paused,
			TargetNodes: &workloadsv1alpha2.TargetNodes{
				NodeNames:     []string{"node-1"},
				WarmupActions: workloadsv1alpha2.WarmupActions{ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{"img:v1"}}},
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{
			Phase: workloadsv1alpha2.WarmupJobPhaseRunning,
		},
	}
	r := newWarmupReconciler(warmup)

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	_ = r.Get(ctx, types.NamespacedName{Name: "test", Namespace: "default"}, updated)
	if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhasePaused {
		t.Errorf("expected phase Paused, got %s", updated.Status.Phase)
	}
}

func TestReconcile_DesiredZeroCompletesImmediately(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			TargetNodes: &workloadsv1alpha2.TargetNodes{
				NodeSelector:  map[string]string{"gpu": "true"},
				WarmupActions: workloadsv1alpha2.WarmupActions{ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{"img:v1"}}},
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{},
	}
	r := newWarmupReconciler(warmup)

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	_ = r.Get(ctx, types.NamespacedName{Name: "test", Namespace: "default"}, updated)
	if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhaseCompleted {
		t.Errorf("expected Completed when no nodes match, got %s", updated.Status.Phase)
	}
	if updated.Status.Desired != 0 {
		t.Errorf("expected desired=0, got %d", updated.Status.Desired)
	}
}

func TestReconcile_CreatesPodForTargetNodes(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			TargetNodes: &workloadsv1alpha2.TargetNodes{
				NodeNames:     []string{"node-1", "node-2"},
				WarmupActions: workloadsv1alpha2.WarmupActions{ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{"nginx:latest"}}},
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{},
	}
	r := newWarmupReconciler(warmup)

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify pods were created
	podList := &corev1.PodList{}
	_ = r.List(ctx, podList)
	if len(podList.Items) != 2 {
		t.Fatalf("expected 2 pods created, got %d", len(podList.Items))
	}

	// Verify status
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	_ = r.Get(ctx, types.NamespacedName{Name: "test", Namespace: "default"}, updated)
	if updated.Status.Desired != 2 {
		t.Errorf("expected desired=2, got %d", updated.Status.Desired)
	}
	if updated.Status.Active != 2 {
		t.Errorf("expected active=2, got %d", updated.Status.Active)
	}
}

func TestReconcile_RespectsParallelism(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			Policies: &workloadsv1alpha2.WarmupPolicies{
				Parallelism: ptr.To(int32(1)),
			},
			TargetNodes: &workloadsv1alpha2.TargetNodes{
				NodeNames:     []string{"node-1", "node-2", "node-3"},
				WarmupActions: workloadsv1alpha2.WarmupActions{ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{"nginx:latest"}}},
			},
		},
		Status: workloadsv1alpha2.RoleBasedGroupWarmupStatus{},
	}
	r := newWarmupReconciler(warmup)

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "test", Namespace: "default"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only 1 pod should be created due to parallelism=1
	podList := &corev1.PodList{}
	_ = r.List(ctx, podList)
	if len(podList.Items) != 1 {
		t.Fatalf("expected 1 pod (parallelism=1), got %d", len(podList.Items))
	}
}

func TestReconcile_InvalidImageFailsWarmup(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-image", Namespace: "default", UID: "uid-invalid-image"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			TargetNodes: &workloadsv1alpha2.TargetNodes{
				NodeNames: []string{"node-1"},
				WarmupActions: workloadsv1alpha2.WarmupActions{
					ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{""}},
				},
			},
		},
	}
	r := newWarmupReconciler(warmup)

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: warmup.Name, Namespace: warmup.Namespace}})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(ctx, types.NamespacedName{Name: warmup.Name, Namespace: warmup.Namespace}, updated); err != nil {
		t.Fatalf("failed to get warmup: %v", err)
	}
	if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhaseFailed {
		t.Fatalf("expected Failed phase, got %q", updated.Status.Phase)
	}
	if len(updated.Status.Conditions) == 0 || updated.Status.Conditions[0].Reason != "InvalidWarmupSpec" {
		t.Fatalf("expected InvalidWarmupSpec condition, got %#v", updated.Status.Conditions)
	}

	pods := &corev1.PodList{}
	if err := r.List(ctx, pods); err != nil {
		t.Fatalf("failed to list pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("invalid warmup should not create pods, got %d", len(pods.Items))
	}
}

func TestReconcile_InvalidCustomizedContainerImageFailsWarmup(t *testing.T) {
	tests := []struct {
		name  string
		image string
	}{
		{name: "empty", image: ""},
		{name: "whitespace", image: " \t"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
			warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-custom-image", Namespace: "default", UID: types.UID(tt.name)},
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					TargetNodes: &workloadsv1alpha2.TargetNodes{
						NodeNames: []string{"node-1"},
						WarmupActions: workloadsv1alpha2.WarmupActions{
							CustomizedAction: &workloadsv1alpha2.CustomizedAction{
								Containers: []corev1.Container{{Name: "custom", Image: tt.image}},
							},
						},
					},
				},
			}
			r := newWarmupReconciler(warmup)

			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: warmup.Name, Namespace: warmup.Namespace}}); err != nil {
				t.Fatalf("unexpected reconcile error: %v", err)
			}

			updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(warmup), updated); err != nil {
				t.Fatalf("failed to get warmup: %v", err)
			}
			if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhaseFailed {
				t.Fatalf("expected Failed phase, got %q", updated.Status.Phase)
			}
			if len(updated.Status.Conditions) == 0 || updated.Status.Conditions[0].Reason != "InvalidWarmupSpec" {
				t.Fatalf("expected InvalidWarmupSpec condition, got %#v", updated.Status.Conditions)
			}

			pods := &corev1.PodList{}
			if err := r.List(ctx, pods); err != nil {
				t.Fatalf("failed to list pods: %v", err)
			}
			if len(pods.Items) != 0 {
				t.Fatalf("invalid warmup should not create pods, got %d", len(pods.Items))
			}
		})
	}
}

func TestValidateWarmupSpecReportsDeterministicTargetPath(t *testing.T) {
	tests := []struct {
		name       string
		spec       workloadsv1alpha2.RoleBasedGroupWarmupSpec
		wantTarget string
	}{
		{
			name: "target nodes",
			spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
				TargetNodes: &workloadsv1alpha2.TargetNodes{
					WarmupActions: workloadsv1alpha2.WarmupActions{
						ImagePreload: &workloadsv1alpha2.ImagePreloadAction{},
					},
				},
			},
			wantTarget: "spec.targetNodes",
		},
		{
			name: "sorted rbg roles",
			spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
				TargetRoleBasedGroup: &workloadsv1alpha2.TargetRoleBasedGroup{
					Roles: map[string]workloadsv1alpha2.WarmupActions{
						"zeta":  {ImagePreload: &workloadsv1alpha2.ImagePreloadAction{}},
						"alpha": {ImagePreload: &workloadsv1alpha2.ImagePreloadAction{}},
					},
				},
			},
			wantTarget: `spec.targetRoleBasedGroup.roles["alpha"]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWarmupSpec(tt.spec)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.wantTarget) {
				t.Fatalf("expected target path %q in error, got %q", tt.wantTarget, err)
			}
		})
	}
}

func TestReconcile_PropagatesPodCreateError(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "create-error", Namespace: "default", UID: "uid-create-error"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			TargetNodes: &workloadsv1alpha2.TargetNodes{
				NodeNames:     []string{"node-1"},
				WarmupActions: workloadsv1alpha2.WarmupActions{ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{"busybox:1.36"}}},
			},
		},
	}
	createErr := errors.New("injected pod create failure")
	scheme := newWarmupTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(warmup).
		WithStatusSubresource(&workloadsv1alpha2.RoleBasedGroupWarmup{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*corev1.Pod); ok {
					return createErr
				}
				return c.Create(ctx, obj, opts...)
			},
		}).Build()
	r := &RoleBasedGroupWarmupReconciler{Client: fakeClient, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)})
	if !errors.Is(err, createErr) {
		t.Fatalf("expected Pod create error to be propagated, got %v", err)
	}
}

func TestReconcile_PropagatesOwnerReferenceError(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "owner-reference-error", Namespace: "default", UID: "uid-owner-reference-error"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			TargetNodes: &workloadsv1alpha2.TargetNodes{
				NodeNames:     []string{"node-1"},
				WarmupActions: workloadsv1alpha2.WarmupActions{ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{"busybox:1.36"}}},
			},
		},
	}
	r := newWarmupReconciler(warmup)
	r.Scheme = runtime.NewScheme()

	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)})
	if err == nil || !strings.Contains(err.Error(), "set owner reference") {
		t.Fatalf("expected owner-reference error to be propagated, got %v", err)
	}
}

// Superseded by TestReconcile_MissingTargetRBGRecoversWhenCreatedLater and
// TestReconcile_MissingTargetRBGFailsAfterGlobalTimeout: a missing target is no
// longer an immediate terminal failure.
func TestReconcile_MissingTargetRBGDoesNotFailImmediately(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "missing-rbg", Namespace: "default", UID: "uid-missing-rbg"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			TargetRoleBasedGroup: &workloadsv1alpha2.TargetRoleBasedGroup{
				Name: "does-not-exist",
				Roles: map[string]workloadsv1alpha2.WarmupActions{
					"worker": {ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{"busybox:1.36"}}},
				},
			},
		},
	}
	r := newWarmupReconciler(warmup)

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: warmup.Name, Namespace: warmup.Namespace}})
	if err != nil {
		t.Fatalf("missing target should wait without reconcile error: %v", err)
	}
	if result.RequeueAfter != TargetWaitRequeuePeriod {
		t.Fatalf("missing target should requeue after %s, got %#v", TargetWaitRequeuePeriod, result)
	}

	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(ctx, types.NamespacedName{Name: warmup.Name, Namespace: warmup.Namespace}, updated); err != nil {
		t.Fatalf("failed to get warmup: %v", err)
	}
	if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhaseRunning {
		t.Fatalf("expected Running phase while waiting, got %q", updated.Status.Phase)
	}
	if cond := apimeta.FindStatusCondition(updated.Status.Conditions, "Failed"); cond != nil {
		t.Fatalf("must not record a terminal Failed condition while waiting, got %#v", cond)
	}
}

func TestReconcile_InvalidLegacyActionsFailBeforeNodeDiscovery(t *testing.T) {
	tests := []struct {
		name    string
		actions workloadsv1alpha2.WarmupActions
	}{
		{
			name: "empty image list",
			actions: workloadsv1alpha2.WarmupActions{
				ImagePreload: &workloadsv1alpha2.ImagePreloadAction{},
			},
		},
		{
			name: "empty customized containers",
			actions: workloadsv1alpha2.WarmupActions{
				CustomizedAction: &workloadsv1alpha2.CustomizedAction{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
			warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-legacy", Namespace: "default", UID: types.UID(tt.name)},
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					TargetNodes: &workloadsv1alpha2.TargetNodes{
						NodeSelector:  map[string]string{"does-not-exist": "true"},
						WarmupActions: tt.actions,
					},
				},
			}
			r := newWarmupReconciler(warmup)

			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: warmup.Name, Namespace: warmup.Namespace}}); err != nil {
				t.Fatalf("unexpected reconcile error: %v", err)
			}

			updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
			if err := r.Get(ctx, types.NamespacedName{Name: warmup.Name, Namespace: warmup.Namespace}, updated); err != nil {
				t.Fatalf("failed to get warmup: %v", err)
			}
			if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhaseFailed {
				t.Fatalf("expected Failed phase, got %q", updated.Status.Phase)
			}
			if len(updated.Status.Conditions) == 0 || updated.Status.Conditions[0].Reason != "InvalidWarmupSpec" {
				t.Fatalf("expected InvalidWarmupSpec condition, got %#v", updated.Status.Conditions)
			}
		})
	}
}

func TestReconcile_InvalidSpecCleansUpExistingPods(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "invalid-update", Namespace: "default", UID: "uid-invalid-update"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			TargetNodes: &workloadsv1alpha2.TargetNodes{
				NodeNames: []string{"node-1"},
				WarmupActions: workloadsv1alpha2.WarmupActions{
					ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{" "}},
				},
			},
		},
	}
	activePod := makeWarmupPod("invalid-update-node-1", "default", warmup.Name, string(warmup.UID), "node-1", corev1.PodRunning)
	r := newWarmupReconciler(warmup, activePod)

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: warmup.Name, Namespace: warmup.Namespace}}); err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(ctx, types.NamespacedName{Name: warmup.Name, Namespace: warmup.Namespace}, updated); err != nil {
		t.Fatalf("failed to get warmup: %v", err)
	}
	if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhaseFailed || updated.Status.Active != 0 {
		t.Fatalf("expected Failed with no active pods, got phase=%q active=%d", updated.Status.Phase, updated.Status.Active)
	}
	if err := r.Get(ctx, types.NamespacedName{Name: activePod.Name, Namespace: activePod.Namespace}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected active warmup pod to be deleted, got err=%v", err)
	}
}

func TestGetDesiredNodes_UsesAPIReaderAfterCachedRBGMiss(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "cached-miss", Namespace: "default", UID: "uid-cached-miss"},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			TargetRoleBasedGroup: &workloadsv1alpha2.TargetRoleBasedGroup{
				Name: "target-rbg",
				Roles: map[string]workloadsv1alpha2.WarmupActions{
					"worker": {ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{"busybox:1.36"}}},
				},
			},
		},
	}
	rbg := &workloadsv1alpha2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Name: "target-rbg", Namespace: "default"}}
	targetPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "target-rbg-worker-0", Namespace: "default",
			Labels: map[string]string{constants.GroupNameLabelKey: "target-rbg", constants.RoleNameLabelKey: "worker"},
		},
		Spec: corev1.PodSpec{NodeName: "node-1"},
	}
	scheme := newWarmupTestScheme()
	cachedClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(warmup).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*workloadsv1alpha2.RoleBasedGroup); ok && key.Name == rbg.Name {
					return apierrors.NewNotFound(workloadsv1alpha2.GroupVersion.WithResource("rolebasedgroups").GroupResource(), key.Name)
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()
	apiClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(rbg, targetPod).Build()
	r := &RoleBasedGroupWarmupReconciler{Client: cachedClient, apiReader: apiClient, Scheme: scheme, Recorder: record.NewFakeRecorder(10)}

	desired, err := r.getDesiredNodesToWarmup(ctx, *warmup)
	if err != nil {
		t.Fatalf("cached miss should fall back to API reader: %v", err)
	}
	if len(desired) != 1 || len(desired["node-1"]) != 1 {
		t.Fatalf("expected actions for node-1, got %#v", desired)
	}
}

// ==================== Target readiness / ordering regression tests ====================

func warmupTargetRBGSpec(name string) workloadsv1alpha2.RoleBasedGroupWarmupSpec {
	return workloadsv1alpha2.RoleBasedGroupWarmupSpec{
		TargetRoleBasedGroup: &workloadsv1alpha2.TargetRoleBasedGroup{
			Name: name,
			Roles: map[string]workloadsv1alpha2.WarmupActions{
				"worker": {ImagePreload: &workloadsv1alpha2.ImagePreloadAction{Images: []string{"busybox:1.36"}}},
			},
		},
	}
}

// A target RoleBasedGroup created after the Warmup must be picked up instead of
// permanently failing the one-shot Warmup.
func TestReconcile_MissingTargetRBGRecoversWhenCreatedLater(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "late-target", Namespace: "default", UID: "uid-late-target"},
		Spec:       warmupTargetRBGSpec("late-rbg"),
	}
	r := newWarmupReconciler(warmup)

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("missing target should requeue while waiting, got %#v", result)
	}

	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get warmup: %v", err)
	}
	if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhaseRunning {
		t.Fatalf("expected Running while waiting for target, got %q", updated.Status.Phase)
	}
	if cond := apimeta.FindStatusCondition(updated.Status.Conditions, ConditionTargetReady); cond == nil ||
		cond.Status != metav1.ConditionFalse || cond.Reason != "RoleBasedGroupNotFound" {
		t.Fatalf("expected TargetReady=False/RoleBasedGroupNotFound, got %#v", updated.Status.Conditions)
	}

	// The target appears; the Warmup must now proceed.
	rbg := &workloadsv1alpha2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Name: "late-rbg", Namespace: "default"}}
	targetPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "late-rbg-worker-0", Namespace: "default",
			Labels: map[string]string{constants.GroupNameLabelKey: "late-rbg", constants.RoleNameLabelKey: "worker"},
		},
		Spec: corev1.PodSpec{NodeName: "node-1"},
	}
	if err := r.Create(ctx, rbg); err != nil {
		t.Fatalf("create rbg: %v", err)
	}
	if err := r.Create(ctx, targetPod); err != nil {
		t.Fatalf("create target pod: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)}); err != nil {
		t.Fatalf("reconcile after target created: %v", err)
	}

	after := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(warmup), after); err != nil {
		t.Fatalf("get warmup: %v", err)
	}
	if after.Status.Phase == workloadsv1alpha2.WarmupJobPhaseFailed {
		t.Fatalf("warmup must recover once the target exists, got %q (%#v)", after.Status.Phase, after.Status.Conditions)
	}
	if cond := apimeta.FindStatusCondition(after.Status.Conditions, ConditionTargetReady); cond == nil ||
		cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected TargetReady=True after target exists, got %#v", after.Status.Conditions)
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.MatchingLabels{LabelWarmupName: warmup.Name}); err != nil {
		t.Fatalf("list warmup pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("expected 1 warmup pod once target is ready, got %d", len(pods.Items))
	}
}

// A partially scheduled selected role must not start a warmup for only the bound node.
func TestReconcile_PartiallyScheduledSelectedRoleWaits(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "partial-scheduling", Namespace: "default", UID: "uid-partial-scheduling"},
		Spec:       warmupTargetRBGSpec("partial-scheduling-rbg"),
	}
	rbg := &workloadsv1alpha2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Name: "partial-scheduling-rbg", Namespace: "default"}}
	boundPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "partial-scheduling-worker-0", Namespace: "default",
			Labels: map[string]string{constants.GroupNameLabelKey: "partial-scheduling-rbg", constants.RoleNameLabelKey: "worker"},
		},
		Spec: corev1.PodSpec{NodeName: "node-0"},
	}
	pendingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "partial-scheduling-worker-1", Namespace: "default",
			Labels: map[string]string{constants.GroupNameLabelKey: "partial-scheduling-rbg", constants.RoleNameLabelKey: "worker"},
		},
		Spec: corev1.PodSpec{NodeName: ""},
	}
	r := newWarmupReconciler(warmup, rbg, boundPod, pendingPod)

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)})
	if err != nil {
		t.Fatalf("reconcile with partially scheduled role: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("expected requeue while selected target Pod is unscheduled, got %#v", result)
	}

	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get warmup: %v", err)
	}
	if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhaseRunning {
		t.Fatalf("expected Running while selected target Pod is unscheduled, got %q", updated.Status.Phase)
	}
	if cond := apimeta.FindStatusCondition(updated.Status.Conditions, ConditionTargetReady); cond == nil ||
		cond.Status != metav1.ConditionFalse || cond.Reason != "TargetPodsNotScheduled" {
		t.Fatalf("expected TargetReady=False/TargetPodsNotScheduled, got %#v", updated.Status.Conditions)
	}
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.MatchingLabels{LabelWarmupName: warmup.Name}); err != nil {
		t.Fatalf("list warmup pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("no warmup Pod should be created while a selected target Pod is unscheduled, got %d", len(pods.Items))
	}

	scheduled := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pendingPod), scheduled); err != nil {
		t.Fatalf("get pending target pod: %v", err)
	}
	scheduled.Spec.NodeName = "node-1"
	if err := r.Update(ctx, scheduled); err != nil {
		t.Fatalf("schedule pending target pod: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)}); err != nil {
		t.Fatalf("reconcile after scheduling: %v", err)
	}

	after := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(warmup), after); err != nil {
		t.Fatalf("get warmup after scheduling: %v", err)
	}
	if after.Status.Desired != 2 {
		t.Fatalf("expected desired=2 after both target Pods are scheduled, got %d (conditions=%#v)", after.Status.Desired, after.Status.Conditions)
	}
	pods = &corev1.PodList{}
	if err := r.List(ctx, pods, client.MatchingLabels{LabelWarmupName: warmup.Name}); err != nil {
		t.Fatalf("list warmup pods after scheduling: %v", err)
	}
	if len(pods.Items) != 2 {
		t.Fatalf("expected 2 warmup Pods after target is ready, got %d", len(pods.Items))
	}
}

// A pending Pod in an unselected role must not prevent a Warmup whose selected role
// has no Pods from completing with NoNodesMatched.
func TestReconcile_UnselectedPendingRoleDoesNotBlock(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "unselected-role", Namespace: "default", UID: "uid-unselected-role"},
		Spec:       warmupTargetRBGSpec("unselected-role-rbg"),
	}
	rbg := &workloadsv1alpha2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Name: "unselected-role-rbg", Namespace: "default"}}
	pendingRouter := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "unselected-role-router-0", Namespace: "default",
			Labels: map[string]string{constants.GroupNameLabelKey: "unselected-role-rbg", constants.RoleNameLabelKey: "router"},
		},
		Spec: corev1.PodSpec{NodeName: ""},
	}
	r := newWarmupReconciler(warmup, rbg, pendingRouter)

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)})
	if err != nil {
		t.Fatalf("reconcile with pending unselected role: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("pending unselected role should not cause a requeue, got %#v", result)
	}

	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get warmup: %v", err)
	}
	if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhaseCompleted {
		t.Fatalf("expected Completed when selected role has no Pods, got %q (conditions=%#v)",
			updated.Status.Phase, updated.Status.Conditions)
	}
	if cond := apimeta.FindStatusCondition(updated.Status.Conditions, "Complete"); cond == nil || cond.Reason != "NoNodesMatched" {
		t.Fatalf("expected Complete/NoNodesMatched, got %#v", updated.Status.Conditions)
	}
}

// A lingering missing target must still fail terminally when globalTimeoutSeconds lapses.
func TestReconcile_MissingTargetRBGFailsAfterGlobalTimeout(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{
			Name: "late-target-timeout", Namespace: "default", UID: "uid-late-timeout",
			// Make the (creation-time) deadline already expired.
			CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * time.Minute)),
		},
		Spec: warmupTargetRBGSpec("never-rbg"),
	}
	warmup.Spec.Policies = &workloadsv1alpha2.WarmupPolicies{GlobalTimeoutSeconds: ptr.To(int64(60))}
	r := newWarmupReconciler(warmup)

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("expired wait should not requeue, got %#v", result)
	}
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get warmup: %v", err)
	}
	if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhaseFailed {
		t.Fatalf("expected Failed after global timeout, got %q", updated.Status.Phase)
	}
	if cond := apimeta.FindStatusCondition(updated.Status.Conditions, "Failed"); cond == nil || cond.Reason != "InvalidTarget" {
		t.Fatalf("expected Failed/InvalidTarget, got %#v", updated.Status.Conditions)
	}
}

// An existing target RBG whose Pods are not scheduled yet must not be reported as a
// successful (but empty) warmup.
func TestReconcile_UnscheduledTargetPodsWaitInsteadOfCompleting(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "unscheduled", Namespace: "default", UID: "uid-unscheduled"},
		Spec:       warmupTargetRBGSpec("pending-rbg"),
	}
	rbg := &workloadsv1alpha2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Name: "pending-rbg", Namespace: "default"}}
	pendingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pending-rbg-worker-0", Namespace: "default",
			Labels: map[string]string{constants.GroupNameLabelKey: "pending-rbg", constants.RoleNameLabelKey: "worker"},
		},
		Spec: corev1.PodSpec{NodeName: ""}, // not scheduled yet
	}
	r := newWarmupReconciler(warmup, rbg, pendingPod)

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get warmup: %v", err)
	}
	if updated.Status.Phase == workloadsv1alpha2.WarmupJobPhaseCompleted {
		t.Fatalf("unscheduled target Pods must not complete the warmup (desired=%d, conditions=%#v)",
			updated.Status.Desired, updated.Status.Conditions)
	}
	if result.RequeueAfter == 0 {
		t.Fatalf("expected requeue while waiting for Pods to schedule, got %#v", result)
	}
	if cond := apimeta.FindStatusCondition(updated.Status.Conditions, ConditionTargetReady); cond == nil ||
		cond.Status != metav1.ConditionFalse || cond.Reason != "TargetPodsNotScheduled" {
		t.Fatalf("expected TargetReady=False/TargetPodsNotScheduled, got %#v", updated.Status.Conditions)
	}

	// Once the Pod is scheduled the warmup proceeds normally.
	scheduled := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pendingPod), scheduled); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	scheduled.Spec.NodeName = "node-1"
	if err := r.Update(ctx, scheduled); err != nil {
		t.Fatalf("schedule pod: %v", err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)}); err != nil {
		t.Fatalf("reconcile after scheduling: %v", err)
	}
	after := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(warmup), after); err != nil {
		t.Fatalf("get warmup: %v", err)
	}
	if after.Status.Desired != 1 {
		t.Fatalf("expected desired=1 after Pod scheduled, got %d (conditions=%#v)", after.Status.Desired, after.Status.Conditions)
	}
}

// An existing target RBG with no Pods at all for the requested roles still completes
// immediately with NoNodesMatched (no Pods to wait for).
func TestReconcile_TargetRBGWithNoPodsCompletesNoNodesMatched(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "empty-target", Namespace: "default", UID: "uid-empty-target"},
		Spec:       warmupTargetRBGSpec("empty-rbg"),
	}
	rbg := &workloadsv1alpha2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Name: "empty-rbg", Namespace: "default"}}
	r := newWarmupReconciler(warmup, rbg)

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("no Pods to wait for, should not requeue, got %#v", result)
	}
	updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(warmup), updated); err != nil {
		t.Fatalf("get warmup: %v", err)
	}
	if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhaseCompleted {
		t.Fatalf("expected Completed, got %q", updated.Status.Phase)
	}
	if cond := apimeta.FindStatusCondition(updated.Status.Conditions, "Complete"); cond == nil || cond.Reason != "NoNodesMatched" {
		t.Fatalf("expected Complete/NoNodesMatched, got %#v", updated.Status.Conditions)
	}
}

// A Warmup whose customizedAction containers cannot be represented in a Pod must be
// rejected by validation and never reach Pod creation.
func TestReconcile_InvalidCustomizedContainerGapIsClosed(t *testing.T) {
	ctx := ctrl.LoggerInto(context.Background(), zap.New(zap.UseDevMode(true)))
	for name, image := range map[string]string{"empty": "", "whitespace": " \t"} {
		t.Run(name, func(t *testing.T) {
			warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
				ObjectMeta: metav1.ObjectMeta{Name: "gap-" + name, Namespace: "default", UID: types.UID(name)},
				Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
					TargetNodes: &workloadsv1alpha2.TargetNodes{
						NodeNames: []string{"node-1"},
						WarmupActions: workloadsv1alpha2.WarmupActions{
							CustomizedAction: &workloadsv1alpha2.CustomizedAction{
								Containers: []corev1.Container{{Name: "c1", Image: image}},
							},
						},
					},
				},
			}
			r := newWarmupReconciler(warmup)
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(warmup)}); err != nil {
				t.Fatalf("unexpected reconcile error: %v", err)
			}
			updated := &workloadsv1alpha2.RoleBasedGroupWarmup{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(warmup), updated); err != nil {
				t.Fatalf("get warmup: %v", err)
			}
			if updated.Status.Phase != workloadsv1alpha2.WarmupJobPhaseFailed {
				t.Fatalf("expected Failed, got %q", updated.Status.Phase)
			}
			if cond := apimeta.FindStatusCondition(updated.Status.Conditions, "Failed"); cond == nil || cond.Reason != "InvalidWarmupSpec" {
				t.Fatalf("expected Failed/InvalidWarmupSpec, got %#v", updated.Status.Conditions)
			}
			pods := &corev1.PodList{}
			if err := r.List(ctx, pods); err != nil {
				t.Fatalf("list pods: %v", err)
			}
			if len(pods.Items) != 0 {
				t.Fatalf("no Pod must be created for an invalid image, got %d", len(pods.Items))
			}
			// And the invalid Pod spec must be rejected before it reaches the API server.
			if _, ok := buildWarmupPodForTest(t, warmup, "node-1"); ok {
				t.Fatal("buildWarmupPod should not be reached for an invalid image")
			}
		})
	}
}

// buildWarmupPodForTest returns whether the resulting Pod would be accepted by the API
// server's own validation: a container with an empty image is invalid.
func buildWarmupPodForTest(t *testing.T, warmup *workloadsv1alpha2.RoleBasedGroupWarmup, node string) (*corev1.Pod, bool) {
	t.Helper()
	r := newWarmupReconciler()
	pod, _ := r.buildWarmupPod(warmup, node, []workloadsv1alpha2.WarmupActions{warmup.Spec.TargetNodes.WarmupActions})
	for _, c := range pod.Spec.Containers {
		if strings.TrimSpace(c.Image) == "" {
			return pod, false
		}
	}
	return pod, true
}
