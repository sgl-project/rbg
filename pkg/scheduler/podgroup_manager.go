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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	workloadsv1alpha "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/reconciler"
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

func (r *PodGroupScheduler) Reconcile(ctx context.Context, data *reconciler.RBGReconcileData, runtimeController *builder.TypedBuilder[reconcile.Request], watchedWorkload *sync.Map, apiReader client.Reader) error {
	metadata := data.Metadata()

	// not support change podGroup scheduler
	if data.IsKubeGangScheduling() {
		// check and load kube podGroup CRD
		_, podGroupExist := watchedWorkload.Load(KubePodGroupCrdName)
		if !podGroupExist {
			if err := utils.CheckCrdExists(apiReader, KubePodGroupCrdName); err != nil {
				return fmt.Errorf("scheduling plugin %s not ready", KubePodGroupCrdName)
			}
			watchedWorkload.LoadOrStore(KubePodGroupCrdName, struct{}{})
			runtimeController.Owns(&schedv1alpha1.PodGroup{})
		}

		return r.createOrUpdateKubePodGroup(ctx, metadata, data.PodGroupPolicy(), data.GetGroupSize())
	} else if data.IsVolcanoGangScheduling() {
		// check and load volcano podGroup CRD
		_, podGroupExist := watchedWorkload.Load(VolcanoPodGroupCrdName)
		if !podGroupExist {
			if err := utils.CheckCrdExists(apiReader, VolcanoPodGroupCrdName); err != nil {
				return fmt.Errorf("scheduling plugin %s not ready", VolcanoPodGroupCrdName)
			}
			watchedWorkload.LoadOrStore(VolcanoPodGroupCrdName, struct{}{})
			runtimeController.Owns(&volcanoschedulingv1beta1.PodGroup{})
		}

		return r.createOrUpdateVolcanoPodGroup(ctx, metadata, data.PodGroupPolicy(), data.GetGroupSize())
	} else {
		return r.deletePodGroup(ctx, metadata, watchedWorkload)
	}
}

func InjectPodGroupProtocol(data *reconciler.RBGReconcileData, pts *coreapplyv1.PodTemplateSpecApplyConfiguration) {
	metadata := data.Metadata()
	if data.IsKubeGangScheduling() {
		pts.WithLabels(map[string]string{KubePodGroupLabelKey: metadata.Name})
	} else if data.IsVolcanoGangScheduling() {
		pts.WithAnnotations(map[string]string{VolcanoPodGroupAnnotationKey: metadata.Name})
	}
}

func (r *PodGroupScheduler) createOrUpdateVolcanoPodGroup(ctx context.Context, metadata reconciler.Metadata, policy *workloadsv1alpha.PodGroupPolicy, groupSize int) error {
	logger := log.FromContext(ctx)
	gvk := utils.GetRbgGVK()

	// Build owner reference using metadata
	ownerRef := metav1.OwnerReference{
		APIVersion:         gvk.GroupVersion().String(),
		Kind:               gvk.Kind,
		Name:               metadata.Name,
		UID:                metadata.UID,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}

	podGroup := &volcanoschedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:            metadata.Name,
			Namespace:       metadata.Namespace,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Spec: volcanoschedulingv1beta1.PodGroupSpec{
			MinMember:         int32(groupSize),
			Queue:             policy.VolcanoScheduling.Queue,
			PriorityClassName: policy.VolcanoScheduling.PriorityClassName,
		},
	}

	err := r.client.Get(ctx, types.NamespacedName{Name: metadata.Name, Namespace: metadata.Namespace}, podGroup)
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

	if podGroup.Spec.MinMember != int32(groupSize) || podGroup.Spec.Queue != policy.VolcanoScheduling.Queue ||
		podGroup.Spec.PriorityClassName != policy.VolcanoScheduling.PriorityClassName {
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := r.client.Get(ctx, types.NamespacedName{Name: metadata.Name, Namespace: metadata.Namespace}, podGroup); err != nil {
				return err
			}
			podGroup.Spec.MinMember = int32(groupSize)
			podGroup.Spec.Queue = policy.VolcanoScheduling.Queue
			podGroup.Spec.PriorityClassName = policy.VolcanoScheduling.PriorityClassName
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

func (r *PodGroupScheduler) createOrUpdateKubePodGroup(ctx context.Context, metadata reconciler.Metadata, policy *workloadsv1alpha.PodGroupPolicy, groupSize int) error {
	logger := log.FromContext(ctx)
	gvk := utils.GetRbgGVK()

	// Build owner reference using metadata
	ownerRef := metav1.OwnerReference{
		APIVersion:         gvk.GroupVersion().String(),
		Kind:               gvk.Kind,
		Name:               metadata.Name,
		UID:                metadata.UID,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}

	podGroup := &schedv1alpha1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:            metadata.Name,
			Namespace:       metadata.Namespace,
			OwnerReferences: []metav1.OwnerReference{ownerRef},
		},
		Spec: schedv1alpha1.PodGroupSpec{
			MinMember:              int32(groupSize),
			ScheduleTimeoutSeconds: policy.KubeScheduling.ScheduleTimeoutSeconds,
		},
	}

	err := r.client.Get(ctx, types.NamespacedName{Name: metadata.Name, Namespace: metadata.Namespace}, podGroup)
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

	if podGroup.Spec.MinMember != int32(groupSize) {
		err = retry.RetryOnConflict(
			retry.DefaultRetry, func() error {
				if err := r.client.Get(
					ctx, types.NamespacedName{Name: metadata.Name, Namespace: metadata.Namespace}, podGroup,
				); err != nil {
					return err
				}
				if !utils.CheckOwnerReference(podGroup.OwnerReferences, utils.GetRbgGVK()) {
					podGroup.OwnerReferences = append(podGroup.OwnerReferences, ownerRef)
				}
				podGroup.Spec.MinMember = int32(groupSize)
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

func (r *PodGroupScheduler) deletePodGroup(ctx context.Context, metadata reconciler.Metadata, watchedWorkload *sync.Map) error {
	if _, podGroupExist := watchedWorkload.Load(KubePodGroupCrdName); podGroupExist {
		kubePodGroup := &schedv1alpha1.PodGroup{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: metadata.Name, Namespace: metadata.Namespace}, kubePodGroup); err == nil {
			// Check if this PodGroup is controlled by the RBG (match UID)
			for _, ref := range kubePodGroup.OwnerReferences {
				if ref.UID == metadata.UID {
					if deleteErr := r.client.Delete(ctx, kubePodGroup); deleteErr != nil {
						return deleteErr
					}
					break
				}
			}
		} else if !apierrors.IsNotFound(err) {
			return err
		}
	}

	if _, podGroupExist := watchedWorkload.Load(VolcanoPodGroupCrdName); podGroupExist {
		volcanoPodGroup := &volcanoschedulingv1beta1.PodGroup{}
		if err := r.client.Get(ctx, types.NamespacedName{Name: metadata.Name, Namespace: metadata.Namespace}, volcanoPodGroup); err == nil {
			// Check if this PodGroup is controlled by the RBG (match UID)
			for _, ref := range volcanoPodGroup.OwnerReferences {
				if ref.UID == metadata.UID {
					if deleteErr := r.client.Delete(ctx, volcanoPodGroup); deleteErr != nil {
						return deleteErr
					}
					break
				}
			}
		} else if !apierrors.IsNotFound(err) {
			return err
		}
	}

	return nil
}
