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

package port_allocator

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PortManager manages port allocation for an Instance
// It handles both PodScoped and RoleScoped port allocation
// PodScoped ports are always stored in the Instance-level ConfigMap
// RoleScoped ports are stored in the InstanceSet-level ConfigMap
type PortManager struct {
	client   client.Client
	instance client.Object
	// instanceCM stores PodScoped ports
	// Format: instance-<instance-name>-ports
	instanceCM *corev1.ConfigMap
	// instanceSetCM stores RoleScoped ports
	// Format: instanceset-<instanceset-name>-ports
	instanceSetCM *corev1.ConfigMap
	// instanceSetName is the name of the InstanceSet that owns this Instance
	instanceSetName string
}

// NewPortManager creates a new PortManager for an Instance
func NewPortManager(ctx context.Context, k8sClient client.Client, instance client.Object) (*PortManager, error) {
	instanceName := instance.GetName()
	namespace := instance.GetNamespace()

	// Get or create the Instance-level ConfigMap for PodScoped ports
	instanceCMName := GetInstancePortConfigMapName(instanceName)
	instanceCM, err := GetOrCreatePortConfigMap(ctx, k8sClient, instanceCMName, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get/create instance port ConfigMap: %w", err)
	}

	instanceSetName := getInstanceSetOwnerName(instance)
	var instanceSetCM *corev1.ConfigMap

	if instanceSetName != "" {
		// Get or create the InstanceSet-level ConfigMap for RoleScoped ports
		instanceSetCMName := GetInstanceSetPortConfigMapName(instanceSetName)
		instanceSetCM, err = GetOrCreatePortConfigMap(ctx, k8sClient, instanceSetCMName, namespace)
		if err != nil {
			return nil, fmt.Errorf("failed to get/create instanceset port ConfigMap: %w", err)
		}
	}

	return &PortManager{
		client:          k8sClient,
		instance:        instance,
		instanceCM:      instanceCM,
		instanceSetCM:   instanceSetCM,
		instanceSetName: instanceSetName,
	}, nil
}

// AllocatePortsForPod allocates both pod-scoped and role-scoped ports for a pod
func (m *PortManager) AllocatePortsForPod(ctx context.Context, pod *corev1.Pod, config *PortAllocatorConfig, componentName string) error {
	if config == nil {
		return nil
	}

	roleScopedCM := m.getRoleScopedPortConfigMap()

	// Collect ports that need allocation
	podScopedToAlloc := m.collectMissingPodScopedPorts(config, pod.Name)
	roleScopedToAlloc := m.collectMissingRoleScopedPorts(config, componentName, roleScopedCM)
	refToAlloc := m.collectMissingReferencePorts(config, pod)

	// Allocate and build change maps
	instanceCMAdds, err := m.allocatePodScopedPorts(podScopedToAlloc, pod.Name)
	if err != nil {
		return fmt.Errorf("failed to allocate pod-scoped ports: %w", err)
	}

	roleScopedCMAdds, err := m.allocateRoleScopedPorts(roleScopedToAlloc, componentName)
	if err != nil {
		return fmt.Errorf("failed to allocate role-scoped ports: %w", err)
	}

	refAdds, err := m.allocateReferencePorts(refToAlloc, pod)
	if err != nil {
		return fmt.Errorf("failed to allocate reference ports: %w", err)
	}

	// Merge reference port adds into instanceCMAdds
	if len(refAdds) > 0 {
		if instanceCMAdds == nil {
			instanceCMAdds = refAdds
		} else {
			for k, v := range refAdds {
				instanceCMAdds[k] = v
			}
		}
	}

	// Apply changes to ConfigMaps
	if err := m.applyConfigMapChanges(ctx, m.instanceCM, instanceCMAdds, nil); err != nil {
		return fmt.Errorf("failed to update instance port ConfigMap: %w", err)
	}

	if err := m.applyConfigMapChanges(ctx, roleScopedCM, roleScopedCMAdds, nil); err != nil {
		return fmt.Errorf("failed to update role-scoped port ConfigMap: %w", err)
	}

	return nil
}

// collectMissingPodScopedPorts returns pod-scoped port allocations that don't exist in the ConfigMap
func (m *PortManager) collectMissingPodScopedPorts(config *PortAllocatorConfig, podName string) []PortAllocation {
	var result []PortAllocation
	for _, alloc := range config.GetPodScopedAllocations() {
		key := FormatPodScopedPortKey(podName, alloc.Name)
		if _, exists := GetPortFromConfigMap(m.instanceCM, key); !exists {
			result = append(result, alloc)
		}
	}
	return result
}

// collectMissingRoleScopedPorts returns role-scoped port allocations that don't exist in the ConfigMap
func (m *PortManager) collectMissingRoleScopedPorts(config *PortAllocatorConfig, componentName string, cm *corev1.ConfigMap) []PortAllocation {
	var result []PortAllocation
	for _, alloc := range config.GetRoleScopedAllocations() {
		key := FormatRoleScopedPortKey(componentName, alloc.Name)
		if _, exists := GetPortFromConfigMap(cm, key); !exists {
			result = append(result, alloc)
		}
	}
	return result
}

// collectMissingReferencePorts returns reference ports that don't exist in the ConfigMap
func (m *PortManager) collectMissingReferencePorts(config *PortAllocatorConfig, pod *corev1.Pod) []PortReference {
	var result []PortReference
	for _, ref := range config.References {
		compName, portName, err := ParseReference(ref.From)
		if err != nil {
			klog.V(2).InfoS("Failed to parse reference", "from", ref.From, "error", err)
			continue
		}

		refPodName := m.getReferencePodName(pod, compName)
		if refPodName == "" {
			klog.V(2).InfoS("Cannot determine reference pod name", "component", compName, "pod", klog.KObj(pod))
			continue
		}

		key := FormatPodScopedPortKey(refPodName, portName)
		if _, exists := GetPortFromConfigMap(m.instanceCM, key); !exists {
			result = append(result, ref)
		}
	}
	return result
}

// allocatePodScopedPorts allocates ports for the given allocations and returns a key->value map
func (m *PortManager) allocatePodScopedPorts(allocs []PortAllocation, podName string) (map[string]string, error) {
	if len(allocs) == 0 {
		return nil, nil
	}

	ports, err := AllocateBatch(int32(len(allocs)))
	if err != nil {
		return nil, err
	}

	result := make(map[string]string, len(allocs))
	for i, alloc := range allocs {
		key := FormatPodScopedPortKey(podName, alloc.Name)
		result[key] = strconv.Itoa(int(ports[i]))
	}
	return result, nil
}

// allocateRoleScopedPorts allocates ports for the given allocations and returns a key->value map
func (m *PortManager) allocateRoleScopedPorts(allocs []PortAllocation, componentName string) (map[string]string, error) {
	if len(allocs) == 0 {
		return nil, nil
	}

	ports, err := AllocateBatch(int32(len(allocs)))
	if err != nil {
		return nil, err
	}

	result := make(map[string]string, len(allocs))
	for i, alloc := range allocs {
		key := FormatRoleScopedPortKey(componentName, alloc.Name)
		result[key] = strconv.Itoa(int(ports[i]))
	}
	return result, nil
}

// allocateReferencePorts allocates ports for the given references and returns a key->value map
func (m *PortManager) allocateReferencePorts(refs []PortReference, pod *corev1.Pod) (map[string]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}

	ports, err := AllocateBatch(int32(len(refs)))
	if err != nil {
		return nil, err
	}

	result := make(map[string]string, len(refs))
	for i, ref := range refs {
		compName, portName, _ := ParseReference(ref.From)
		refPodName := m.getReferencePodName(pod, compName)
		if refPodName != "" {
			key := FormatPodScopedPortKey(refPodName, portName)
			result[key] = strconv.Itoa(int(ports[i]))
		}
	}
	return result, nil
}

// InjectPortsIntoPod injects allocated ports into the pod spec
// componentName is the component name of the pod, used for role-scoped port key
func (m *PortManager) InjectPortsIntoPod(pod *corev1.Pod, config *PortAllocatorConfig, componentName string) error {
	if config == nil {
		return nil
	}

	roleScopedCM := m.getRoleScopedPortConfigMap()

	// Process allocations
	for _, alloc := range config.Allocations {
		var key string
		var cm *corev1.ConfigMap

		if alloc.Scope == PodScoped {
			key = FormatPodScopedPortKey(pod.Name, alloc.Name)
			cm = m.instanceCM
		} else {
			key = FormatRoleScopedPortKey(componentName, alloc.Name)
			cm = roleScopedCM
		}

		portValue, exists := GetPortFromConfigMap(cm, key)
		if !exists {
			return fmt.Errorf("port not found for allocation %s (key: %s)", alloc.Name, key)
		}

		// Inject environment variable
		if err := m.injectEnvVar(pod, alloc.Env, cm.Name, key); err != nil {
			return err
		}

		// Inject annotation if specified
		if alloc.AnnotationKey != "" {
			if pod.Annotations == nil {
				pod.Annotations = make(map[string]string)
			}
			pod.Annotations[alloc.AnnotationKey] = portValue
		}
	}

	// Process references
	for _, ref := range config.References {
		refComponentName, portName, err := ParseReference(ref.From)
		if err != nil {
			return fmt.Errorf("invalid reference %s: %w", ref.From, err)
		}

		refPodName := m.getReferencePodName(pod, refComponentName)
		if refPodName == "" {
			return fmt.Errorf("cannot determine pod name for reference %s", ref.From)
		}

		key := FormatPodScopedPortKey(refPodName, portName)
		if _, exists := GetPortFromConfigMap(m.instanceCM, key); !exists {
			return fmt.Errorf("referenced port not found: %s (key: %s)", ref.From, key)
		}

		// Inject environment variable
		if err := m.injectEnvVar(pod, ref.Env, m.instanceCM.Name, key); err != nil {
			return err
		}
	}

	return nil
}

// injectEnvVar injects an environment variable using configMapKeyRef
func (m *PortManager) injectEnvVar(pod *corev1.Pod, envName, cmName, cmKey string) error {
	envVar := corev1.EnvVar{
		Name: envName,
		ValueFrom: &corev1.EnvVarSource{
			ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: cmName,
				},
				Key: cmKey,
			},
		},
	}

	// Add to all containers
	for i := range pod.Spec.Containers {
		pod.Spec.Containers[i].Env = appendOrReplaceEnv(pod.Spec.Containers[i].Env, envVar)
	}

	// Add to all init containers
	for i := range pod.Spec.InitContainers {
		pod.Spec.InitContainers[i].Env = appendOrReplaceEnv(pod.Spec.InitContainers[i].Env, envVar)
	}

	return nil
}

// ReleasePodScopedPorts releases pod-scoped ports for a deleted pod
// Role-scoped ports are NOT released here (they are shared)
func (m *PortManager) ReleasePodScopedPorts(ctx context.Context, pod *corev1.Pod, config *PortAllocatorConfig) error {
	if config == nil {
		return nil
	}

	podScopedAllocations := config.GetPodScopedAllocations()
	if len(podScopedAllocations) == 0 {
		return nil
	}

	// Collect keys to remove and release ports from the in-memory allocator first.
	keysToRemove := make([]string, 0, len(podScopedAllocations))
	for _, alloc := range podScopedAllocations {
		key := FormatPodScopedPortKey(pod.Name, alloc.Name)
		portStr, exists := GetPortFromConfigMap(m.instanceCM, key)
		if !exists {
			continue
		}

		port, err := strconv.ParseInt(portStr, 10, 32)
		if err != nil {
			continue
		}

		// Release the port from the in-memory allocator (idempotent, safe outside retry).
		if err := Release(int32(port)); err != nil {
			klog.V(2).InfoS("Failed to release port", "port", port, "error", err)
		}

		keysToRemove = append(keysToRemove, key)
	}

	// Update ConfigMap with retry-on-conflict
	if len(keysToRemove) > 0 {
		if err := UpdatePortConfigMap(ctx, m.client, m.instanceCM, func(cm *corev1.ConfigMap) {
			for _, key := range keysToRemove {
				RemovePortFromConfigMap(cm, key)
			}
		}); err != nil {
			return fmt.Errorf("failed to update instance port ConfigMap: %w", err)
		}
	}

	return nil
}

// SyncPortAllocations handles port allocation changes during updates
// It compares current ConfigMap state with new config and allocates/releases ports accordingly
func (m *PortManager) SyncPortAllocations(ctx context.Context, pod *corev1.Pod, oldConfig, newConfig *PortAllocatorConfig, componentName string) error {
	if oldConfig == nil && newConfig == nil {
		return nil
	}

	oldPodScoped, oldRoleScoped := buildPortNameSets(oldConfig)
	newPodScoped, newRoleScoped := buildPortNameSets(newConfig)

	podScopedToAdd := newPodScoped.Difference(oldPodScoped)
	podScopedToRemove := oldPodScoped.Difference(newPodScoped)
	roleScopedToAdd := newRoleScoped.Difference(oldRoleScoped)
	roleScopedToRemove := oldRoleScoped.Difference(newRoleScoped)

	roleScopedCM := m.getRoleScopedPortConfigMap()

	instanceCMAdds, err := m.allocatePortsToMap(podScopedToAdd, pod.Name, FormatPodScopedPortKey)
	if err != nil {
		return fmt.Errorf("failed to allocate new pod-scoped ports: %w", err)
	}
	roleScopedCMAdds, err := m.allocatePortsToMap(roleScopedToAdd, componentName, FormatRoleScopedPortKey)
	if err != nil {
		return fmt.Errorf("failed to allocate new role-scoped ports: %w", err)
	}

	instanceCMRemoves := m.collectPortsToRemove(podScopedToRemove, m.instanceCM, pod.Name, FormatPodScopedPortKey)
	roleScopedCMRemoves := m.collectPortsToRemove(roleScopedToRemove, roleScopedCM, componentName, FormatRoleScopedPortKey)

	if err := m.applyConfigMapChanges(ctx, m.instanceCM, instanceCMAdds, instanceCMRemoves); err != nil {
		return fmt.Errorf("failed to update instance port ConfigMap: %w", err)
	}

	if err := m.applyConfigMapChanges(ctx, roleScopedCM, roleScopedCMAdds, roleScopedCMRemoves); err != nil {
		return fmt.Errorf("failed to update role-scoped port ConfigMap: %w", err)
	}

	return nil
}

// portKeyFormatter formats a port key given an owner name and port name
type portKeyFormatter func(ownerName, portName string) string

// buildPortNameSets extracts pod-scoped and role-scoped port name sets from a config
func buildPortNameSets(config *PortAllocatorConfig) (podScoped, roleScoped sets.Set[string]) {
	podScoped = sets.New[string]()
	roleScoped = sets.New[string]()
	if config == nil {
		return
	}
	for _, alloc := range config.GetPodScopedAllocations() {
		podScoped.Insert(alloc.Name)
	}
	for _, alloc := range config.GetRoleScopedAllocations() {
		roleScoped.Insert(alloc.Name)
	}
	return
}

// allocatePortsToMap allocates ports for the given port names and returns a key->value map
func (m *PortManager) allocatePortsToMap(portNames sets.Set[string], ownerName string, keyFmt portKeyFormatter) (map[string]string, error) {
	if portNames.Len() == 0 {
		return nil, nil
	}

	ports, err := AllocateBatch(int32(portNames.Len()))
	if err != nil {
		return nil, err
	}

	result := make(map[string]string, portNames.Len())
	for i, name := range portNames.UnsortedList() {
		key := keyFmt(ownerName, name)
		result[key] = strconv.Itoa(int(ports[i]))
	}
	return result, nil
}

// collectPortsToRemove releases ports from the in-memory allocator and returns keys to remove from ConfigMap
func (m *PortManager) collectPortsToRemove(portNames sets.Set[string], cm *corev1.ConfigMap, ownerName string, keyFmt portKeyFormatter) []string {
	if portNames.Len() == 0 {
		return nil
	}

	keys := make([]string, 0, portNames.Len())
	for _, name := range portNames.UnsortedList() {
		key := keyFmt(ownerName, name)
		if portStr, exists := GetPortFromConfigMap(cm, key); exists {
			if port, err := strconv.ParseInt(portStr, 10, 32); err == nil {
				if err := Release(int32(port)); err != nil {
					klog.V(2).InfoS("Failed to release port", "port", port, "error", err)
				}
			}
			keys = append(keys, key)
		}
	}
	return keys
}

// applyConfigMapChanges applies adds and removes to a ConfigMap with retry-on-conflict
func (m *PortManager) applyConfigMapChanges(ctx context.Context, cm *corev1.ConfigMap, adds map[string]string, removes []string) error {
	if len(adds) == 0 && len(removes) == 0 {
		return nil
	}
	return UpdatePortConfigMap(ctx, m.client, cm, func(cm *corev1.ConfigMap) {
		for k, v := range adds {
			SetPortInConfigMap(cm, k, v)
		}
		for _, key := range removes {
			RemovePortFromConfigMap(cm, key)
		}
	})
}

// getRoleScopedPortConfigMap returns the ConfigMap for role-scoped ports
// If Instance is managed by InstanceSet, returns instanceSetCM; otherwise returns instanceCM
func (m *PortManager) getRoleScopedPortConfigMap() *corev1.ConfigMap {
	if m.instanceSetCM != nil {
		return m.instanceSetCM
	}
	return m.instanceCM
}

// GetInstanceConfigMap returns the Instance-level ConfigMap
func (m *PortManager) GetInstanceConfigMap() *corev1.ConfigMap {
	return m.instanceCM
}

// GetInstanceSetConfigMap returns the InstanceSet-level ConfigMap (might be nil for standalone Instances)
func (m *PortManager) GetInstanceSetConfigMap() *corev1.ConfigMap {
	return m.instanceSetCM
}

// IsManagedByInstanceSet returns true if the Instance is managed by an InstanceSet
func (m *PortManager) IsManagedByInstanceSet() bool {
	return m.instanceSetName != ""
}

// getReferencePodName constructs the pod name for a referenced component
// Pod name format: <instance-name>-<component-name>-<component-id>
// e.g., "rbg-instance-0-worker-2" -> prefix "rbg-instance-0", target "rbg-instance-0-leader-0"
// The reference always points to the first pod (id=0) of the target component
func (m *PortManager) getReferencePodName(currentPod *corev1.Pod, componentName string) string {
	if currentPod == nil {
		return ""
	}

	// Extract prefix from current pod name
	// Format: <prefix>-<component-name>-<component-id>
	// We need to find the last two hyphens to get the prefix
	podName := currentPod.Name

	// Find the last hyphen (component-id boundary)
	lastHyphen := strings.LastIndex(podName, "-")
	if lastHyphen == -1 {
		return ""
	}

	// Find the second-to-last hyphen (component-name boundary)
	secondLastHyphen := strings.LastIndex(podName[:lastHyphen], "-")
	if secondLastHyphen == -1 {
		return ""
	}

	// Extract prefix (everything before component-name)
	prefix := podName[:secondLastHyphen]

	// Construct target pod name: <prefix>-<component-name>-0
	// Always reference the first pod (id=0) of the target component
	return prefix + "-" + componentName + "-0"
}

// appendOrReplaceEnv appends or replaces an environment variable
func appendOrReplaceEnv(envs []corev1.EnvVar, newEnv corev1.EnvVar) []corev1.EnvVar {
	for i, env := range envs {
		if env.Name == newEnv.Name {
			envs[i] = newEnv
			return envs
		}
	}
	return append(envs, newEnv)
}

// getInstanceSetOwnerName returns the name of the InstanceSet that owns this Instance
func getInstanceSetOwnerName(instance client.Object) string {
	if instance == nil {
		return ""
	}

	ownerRefs := instance.GetOwnerReferences()
	for _, ref := range ownerRefs {
		if ref.Kind == "RoleInstanceSet" {
			return ref.Name
		}
	}
	return ""
}

// Backward compatibility - InstancePortManager alias
type InstancePortManager = PortManager

// NewInstancePortManager is an alias for NewPortManager
var NewInstancePortManager = NewPortManager
