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
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestBuildWarmupPodCustomizedActionMetadata(t *testing.T) {
	r := newWarmupReconciler()
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
	}
	actions := []workloadsv1alpha2.WarmupActions{
		{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			TimeoutSeconds: ptr.To(int64(120)),
			Containers: []corev1.Container{
				{Name: "prefill-check", Image: "busybox", Command: []string{"true"}},
			},
		}},
		{CustomizedAction: &workloadsv1alpha2.CustomizedAction{
			TimeoutSeconds: ptr.To(int64(30)),
			Containers: []corev1.Container{
				{Name: "decode-check", Image: "alpine", Command: []string{"true"}},
			},
		}},
	}

	pod, conflict := r.buildWarmupPod(warmup, "node-1", actions)
	if conflict {
		t.Fatal("unexpected volume conflict")
	}
	if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 30 {
		t.Fatalf("expected minimum deadline 30, got %v", pod.Spec.ActiveDeadlineSeconds)
	}

	mappings, err := customizedActionMappingsFromPod(pod)
	if err != nil {
		t.Fatalf("decode mappings: %v", err)
	}
	want := []customizedActionContainerMapping{
		{PodContainerName: "custom-0", ContainerNames: []string{"prefill-check"}},
		{PodContainerName: "custom-1", ContainerNames: []string{"decode-check"}},
	}
	if !apiequality.Semantic.DeepEqual(want, mappings) {
		t.Fatalf("expected mappings %#v, got %#v", want, mappings)
	}
}

func TestBuildWarmupPodCustomizedActionMetadataDeduplicatesIdentities(t *testing.T) {
	r := newWarmupReconciler()
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "uid-1"},
	}
	actions := []workloadsv1alpha2.WarmupActions{
		{CustomizedAction: &workloadsv1alpha2.CustomizedAction{Containers: []corev1.Container{
			{Name: "zeta-check", Image: "busybox", Command: []string{"true"}},
		}}},
		{CustomizedAction: &workloadsv1alpha2.CustomizedAction{Containers: []corev1.Container{
			{Name: "alpha-check", Image: "busybox", Command: []string{"true"}},
		}}},
	}

	pod, _ := r.buildWarmupPod(warmup, "node-1", actions)
	if pod.Spec.ActiveDeadlineSeconds != nil {
		t.Fatalf("expected no deadline, got %d", *pod.Spec.ActiveDeadlineSeconds)
	}
	mappings, err := customizedActionMappingsFromPod(pod)
	if err != nil {
		t.Fatalf("decode mappings: %v", err)
	}
	want := []customizedActionContainerMapping{{
		PodContainerName: "custom-0",
		ContainerNames:   []string{"alpha-check", "zeta-check"},
	}}
	if !apiequality.Semantic.DeepEqual(want, mappings) {
		t.Fatalf("expected mappings %#v, got %#v", want, mappings)
	}
}

func TestCustomizedActionMappingsFromPodRejectsMalformedJSON(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		AnnotationCustomizedActionContainers: "not-json",
	}}}
	if _, err := customizedActionMappingsFromPod(pod); err == nil {
		t.Fatal("expected malformed mapping annotation to fail")
	}
}
