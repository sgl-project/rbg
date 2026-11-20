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

package podcache

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

const (
	ScopeAll      = "all"
	ScopeFiltered = "filtered"
)

func ValidateScope(scope string) error {
	if scope != ScopeAll && scope != ScopeFiltered {
		return fmt.Errorf("invalid --pod-cache-scope %q: expected all or filtered", scope)
	}
	return nil
}

// Admitted controls retention, not ownership or adoption.
func Admitted(pod *corev1.Pod) bool {
	for _, key := range constants.LegacyPodLabelKeys() {
		if _, ok := pod.Labels[key]; ok {
			return true
		}
	}
	if pod.Labels[constants.ManagedLabelKey] == constants.ManagedLabelValue {
		return true
	}
	for _, owner := range pod.OwnerReferences {
		gv, err := schema.ParseGroupVersion(owner.APIVersion)
		if err == nil && gv.Group == "workloads.x-k8s.io" {
			return true
		}
	}
	return false
}

// Transform is idempotent: watched objects pass through it before the reflector
// and again when they enter the informer's queue.
func Transform(obj interface{}) (interface{}, error) {
	pod, ok := obj.(*corev1.Pod)
	if !ok || pod == nil {
		return obj, nil
	}
	if Admitted(pod) {
		pod.ManagedFields = nil
		return pod, nil
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: pod.Name, Namespace: pod.Namespace, UID: pod.UID,
		ResourceVersion: pod.ResourceVersion, DeletionTimestamp: pod.DeletionTimestamp,
		Labels: pod.Labels, OwnerReferences: pod.OwnerReferences,
	}}, nil
}
