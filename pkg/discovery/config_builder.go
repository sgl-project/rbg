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

package discovery

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/ordinals"
	"sigs.k8s.io/rbgs/pkg/utils"

	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

type ConfigBuilder struct {
	client client.Client
	rbg    *workloadsv1alpha2.RoleBasedGroup
	role   *workloadsv1alpha2.RoleSpec
}

func NewConfigBuilder(
	client client.Client,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
) *ConfigBuilder {
	return &ConfigBuilder{
		client: client,
		rbg:    rbg,
		role:   role,
	}
}

type ClusterConfig struct {
	Group GroupInfo `json:"group"`
	Roles RolesInfo `json:"roles"`
}

type GroupInfo struct {
	Name  string   `json:"name"`
	Size  int      `json:"size"`
	Roles []string `json:"roles"`
}

type RolesInfo map[string]RoleInstances

type RoleInstances struct {
	Size      int        `json:"size"`
	Instances []Instance `json:"instances"`
}

type Instance struct {
	Address string           `json:"address"`
	Ports   map[string]int32 `json:"ports,omitempty"` // Key: port name, Value: port number
}

// CompactConfig is the KEP-576 v1 discovery ConfigMap content. It replaces
// per-instance enumeration with membership, component, and address templates.
type CompactConfig struct {
	APIVersion string        `json:"apiVersion"`
	Group      string        `json:"group"`
	Roles      []CompactRole `json:"roles"`
}

type CompactRole struct {
	Name             string             `json:"name"`
	Members          int                `json:"members"`
	ExcludedOrdinals string             `json:"excludedOrdinals,omitempty"`
	Ports            map[string]int32   `json:"ports,omitempty"`
	Components       []CompactComponent `json:"components"`
}

type CompactComponent struct {
	Name   string `json:"name"`
	Size   int    `json:"size"`
	Offset int    `json:"offset,omitempty"`
	Base   string `json:"base"`
}

func (b *ConfigBuilder) Build(ctx context.Context) ([]byte, error) {
	roles, err := b.buildRolesInfo(ctx)
	if err != nil {
		return nil, err
	}

	config := ClusterConfig{
		Group: GroupInfo{
			Name:  b.rbg.Name,
			Size:  len(b.rbg.Spec.Roles),
			Roles: b.getRoleNames(),
		},
		Roles: roles,
	}
	return yaml.Marshal(config)
}

// BuildCompact serializes the KEP-576 v1 content. The controller must pass an
// RBG whose roles are already filtered to stateful RoleInstanceSet roles.
func (b *ConfigBuilder) BuildCompact(ctx context.Context) ([]byte, error) {
	if !b.SupportsCompactContent() {
		return nil, fmt.Errorf("compact discovery content requires stateful RoleInstanceSet roles")
	}

	roles := make([]CompactRole, 0, len(b.rbg.Spec.Roles))
	for i := range b.rbg.Spec.Roles {
		role := &b.rbg.Spec.Roles[i]
		compact, err := b.buildCompactRole(ctx, role)
		if err != nil {
			return nil, err
		}
		roles = append(roles, *compact)
	}

	return yaml.Marshal(CompactConfig{
		APIVersion: string(constants.DiscoveryConfigVersionV1),
		Group:      b.rbg.Name,
		Roles:      roles,
	})
}

// SupportsCompactContent reports whether every role can be represented by the
// compact format. Deprecated stateful backends keep the v1alpha1 enumeration,
// even when admission is bypassed.
func (b *ConfigBuilder) SupportsCompactContent() bool {
	for i := range b.rbg.Spec.Roles {
		role := &b.rbg.Spec.Roles[i]
		if !workloadsv1alpha2.IsStatefulRole(role) {
			continue
		}
		if role.GetWorkloadType() != constants.RoleInstanceSetWorkloadType {
			return false
		}
	}
	return true
}

func (b *ConfigBuilder) buildCompactRole(ctx context.Context, role *workloadsv1alpha2.RoleSpec) (*CompactRole, error) {
	serviceName, err := utils.GetCompatibleHeadlessServiceName(ctx, b.client, b.rbg, role)
	if err != nil {
		return nil, fmt.Errorf("GetCompatibleHeadlessServiceName error: %s", err.Error())
	}

	members, excludedOrdinals, err := b.memberSummary(ctx, role)
	if err != nil {
		return nil, err
	}

	ports := make(map[string]int32, len(role.ServicePorts))
	for _, port := range role.ServicePorts {
		ports[generatePortKey(port)] = port.Port
	}

	components, err := compactComponents(b.rbg.GetWorkloadName(role), role, serviceName)
	if err != nil {
		return nil, err
	}
	if err := validateCompactPlaceholders(role, components); err != nil {
		return nil, fmt.Errorf("role %s: %w", role.Name, err)
	}

	return &CompactRole{
		Name:             role.Name,
		Members:          members,
		ExcludedOrdinals: excludedOrdinals,
		Ports:            ports,
		Components:       components,
	}, nil
}

func compactComponents(
	workloadName string,
	role *workloadsv1alpha2.RoleSpec,
	serviceName string,
) ([]CompactComponent, error) {
	ccp := role.GetCustomComponentsPattern()
	lwp := role.GetLeaderWorkerPattern()

	switch {
	case ccp != nil:
		components := make([]CompactComponent, 0, len(ccp.Components))
		for _, comp := range ccp.Components {
			// The legacy address loop emits no address for a non-positive
			// size; normalize the compact field to the same zero-size meaning.
			size := ptr.Deref(comp.Size, 1)
			if size < 0 {
				size = 0
			}
			// An explicit component ServiceName overrides the shared headless
			// service as the pod subdomain.
			svcName := comp.ServiceName
			if svcName == "" {
				svcName = serviceName
			}
			components = append(components, CompactComponent{
				Name:   comp.Name,
				Size:   int(size),
				Offset: 0,
				Base: fmt.Sprintf(
					"%s-{roleIndex}-%s-{componentIndex}.%s",
					workloadName, comp.Name, svcName,
				),
			})
		}
		return components, nil
	case lwp != nil:
		size := int(ptr.Deref(lwp.Size, 1))
		base := fmt.Sprintf("%s-{roleIndex}-{componentIndex}.%s", workloadName, serviceName)
		components := []CompactComponent{{
			Name:   "leader",
			Size:   1,
			Offset: 0,
			Base:   base,
		}}
		if size > 1 {
			components = append(components, CompactComponent{
				Name:   "worker",
				Size:   size - 1,
				Offset: 1,
				Base:   base,
			})
		}
		return components, nil
	default:
		return []CompactComponent{{
			Name:   role.Name,
			Size:   1,
			Offset: 0,
			Base:   fmt.Sprintf("%s-{roleIndex}.%s", workloadName, serviceName),
		}}, nil
	}
}

var compactPlaceholderPattern = regexp.MustCompile(`\{[^{}]+\}`)

// validateCompactPlaceholders rejects templates that could not decode to the
// addresses produced by the v1alpha1 builder. Unknown placeholders and a
// pattern's missing placeholder are controller bugs.
func validateCompactPlaceholders(role *workloadsv1alpha2.RoleSpec, components []CompactComponent) error {
	required := map[string]bool{"{roleIndex}": true}
	if role.GetCustomComponentsPattern() != nil || role.GetLeaderWorkerPattern() != nil {
		required["{componentIndex}"] = true
	}
	for _, component := range components {
		found := make(map[string]bool)
		for _, placeholder := range compactPlaceholderPattern.FindAllString(component.Base, -1) {
			found[placeholder] = true
		}
		for placeholder, needed := range required {
			if needed && !found[placeholder] {
				return fmt.Errorf("component %s base %q is missing placeholder %s", component.Name, component.Base, placeholder)
			}
		}
		for placeholder := range found {
			if !required[placeholder] {
				return fmt.Errorf("component %s base %q contains unknown placeholder %s", component.Name, component.Base, placeholder)
			}
		}
	}
	return nil
}

func (b *ConfigBuilder) getRoleNames() []string {
	names := make([]string, 0, len(b.rbg.Spec.Roles))
	for _, r := range b.rbg.Spec.Roles {
		names = append(names, r.Name)
	}
	return names
}

func (b *ConfigBuilder) buildRolesInfo(ctx context.Context) (RolesInfo, error) {
	roles := make(RolesInfo)
	for _, role := range b.rbg.Spec.Roles {
		instances, err := b.buildInstances(ctx, &role)
		if err != nil {
			return nil, err
		}
		roles[role.Name] = RoleInstances{
			Size:      len(instances),
			Instances: instances,
		}
	}
	return roles, nil
}

func (b *ConfigBuilder) buildInstances(ctx context.Context, role *workloadsv1alpha2.RoleSpec) ([]Instance, error) {
	serviceName, err := utils.GetCompatibleHeadlessServiceName(ctx, b.client, b.rbg, role)
	if err != nil {
		return nil, fmt.Errorf("GetCompatibleHeadlessServiceName error: %s", err.Error())
	}

	workloadName := b.rbg.GetWorkloadName(role)

	portMap := make(map[string]int32, len(role.ServicePorts))
	for _, port := range role.ServicePorts {
		portMap[generatePortKey(port)] = port.Port
	}

	members, err := b.memberOrdinals(ctx, role)
	if err != nil {
		return nil, err
	}
	var instances []Instance
	for _, i := range members {
		addresses := memberAddresses(workloadName, role, serviceName, i)

		for _, addr := range addresses {
			inst := Instance{Address: addr}
			if len(portMap) > 0 {
				inst.Ports = make(map[string]int32, len(portMap))
				for k, v := range portMap {
					inst.Ports[k] = v
				}
			}
			instances = append(instances, inst)
		}
	}
	return instances, nil
}

func generatePortKey(port corev1.ServicePort) string {
	if port.Name != "" {
		return strings.ToLower(strings.ReplaceAll(port.Name, "-", "_"))
	}
	return fmt.Sprintf("port%d", port.Port)
}

// memberAddresses returns the v1alpha1 instance addresses for one selected
// member ordinal. It is shared by the enumeration and the size estimator so
// their address sets cannot drift.
func memberAddresses(
	workloadName string,
	role *workloadsv1alpha2.RoleSpec,
	serviceName string,
	ordinal int,
) []string {
	ccp := role.GetCustomComponentsPattern()
	lwp := role.GetLeaderWorkerPattern()
	i := int32(ordinal)
	var addresses []string

	switch {
	case ccp != nil:
		for _, comp := range ccp.Components {
			// an explicit component ServiceName overrides the shared headless service as the pod subdomain.
			svcName := comp.ServiceName
			if svcName == "" {
				svcName = serviceName
			}
			for j := int32(0); j < ptr.Deref(comp.Size, 1); j++ {
				addresses = append(addresses, fmt.Sprintf(
					"%s-%d-%s-%d.%s",
					workloadName, i, comp.Name, j, svcName,
				))
			}
		}
	case lwp != nil:
		size := int32(1)
		if lwp.Size != nil {
			size = *lwp.Size
		}
		if role.GetWorkloadType() == constants.LeaderWorkerSetWorkloadType {
			groupStart := i * size
			addresses = append(addresses, fmt.Sprintf("%s-%d.%s", workloadName, groupStart, serviceName))
			for j := int32(0); j < size-1; j++ {
				addresses = append(addresses, fmt.Sprintf("%s-%d.%s", workloadName, groupStart+j+1, serviceName))
			}
		} else {
			addresses = append(addresses, fmt.Sprintf("%s-%d-0.%s", workloadName, i, serviceName))
			for j := int32(0); j < size-1; j++ {
				addresses = append(addresses, fmt.Sprintf("%s-%d-%d.%s", workloadName, i, j+1, serviceName))
			}
		}
	default:
		addresses = append(addresses, fmt.Sprintf("%s-%d.%s", workloadName, i, serviceName))
	}

	return addresses
}

// LegacySizeLowerBound estimates a lower bound for the serialized v1alpha1
// content without enumerating instances. It lets the controller reject an
// oversized rollback before allocating the full instance list.
func (b *ConfigBuilder) LegacySizeLowerBound(ctx context.Context) (int, error) {
	total := 0
	for i := range b.rbg.Spec.Roles {
		role := &b.rbg.Spec.Roles[i]
		if !workloadsv1alpha2.IsStatefulRole(role) {
			continue
		}
		members, _, err := b.memberSummary(ctx, role)
		if err != nil {
			return 0, err
		}
		if members == 0 {
			continue
		}
		serviceName, err := utils.GetCompatibleHeadlessServiceName(ctx, b.client, b.rbg, role)
		if err != nil {
			return 0, err
		}
		workloadName := b.rbg.GetWorkloadName(role)
		// YAML emits each instance as a list entry; ports only add more bytes,
		// so excluding them keeps this estimate a safe lower bound.
		for _, address := range memberAddresses(workloadName, role, serviceName, 0) {
			total += members * (len(address) + len("\n    - address: \n"))
		}
	}
	return total, nil
}

func semanticallyEqualConfigmap(old, new *corev1.ConfigMap) (bool, string) {
	if old == nil && new == nil {
		return true, ""
	}
	if old == nil || new == nil {
		return false, fmt.Sprintf("nil mismatch: old=%v, new=%v", old, new)
	}
	// Defensive copy to prevent side effects
	oldCopy := old.DeepCopy()
	newCopy := new.DeepCopy()

	oldCopy.Annotations = utils.FilterSystemAnnotations(oldCopy.Annotations)
	newCopy.Annotations = utils.FilterSystemAnnotations(newCopy.Annotations)

	objectMetaIgnoreOpts := cmpopts.IgnoreFields(
		metav1.ObjectMeta{},
		"ResourceVersion",
		"UID",
		"CreationTimestamp",
		"Generation",
		"ManagedFields",
		"SelfLink",
	)

	opts := cmp.Options{
		objectMetaIgnoreOpts,
		cmpopts.SortSlices(
			func(a, b metav1.OwnerReference) bool {
				return a.UID < b.UID // Make OwnerReferences comparison order-insensitive
			},
		),
		cmpopts.EquateEmpty(),
	}

	diff := cmp.Diff(oldCopy, newCopy, opts)
	return diff == "", diff
}

// Discovery follows the committed child target, not live Pods or the exclusion
// input. Before initial child creation it uses the same initial planner.
func (b *ConfigBuilder) memberOrdinals(ctx context.Context, role *workloadsv1alpha2.RoleSpec) ([]int, error) {
	dense := func() ([]int, error) { return (ordinals.Ranges{}).Members(int(*role.Replicas)) }
	invalidInput := workloadsv1alpha2.ValidateRoleExcludedOrdinals(b.rbg, role) != nil
	if invalidInput && role.GetWorkloadType() != constants.RoleInstanceSetWorkloadType {
		return nil, nil
	}
	if role.GetWorkloadType() != constants.RoleInstanceSetWorkloadType || !workloadsv1alpha2.IsStatefulRole(role) {
		return dense()
	}
	set := &workloadsv1alpha2.RoleInstanceSet{}
	err := b.client.Get(ctx, client.ObjectKey{Namespace: b.rbg.Namespace, Name: b.rbg.GetWorkloadName(role)}, set)
	_, present := role.Annotations[constants.ExcludedOrdinalsAnnotationKey]
	if apierrors.IsNotFound(err) {
		// Never plan new membership from invalid input. Existing roles below
		// keep publishing their committed target until the input is repaired.
		if invalidInput {
			return nil, nil
		}
		if !present {
			return dense()
		}
		e, err := ordinals.Parse(role.Annotations[constants.ExcludedOrdinalsAnnotationKey])
		if err != nil {
			return nil, err
		}
		return ordinals.Select(nil, int(*role.Replicas), e)
	}
	if err != nil {
		return nil, err
	}
	if invalidInput || present || set.Annotations[constants.CommittedSkipOrdinalsAnnotationKey] != "" {
		owner := metav1.GetControllerOf(set)
		// Discovery runs before role reconciliation. Allow the same dense
		// orphan that the selective writer can claim, without adopting it here.
		// Sparse membership must never be inherited from an ownerless child.
		denseOrphan := owner == nil &&
			set.Annotations[constants.CommittedSkipOrdinalsAnnotationKey] == "" &&
			set.Labels[constants.GroupNameLabelKey] == b.rbg.Name
		if !denseOrphan && (owner == nil || owner.UID != b.rbg.UID) {
			return nil, fmt.Errorf("discovery RIS %s has a different controller owner", set.Name)
		}
		return set.CommittedOrdinals()
	}
	return dense()
}

// memberSummary is the range-jumping counterpart of memberOrdinals: it returns
// the selected member count and the canonical excluded-ordinal record without
// materializing the member list. It intentionally mirrors the decisions in
// memberOrdinals; tests cross-check the two representations stay equivalent.
func (b *ConfigBuilder) memberSummary(ctx context.Context, role *workloadsv1alpha2.RoleSpec) (int, string, error) {
	replicas := int(*role.Replicas)
	dense := func() (int, string, error) { return replicas, "", nil }
	invalidInput := workloadsv1alpha2.ValidateRoleExcludedOrdinals(b.rbg, role) != nil
	if invalidInput && role.GetWorkloadType() != constants.RoleInstanceSetWorkloadType {
		return 0, "", nil
	}
	if role.GetWorkloadType() != constants.RoleInstanceSetWorkloadType || !workloadsv1alpha2.IsStatefulRole(role) {
		return dense()
	}
	set := &workloadsv1alpha2.RoleInstanceSet{}
	err := b.client.Get(ctx, client.ObjectKey{Namespace: b.rbg.Namespace, Name: b.rbg.GetWorkloadName(role)}, set)
	_, present := role.Annotations[constants.ExcludedOrdinalsAnnotationKey]
	if apierrors.IsNotFound(err) {
		// Never plan new membership from invalid input. Existing roles below
		// keep publishing their committed target until the input is repaired.
		if invalidInput {
			return 0, "", nil
		}
		if !present {
			return dense()
		}
		excluded, err := ordinals.Parse(role.Annotations[constants.ExcludedOrdinalsAnnotationKey])
		if err != nil {
			return 0, "", err
		}
		record, err := ordinals.Summarize(replicas, excluded)
		return replicas, record, err
	}
	if err != nil {
		return 0, "", err
	}
	if invalidInput || present || set.Annotations[constants.CommittedSkipOrdinalsAnnotationKey] != "" {
		owner := metav1.GetControllerOf(set)
		// Mirror memberOrdinals: a dense orphan may be projected, but sparse
		// membership must never be inherited from an ownerless child.
		denseOrphan := owner == nil &&
			set.Annotations[constants.CommittedSkipOrdinalsAnnotationKey] == "" &&
			set.Labels[constants.GroupNameLabelKey] == b.rbg.Name
		if !denseOrphan && (owner == nil || owner.UID != b.rbg.UID) {
			return 0, "", fmt.Errorf("discovery RIS %s has a different controller owner", set.Name)
		}
		committed := 0
		if set.Spec.Replicas != nil {
			committed = int(*set.Spec.Replicas)
		}
		skips, err := ordinals.Parse(set.Annotations[constants.CommittedSkipOrdinalsAnnotationKey])
		if err != nil {
			return 0, "", fmt.Errorf("invalid %s: %w", constants.CommittedSkipOrdinalsAnnotationKey, err)
		}
		record, err := ordinals.Summarize(committed, skips)
		return committed, record, err
	}
	return dense()
}
