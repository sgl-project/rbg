/*
Copyright 2024 The RoleBasedGroup Authors.

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

// Package volcano implements the PodGroupManager interface for
// the Volcano PodGroup (scheduling.volcano.sh).
package volcano

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
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scheduler/common"
	"sigs.k8s.io/rbgs/pkg/utils"
	volcanoschedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

const (
	// CrdName is the CRD name for the Volcano PodGroup.
	CrdName = "podgroups.scheduling.volcano.sh"

	// AnnotationKey is the pod annotation key used to associate a pod with a Volcano PodGroup.
	AnnotationKey = "scheduling.k8s.io/group-name"
)

// PodGroupManager manages Volcano PodGroups for gang scheduling.
type PodGroupManager struct {
	client client.Client
}

// New returns a new PodGroupManager for Volcano.
func New(c client.Client) *PodGroupManager {
	return &PodGroupManager{client: c}
}

// ReconcilePodGroup creates, updates, or deletes the Volcano PodGroup
// based on the gang-scheduling annotation on the RBG.
func (m *PodGroupManager) ReconcilePodGroup(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	runtimeController *builder.TypedBuilder[reconcile.Request],
	watchedWorkload *sync.Map,
	apiReader client.Reader,
) error {
	if !isGangSchedulingEnabled(rbg) {
		return m.deletePodGroup(ctx, rbg, watchedWorkload)
	}

	if _, loaded := watchedWorkload.Load(CrdName); !loaded {
		if err := utils.CheckCrdExists(apiReader, CrdName); err != nil {
			return fmt.Errorf("scheduling plugin %s not ready", CrdName)
		}
		watchedWorkload.LoadOrStore(CrdName, struct{}{})
		runtimeController.Owns(&volcanoschedulingv1beta1.PodGroup{})
	}

	return m.createOrUpdate(ctx, rbg)
}

// InjectPodGroupLabels injects the Volcano PodGroup annotation into the pod template spec.
func (m *PodGroupManager) InjectPodGroupLabels(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	pts *coreapplyv1.PodTemplateSpecApplyConfiguration,
) {
	if isGangSchedulingEnabled(rbg) {
		pts.WithAnnotations(map[string]string{AnnotationKey: rbg.Name})
	}
}

func isGangSchedulingEnabled(rbg *workloadsv1alpha2.RoleBasedGroup) bool {
	return rbg.Annotations[constants.GangSchedulingAnnotationKey] == "true"
}

func (m *PodGroupManager) createOrUpdate(ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup) error {
	logger := log.FromContext(ctx)
	queue := rbg.Annotations[constants.GangSchedulingVolcanoQueueKey]
	priorityClassName := rbg.Annotations[constants.GangSchedulingVolcanoPriorityClassKey]
	desiredAnnotations := common.InheritPodGroupAnnotations(rbg.Annotations, volcanoschedulingv1beta1.AnnotationPrefix)

	podGroup := &volcanoschedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.Name,
			Namespace: rbg.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(rbg, utils.GetRbgGVK()),
			},
			Annotations: desiredAnnotations,
		},
		Spec: volcanoschedulingv1beta1.PodGroupSpec{
			MinMember:         int32(rbg.GetGroupSize()),
			Queue:             queue,
			PriorityClassName: priorityClassName,
		},
	}

	err := m.client.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup)
	if err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "get pod group error")
		return err
	}

	if apierrors.IsNotFound(err) {
		if createErr := m.client.Create(ctx, podGroup); createErr != nil {
			logger.Error(createErr, "create pod group error")
			return createErr
		}
		return nil
	}

	if podGroup.Spec.MinMember != int32(rbg.GetGroupSize()) ||
		podGroup.Spec.Queue != queue ||
		podGroup.Spec.PriorityClassName != priorityClassName {
		updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if fetchErr := m.client.Get(
				ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup,
			); fetchErr != nil {
				return fetchErr
			}
			podGroup.Spec.MinMember = int32(rbg.GetGroupSize())
			podGroup.Spec.Queue = queue
			podGroup.Spec.PriorityClassName = priorityClassName
			return m.client.Update(ctx, podGroup)
		})
		if updateErr != nil {
			logger.Error(updateErr, "update pod group error")
			return updateErr
		}
	}

	return nil
}

func (m *PodGroupManager) deletePodGroup(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	watchedWorkload *sync.Map,
) error {
	if _, loaded := watchedWorkload.Load(CrdName); !loaded {
		return nil
	}

	podGroup := &volcanoschedulingv1beta1.PodGroup{}
	err := m.client.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if metav1.IsControlledBy(podGroup, rbg) {
		if deleteErr := m.client.Delete(ctx, podGroup); deleteErr != nil {
			return deleteErr
		}
	}

	return nil
}
