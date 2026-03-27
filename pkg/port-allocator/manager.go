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
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instanceutil "sigs.k8s.io/rbgs/pkg/inplace/instance"
	roleinstanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

const (
	// PortAllocatorAnnotationKey is the annotation key for port allocator configuration
	PortAllocatorAnnotationKey = "rolebasedgroup.workloads.x-k8s.io/port-allocator"
)

// FormatPodScopedPortKey returns the key for pod-scoped port allocation
// Format: <pod-name>.<port-name>
func FormatPodScopedPortKey(podName, portName string) string {
	return podName + "." + portName
}

// FormatRoleScopedPortKey returns the key for role-scoped port allocation
// Format: <component-name>.<port-name> (shared across all pods with the same component)
func FormatRoleScopedPortKey(componentName, portName string) string {
	return componentName + "." + portName
}

// AllocateRoleScopedPorts allocates RoleScoped ports and returns a map for annotation
// The returned map uses format: <component-name>.<port-name> -> port value (string)
func AllocateRoleScopedPorts(config *PortAllocatorConfig, componentName string) (map[string]string, error) {
	roleScopedAllocs := config.GetRoleScopedAllocations()
	if len(roleScopedAllocs) == 0 {
		return nil, nil
	}

	ports, err := AllocateBatch(int32(len(roleScopedAllocs)))
	if err != nil {
		return nil, fmt.Errorf("failed to allocate role-scoped ports: %w", err)
	}

	result := make(map[string]string, len(roleScopedAllocs))
	for i, alloc := range roleScopedAllocs {
		key := FormatRoleScopedPortKey(componentName, alloc.Name)
		result[key] = strconv.Itoa(int(ports[i]))
	}
	return result, nil
}

// AllocatePodScopedPorts allocates PodScoped ports and returns a map for annotation
// The returned map uses format: <pod-name>.<port-name> -> port value (string)
func AllocatePodScopedPorts(config *PortAllocatorConfig, podName string) (map[string]string, error) {
	if config == nil {
		return nil, nil
	}

	podScopedAllocs := config.GetPodScopedAllocations()
	if len(podScopedAllocs) == 0 {
		return nil, nil
	}

	ports, err := AllocateBatch(int32(len(podScopedAllocs)))
	if err != nil {
		return nil, fmt.Errorf("failed to allocate pod-scoped ports: %w", err)
	}

	result := make(map[string]string, len(podScopedAllocs))
	for i, alloc := range podScopedAllocs {
		key := FormatPodScopedPortKey(podName, alloc.Name)
		result[key] = strconv.Itoa(int(ports[i]))
	}
	return result, nil
}

// InjectPortsIntoPod injects allocated ports into the pod spec as environment variables and annotations
// instance is used to determine the role template type for reference port resolution
// componentName is used to look up RoleScoped ports
func InjectPortsIntoPod(pod *corev1.Pod, instance *workloadsv1alpha2.RoleInstance, config *PortAllocatorConfig, componentName string) error {
	if config == nil || len(instance.Annotations) == 0 {
		return nil
	}
	portAnnotations := instance.Annotations

	for _, alloc := range config.Allocations {
		var key string
		if alloc.Scope == PodScoped {
			key = FormatPodScopedPortKey(pod.Name, alloc.Name)
		} else {
			key = FormatRoleScopedPortKey(componentName, alloc.Name)
		}

		portValue, exists := portAnnotations[key]
		if !exists {
			return fmt.Errorf("port not found for allocation %s (key: %s)", alloc.Name, key)
		}

		injectEnvVar(pod, alloc.Env, portValue)

		if alloc.AnnotationKey != "" {
			if pod.Annotations == nil {
				pod.Annotations = make(map[string]string)
			}
			pod.Annotations[alloc.AnnotationKey] = portValue
		}
	}

	// Process references
	for _, ref := range config.References {
		_, refComponentName, portName, err := ParseReference(ref.From)
		if err != nil {
			return fmt.Errorf("invalid reference %s: %w", ref.From, err)
		}

		refPodName := GetReferencePodName(instance, refComponentName)
		if refPodName == "" {
			return fmt.Errorf("cannot determine pod name for reference %s", ref.From)
		}

		key := FormatPodScopedPortKey(refPodName, portName)
		portValue, exists := portAnnotations[key]
		if !exists {
			return fmt.Errorf("referenced port not found: %s (key: %s)", ref.From, key)
		}

		injectEnvVar(pod, ref.Env, portValue)
	}

	return nil
}

// injectEnvVar injects an environment variable with a direct value
func injectEnvVar(pod *corev1.Pod, envName, value string) {
	envVar := corev1.EnvVar{
		Name:  envName,
		Value: value,
	}

	// Add to all containers
	for i := range pod.Spec.Containers {
		pod.Spec.Containers[i].Env = appendOrReplaceEnv(pod.Spec.Containers[i].Env, envVar)
	}

	// Add to all init containers
	for i := range pod.Spec.InitContainers {
		pod.Spec.InitContainers[i].Env = appendOrReplaceEnv(pod.Spec.InitContainers[i].Env, envVar)
	}
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

// GetReferencePodName constructs the pod name for a referenced component
// Uses the FormatComponentPodName utility to ensure consistent pod naming logic.
// The reference always points to the first pod (id=0) of the target component.
func GetReferencePodName(instance *workloadsv1alpha2.RoleInstance, componentName string) string {
	if instance == nil {
		return ""
	}
	roleTemplateType := instance.GetRoleTemplateType()
	return roleinstanceutil.FormatComponentPodName(instance.Name, componentName, 0, roleTemplateType)
}

// CollectRoleScopedPortsFromInstanceSet collects RoleScoped ports from InstanceSet annotation for a specific component
func CollectRoleScopedPortsFromInstanceSet(instanceSetAnnotations map[string]string, componentName string, config *PortAllocatorConfig) map[string]string {
	if instanceSetAnnotations == nil || config == nil {
		return nil
	}

	result := make(map[string]string)
	for _, alloc := range config.GetRoleScopedAllocations() {
		key := FormatRoleScopedPortKey(componentName, alloc.Name)
		if port, exists := instanceSetAnnotations[key]; exists {
			result[key] = port
		}
	}
	return result
}

// CollectAllPortsForInstance collects all ports needed for an instance
// This includes RoleScoped ports from InstanceSet and PodScoped ports for each pod
func CollectAllPortsForInstance(instanceSetAnnotations map[string]string, componentName string, config *PortAllocatorConfig) map[string]string {
	result := make(map[string]string)

	// Collect RoleScoped ports from InstanceSet annotation
	if roleScopedPorts := CollectRoleScopedPortsFromInstanceSet(instanceSetAnnotations, componentName, config); roleScopedPorts != nil {
		for k, v := range roleScopedPorts {
			result[k] = v
		}
	}

	return result
}

// AllocatePortsForInstance derives RoleScoped ports and allocates PodScoped ports for an instance
func AllocatePortsForInstance(instance *workloadsv1alpha2.RoleInstance, instanceSet *workloadsv1alpha2.RoleInstanceSet) {
	if instance.Annotations == nil {
		instance.Annotations = make(map[string]string)
	}

	for _, component := range instance.Spec.Components {
		if !HasPortAllocatorConfig(&component.Template) {
			continue
		}
		config, err := ParsePortAllocatorConfigFromTemplate(&component.Template)
		if err != nil {
			continue
		}

		// Copy RoleScoped ports from InstanceSet annotation to Instance annotation
		for _, alloc := range config.GetRoleScopedAllocations() {
			key := FormatRoleScopedPortKey(component.Name, alloc.Name)
			if port, exists := instanceSet.Annotations[key]; exists {
				instance.Annotations[key] = port
			}
		}

		// Allocate PodScoped ports for each pod that will be created
		size := instanceutil.GetComponentSize(&component)
		for id := int32(0); id < size; id++ {
			roleTemplateType := instance.GetRoleTemplateType()
			podName := roleinstanceutil.FormatComponentPodName(instance.Name, component.Name, id, roleTemplateType)
			podAnnotations, err := AllocatePodScopedPorts(config, podName)
			if err != nil {
				continue
			}
			for k, v := range podAnnotations {
				instance.Annotations[k] = v
			}
		}
	}
}
