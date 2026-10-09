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

package volcano

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scheduler/common"
	"sigs.k8s.io/rbgs/pkg/utils"
	volcanoschedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

const (
	// HyperNodeCrdName is the CRD name for Volcano HyperNodes.
	HyperNodeCrdName = "hypernodes.topology.volcano.sh"
)

var _ common.PlacementScheduler = (*GangScheduler)(nil)

// ReconcilePlacement compiles the scheduler-independent PlacementPlan into Volcano
// PodGroups. When the plan contains no topology it delegates to the KEP-430
// gang-only path, preserving the existing PodGroup rendering exactly.
func (m *GangScheduler) ReconcilePlacement(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	plan *common.PlacementPlan,
	runtimeController *builder.TypedBuilder[reconcile.Request],
	watchedWorkload *sync.Map,
	apiReader client.Reader,
) (*common.PlacementRenderResult, error) {
	if plan == nil {
		if _, loaded := watchedWorkload.Load(CrdName); !loaded {
			// A nil plan means no gang and no topology objects should remain. If the
			// Volcano CRD is not installed, there is nothing this backend can render or
			// clean up, so preserve the legacy no-op behavior.
			if err := utils.CheckCrdExists(apiReader, CrdName); err != nil {
				return nil, nil
			}
			watchedWorkload.LoadOrStore(CrdName, struct{}{})
			runtimeController.Owns(&volcanoschedulingv1beta1.PodGroup{})
		}
		if err := m.deleteOrphanPlacementPodGroups(ctx, rbg, nil); err != nil {
			return nil, err
		}
		return nil, nil
	}

	if _, loaded := watchedWorkload.Load(CrdName); !loaded {
		if err := utils.CheckCrdExists(apiReader, CrdName); err != nil {
			return nil, fmt.Errorf("scheduling plugin %s not ready", CrdName)
		}
		watchedWorkload.LoadOrStore(CrdName, struct{}{})
		runtimeController.Owns(&volcanoschedulingv1beta1.PodGroup{})
	}

	if !plan.HasTopology() {
		root := plan.Root
		if len(plan.TopLevelGroups()) == 1 {
			root = plan.TopLevelGroups()[0]
		}
		if err := m.createOrUpdate(ctx, rbg, root.Gang, apiReader); err != nil {
			return nil, err
		}
		// Topology may have been removed while gang scheduling remains. The gang-only
		// compiler owns exactly the legacy rbg.Name PodGroup; every other owned
		// PodGroup from the previous topology plan is now stale and must be removed.
		if err := m.deleteOrphanPlacementPodGroups(ctx, rbg, []string{rbg.Name}); err != nil {
			return nil, err
		}
		return &common.PlacementRenderResult{}, nil
	}

	levels, err := m.loadTopologyLevels(ctx, apiReader)
	if err != nil {
		return nil, err
	}
	if err := validatePlacementLevels(plan.Root, levels); err != nil {
		return nil, err
	}

	result := &common.PlacementRenderResult{}
	groups := plan.TopLevelGroups()
	names := make([]string, 0, len(groups))
	desiredGroups := make([]*volcanoschedulingv1beta1.PodGroup, 0, len(groups))
	for i, group := range groups {
		name := placementPodGroupName(rbg, group, i)
		names = append(names, name)
		desired, preferredAbsorbed, err := m.buildPlacementPodGroup(ctx, rbg, group, name, apiReader)
		if err != nil {
			return nil, err
		}
		result.PreferredAbsorbed = result.PreferredAbsorbed || preferredAbsorbed
		desiredGroups = append(desiredGroups, desired)
	}

	// Compile every placement group before touching the API. If a later group fails
	// capability or validation, earlier groups must not have been created or updated.
	for _, desired := range desiredGroups {
		if err := m.applyPodGroup(ctx, rbg, desired); err != nil {
			return nil, err
		}
	}
	if err := m.deleteOrphanPlacementPodGroups(ctx, rbg, names); err != nil {
		return nil, err
	}
	return result, nil
}

// InjectPlacementSchedulingFields injects Volcano as schedulerName for every role and
// the unique PodGroup annotation for roles covered by the plan. Membership is derived
// from the logical plan, not from a gang-only translator.
func (m *GangScheduler) InjectPlacementSchedulingFields(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
	plan *common.PlacementPlan,
	pts *coreapplyv1.PodTemplateSpecApplyConfiguration,
) {
	if plan == nil {
		return
	}
	if pts.Spec == nil {
		pts.Spec = &coreapplyv1.PodSpecApplyConfiguration{}
	}
	pts.Spec.WithSchedulerName(SchedulerName)

	group, index := findTopLevelGroupForRole(plan, role.Name)
	if group == nil {
		return
	}
	pts.WithAnnotations(map[string]string{AnnotationKey: placementPodGroupName(rbg, group, index)})
}

func placementPodGroupName(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	group *common.PlacementGroup,
	index int,
) string {
	if group.Gang != nil && !groupContainsTopology(group) && len(group.Scope.Roles) > 0 && group.Scope.PartitionBy == common.PartitionByNone {
		return rbg.Name
	}
	placementName := placementGroupName(group)
	if placementName == "" {
		placementName = fmt.Sprintf("g-%d", index)
	}
	if group.ID != "" {
		return boundedPlacementPodGroupName(rbg.Name, placementName, string(rbg.UID))
	}
	return fmt.Sprintf("%s-placement-%d", rbg.Name, index)
}

func placementGroupName(group *common.PlacementGroup) string {
	var names []string
	var visit func(group *common.PlacementGroup)
	visit = func(group *common.PlacementGroup) {
		if group == nil {
			return
		}
		if group.Name != "" {
			names = append(names, group.Name)
		}
		for _, child := range group.Children {
			visit(child)
		}
	}
	visit(group)
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// boundedPlacementPodGroupName keeps generated names within the DNS-label limit while
// retaining readable source identity and distinguishing RBGs whose names share a long
// prefix. The placement budget includes the two-character "r-"/"p-" prefix, so a
// source name is truncated to twenty readable characters. The UID hash is six bytes
// (twelve hexadecimal characters), which separates same-named scopes from different
// RBG lifecycles.
func boundedPlacementPodGroupName(rbgName, placementName, rbgUID string) string {
	const (
		maxPrefixLength    = 26
		maxPlacementLength = 22
	)
	prefix := rbgName
	if len(prefix) > maxPrefixLength {
		prefix = prefix[:maxPrefixLength]
	}
	if len(placementName) > maxPlacementLength {
		placementName = placementName[:maxPlacementLength]
	}
	uidHash := sha256.Sum256([]byte(rbgUID))
	return prefix + "-" + placementName + "-" + hex.EncodeToString(uidHash[:6])
}

func groupContainsTopology(group *common.PlacementGroup) bool {
	if group == nil {
		return false
	}
	if group.Topology != nil {
		return true
	}
	for _, child := range group.Children {
		if groupContainsTopology(child) {
			return true
		}
	}
	return false
}

func findTopLevelGroupForRole(plan *common.PlacementPlan, roleName string) (*common.PlacementGroup, int) {
	for i, group := range plan.TopLevelGroups() {
		if containsString(group.Scope.Roles, roleName) {
			return group, i
		}
	}
	return nil, -1
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func (m *GangScheduler) loadTopologyLevels(ctx context.Context, reader client.Reader) (map[string]int, error) {
	// Report an older Volcano installation explicitly instead of failing on an
	// unstructured HyperNode list. PodGroup support alone is not sufficient here.
	if err := utils.CheckCrdExists(reader, HyperNodeCrdName); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, common.NewSchedulerUnsupportedError(
				"Volcano HyperNode CRD %s is not installed; topology-aware scheduling requires Volcano HyperNodes", HyperNodeCrdName)
		}
		return nil, fmt.Errorf("check Volcano HyperNode CRD %s: %w", HyperNodeCrdName, err)
	}

	hyperNodes := &unstructured.UnstructuredList{}
	hyperNodes.SetAPIVersion("topology.volcano.sh/v1alpha1")
	hyperNodes.SetKind("HyperNodeList")
	if err := reader.List(ctx, hyperNodes); err != nil {
		return nil, fmt.Errorf("list Volcano HyperNodes: %w", err)
	}
	levels := make(map[string]int, len(hyperNodes.Items))
	for i := range hyperNodes.Items {
		item := &hyperNodes.Items[i]
		name, _, _ := unstructured.NestedString(item.Object, "spec", "tierName")
		if name == "" {
			continue
		}
		tier, _, _ := unstructured.NestedInt64(item.Object, "spec", "tier")
		if existing, exists := levels[name]; exists && int64(existing) != tier {
			return nil, common.NewTopologyTranslationError(
				"Volcano HyperNode tierName %q maps to different tiers %d and %d", name, existing, tier)
		}
		levels[name] = int(tier)
	}
	return levels, nil
}

func validatePlacementLevels(group *common.PlacementGroup, levels map[string]int) error {
	if group == nil {
		return nil
	}
	if group.Topology != nil {
		if err := validateTopologyLevelOrder(group.Topology, levels); err != nil {
			return err
		}
	}
	for _, child := range group.Children {
		if err := validatePlacementLevels(child, levels); err != nil {
			return err
		}
		if group.Topology != nil && child.Topology != nil {
			if err := validateParentChildTopology(group.Topology, child.Topology, levels); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateTopologyLevelOrder(
	constraint *workloadsv1alpha2.TopologyConstraint,
	levels map[string]int,
) error {
	if constraint == nil || constraint.Pack == nil {
		return nil
	}
	if _, ok := topologyLevelTier(constraint.Pack.Required, levels); constraint.Pack.Required != nil && !ok {
		return common.NewTopologyTranslationError(
			"Volcano HyperNodes contain no tierName %q", *constraint.Pack.Required)
	}
	if _, ok := topologyLevelTier(constraint.Pack.Preferred, levels); constraint.Pack.Preferred != nil && !ok {
		return common.NewTopologyTranslationError(
			"Volcano HyperNodes contain no tierName %q", *constraint.Pack.Preferred)
	}
	// Intentionally no required/preferred ordering check: operators may use
	// site-specific topology names whose relative order is scheduler-owned.
	return nil
}

func validateParentChildTopology(parent, child *workloadsv1alpha2.TopologyConstraint, levels map[string]int) error {
	if parent == nil || child == nil || parent.Pack == nil || child.Pack == nil {
		return nil
	}
	if parent.Pack.Required != nil && child.Pack.Required != nil {
		parentTier, parentOK := topologyLevelTier(parent.Pack.Required, levels)
		if !parentOK {
			return common.NewTopologyTranslationError(
				"Volcano HyperNodes contain no tierName %q", *parent.Pack.Required)
		}
		childTier, childOK := topologyLevelTier(child.Pack.Required, levels)
		if !childOK {
			return common.NewTopologyTranslationError(
				"Volcano HyperNodes contain no tierName %q", *child.Pack.Required)
		}
		if childTier > parentTier {
			return common.NewTopologyTranslationError(
				"child topology required level %q (tier %d) is broader than parent level %q (tier %d)",
				*child.Pack.Required, childTier, *parent.Pack.Required, parentTier)
		}
	}
	if parent.Pack.Preferred != nil && child.Pack.Preferred != nil {
		parentTier, parentOK := topologyLevelTier(parent.Pack.Preferred, levels)
		if !parentOK {
			return common.NewTopologyTranslationError(
				"Volcano HyperNodes contain no tierName %q", *parent.Pack.Preferred)
		}
		childTier, childOK := topologyLevelTier(child.Pack.Preferred, levels)
		if !childOK {
			return common.NewTopologyTranslationError(
				"Volcano HyperNodes contain no tierName %q", *child.Pack.Preferred)
		}
		if childTier > parentTier {
			return common.NewTopologyTranslationError(
				"child topology preferred level %q (tier %d) is broader than parent preferred level %q (tier %d)",
				*child.Pack.Preferred, childTier, *parent.Pack.Preferred, parentTier)
		}
	}
	return nil
}

func topologyLevelTier(value *string, levels map[string]int) (int, bool) {
	if value == nil || *value == "" {
		return 0, false
	}
	tier, exists := levels[*value]
	return tier, exists
}

func (m *GangScheduler) supportsNetworkTopology(ctx context.Context, reader client.Reader) (bool, error) {
	m.networkTopologyMu.Lock()
	defer m.networkTopologyMu.Unlock()
	if !m.networkTopologyProbedAt.IsZero() && time.Since(m.networkTopologyProbedAt) < subGroupProbeTTL {
		return m.networkTopologySupported, nil
	}
	supported, err := checkPodGroupCRDHasNetworkTopology(ctx, reader)
	if err != nil {
		return false, err
	}
	m.networkTopologySupported = supported
	m.networkTopologyProbedAt = time.Now()
	return supported, nil
}

func (m *GangScheduler) supportsTopologySubGroup(ctx context.Context, reader client.Reader) (bool, error) {
	m.topologySubGroupMu.Lock()
	defer m.topologySubGroupMu.Unlock()
	if !m.topologySubGroupProbedAt.IsZero() && time.Since(m.topologySubGroupProbedAt) < subGroupProbeTTL {
		return m.topologySubGroupSupported, nil
	}
	supported, err := checkPodGroupCRDHasTopologySubGroup(ctx, reader)
	if err != nil {
		return false, err
	}
	m.topologySubGroupSupported = supported
	m.topologySubGroupProbedAt = time.Now()
	return supported, nil
}

func checkPodGroupCRDHasNetworkTopology(ctx context.Context, reader client.Reader) (bool, error) {
	return podGroupCRDHasProperty(ctx, reader, "networkTopology")
}

func checkPodGroupCRDHasTopologySubGroup(ctx context.Context, reader client.Reader) (bool, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := reader.Get(ctx, client.ObjectKey{Name: CrdName}, crd); err != nil {
		return false, fmt.Errorf("get CRD %s: %w", CrdName, err)
	}
	for _, version := range crd.Spec.Versions {
		if version.Name != volcanoschedulingv1beta1.SchemeGroupVersion.Version || !version.Served {
			continue
		}
		specProps, ok := versionSchemaProperties(version.Schema)
		if !ok {
			continue
		}
		subGroupProps, ok := specProps.Properties["subGroupPolicy"]
		if !ok || subGroupProps.Items == nil || subGroupProps.Items.Schema == nil {
			return false, nil
		}
		if _, ok := subGroupProps.Items.Schema.Properties["networkTopology"]; !ok {
			return false, nil
		}
		return true, nil
	}
	return false, nil
}

func podGroupCRDHasProperty(ctx context.Context, reader client.Reader, property string) (bool, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := reader.Get(ctx, client.ObjectKey{Name: CrdName}, crd); err != nil {
		return false, fmt.Errorf("get CRD %s: %w", CrdName, err)
	}
	for _, version := range crd.Spec.Versions {
		if version.Name != volcanoschedulingv1beta1.SchemeGroupVersion.Version || !version.Served {
			continue
		}
		specProps, ok := versionSchemaProperties(version.Schema)
		if !ok {
			continue
		}
		if _, ok := specProps.Properties[property]; ok {
			return true, nil
		}
	}
	return false, nil
}

func versionSchemaProperties(schema *apiextensionsv1.CustomResourceValidation) (apiextensionsv1.JSONSchemaProps, bool) {
	if schema == nil || schema.OpenAPIV3Schema == nil {
		return apiextensionsv1.JSONSchemaProps{}, false
	}
	specProps, ok := schema.OpenAPIV3Schema.Properties["spec"]
	if !ok {
		return apiextensionsv1.JSONSchemaProps{}, false
	}
	return specProps, true
}

func (m *GangScheduler) buildPlacementPodGroup(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	group *common.PlacementGroup,
	name string,
	apiReader client.Reader,
) (*volcanoschedulingv1beta1.PodGroup, bool, error) {
	minMember, gangSubGroups, err := placementGangSpec(rbg, group)
	if err != nil {
		return nil, false, err
	}
	if len(gangSubGroups) > 0 {
		supported, supportErr := m.supportsSubGroupPolicy(ctx, apiReader)
		if supportErr != nil {
			return nil, false, fmt.Errorf("check Volcano PodGroup CRD for subGroupPolicy support: %w", supportErr)
		}
		if !supported {
			return nil, false, common.NewIncompatibleGangConfigError(
				"gang scheduling with per-role minimums requires Volcano PodGroup CRD with subGroupPolicy field")
		}
	}

	subGroups, preferredAbsorbed, err := buildTopologySubGroups(rbg, group)
	if err != nil {
		return nil, false, err
	}
	networkTopology, rootAbsorbed := rootNetworkTopologyForGroup(group)
	if networkTopology != nil {
		supported, supportErr := m.supportsNetworkTopology(ctx, apiReader)
		if supportErr != nil {
			return nil, false, fmt.Errorf("check Volcano PodGroup CRD for networkTopology support: %w", supportErr)
		}
		if !supported {
			return nil, false, common.NewSchedulerUnsupportedError(
				"Volcano PodGroup CRD does not support networkTopology")
		}
	}
	if len(subGroups) > 0 {
		supported, supportErr := m.supportsTopologySubGroup(ctx, apiReader)
		if supportErr != nil {
			return nil, false, fmt.Errorf("check Volcano PodGroup CRD for topology subGroupPolicy support: %w", supportErr)
		}
		if !supported {
			return nil, false, common.NewSchedulerUnsupportedError(
				"Volcano PodGroup CRD does not support topology subGroupPolicy")
		}
	}
	// Gang subgroups and topology subgroups both use role identity. Merge them so a
	// role does not appear twice in the same PodGroup.
	subGroups = mergeSubGroupPolicies(gangSubGroups, subGroups)

	preferredAbsorbed = preferredAbsorbed || rootAbsorbed

	queue := rbg.Annotations[constants.GangSchedulingVolcanoQueueKey]
	priorityClassName := rbg.Annotations[constants.GangSchedulingVolcanoPriorityClassKey]
	annotations := common.InheritPodGroupAnnotations(rbg.Annotations, volcanoschedulingv1beta1.AnnotationPrefix)

	return &volcanoschedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: rbg.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(rbg, utils.GetRbgGVK()),
			},
			Annotations: annotations,
		},
		Spec: volcanoschedulingv1beta1.PodGroupSpec{
			MinMember:         minMember,
			Queue:             queue,
			PriorityClassName: priorityClassName,
			SubGroupPolicy:    subGroups,
			NetworkTopology:   networkTopology,
		},
	}, preferredAbsorbed, nil
}

func placementGangSpec(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	group *common.PlacementGroup,
) (int32, []volcanoschedulingv1beta1.SubGroupPolicySpec, error) {
	if group.Gang == nil {
		return 0, nil, nil
	}
	if len(group.Gang.MinReplicas) > 0 {
		return buildGangSpec(rbg, group.Gang)
	}
	size, err := common.GangSize(rbg, group.Gang)
	return size, nil, err
}

func buildTopologySubGroups(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	group *common.PlacementGroup,
) ([]volcanoschedulingv1beta1.SubGroupPolicySpec, bool, error) {
	var policies []volcanoschedulingv1beta1.SubGroupPolicySpec
	absorbed := false

	for _, child := range group.Children {
		if child.Scope.PartitionBy != common.PartitionByRoleInstance || len(child.Scope.Roles) != 1 {
			return nil, false, common.NewSchedulerUnsupportedError(
				"Volcano cannot render topology child group roles %v with partition %q; use one role per per-instance subgroup",
				child.Scope.Roles, child.Scope.PartitionBy)
		}
		roleName := child.Scope.Roles[0]
		role := findRole(rbg, roleName)
		if role == nil {
			return nil, false, common.NewTopologyTranslationError("topology constraint references unknown role %q", roleName)
		}
		if !emitsRoleInstanceLabel(role) {
			return nil, false, common.NewTopologyTranslationError(
				"instance topology constraint for role %q requires a workload that emits %s",
				roleName, constants.RoleInstanceNameLabelKey)
		}
		subGroupSize := workloadsv1alpha2.ComputeSubGroupSize(role)
		if subGroupSize <= 0 {
			return nil, false, common.NewTopologyTranslationError(
				"instance topology constraint for role %q produces no pods; subgroup size must be at least 1",
				roleName)
		}
		networkTopology, childAbsorbed := networkTopologyForGroup(child)
		absorbed = absorbed || childAbsorbed
		policies = append(policies, volcanoschedulingv1beta1.SubGroupPolicySpec{
			Name: roleName,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.GroupNameLabelKey: rbg.Name,
					constants.RoleNameLabelKey:  roleName,
				},
			},
			MatchLabelKeys:  []string{constants.RoleInstanceNameLabelKey},
			SubGroupSize:    ptr.To(subGroupSize),
			NetworkTopology: networkTopology,
		})
	}

	// A standalone per-instance top-level group carries its topology on the subgroup,
	// not on the whole PodGroup.
	if group.Scope.PartitionBy == common.PartitionByRoleInstance && len(group.Scope.Roles) == 1 {
		roleName := group.Scope.Roles[0]
		role := findRole(rbg, roleName)
		if role == nil {
			return nil, false, common.NewTopologyTranslationError("topology constraint references unknown role %q", roleName)
		}
		if !emitsRoleInstanceLabel(role) {
			return nil, false, common.NewTopologyTranslationError(
				"instance topology constraint for role %q requires a workload that emits %s",
				roleName, constants.RoleInstanceNameLabelKey)
		}
		subGroupSize := workloadsv1alpha2.ComputeSubGroupSize(role)
		if subGroupSize <= 0 {
			return nil, false, common.NewTopologyTranslationError(
				"instance topology constraint for role %q produces no pods; subgroup size must be at least 1",
				roleName)
		}
		networkTopology, childAbsorbed := networkTopologyForGroup(group)
		absorbed = absorbed || childAbsorbed
		policies = append(policies, volcanoschedulingv1beta1.SubGroupPolicySpec{
			Name: roleName,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.GroupNameLabelKey: rbg.Name,
					constants.RoleNameLabelKey:  roleName,
				},
			},
			MatchLabelKeys:  []string{constants.RoleInstanceNameLabelKey},
			SubGroupSize:    ptr.To(subGroupSize),
			NetworkTopology: networkTopology,
		})
	}
	return policies, absorbed, nil
}

func rootNetworkTopologyForGroup(group *common.PlacementGroup) (
	*volcanoschedulingv1beta1.NetworkTopologySpec,
	bool,
) {
	// A per-instance placement scope is a collection of independent subgroups. Its
	// topology belongs on each subgroup, not on the whole PodGroup; rendering it at
	// both levels would accidentally require every RoleInstance to share one domain.
	if group != nil && group.Scope.PartitionBy == common.PartitionByRoleInstance {
		return nil, false
	}
	return networkTopologyForGroup(group)
}

func networkTopologyForGroup(group *common.PlacementGroup) (*volcanoschedulingv1beta1.NetworkTopologySpec, bool) {
	if group == nil || group.Topology == nil || group.Topology.Pack == nil {
		return nil, false
	}
	pack := group.Topology.Pack
	if pack.Required == nil {
		// Volcano's soft mode has no tier threshold, so a requested preferred level
		// cannot be anchored. The controller reports this as PreferredAbsorbed.
		return &volcanoschedulingv1beta1.NetworkTopologySpec{
			Mode: volcanoschedulingv1beta1.SoftNetworkTopologyMode,
		}, pack.Preferred != nil
	}
	return &volcanoschedulingv1beta1.NetworkTopologySpec{
		Mode:            volcanoschedulingv1beta1.HardNetworkTopologyMode,
		HighestTierName: *pack.Required,
	}, pack.Preferred != nil
}

func mergeSubGroupPolicies(
	gangPolicies []volcanoschedulingv1beta1.SubGroupPolicySpec,
	topologyPolicies []volcanoschedulingv1beta1.SubGroupPolicySpec,
) []volcanoschedulingv1beta1.SubGroupPolicySpec {
	if len(gangPolicies) == 0 {
		return topologyPolicies
	}
	if len(topologyPolicies) == 0 {
		return gangPolicies
	}
	merged := make([]volcanoschedulingv1beta1.SubGroupPolicySpec, 0, len(gangPolicies)+len(topologyPolicies))
	index := make(map[string]int, len(gangPolicies)+len(topologyPolicies))
	for _, policy := range append(gangPolicies, topologyPolicies...) {
		if existing, exists := index[policy.Name]; exists {
			current := &merged[existing]
			if policy.SubGroupSize != nil {
				current.SubGroupSize = policy.SubGroupSize
			}
			if policy.MinSubGroups != nil {
				current.MinSubGroups = policy.MinSubGroups
			}
			if policy.NetworkTopology != nil {
				current.NetworkTopology = policy.NetworkTopology
			}
			continue
		}
		index[policy.Name] = len(merged)
		merged = append(merged, policy)
	}
	return merged
}

func findRole(rbg *workloadsv1alpha2.RoleBasedGroup, roleName string) *workloadsv1alpha2.RoleSpec {
	for i := range rbg.Spec.Roles {
		if rbg.Spec.Roles[i].Name == roleName {
			return &rbg.Spec.Roles[i]
		}
	}
	return nil
}

func podGroupOwnerRefsEqual(left, right *volcanoschedulingv1beta1.PodGroup) bool {
	return apiequality.Semantic.DeepEqual(left.OwnerReferences, right.OwnerReferences)
}

func (m *GangScheduler) applyPodGroup(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	desired *volcanoschedulingv1beta1.PodGroup,
) error {
	existing := &volcanoschedulingv1beta1.PodGroup{}
	err := m.client.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && podGroupSpecEqual(existing, desired) {
		return nil
	}

	desired.APIVersion = volcanoschedulingv1beta1.SchemeGroupVersion.String()
	desired.Kind = "PodGroup"
	return utils.PatchObjectApplyConfiguration(ctx, m.client, desired, utils.PatchSpec)
}

func podGroupSpecEqual(left, right *volcanoschedulingv1beta1.PodGroup) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Spec.Queue == right.Spec.Queue &&
		left.Spec.PriorityClassName == right.Spec.PriorityClassName &&
		left.Spec.MinMember == right.Spec.MinMember &&
		apiequality.Semantic.DeepEqual(left.Annotations, right.Annotations) &&
		podGroupOwnerRefsEqual(left, right) &&
		apiequality.Semantic.DeepEqual(left.Spec.SubGroupPolicy, right.Spec.SubGroupPolicy) &&
		apiequality.Semantic.DeepEqual(left.Spec.NetworkTopology, right.Spec.NetworkTopology)
}

func (m *GangScheduler) deleteOrphanPlacementPodGroups(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	keep []string,
) error {
	list := &volcanoschedulingv1beta1.PodGroupList{}
	if err := m.client.List(ctx, list, &client.ListOptions{Namespace: rbg.Namespace}); err != nil {
		return err
	}
	keepSet := make(map[string]struct{}, len(keep))
	for _, name := range keep {
		keepSet[name] = struct{}{}
	}
	for i := range list.Items {
		item := &list.Items[i]
		if !metav1.IsControlledBy(item, rbg) {
			continue
		}
		if _, exists := keepSet[item.Name]; exists {
			continue
		}
		if err := m.client.Delete(ctx, item); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
