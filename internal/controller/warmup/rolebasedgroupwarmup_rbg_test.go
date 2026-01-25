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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func TestGetDesiredNodesToWarmUp_TargetRoleBasedGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	// Create a RoleBasedGroup
	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
		},
	}

	// Create Pods belonging to the RBG with different roles on different nodes
	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prefill-0",
			Namespace: "default",
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: "test-rbg",
				workloadsv1alpha1.SetRoleLabelKey: "prefill",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
		},
	}

	pod2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "decode-0",
			Namespace: "default",
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: "test-rbg",
				workloadsv1alpha1.SetRoleLabelKey: "decode",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-2",
		},
	}

	pod3 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prefill-1",
			Namespace: "default",
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: "test-rbg",
				workloadsv1alpha1.SetRoleLabelKey: "prefill",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-1", // Same node as pod1
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(rbg, pod1, pod2, pod3).
		Build()

	reconciler := &RoleBasedGroupWarmUpReconciler{
		Client:   client,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	warmup := workloadsv1alpha1.RoleBasedGroupWarmUp{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-warmup",
			Namespace: "default",
		},
		Spec: workloadsv1alpha1.RoleBasedGroupWarmUpSpec{
			TargetRoleBasedGroup: &workloadsv1alpha1.TargetRoleBasedGroup{
				Name: "test-rbg",
				Roles: map[string]workloadsv1alpha1.WarmUpActions{
					"prefill": {
						ImagePreload: &workloadsv1alpha1.ImagePreloadAction{
							Items: []string{"nginx:latest", "redis:7.0"},
						},
					},
					"decode": {
						ImagePreload: &workloadsv1alpha1.ImagePreloadAction{
							Items: []string{"postgres:15"},
						},
					},
				},
			},
		},
	}

	nodesWithActions, err := reconciler.getDesiredNodesToWarmUp(context.Background(), warmup)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	// Verify node-1 has prefill actions (from 2 prefill pods on same node)
	if actions, exists := nodesWithActions["node-1"]; !exists {
		t.Error("Expected node-1 to have warmup actions")
	} else if len(actions) != 1 {
		t.Errorf("Expected node-1 to have 1 action set, got %d", len(actions))
	}

	// Verify node-2 has decode actions
	if actions, exists := nodesWithActions["node-2"]; !exists {
		t.Error("Expected node-2 to have warmup actions")
	} else if len(actions) != 1 {
		t.Errorf("Expected node-2 to have 1 action set, got %d", len(actions))
	}
}

func TestGetDesiredNodesToWarmUp_MixedTargets(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	rbg := &workloadsv1alpha1.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-rbg",
			Namespace: "default",
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			Labels: map[string]string{
				workloadsv1alpha1.SetNameLabelKey: "test-rbg",
				workloadsv1alpha1.SetRoleLabelKey: "worker",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(rbg, pod).
		Build()

	reconciler := &RoleBasedGroupWarmUpReconciler{
		Client:   client,
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	// Test with both TargetNodes and TargetRoleBasedGroup
	warmup := workloadsv1alpha1.RoleBasedGroupWarmUp{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-warmup",
			Namespace: "default",
		},
		Spec: workloadsv1alpha1.RoleBasedGroupWarmUpSpec{
			TargetNodes: &workloadsv1alpha1.TargetNodes{
				NodeNames: []string{"node-2"},
				WarmUpActions: workloadsv1alpha1.WarmUpActions{
					ImagePreload: &workloadsv1alpha1.ImagePreloadAction{
						Items: []string{"busybox:latest"},
					},
				},
			},
			TargetRoleBasedGroup: &workloadsv1alpha1.TargetRoleBasedGroup{
				Name: "test-rbg",
				Roles: map[string]workloadsv1alpha1.WarmUpActions{
					"worker": {
						ImagePreload: &workloadsv1alpha1.ImagePreloadAction{
							Items: []string{"nginx:latest"},
						},
					},
				},
			},
		},
	}

	nodesWithActions, err := reconciler.getDesiredNodesToWarmUp(context.Background(), warmup)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	// Should have both node-1 (from RBG) and node-2 (from TargetNodes)
	if len(nodesWithActions) != 2 {
		t.Errorf("Expected 2 nodes with actions, got %d", len(nodesWithActions))
	}

	if _, exists := nodesWithActions["node-1"]; !exists {
		t.Error("Expected node-1 to have warmup actions")
	}

	if _, exists := nodesWithActions["node-2"]; !exists {
		t.Error("Expected node-2 to have warmup actions")
	}
}

func TestBuildWarmUpPod_MergedActions(t *testing.T) {
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
	}

	// Multiple actions with overlapping images
	actions := []workloadsv1alpha1.WarmUpActions{
		{
			ImagePreload: &workloadsv1alpha1.ImagePreloadAction{
				Items: []string{"nginx:latest", "redis:7.0"},
				PullSecrets: []corev1.LocalObjectReference{
					{Name: "secret-1"},
				},
			},
		},
		{
			ImagePreload: &workloadsv1alpha1.ImagePreloadAction{
				Items: []string{"nginx:latest", "postgres:15"}, // nginx is duplicate
				PullSecrets: []corev1.LocalObjectReference{
					{Name: "secret-2"},
				},
			},
		},
	}

	pod := reconciler.buildWarmUpPod(warmup, "node-1", actions)

	// Verify images are deduplicated
	if len(pod.Spec.Containers) != 3 {
		t.Errorf("Expected 3 unique containers, got %d", len(pod.Spec.Containers))
	}

	// Verify pull secrets are deduplicated
	if len(pod.Spec.ImagePullSecrets) != 2 {
		t.Errorf("Expected 2 unique pull secrets, got %d", len(pod.Spec.ImagePullSecrets))
	}

	// Verify image names
	imageSet := make(map[string]bool)
	for _, container := range pod.Spec.Containers {
		imageSet[container.Image] = true
	}

	expectedImages := []string{"nginx:latest", "redis:7.0", "postgres:15"}
	for _, img := range expectedImages {
		if !imageSet[img] {
			t.Errorf("Expected image %s in containers", img)
		}
	}
}

func TestValidate_TargetRoleBasedGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)

	reconciler := &RoleBasedGroupWarmUpReconciler{
		Client:   fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme:   scheme,
		Recorder: record.NewFakeRecorder(10),
	}

	tests := []struct {
		name    string
		warmup  workloadsv1alpha1.RoleBasedGroupWarmUp
		wantErr bool
	}{
		{
			name: "valid TargetRoleBasedGroup",
			warmup: workloadsv1alpha1.RoleBasedGroupWarmUp{
				Spec: workloadsv1alpha1.RoleBasedGroupWarmUpSpec{
					TargetRoleBasedGroup: &workloadsv1alpha1.TargetRoleBasedGroup{
						Name: "test-rbg",
						Roles: map[string]workloadsv1alpha1.WarmUpActions{
							"worker": {
								ImagePreload: &workloadsv1alpha1.ImagePreloadAction{
									Items: []string{"nginx:latest"},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "valid with no namespace field",
			warmup: workloadsv1alpha1.RoleBasedGroupWarmUp{
				Spec: workloadsv1alpha1.RoleBasedGroupWarmUpSpec{
					TargetRoleBasedGroup: &workloadsv1alpha1.TargetRoleBasedGroup{
						Name: "test-rbg",
						Roles: map[string]workloadsv1alpha1.WarmUpActions{
							"worker": {},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "missing name",
			warmup: workloadsv1alpha1.RoleBasedGroupWarmUp{
				Spec: workloadsv1alpha1.RoleBasedGroupWarmUpSpec{
					TargetRoleBasedGroup: &workloadsv1alpha1.TargetRoleBasedGroup{
						Roles: map[string]workloadsv1alpha1.WarmUpActions{
							"worker": {},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "no roles",
			warmup: workloadsv1alpha1.RoleBasedGroupWarmUp{
				Spec: workloadsv1alpha1.RoleBasedGroupWarmUpSpec{
					TargetRoleBasedGroup: &workloadsv1alpha1.TargetRoleBasedGroup{
						Name:  "test-rbg",
						Roles: map[string]workloadsv1alpha1.WarmUpActions{},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "no targets specified",
			warmup: workloadsv1alpha1.RoleBasedGroupWarmUp{
				Spec: workloadsv1alpha1.RoleBasedGroupWarmUpSpec{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reconciler.validate(tt.warmup)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
