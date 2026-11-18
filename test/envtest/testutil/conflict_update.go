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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// UpdatePodStatusOnConflict updates a Pod's status through a read-modify-write
// cycle that rereads the object and reapplies mutate on every conflict retry.
// Both object and status writes advance resourceVersion, so a controller that
// touches the Pod between the read and the write otherwise fails the fixture
// with an optimistic-concurrency error. A retry must never reuse the stale
// resourceVersion of the original read.
func UpdatePodStatusOnConflict(ctx context.Context, key client.ObjectKey, mutate func(*corev1.Pod)) error {
	return updateOnConflict(ctx, key, mutate, func(pod *corev1.Pod) error {
		return K8sClient.Status().Update(ctx, pod)
	})
}

// UpdatePodOnConflict updates a Pod object (typically metadata) through a
// read-modify-write cycle with conflict retries. It is the fixture-side
// counterpart of the controller write paths, which already use
// retry.RetryOnConflict.
func UpdatePodOnConflict(ctx context.Context, key client.ObjectKey, mutate func(*corev1.Pod)) error {
	return updateOnConflict(ctx, key, mutate, func(pod *corev1.Pod) error {
		return K8sClient.Update(ctx, pod)
	})
}

func updateOnConflict(ctx context.Context, key client.ObjectKey, mutate func(*corev1.Pod), write func(*corev1.Pod) error) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		pod := &corev1.Pod{}
		if err := K8sClient.Get(ctx, key, pod); err != nil {
			return err
		}
		mutate(pod)
		return write(pod)
	})
}

// UpdateRoleInstanceStatusOnConflict updates a RoleInstance's status through a
// read-modify-write cycle with conflict retries, rereading the object before
// every attempt.
func UpdateRoleInstanceStatusOnConflict(ctx context.Context, key client.ObjectKey, mutate func(*workloadsv1alpha2.RoleInstance)) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ri := &workloadsv1alpha2.RoleInstance{}
		if err := K8sClient.Get(ctx, key, ri); err != nil {
			return err
		}
		mutate(ri)
		return K8sClient.Status().Update(ctx, ri)
	})
}
