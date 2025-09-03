package scheduler

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	schedv1alpha1 "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
	volcanoschedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

const (
	KubePodGroupLabelKey         = "pod-group.scheduling.sigs.k8s.io/name"
	VolcanoPodGroupAnnotationKey = "scheduling.k8s.io/group-name"

	// KubePodGroupCrdName is PodGroup CRD Name
	KubePodGroupCrdName = "podgroups.scheduling.x-k8s.io"

	VolcanoPodGroupCrdName = "podgroups.scheduling.volcano.sh"
)

type PodGroupScheduler struct {
	client client.Client
}

func NewPodGroupScheduler(client client.Client) *PodGroupScheduler {
	return &PodGroupScheduler{client: client}
}

func (r *PodGroupScheduler) Reconcile(ctx context.Context, rbg *workloadsv1alpha.RoleBasedGroup) error {
	// not support change podGroup scheduler
	if rbg.IsKubeGangScheduling() {
		return r.createOrUpdateKubePodGroup(ctx, rbg)
	} else if rbg.IsVolcanoGangScheduling() {
		return r.createOrUpdateVolcanoPodGroup(ctx, rbg)
	} else {
		return r.deletePodGroup(ctx, rbg)
	}
}

func InjectPodGroupProtocol(rbg *workloadsv1alpha.RoleBasedGroup, pts *coreapplyv1.PodTemplateSpecApplyConfiguration) {
	if rbg.IsKubeGangScheduling() {
		pts.WithLabels(map[string]string{KubePodGroupLabelKey: rbg.Name})
	} else if rbg.IsVolcanoGangScheduling() {
		pts.WithAnnotations(map[string]string{VolcanoPodGroupAnnotationKey: rbg.Name})
	}
}

func GetPodGroupCrdName(rbg *workloadsv1alpha.RoleBasedGroup) string {
	if rbg.IsKubeGangScheduling() {
		return KubePodGroupCrdName
	} else if rbg.IsVolcanoGangScheduling() {
		return VolcanoPodGroupCrdName
	}
	return ""
}

func (r *PodGroupScheduler) createOrUpdateVolcanoPodGroup(ctx context.Context, rbg *workloadsv1alpha.RoleBasedGroup) error {
	logger := log.FromContext(ctx)
	podGroup := &volcanoschedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.Name,
			Namespace: rbg.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(rbg, rbg.GroupVersionKind()),
			},
		},
		Spec: volcanoschedulingv1beta1.PodGroupSpec{
			MinMember:         int32(rbg.GetGroupSize()),
			Queue:             rbg.Spec.PodGroupPolicy.VolcanoScheduling.Queue,
			PriorityClassName: rbg.Spec.PodGroupPolicy.VolcanoScheduling.PriorityClassName,
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

	if podGroup.Spec.MinMember != int32(rbg.GetGroupSize()) || podGroup.Spec.Queue != rbg.Spec.PodGroupPolicy.VolcanoScheduling.Queue ||
		podGroup.Spec.PriorityClassName != rbg.Spec.PodGroupPolicy.VolcanoScheduling.PriorityClassName {
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := r.client.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup); err != nil {
				return err
			}
			podGroup.Spec.MinMember = int32(rbg.GetGroupSize())
			podGroup.Spec.Queue = rbg.Spec.PodGroupPolicy.VolcanoScheduling.Queue
			podGroup.Spec.PriorityClassName = rbg.Spec.PodGroupPolicy.VolcanoScheduling.PriorityClassName
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

func (r *PodGroupScheduler) createOrUpdateKubePodGroup(ctx context.Context, rbg *workloadsv1alpha.RoleBasedGroup) error {
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
	kubePodGroup := &schedv1alpha1.PodGroup{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, kubePodGroup); err == nil {
		if deleteErr := r.client.Delete(ctx, kubePodGroup); deleteErr != nil {
			return deleteErr
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	volcanoPodGroup := &volcanoschedulingv1beta1.PodGroup{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, volcanoPodGroup); err == nil {
		if deleteErr := r.client.Delete(ctx, volcanoPodGroup); deleteErr != nil {
			return deleteErr
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}
