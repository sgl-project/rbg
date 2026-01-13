/*
Copyright 2025.

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

package workloadsxk8sio

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func TestRoleBasedGroupWarmUpReconciler_buildWarmUpPod(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	reconciler := &RoleBasedGroupWarmUpReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	warmup := &workloadsv1alpha1.RoleBasedGroupWarmUp{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-warmup",
			Namespace: "default",
			UID:       types.UID("test-uid"),
		},
		Spec: workloadsv1alpha1.RoleBasedGroupWarmUpSpec{
			TargetNodes: &workloadsv1alpha1.TargetNodes{
				NodeNames: []string{"node-1", "node-2"},
				WarmUpActions: workloadsv1alpha1.WarmUpActions{
					ImagePreload: &workloadsv1alpha1.ImagePreloadAction{
						Items: []string{"nginx:latest", "busybox:latest"},
						PullSecrets: []corev1.LocalObjectReference{
							{Name: "my-secret"},
						},
					},
				},
			},
		},
	}

	pod := reconciler.buildWarmUpPod(warmup, "node-1", []workloadsv1alpha1.WarmUpActions{warmup.Spec.TargetNodes.WarmUpActions})

	// Verify pod metadata
	if pod.Name != "test-warmup-node-1" {
		t.Errorf("Expected pod name 'test-warmup-node-1', got '%s'", pod.Name)
	}
	if pod.Namespace != "default" {
		t.Errorf("Expected pod namespace 'default', got '%s'", pod.Namespace)
	}

	// Verify pod labels
	if pod.Labels[LabelWarmUpName] != "test-warmup" {
		t.Errorf("Expected warmup-name label 'test-warmup', got '%s'", pod.Labels[LabelWarmUpName])
	}
	if pod.Labels[LabelWarmUpUID] != "test-uid" {
		t.Errorf("Expected warmup-uid label 'test-uid', got '%s'", pod.Labels[LabelWarmUpUID])
	}
	if pod.Labels[LabelNodeName] != "node-1" {
		t.Errorf("Expected node-name label 'node-1', got '%s'", pod.Labels[LabelNodeName])
	}

	// Verify pod spec
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("Expected RestartPolicy Never, got '%s'", pod.Spec.RestartPolicy)
	}
	if pod.Spec.NodeSelector["kubernetes.io/hostname"] != "node-1" {
		t.Errorf("Expected node selector 'node-1', got '%s'", pod.Spec.NodeSelector["kubernetes.io/hostname"])
	}

	// Verify init containers (images are loaded as init containers)
	if len(pod.Spec.InitContainers) != 2 {
		t.Fatalf("Expected 2 init containers, got %d", len(pod.Spec.InitContainers))
	}
	if pod.Spec.InitContainers[0].Name != "warmup-0" {
		t.Errorf("Expected container name 'warmup-0', got '%s'", pod.Spec.InitContainers[0].Name)
	}
	if pod.Spec.InitContainers[0].Image != "nginx:latest" {
		t.Errorf("Expected image 'nginx:latest', got '%s'", pod.Spec.InitContainers[0].Image)
	}
	if len(pod.Spec.InitContainers[0].Command) != 3 || pod.Spec.InitContainers[0].Command[0] != "sh" {
		t.Errorf("Expected command ['sh', '-c', 'exit 0'], got %v", pod.Spec.InitContainers[0].Command)
	}

	// Verify image pull secrets
	if len(pod.Spec.ImagePullSecrets) != 1 || pod.Spec.ImagePullSecrets[0].Name != "my-secret" {
		t.Errorf("Expected image pull secret 'my-secret', got %v", pod.Spec.ImagePullSecrets)
	}
}

func TestRoleBasedGroupWarmUpReconciler_statusEqual(t *testing.T) {
	now := metav1.Now()
	tests := []struct {
		name     string
		a        workloadsv1alpha1.RoleBasedGroupWarmUpStatus
		b        workloadsv1alpha1.RoleBasedGroupWarmUpStatus
		expected bool
	}{
		{
			name: "equal statuses",
			a: workloadsv1alpha1.RoleBasedGroupWarmUpStatus{
				Desired:   3,
				Active:    1,
				Ready:     1,
				Succeeded: 1,
				Failed:    1,
				Phase:     workloadsv1alpha1.WarmUpJobPhaseRunning,
				StartTime: now,
			},
			b: workloadsv1alpha1.RoleBasedGroupWarmUpStatus{
				Desired:   3,
				Active:    1,
				Ready:     1,
				Succeeded: 1,
				Failed:    1,
				Phase:     workloadsv1alpha1.WarmUpJobPhaseRunning,
				StartTime: now,
			},
			expected: true,
		},
		{
			name: "different desired",
			a: workloadsv1alpha1.RoleBasedGroupWarmUpStatus{
				Desired: 3,
			},
			b: workloadsv1alpha1.RoleBasedGroupWarmUpStatus{
				Desired: 2,
			},
			expected: false,
		},
		{
			name: "different phase",
			a: workloadsv1alpha1.RoleBasedGroupWarmUpStatus{
				Phase: workloadsv1alpha1.WarmUpJobPhaseRunning,
			},
			b: workloadsv1alpha1.RoleBasedGroupWarmUpStatus{
				Phase: workloadsv1alpha1.WarmUpJobPhaseCompleted,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := statusEqual(tt.a, tt.b)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestRoleBasedGroupWarmUpReconciler_isPodReady(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "pod is ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "pod is not ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "no ready condition",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPodReady(tt.pod)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestRoleBasedGroupWarmUpReconciler_Reconcile_SkipWhenDeleting(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	now := metav1.Now()
	warmup := &workloadsv1alpha1.RoleBasedGroupWarmUp{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-warmup",
			Namespace:         "default",
			Finalizers:        []string{"test-finalizer"},
			DeletionTimestamp: &now,
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(warmup).Build()
	reconciler := &RoleBasedGroupWarmUpReconciler{
		Client:   client,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-warmup",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.Background(), req)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if result.Requeue {
		t.Error("Expected no requeue")
	}
}

func TestRoleBasedGroupWarmUpReconciler_Reconcile_SkipWhenNoImages(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	// This warmup has no valid warmup actions, so validation should fail
	warmup := &workloadsv1alpha1.RoleBasedGroupWarmUp{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-warmup",
			Namespace: "default",
		},
		Spec: workloadsv1alpha1.RoleBasedGroupWarmUpSpec{
			// No targets specified at all
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(warmup).Build()
	reconciler := &RoleBasedGroupWarmUpReconciler{
		Client:   client,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-warmup",
			Namespace: "default",
		},
	}

	// This should fail validation
	result, err := reconciler.Reconcile(context.Background(), req)
	if err == nil {
		t.Error("Expected validation error, got nil")
	}
	if result.Requeue {
		t.Error("Expected no requeue")
	}
}
