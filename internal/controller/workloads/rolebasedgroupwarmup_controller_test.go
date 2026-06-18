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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

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
