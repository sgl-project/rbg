package scheduler

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	schedv1alpha1 "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
)

type PodGroupScheduler struct {
	client client.Client
}

func NewPodGroupScheduler(client client.Client) *PodGroupScheduler {
	return &PodGroupScheduler{client: client}
}

func (r *PodGroupScheduler) Reconcile(ctx context.Context, rbg *workloadsv1alpha.RoleBasedGroup) error {
	if rbg.EnableGangScheduling() {
		return r.createOrUpdatePodGroup(ctx, rbg)
	} else {
		return r.deletePodGroup(ctx, rbg)
	}

}

func (r *PodGroupScheduler) createOrUpdatePodGroup(ctx context.Context, rbg *workloadsv1alpha.RoleBasedGroup) error {
	logger := log.FromContext(ctx)
	podGroup := &schedv1alpha1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.Name,
			Namespace: rbg.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(rbg, rbg.GroupVersionKind()),
			},
		},
		Spec: schedv1alpha1.PodGroupSpec{
			MinMember:              int32(rbg.GetGroupSize()),
			ScheduleTimeoutSeconds: rbg.Spec.PodGroupPolicy.KubeScheduling.ScheduleTimeoutSeconds,
		},
	}

	err := r.client.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup)
	if err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "get pod group error")
		return err
	}

	if apierrors.IsNotFound(err) {
		err = r.client.Create(ctx, podGroup)
		if err != nil {
			logger.Error(err, "create pod group error")
		}
		return err
	}

	if podGroup.Spec.MinMember != int32(rbg.GetGroupSize()) {
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := r.client.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup); err != nil {
				return err
			}
			podGroup.Spec.MinMember = int32(rbg.GetGroupSize())
			updateErr := r.client.Update(ctx, podGroup)
			return updateErr
		})
		if err != nil {
			logger.Error(err, "update pod group error")
		}
		return err
	}

	return nil
}

func (r *PodGroupScheduler) deletePodGroup(ctx context.Context, rbg *workloadsv1alpha.RoleBasedGroup) error {
	podGroup := &schedv1alpha1.PodGroup{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	return r.client.Delete(ctx, podGroup)
}
