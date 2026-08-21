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

// Package volcano implements the GangScheduler interface for
// the Volcano PodGroup (scheduling.volcano.sh).
//
// To enable Volcano gang scheduling, the controller must be started with
// --scheduler-name=volcano (or set schedulerName: volcano in Helm values.yaml).
// This ensures the controller creates Volcano PodGroup CRs and injects the
// required annotations into pod templates.
package volcano

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

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

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

const (
	// CrdName is the CRD name for the Volcano PodGroup.
	CrdName = "podgroups.scheduling.volcano.sh"

	// AnnotationKey is the pod annotation key used to associate a pod with a Volcano PodGroup.
	AnnotationKey = "scheduling.k8s.io/group-name"

	// SchedulerName is the scheduler name set on pod.spec.schedulerName.
	SchedulerName = "volcano"
)

// GangScheduler manages Volcano PodGroups for gang scheduling.
type GangScheduler struct {
	client            client.Client
	hasSubGroupPolicy atomic.Bool
}

// New returns a new GangScheduler for Volcano.
func New(c client.Client) *GangScheduler {
	return &GangScheduler{client: c}
}

// ReconcilePodGroup creates, updates, or deletes the Volcano PodGroup
// based on the gang scheduling configuration.
// gangStrategy is nil for annotation-compat basic gang; non-nil for CoordinatedPolicy gang.
func (m *GangScheduler) ReconcilePodGroup(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	gangStrategy *workloadsv1alpha2.GangSchedulingStrategy,
	runtimeController *builder.TypedBuilder[reconcile.Request],
	watchedWorkload *sync.Map,
	apiReader client.Reader,
) error {
	// Determine if gang scheduling is enabled
	gangEnabled := isGangSchedulingEnabled(rbg, gangStrategy)

	if !gangEnabled {
		return m.deletePodGroup(ctx, rbg, watchedWorkload)
	}

	if _, loaded := watchedWorkload.Load(CrdName); !loaded {
		if err := utils.CheckCrdExists(apiReader, CrdName); err != nil {
			return fmt.Errorf("scheduling plugin %s not ready", CrdName)
		}
		watchedWorkload.LoadOrStore(CrdName, struct{}{})
		runtimeController.Owns(&volcanoschedulingv1beta1.PodGroup{})

		// Check if the PodGroup CRD has subGroupPolicy field
		hasSubGroup := checkPodGroupCRDHasSubGroup(apiReader)
		m.hasSubGroupPolicy.Store(hasSubGroup)
	}

	return m.createOrUpdate(ctx, rbg, gangStrategy)
}

// InjectPodSchedulingFields injects the Volcano PodGroup annotation and schedulerName
// into the pod template spec.
func (m *GangScheduler) InjectPodSchedulingFields(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
	pts *coreapplyv1.PodTemplateSpecApplyConfiguration,
) {
	if !isGangSchedulingEnabled(rbg, nil) {
		return
	}

	// Inject schedulerName into pod spec
	if pts.Spec == nil {
		pts.Spec = &coreapplyv1.PodSpecApplyConfiguration{}
	}
	pts.Spec.WithSchedulerName(SchedulerName)

	// Inject PodGroup annotation
	pts.WithAnnotations(map[string]string{AnnotationKey: rbg.Name})
}

func isGangSchedulingEnabled(rbg *workloadsv1alpha2.RoleBasedGroup, gangStrategy *workloadsv1alpha2.GangSchedulingStrategy) bool {
	// CoordinatedPolicy gang strategy takes priority
	if gangStrategy != nil {
		return true
	}
	// Fall back to annotation compatibility
	return rbg.Annotations[constants.GangSchedulingAnnotationKey] == "true"
}

func (m *GangScheduler) createOrUpdate(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	gangStrategy *workloadsv1alpha2.GangSchedulingStrategy,
) error {
	logger := log.FromContext(ctx)
	queue := rbg.Annotations[constants.GangSchedulingVolcanoQueueKey]
	priorityClassName := rbg.Annotations[constants.GangSchedulingVolcanoPriorityClassKey]
	desiredAnnotations := common.InheritPodGroupAnnotations(rbg.Annotations, volcanoschedulingv1beta1.AnnotationPrefix)

	// Calculate minMember
	minMember := int32(rbg.GetGroupSize())

	// If gangStrategy has minReplicas, check subGroupPolicy support
	if gangStrategy != nil && len(gangStrategy.MinReplicas) > 0 {
		if !m.hasSubGroupPolicy.Load() {
			return fmt.Errorf("gang scheduling with per-role minimums (minReplicas) requires Volcano PodGroup CRD with subGroupPolicy field; the installed Volcano version does not support this feature")
		}
		// Calculate minMember as sum of (minReplicas × subGroupSize) for each role
		minMember = int32(calculateGangMinimum(rbg, gangStrategy))
	}

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
			MinMember:         minMember,
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

	// Update if needed
	desiredMinMember := minMember
	if podGroup.Spec.MinMember != desiredMinMember ||
		podGroup.Spec.Queue != queue ||
		podGroup.Spec.PriorityClassName != priorityClassName {
		updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if fetchErr := m.client.Get(
				ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup,
			); fetchErr != nil {
				return fetchErr
			}
			podGroup.Spec.MinMember = desiredMinMember
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

// calculateGangMinimum computes the gang minimum as Σ(minReplicas × subGroupSize) for each role.
func calculateGangMinimum(rbg *workloadsv1alpha2.RoleBasedGroup, gangStrategy *workloadsv1alpha2.GangSchedulingStrategy) int {
	total := 0
	for _, role := range rbg.Spec.Roles {
		if minReplicas, exists := gangStrategy.MinReplicas[role.Name]; exists {
			subGroupSize := int(workloadsv1alpha2.ComputeSubGroupSize(&role))
			total += subGroupSize * int(minReplicas)
		}
	}
	return total
}

func (m *GangScheduler) deletePodGroup(
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

// checkPodGroupCRDHasSubGroup inspects the PodGroup CRD schema to determine
// whether the subGroupPolicy field is available. This follows the pattern
// from kthena's podgroupmanager.
func checkPodGroupCRDHasSubGroup(reader client.Reader) bool {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := reader.Get(context.Background(), client.ObjectKey{Name: CrdName}, crd); err != nil {
		return false
	}

	for _, version := range crd.Spec.Versions {
		schema := version.Schema
		if schema == nil || schema.OpenAPIV3Schema == nil {
			continue
		}

		specProps, ok := schema.OpenAPIV3Schema.Properties["spec"]
		if !ok {
			continue
		}

		if _, ok := specProps.Properties["subGroupPolicy"]; ok {
			return true
		}
	}
	return false
}
