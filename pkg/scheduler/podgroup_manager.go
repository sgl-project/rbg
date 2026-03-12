package scheduler

import (
	"context"
	"fmt"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	workloadsv1alpha "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
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

func (r *PodGroupScheduler) Reconcile(ctx context.Context, rbg *workloadsv1alpha.RoleBasedGroup, runtimeController *builder.TypedBuilder[reconcile.Request], watchedWorkload *sync.Map, apiReader client.Reader) error {
	// not support change podGroup scheduler
	if rbg.IsKubeGangScheduling() {
		// check and load kube podGroup CRD
		_, podGroupExist := watchedWorkload.Load(KubePodGroupCrdName)
		if !podGroupExist {
			if err := utils.CheckCrdExists(apiReader, KubePodGroupCrdName); err != nil {
				return fmt.Errorf("scheduling plugin %s not ready", KubePodGroupCrdName)
			}
			watchedWorkload.LoadOrStore(KubePodGroupCrdName, struct{}{})
			runtimeController.Owns(&schedv1alpha1.PodGroup{})
		}

		return r.createOrUpdateKubePodGroup(ctx, rbg)
	} else if rbg.IsVolcanoGangScheduling() {
		// check and load volcano podGroup CRD
		_, podGroupExist := watchedWorkload.Load(VolcanoPodGroupCrdName)
		if !podGroupExist {
			if err := utils.CheckCrdExists(apiReader, VolcanoPodGroupCrdName); err != nil {
				return fmt.Errorf("scheduling plugin %s not ready", VolcanoPodGroupCrdName)
			}
			watchedWorkload.LoadOrStore(VolcanoPodGroupCrdName, struct{}{})
			runtimeController.Owns(&volcanoschedulingv1beta1.PodGroup{})
		}

		return r.createOrUpdateVolcanoPodGroup(ctx, rbg)
	} else {
		return r.deletePodGroup(ctx, rbg, watchedWorkload)
	}
}

func InjectPodGroupProtocol(rbg *workloadsv1alpha.RoleBasedGroup, pts *coreapplyv1.PodTemplateSpecApplyConfiguration) {
	if rbg.IsKubeGangScheduling() {
		pts.WithLabels(map[string]string{KubePodGroupLabelKey: rbg.Name})
	} else if rbg.IsVolcanoGangScheduling() {
		pts.WithAnnotations(map[string]string{VolcanoPodGroupAnnotationKey: rbg.Name})
	}
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
	gvk := utils.GetRbgGVK()
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
		err = retry.RetryOnConflict(
			retry.DefaultRetry, func() error {
				if err := r.client.Get(
					ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup,
				); err != nil {
					return err
				}
				if !utils.CheckOwnerReference(podGroup.OwnerReferences, utils.GetRbgGVK()) {
					podGroup.OwnerReferences = append(podGroup.OwnerReferences, *metav1.NewControllerRef(rbg, gvk))
				}
				podGroup.Spec.MinMember = int32(rbg.GetGroupSize())
				updateErr := r.client.Update(ctx, podGroup)
				return updateErr
			},
		)
		if err != nil {
			logger.Error(err, "update pod group error")
		}
		return err
	}

	return nil
}

func (r *PodGroupScheduler) deletePodGroup(ctx context.Context, rbg *workloadsv1alpha.RoleBasedGroup, watchedWorkload *sync.Map) error {
	if _, podGroupExist := watchedWorkload.Load(KubePodGroupCrdName); podGroupExist {
		kubePodGroup := &schedv1alpha1.PodGroup{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, kubePodGroup); err == nil {
			if metav1.IsControlledBy(kubePodGroup, rbg) {
				if deleteErr := r.client.Delete(ctx, kubePodGroup); deleteErr != nil {
					return deleteErr
				}
			}
		} else if !apierrors.IsNotFound(err) {
			return err
		}
	}

	if _, podGroupExist := watchedWorkload.Load(VolcanoPodGroupCrdName); podGroupExist {
		volcanoPodGroup := &volcanoschedulingv1beta1.PodGroup{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, volcanoPodGroup); err == nil {
			if metav1.IsControlledBy(volcanoPodGroup, rbg) {
				if deleteErr := r.client.Delete(ctx, volcanoPodGroup); deleteErr != nil {
					return deleteErr
				}
			}
		} else if !apierrors.IsNotFound(err) {
			return err
		}
	}

	return nil
}
