package statelessmode

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statelessmode/core"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statelessmode/sync"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statelessmode/utils"
	utils2 "sigs.k8s.io/rbgs/pkg/utils"
)

// StatusUpdater is interface for updating InstanceSet status.
type StatusUpdater interface {
	UpdateInstanceSetStatus(set *workloadsv1alpha2.RoleInstanceSet, newStatus *workloadsv1alpha2.RoleInstanceSetStatus, instances []*workloadsv1alpha2.RoleInstance) error
}

func newStatusUpdater(c client.Client) StatusUpdater {
	return &realStatusUpdater{Client: c}
}

type realStatusUpdater struct {
	client.Client
}

func (r *realStatusUpdater) UpdateInstanceSetStatus(set *workloadsv1alpha2.RoleInstanceSet, newStatus *workloadsv1alpha2.RoleInstanceSetStatus, instances []*workloadsv1alpha2.RoleInstance) error {
	r.calculateStatus(set, newStatus, instances)
	if r.inconsistentStatus(set, newStatus) {
		klog.Infof("To update InstanceSet status for  %s/%s, replicas=%d ready=%d available=%d updated=%d updatedReady=%d, revisions current=%s update=%s",
			set.Namespace, set.Name, newStatus.Replicas, newStatus.ReadyReplicas, newStatus.AvailableReplicas, newStatus.UpdatedReplicas, newStatus.UpdatedReadyReplicas, newStatus.CurrentRevision, newStatus.UpdateRevision)
		if err := r.updateStatus(set, newStatus); err != nil {
			return err
		}
	}

	return core.New(set).ExtraStatusCalculation(newStatus, instances)
}

func (r *realStatusUpdater) updateStatus(set *workloadsv1alpha2.RoleInstanceSet, newStatus *workloadsv1alpha2.RoleInstanceSetStatus) error {
	// Skip status update if the object is being deleted to avoid StorageError
	// caused by precondition failures when the object's UID becomes empty during foreground deletion.
	if set.DeletionTimestamp != nil {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		clone := &workloadsv1alpha2.RoleInstanceSet{}
		if err := r.Get(context.TODO(), types.NamespacedName{Namespace: set.Namespace, Name: set.Name}, clone); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		// Re-check deletion timestamp after refreshing from API server
		if clone.DeletionTimestamp != nil {
			return nil
		}
		clone.Status = *newStatus
		return r.Status().Update(context.TODO(), clone)
	})
}

func (r *realStatusUpdater) inconsistentStatus(set *workloadsv1alpha2.RoleInstanceSet, newStatus *workloadsv1alpha2.RoleInstanceSetStatus) bool {
	oldStatus := set.Status
	return newStatus.ObservedGeneration > oldStatus.ObservedGeneration ||
		newStatus.Replicas != oldStatus.Replicas ||
		newStatus.ReadyReplicas != oldStatus.ReadyReplicas ||
		newStatus.AvailableReplicas != oldStatus.AvailableReplicas ||
		newStatus.UpdatedReadyReplicas != oldStatus.UpdatedReadyReplicas ||
		newStatus.UpdatedReplicas != oldStatus.UpdatedReplicas ||
		newStatus.ExpectedUpdatedReplicas != oldStatus.ExpectedUpdatedReplicas ||
		newStatus.UpdateRevision != oldStatus.UpdateRevision ||
		newStatus.CurrentRevision != oldStatus.CurrentRevision ||
		newStatus.LabelSelector != oldStatus.LabelSelector
}

func (r *realStatusUpdater) calculateStatus(set *workloadsv1alpha2.RoleInstanceSet, newStatus *workloadsv1alpha2.RoleInstanceSetStatus, instances []*workloadsv1alpha2.RoleInstance) {
	coreControl := core.New(set)
	for _, instance := range instances {
		newStatus.Replicas++
		if coreControl.IsInstanceUpdateReady(instance, 0) {
			newStatus.ReadyReplicas++
		}
		if sync.IsInstanceAvailable(coreControl, instance, set.Spec.MinReadySeconds) {
			newStatus.AvailableReplicas++
		}
		if utils.EqualToRevisionHash("", instance, newStatus.UpdateRevision) {
			newStatus.UpdatedReplicas++
		}
		if utils.EqualToRevisionHash("", instance, newStatus.UpdateRevision) && coreControl.IsInstanceUpdateReady(instance, 0) {
			newStatus.UpdatedReadyReplicas++
		}
	}
	// Consider the update revision as stable if revisions of all instances are consistent to it, no need to wait all of them ready
	if newStatus.UpdatedReplicas == newStatus.Replicas {
		newStatus.CurrentRevision = newStatus.UpdateRevision
	}

	if newStatus.UpdateRevision == newStatus.CurrentRevision {
		newStatus.ExpectedUpdatedReplicas = *set.Spec.Replicas
	} else {
		if partition, err := utils2.CalculatePartitionReplicas(set.Spec.UpdateStrategy.Partition, set.Spec.Replicas); err == nil {
			newStatus.ExpectedUpdatedReplicas = *set.Spec.Replicas - int32(partition)
		}
	}
}
