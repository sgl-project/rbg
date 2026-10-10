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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	lwspod "sigs.k8s.io/lws/pkg/utils/pod"
	lwsrevision "sigs.k8s.io/lws/pkg/utils/revision"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	inplaceinstance "sigs.k8s.io/rbgs/pkg/inplace/instance"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
	risutils "sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statelessmode/utils"
)

// targetRoleWorkload uses uncached reads: cached capacity counts may describe the
// previous revision even after the RBG has observed a new generation.
func (r *RoleBasedGroupReconciler) targetRoleWorkload(
	ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec, expectedHash string,
) (client.Object, error) {
	var workload client.Object
	switch role.GetWorkloadSpec().Kind {
	case "RoleInstanceSet":
		workload = &workloadsv1alpha2.RoleInstanceSet{}
	case "Deployment":
		workload = &appsv1.Deployment{}
	case "StatefulSet":
		workload = &appsv1.StatefulSet{}
	case "LeaderWorkerSet":
		workload = &lwsv1.LeaderWorkerSet{}
	default:
		return nil, fmt.Errorf("unsupported rollout workload kind %q", role.GetWorkloadSpec().Kind)
	}
	key := client.ObjectKey{Namespace: rbg.Namespace, Name: rbg.GetWorkloadName(role)}
	if err := r.apiReader.Get(ctx, key, workload); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(workload, rbg) || !workload.GetDeletionTimestamp().IsZero() ||
		expectedHash == "" || workload.GetLabels()[fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name)] != expectedHash {
		return nil, nil
	}
	return workload, nil
}

func (r *RoleBasedGroupReconciler) roleWorkloadRolloutComplete(
	ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec, expectedHash string,
) (bool, error) {
	workload, err := r.targetRoleWorkload(ctx, rbg, role, expectedHash)
	if err != nil || workload == nil || role.Replicas == nil {
		return false, err
	}
	desired := *role.Replicas
	switch workload := workload.(type) {
	case *workloadsv1alpha2.RoleInstanceSet:
		return r.roleInstanceSetRolloutComplete(ctx, workload, desired)
	case *appsv1.Deployment:
		status := workload.Status
		return ptr.Deref(workload.Spec.Replicas, -1) == desired &&
			status.ObservedGeneration >= workload.Generation && status.Replicas == desired &&
			status.UpdatedReplicas == desired && status.ReadyReplicas == desired, nil
	case *appsv1.StatefulSet:
		return rolloutStatefulSetComplete(workload, desired), nil
	case *lwsv1.LeaderWorkerSet:
		return r.leaderWorkerSetRolloutComplete(ctx, workload, desired)
	}
	return false, nil
}

func (r *RoleBasedGroupReconciler) roleInstanceSetRolloutComplete(
	ctx context.Context, ris *workloadsv1alpha2.RoleInstanceSet, desired int32,
) (bool, error) {
	status := ris.Status
	if ptr.Deref(ris.Spec.Replicas, -1) != desired || status.ObservedGeneration < ris.Generation ||
		status.Replicas != desired || status.ReadyReplicas != desired ||
		status.UpdatedReplicas != desired || status.UpdatedReadyReplicas != desired {
		return false, nil
	}

	// Aggregate counts can lag an instance update; verify live instances without filtering by revision.
	selector, err := metav1.LabelSelectorAsSelector(ris.Spec.Selector)
	if err != nil {
		return false, err
	}
	instances := &workloadsv1alpha2.RoleInstanceList{}
	if err := r.apiReader.List(ctx, instances, client.InNamespace(ris.Namespace),
		client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return false, err
	}
	var count int32
	for i := range instances.Items {
		instance := &instances.Items[i]
		if !metav1.IsControlledBy(instance, ris) {
			continue
		}
		count++
		if !instance.DeletionTimestamp.IsZero() || status.UpdateRevision == "" ||
			!risutils.EqualToRevisionHash("", instance, status.UpdateRevision) ||
			!inplaceinstance.IsRoleInstanceReady(instance) ||
			inplaceupdate.DefaultCheckInPlaceUpdateCompleted(instance) != nil {
			return false, nil
		}
	}
	return count == desired, nil
}

func rolloutStatefulSetComplete(sts *appsv1.StatefulSet, desired int32) bool {
	return sts.DeletionTimestamp.IsZero() && ptr.Deref(sts.Spec.Replicas, -1) == desired &&
		sts.Status.ObservedGeneration >= sts.Generation && sts.Status.Replicas == desired &&
		sts.Status.UpdatedReplicas == desired && sts.Status.ReadyReplicas == desired
}

// NewRevision only lists ControllerRevisions, but its API requires client.Client.
// Override List to keep even that read off the cache; it never creates a revision.
type rolloutRevisionReader struct {
	client.Client
	reader client.Reader
}

func (r rolloutRevisionReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return r.reader.List(ctx, list, opts...)
}

func (r *RoleBasedGroupReconciler) leaderWorkerSetRolloutComplete(
	ctx context.Context, lws *lwsv1.LeaderWorkerSet, desired int32,
) (bool, error) {
	if ptr.Deref(lws.Spec.Replicas, -1) != desired || lws.Status.Replicas != desired ||
		lws.Status.ReadyReplicas != desired || lws.Status.UpdatedReplicas != desired {
		return false, nil
	}
	// LWS has no observedGeneration (including on its conditions). Derive the
	// target template hash and verify the actual leader/worker workload chain.
	revision, err := lwsrevision.NewRevision(ctx, rolloutRevisionReader{Client: r.client, reader: r.apiReader}, lws, "")
	if err != nil {
		return false, err
	}
	hash := revision.Labels[lwsv1.RevisionKey]
	leader := &appsv1.StatefulSet{}
	if err := r.apiReader.Get(ctx, client.ObjectKeyFromObject(lws), leader); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(leader, lws) || leader.Labels[lwsv1.RevisionKey] != hash ||
		!rolloutStatefulSetComplete(leader, desired) {
		return false, nil
	}
	for ordinal := int32(0); ordinal < desired; ordinal++ {
		complete, err := r.leaderWorkerGroupRolloutComplete(ctx, lws, leader, ordinal, hash)
		if err != nil || !complete {
			return false, err
		}
	}
	return true, nil
}

func (r *RoleBasedGroupReconciler) leaderWorkerGroupRolloutComplete(
	ctx context.Context, lws *lwsv1.LeaderWorkerSet, leader *appsv1.StatefulSet, ordinal int32, hash string,
) (bool, error) {
	key := client.ObjectKey{Namespace: lws.Namespace, Name: fmt.Sprintf("%s-%d", lws.Name, ordinal)}
	pod := &corev1.Pod{}
	if err := r.apiReader.Get(ctx, key, pod); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(pod, leader) || !pod.DeletionTimestamp.IsZero() ||
		pod.Labels[lwsv1.RevisionKey] != hash || !lwspod.PodRunningAndReady(*pod) {
		return false, nil
	}
	workers := ptr.Deref(lws.Spec.LeaderWorkerTemplate.Size, 1) - 1
	if workers == 0 {
		return true, nil
	}
	worker := &appsv1.StatefulSet{}
	if err := r.apiReader.Get(ctx, key, worker); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return metav1.IsControlledBy(worker, pod) && worker.Labels[lwsv1.RevisionKey] == hash &&
		rolloutStatefulSetComplete(worker, workers), nil
}
