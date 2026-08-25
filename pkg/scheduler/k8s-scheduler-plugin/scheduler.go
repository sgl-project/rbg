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

// Package kubeschedulerplugin implements the GangScheduler interface for
// the Kubernetes scheduler-plugins PodGroup (scheduling.x-k8s.io).
//
// This is the default gang scheduling implementation. The controller uses
// scheduler-plugins by default (--scheduler-name=scheduler-plugins), but can
// be configured to use Volcano instead via the --scheduler-name flag or
// schedulerName Helm value.
package kubeschedulerplugin

import (
	"context"
	"fmt"
	"strconv"
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
	schedv1alpha1 "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
)

const (
	// CrdName is the CRD name for the kube scheduler-plugins PodGroup.
	CrdName = "podgroups.scheduling.x-k8s.io"

	// inheritSchedulingPolicyAnnotations is the PodGroup annotation prefix inherited from the workload.
	inheritSchedulingPolicyAnnotations = "scheduling.x-k8s.io/"

	// LabelKey is the pod label key used to associate a pod with a PodGroup.
	LabelKey = "pod-group.scheduling.sigs.k8s.io/name"

	defaultScheduleTimeoutSeconds = int32(60)
)

// GangScheduler manages kube scheduler-plugins PodGroups for gang scheduling.
type GangScheduler struct {
	client client.Client
}

// New returns a new GangScheduler for the kube scheduler plugin.
func New(c client.Client) *GangScheduler {
	return &GangScheduler{client: c}
}

// ReconcilePodGroup creates, updates, or deletes the kube PodGroup
// based on the gang scheduling configuration.
// gangStrategy is nil for annotation-compat basic gang; non-nil for CoordinatedPolicy gang.
// Note: scheduler-plugins does not support subGroupPolicy; if gangStrategy.MinReplicas
// is non-empty, an error is returned (runtime safety net).
func (m *GangScheduler) ReconcilePodGroup(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	gangStrategy *workloadsv1alpha2.GangSchedulingStrategy,
	runtimeController *builder.TypedBuilder[reconcile.Request],
	watchedWorkload *sync.Map,
	apiReader client.Reader,
) error {
	// Runtime safety net: scheduler-plugins does not support per-role minimums
	if gangStrategy != nil && len(gangStrategy.MinReplicas) > 0 {
		return fmt.Errorf("scheduler-plugins does not support per-role minimum gang scheduling (minReplicas); use --scheduler-name=volcano with Volcano >= 1.14")
	}

	gangEnabled := gangStrategy != nil

	if !gangEnabled {
		return m.deletePodGroup(ctx, rbg, watchedWorkload)
	}

	if _, loaded := watchedWorkload.Load(CrdName); !loaded {
		if err := utils.CheckCrdExists(apiReader, CrdName); err != nil {
			return fmt.Errorf("scheduling plugin %s not ready", CrdName)
		}
		watchedWorkload.LoadOrStore(CrdName, struct{}{})
		runtimeController.Owns(&schedv1alpha1.PodGroup{})
	}

	return m.createOrUpdate(ctx, rbg)
}

// InjectPodSchedulingFields injects the kube PodGroup label into the pod template spec.
//
// pod.spec.schedulerName is deliberately left untouched. scheduler-plugins runs as a
// separate scheduler binary whose profile name is chosen by whoever deploys it (the
// upstream chart defaults to "scheduler-plugins-scheduler" but it is configurable), so
// the controller cannot know it. Coscheduling associates a pod with its PodGroup through
// LabelKey, which is the actual contract; selecting the scheduler stays a deployment
// decision (make it the default scheduler, or set schedulerName in the role template).
func (m *GangScheduler) InjectPodSchedulingFields(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
	gangStrategy *workloadsv1alpha2.GangSchedulingStrategy,
	pts *coreapplyv1.PodTemplateSpecApplyConfiguration,
) {
	if gangStrategy == nil {
		return
	}

	// Inject PodGroup label
	pts.WithLabels(map[string]string{LabelKey: rbg.Name})
}

func getScheduleTimeoutSeconds(rbg *workloadsv1alpha2.RoleBasedGroup) *int32 {
	if rbg.Annotations != nil {
		if v, ok := rbg.Annotations[constants.GangSchedulingScheduleTimeoutSecondsKey]; ok {
			if parsed, err := strconv.ParseInt(v, 10, 32); err == nil {
				t := int32(parsed)
				return &t
			}
		}
	}
	t := defaultScheduleTimeoutSeconds
	return &t
}

func (m *GangScheduler) createOrUpdate(ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup) error {
	logger := log.FromContext(ctx)
	gvk := utils.GetRbgGVK()
	desiredAnnotations := common.InheritPodGroupAnnotations(rbg.Annotations, inheritSchedulingPolicyAnnotations)
	podGroup := &schedv1alpha1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.Name,
			Namespace: rbg.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(rbg, gvk),
			},
			Annotations: desiredAnnotations,
		},
		Spec: schedv1alpha1.PodGroupSpec{
			MinMember:              int32(rbg.GetGroupSize()),
			ScheduleTimeoutSeconds: getScheduleTimeoutSeconds(rbg),
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

	desiredMinMember := int32(rbg.GetGroupSize())
	desiredTimeout := getScheduleTimeoutSeconds(rbg)
	if podGroup.Spec.MinMember != desiredMinMember ||
		(podGroup.Spec.ScheduleTimeoutSeconds == nil || *podGroup.Spec.ScheduleTimeoutSeconds != *desiredTimeout) {
		updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if fetchErr := m.client.Get(
				ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup,
			); fetchErr != nil {
				return fetchErr
			}
			if !utils.CheckOwnerReference(podGroup.OwnerReferences, gvk) {
				podGroup.OwnerReferences = append(podGroup.OwnerReferences, *metav1.NewControllerRef(rbg, gvk))
			}
			podGroup.Spec.MinMember = desiredMinMember
			podGroup.Spec.ScheduleTimeoutSeconds = desiredTimeout
			return m.client.Update(ctx, podGroup)
		})
		if updateErr != nil {
			logger.Error(updateErr, "update pod group error")
			return updateErr
		}
	}

	return nil
}

func (m *GangScheduler) deletePodGroup(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	watchedWorkload *sync.Map,
) error {
	if _, loaded := watchedWorkload.Load(CrdName); !loaded {
		return nil
	}

	podGroup := &schedv1alpha1.PodGroup{}
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
