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
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
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

	// subGroupProbeTTL bounds how long the PodGroup CRD capability probe is reused.
	// Volcano is upgraded rarely, so a few minutes of staleness is acceptable in
	// exchange for keeping an uncached CRD read off every reconcile.
	subGroupProbeTTL = 5 * time.Minute
)

// GangScheduler manages Volcano PodGroups for gang scheduling.
type GangScheduler struct {
	client client.Client

	subGroupMu        sync.Mutex
	subGroupSupported bool
	subGroupProbedAt  time.Time
}

// New returns a new GangScheduler for Volcano.
func New(c client.Client) *GangScheduler {
	return &GangScheduler{client: c}
}

// ReconcilePodGroup creates, updates, or deletes the Volcano PodGroup
// based on the gang scheduling configuration.
// gangStrategy is nil when gang scheduling is disabled, in which case any existing
// PodGroup is deleted. A non-nil strategy with an empty MinReplicas map is an
// all-or-nothing gang over the roles it covers; a non-empty map builds a
// subGroupPolicy holding each named role to its minimum, with covered roles
// absent from the map participating in full.
func (m *GangScheduler) ReconcilePodGroup(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	gangStrategy *common.GangStrategy,
	runtimeController *builder.TypedBuilder[reconcile.Request],
	watchedWorkload *sync.Map,
	apiReader client.Reader,
) error {
	// Determine if gang scheduling is enabled
	gangEnabled := gangStrategy != nil

	if !gangEnabled {
		return m.deletePodGroup(ctx, rbg, watchedWorkload)
	}

	if _, loaded := watchedWorkload.Load(CrdName); !loaded {
		if err := utils.CheckCrdExists(apiReader, CrdName); err != nil {
			return fmt.Errorf("scheduling plugin %s not ready", CrdName)
		}
		watchedWorkload.LoadOrStore(CrdName, struct{}{})
		runtimeController.Owns(&volcanoschedulingv1beta1.PodGroup{})
	}

	return m.createOrUpdate(ctx, rbg, gangStrategy, apiReader)
}

// InjectPodSchedulingFields injects the Volcano PodGroup annotation and schedulerName
// into the pod template spec.
//
// schedulerName is injected for every role: Volcano must own the scheduling decision
// for the whole group, otherwise excluded roles would be placed by a different
// scheduler and could starve the gang of resources.
//
// The PodGroup annotation is what actually enrolls a pod in the gang, so it is only
// injected for roles that participate. A CoordinatedPolicy rule scopes its gang with
// spec.policies[].roles; roles outside that scope are excluded by definition, and
// annotating them would make Volcano count their pods against a minMember that never
// budgeted for them.
func (m *GangScheduler) InjectPodSchedulingFields(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
	gangStrategy *common.GangStrategy,
	pts *coreapplyv1.PodTemplateSpecApplyConfiguration,
) {
	if gangStrategy == nil {
		return
	}

	// Inject schedulerName into pod spec
	if pts.Spec == nil {
		pts.Spec = &coreapplyv1.PodSpecApplyConfiguration{}
	}
	pts.Spec.WithSchedulerName(SchedulerName)

	if !common.RoleInGang(role, gangStrategy) {
		return
	}

	// Inject PodGroup annotation
	pts.WithAnnotations(map[string]string{AnnotationKey: rbg.Name})
}

func (m *GangScheduler) createOrUpdate(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	gangStrategy *common.GangStrategy,
	apiReader client.Reader,
) error {
	logger := log.FromContext(ctx)
	queue := rbg.Annotations[constants.GangSchedulingVolcanoQueueKey]
	priorityClassName := rbg.Annotations[constants.GangSchedulingVolcanoPriorityClassKey]
	desiredAnnotations := common.InheritPodGroupAnnotations(rbg.Annotations, volcanoschedulingv1beta1.AnnotationPrefix)

	// Calculate minMember over the roles the gang covers
	var (
		minMember      int32
		subGroupPolicy []volcanoschedulingv1beta1.SubGroupPolicySpec
		err            error
	)

	// If gangStrategy has minReplicas, check subGroupPolicy support
	if len(gangStrategy.MinReplicas) > 0 {
		supported, supportErr := m.supportsSubGroupPolicy(ctx, apiReader)
		if supportErr != nil {
			return fmt.Errorf("check Volcano PodGroup CRD for subGroupPolicy support: %w", supportErr)
		}
		if !supported {
			return common.NewIncompatibleGangConfigError("gang scheduling with per-role minimums (minReplicas) requires Volcano PodGroup CRD with subGroupPolicy field; the installed Volcano version does not support this feature")
		}
		minMember, subGroupPolicy, err = buildGangSpec(rbg, gangStrategy)
		if err != nil {
			return err
		}
	} else {
		minMember, err = common.GangSize(rbg, gangStrategy)
		if err != nil {
			return err
		}
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
			SubGroupPolicy:    subGroupPolicy,
		},
	}

	err = m.client.Get(ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup)
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
		podGroup.Spec.PriorityClassName != priorityClassName ||
		!apiequality.Semantic.DeepEqual(podGroup.Spec.SubGroupPolicy, subGroupPolicy) {
		updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if fetchErr := m.client.Get(
				ctx, types.NamespacedName{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup,
			); fetchErr != nil {
				return fetchErr
			}
			podGroup.Spec.MinMember = desiredMinMember
			podGroup.Spec.Queue = queue
			podGroup.Spec.PriorityClassName = priorityClassName
			podGroup.Spec.SubGroupPolicy = subGroupPolicy
			return m.client.Update(ctx, podGroup)
		})
		if updateErr != nil {
			logger.Error(updateErr, "update pod group error")
			return updateErr
		}
	}

	return nil
}

// buildGangSpec computes the PodGroup minMember and one Volcano SubGroupPolicy entry
// per role the gang covers.
//
// A Volcano subGroup maps to one RBG RoleInstance, which is the atomic scheduling
// unit: subGroupSize is the number of pods a single instance produces, and
// minSubGroups is the minimum number of instances that must be schedulable before
// the gang is dispatched. minMember is therefore Σ(minSubGroups × subGroupSize).
//
// A role with a configured minimum is held to that minimum. A covered role without
// one is an all-or-nothing role and participates in full, so it is held to its full
// replica count. Its entry is what keeps that guarantee meaningful: Volcano counts
// every scheduled pod of the PodGroup toward minMember, so without the entry, pods of
// a minimum role beyond its minimum could stand in for the all-or-nothing role's
// missing replicas and dispatch the gang early.
//
// MatchLabelKeys is what partitions the role's pods into per-instance subGroups.
// Without it every pod of the role collapses into a single subGroup, which
// contradicts subGroupSize and leaves the pods permanently unschedulable.
//
// The cross-CR rules are enforced only here. Admission validates just the
// self-contained parts of a CoordinatedPolicy, because a policy may legitimately
// name a role that does not exist yet or one that is temporarily scaled below its
// minimum. Left unchecked, such a minReplicas would silently turn the gang guarantee
// off (minMember 0) or make it permanently unsatisfiable, so every violation here is
// reported as an IncompatibleGangConfigError.
func buildGangSpec(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	gangStrategy *common.GangStrategy,
) (int32, []volcanoschedulingv1beta1.SubGroupPolicySpec, error) {
	if unknown := common.UnknownGangRoles(rbg, gangStrategy); len(unknown) > 0 {
		return 0, nil, common.NewIncompatibleGangConfigError(
			"gang scheduling references roles that do not exist in the RoleBasedGroup: %v; "+
				"fix the role names in CoordinatedPolicy %s/%s",
			unknown, rbg.Namespace, rbg.Name)
	}

	policies := make([]volcanoschedulingv1beta1.SubGroupPolicySpec, 0, len(gangStrategy.Roles))
	var minMember int32

	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		if !common.RoleInGang(role, gangStrategy) {
			continue
		}

		replicas := ptr.Deref(role.Replicas, 1)
		minReplicas, hasMinimum := gangStrategy.MinReplicas[role.Name]
		if hasMinimum {
			if minReplicas < 1 {
				return 0, nil, common.NewIncompatibleGangConfigError(
					"gang scheduling minReplicas for role %q must be at least 1, got %d", role.Name, minReplicas)
			}
			if minReplicas > replicas {
				return 0, nil, common.NewIncompatibleGangConfigError(
					"gang scheduling minReplicas for role %q is %d but the role only has %d replicas, so the gang can never be satisfied; "+
						"lower the minimum in CoordinatedPolicy %s/%s, or raise the role's replicas at whichever "+
						"owns them (spec.replicas, or the autoscaler behind its RoleBasedGroupScalingAdapter)",
					role.Name, minReplicas, replicas, rbg.Namespace, rbg.Name)
			}
		} else {
			// All-or-nothing role: the gang needs every replica.
			minReplicas = replicas
		}

		// A covered role scaled to zero contributes nothing to the gang.
		if minReplicas == 0 {
			continue
		}

		if !emitsRoleInstanceLabel(role) {
			return 0, nil, common.NewIncompatibleGangConfigError(
				"gang scheduling with subGroupPolicy is not supported for role %q backed by workload type %q: "+
					"the %s pod label is needed to partition the role into per-instance subGroups",
				role.Name, role.GetWorkloadType(), constants.RoleInstanceNameLabelKey)
		}

		subGroupSize := workloadsv1alpha2.ComputeSubGroupSize(role)
		minMember += subGroupSize * minReplicas
		policies = append(policies, volcanoschedulingv1beta1.SubGroupPolicySpec{
			Name:         role.Name,
			SubGroupSize: ptr.To(subGroupSize),
			MinSubGroups: ptr.To(minReplicas),
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.GroupNameLabelKey: rbg.Name,
					constants.RoleNameLabelKey:  role.Name,
				},
			},
			MatchLabelKeys: []string{constants.RoleInstanceNameLabelKey},
		})
	}

	if minMember == 0 {
		return 0, nil, common.NewIncompatibleGangConfigError(
			"gang scheduling resolved to minMember 0, which would provide no gang guarantee; "+
				"the roles it covers in CoordinatedPolicy %s/%s are all scaled to zero",
			rbg.Namespace, rbg.Name)
	}

	return minMember, policies, nil
}

// emitsRoleInstanceLabel reports whether the role's workload backing writes the
// per-instance pod label that MatchLabelKeys relies on. Only RoleInstanceSet does;
// the deprecated Deployment/StatefulSet/LeaderWorkerSet backings do not, and
// declaring subGroups over pods that cannot be partitioned leaves them Pending.
func emitsRoleInstanceLabel(role *workloadsv1alpha2.RoleSpec) bool {
	return role.GetWorkloadType() == constants.RoleInstanceSetWorkloadType
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

// supportsSubGroupPolicy reports whether the installed Volcano PodGroup CRD carries
// the subGroupPolicy field, reusing the answer for subGroupProbeTTL.
//
// The probe reads the CRD through the uncached reader, so running it on every
// reconcile would put a direct apiserver call on the hot path. Caching it forever is
// wrong in the other direction: the answer flips when Volcano is upgraded or
// downgraded, and a stale positive would keep emitting a subGroupPolicy that the
// installed scheduler drops on the floor.
//
// A failed probe is not cached, so a transient read error is retried on the next
// reconcile instead of being remembered as an unsupported Volcano version.
func (m *GangScheduler) supportsSubGroupPolicy(ctx context.Context, reader client.Reader) (bool, error) {
	m.subGroupMu.Lock()
	defer m.subGroupMu.Unlock()

	if !m.subGroupProbedAt.IsZero() && time.Since(m.subGroupProbedAt) < subGroupProbeTTL {
		return m.subGroupSupported, nil
	}

	supported, err := checkPodGroupCRDHasSubGroup(ctx, reader)
	if err != nil {
		return false, err
	}
	m.subGroupSupported = supported
	m.subGroupProbedAt = time.Now()
	return supported, nil
}

// checkPodGroupCRDHasSubGroup inspects the served v1beta1 PodGroup schema to
// determine whether the subGroupPolicy field is available. A read failure is
// returned separately from a schema that simply lacks the field, so the caller does
// not report a transient error as an unsupported Volcano version.
func checkPodGroupCRDHasSubGroup(ctx context.Context, reader client.Reader) (bool, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := reader.Get(ctx, client.ObjectKey{Name: CrdName}, crd); err != nil {
		return false, fmt.Errorf("get CRD %s: %w", CrdName, err)
	}

	for _, version := range crd.Spec.Versions {
		// Only the version this code writes decides support: another version's schema
		// says nothing about whether a v1beta1 PodGroup will keep the field.
		if version.Name != volcanoschedulingv1beta1.SchemeGroupVersion.Version || !version.Served {
			continue
		}

		schema := version.Schema
		if schema == nil || schema.OpenAPIV3Schema == nil {
			continue
		}

		specProps, ok := schema.OpenAPIV3Schema.Properties["spec"]
		if !ok {
			continue
		}

		if _, ok := specProps.Properties["subGroupPolicy"]; ok {
			return true, nil
		}
	}
	return false, nil
}
