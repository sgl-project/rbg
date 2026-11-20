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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

var (
	// ErrObjectReplaced is returned when the object under the selected key has
	// a different UID than the caller's snapshot. The fixture must re-list and
	// re-target a replacement rather than mutating it silently.
	ErrObjectReplaced = errors.New("object replaced while updating")

	// ErrConflictExhausted is returned when conflict retries are exhausted.
	// Polling fixtures can treat it as transient and retry on the next poll.
	ErrConflictExhausted = errors.New("conflict retries exhausted")
)

// IsRetryable reports whether err represents a transient condition that a
// polling fixture can retry by re-listing and re-targeting objects.
func IsRetryable(err error) bool {
	return errors.Is(err, ErrObjectReplaced) || errors.Is(err, ErrConflictExhausted)
}

// ShouldSkipPodStatusUpdate reports whether a fixture should leave a Pod's
// status untouched. It covers terminating Pods and both terminal phases.
func ShouldSkipPodStatusUpdate(pod *corev1.Pod) bool {
	return pod.DeletionTimestamp != nil ||
		pod.Status.Phase == corev1.PodFailed ||
		pod.Status.Phase == corev1.PodSucceeded
}

// UpdatePodStatusOnConflict updates a Pod's status through a read-modify-write
// cycle that rereads the object and reapplies mutate on every conflict retry.
// Both object and status writes advance resourceVersion, so a controller that
// touches the Pod between the read and the write otherwise fails the fixture
// with an optimistic-concurrency error. A retry must never reuse the stale
// resourceVersion of the original read.
// snapshot supplies the selected object's name, namespace, and UID; its other
// fields are not used. mutate returns true when the freshly read object should
// be left unwritten.
func UpdatePodStatusOnConflict(ctx context.Context, snapshot *corev1.Pod, mutate func(*corev1.Pod) bool) error {
	return updateOnConflict(ctx, snapshot, mutate, func(pod *corev1.Pod) error {
		return K8sClient.Status().Update(ctx, pod)
	})
}

// UpdatePodOnConflict updates a Pod object (typically metadata) through a
// read-modify-write cycle with conflict retries. It is the fixture-side
// counterpart of the controller write paths, which already use
// retry.RetryOnConflict.
func UpdatePodOnConflict(ctx context.Context, snapshot *corev1.Pod, mutate func(*corev1.Pod) bool) error {
	return updateOnConflict(ctx, snapshot, mutate, func(pod *corev1.Pod) error {
		return K8sClient.Update(ctx, pod)
	})
}

func updateOnConflict(ctx context.Context, snapshot *corev1.Pod, mutate func(*corev1.Pod) bool, write func(*corev1.Pod) error) error {
	if snapshot.UID == "" {
		return fmt.Errorf("%s: snapshot UID is empty", client.ObjectKeyFromObject(snapshot))
	}
	key := client.ObjectKeyFromObject(snapshot)
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		pod := &corev1.Pod{}
		if err := K8sClient.Get(ctx, key, pod); err != nil {
			return err
		}
		if err := checkUID(key, snapshot.UID, pod.UID); err != nil {
			return err
		}
		if mutate(pod) {
			return nil
		}
		return write(pod)
	})
	if err != nil {
		if errors.Is(err, ErrObjectReplaced) {
			return err
		}
		if apierrors.IsConflict(err) {
			return fmt.Errorf("%w: %v", ErrConflictExhausted, err)
		}
	}
	return err
}

// checkUID reports an error when the object under key was deleted and
// recreated after the caller selected it: the mutation then targets a
// different object, and the fixture must be re-targeted.
func checkUID(key client.ObjectKey, expected, got types.UID) error {
	if expected != got {
		return fmt.Errorf("%w: %s changed UID from %s to %s", ErrObjectReplaced, key, expected, got)
	}
	return nil
}

// UpdateRoleInstanceStatusOnConflict updates a RoleInstance's status through a
// read-modify-write cycle with conflict retries, rereading the object before
// every attempt.
func UpdateRoleInstanceStatusOnConflict(ctx context.Context, snapshot *workloadsv1alpha2.RoleInstance, mutate func(*workloadsv1alpha2.RoleInstance) bool) error {
	if snapshot.UID == "" {
		return fmt.Errorf("%s: snapshot UID is empty", client.ObjectKeyFromObject(snapshot))
	}
	key := client.ObjectKeyFromObject(snapshot)
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		ri := &workloadsv1alpha2.RoleInstance{}
		if err := K8sClient.Get(ctx, key, ri); err != nil {
			return err
		}
		if err := checkUID(key, snapshot.UID, ri.UID); err != nil {
			return err
		}
		if mutate(ri) {
			return nil
		}
		return K8sClient.Status().Update(ctx, ri)
	})
	if err != nil {
		if errors.Is(err, ErrObjectReplaced) {
			return err
		}
		if apierrors.IsConflict(err) {
			return fmt.Errorf("%w: %v", ErrConflictExhausted, err)
		}
	}
	return err
}
