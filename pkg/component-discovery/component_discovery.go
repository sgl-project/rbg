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

// Package componentdiscovery implements intra-role component address and port injection
// for CustomComponentsPattern pods.  It parses the
// rolebasedgroup.workloads.x-k8s.io/component-discovery annotation placed on a component's
// Pod template and injects the resolved FQDN / port values as environment variables into
// every container of the Pod at creation time.
package componentdiscovery

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

const (
	// ComponentDiscoveryAnnotationKey is the annotation placed on a component's Pod template
	// to request intra-role address / port injection.
	// Value: JSON-encoded ComponentDiscoveryConfig.
	ComponentDiscoveryAnnotationKey = "rolebasedgroup.workloads.x-k8s.io/component-discovery"
)

// ComponentDiscoveryConfig is the top-level configuration for the component-discovery annotation.
type ComponentDiscoveryConfig struct {
	// AddressRefs specifies intra-role pod address references to inject as env vars.
	// +optional
	AddressRefs []ComponentAddressRef `json:"addressRefs,omitempty"`

	// PortRefs specifies intra-role port value references to inject as env vars.
	// Port values are resolved from the port-allocator annotation on the referenced component.
	// +optional
	PortRefs []ComponentPortRef `json:"portRefs,omitempty"`
}

// ComponentAddressRef injects the FQDN address of a specific pod within a named component
// of the same role instance as an environment variable.
type ComponentAddressRef struct {
	// Env is the name of the environment variable to inject.
	// Must be a valid environment variable name.
	Env string `json:"env"`

	// Component is the name of the InstanceComponent within the same role to reference.
	Component string `json:"component"`

	// Index is the zero-based ordinal index of the pod within the component.
	// Defaults to 0 (the first pod).
	// +optional
	Index int32 `json:"index,omitempty"`
}

// ComponentPortRef injects a port value that was allocated by the port-allocator annotation
// on a named component of the same role instance as an environment variable.
type ComponentPortRef struct {
	// Env is the name of the environment variable to inject.
	// Must be a valid environment variable name.
	Env string `json:"env"`

	// Component is the name of the InstanceComponent within the same role to reference.
	Component string `json:"component"`

	// PortName is the logical port name as defined in the port-allocator annotation's
	// allocations[].name field on the referenced component.
	PortName string `json:"portName"`

	// Index is the zero-based ordinal index of the pod within the component.
	// Used to resolve PodScoped port keys. Defaults to 0.
	// +optional
	Index int32 `json:"index,omitempty"`
}

// HasComponentDiscoveryConfig returns true when the pod template carries the
// component-discovery annotation.
func HasComponentDiscoveryConfig(template *corev1.PodTemplateSpec) bool {
	if template == nil {
		return false
	}
	_, ok := template.Annotations[ComponentDiscoveryAnnotationKey]
	return ok
}

// ParseComponentDiscoveryConfig parses the JSON value of the component-discovery annotation.
func ParseComponentDiscoveryConfig(annotationValue string) (*ComponentDiscoveryConfig, error) {
	cfg := &ComponentDiscoveryConfig{}
	if err := json.Unmarshal([]byte(annotationValue), cfg); err != nil {
		return nil, fmt.Errorf("invalid component-discovery annotation JSON: %w", err)
	}
	return cfg, nil
}

// InjectComponentDiscovery processes the component-discovery annotation on a Pod, injects the
// resolved FQDN and port env vars into every container, and then removes the annotation (it is
// a controller directive and must not appear on the live Pod object).
//
// The function is a no-op when the annotation is absent.
func InjectComponentDiscovery(pod *corev1.Pod, instance *workloadsv1alpha2.RoleInstance) error {
	if pod.Annotations == nil {
		return nil
	}
	annotationValue, ok := pod.Annotations[ComponentDiscoveryAnnotationKey]
	if !ok {
		return nil
	}

	cfg, err := ParseComponentDiscoveryConfig(annotationValue)
	if err != nil {
		return fmt.Errorf("pod %s: %w", pod.Name, err)
	}

	// Build lookup maps from the RoleInstance spec.
	compServiceName := make(map[string]string, len(instance.Spec.Components))
	for _, comp := range instance.Spec.Components {
		compServiceName[comp.Name] = comp.ServiceName
	}

	// 1. Inject address env vars.
	for _, ref := range cfg.AddressRefs {
		svcName, exists := compServiceName[ref.Component]
		if !exists {
			return fmt.Errorf("pod %s: component-discovery addressRef references unknown component %q", pod.Name, ref.Component)
		}
		// Pod naming convention for ComponentsTemplateType:
		//   <instanceName>-<componentName>-<id>
		// FQDN: <podName>.<serviceName>.<namespace>.svc.cluster.local
		targetPodName := fmt.Sprintf("%s-%s-%d", instance.Name, ref.Component, ref.Index)
		fqdn := fmt.Sprintf("%s.%s.%s.svc.cluster.local", targetPodName, svcName, instance.Namespace)
		injectEnvVarIntoPod(pod, ref.Env, fqdn)
	}

	// 2. Inject port env vars.
	for _, ref := range cfg.PortRefs {
		portValue, err := resolvePortRef(ref, instance)
		if err != nil {
			return fmt.Errorf("pod %s: %w", pod.Name, err)
		}
		injectEnvVarIntoPod(pod, ref.Env, portValue)
	}

	// 3. Remove the annotation — it is a controller directive, not a pod-level annotation.
	delete(pod.Annotations, ComponentDiscoveryAnnotationKey)

	return nil
}

// resolvePortRef resolves a ComponentPortRef to a port value string by looking up the
// RoleInstance annotation using both PodScoped and RoleScoped key formats.
func resolvePortRef(ref ComponentPortRef, instance *workloadsv1alpha2.RoleInstance) (string, error) {
	if len(instance.Annotations) == 0 {
		return "", fmt.Errorf("component-discovery portRef: instance %s has no annotations, cannot resolve port %q for component %q",
			instance.Name, ref.PortName, ref.Component)
	}

	// PodScoped key format:  <pod-name>.<port-name>
	targetPodName := fmt.Sprintf("%s-%s-%d", instance.Name, ref.Component, ref.Index)
	podScopedKey := fmt.Sprintf("%s.%s", targetPodName, ref.PortName)

	// RoleScoped key format: <component-name>.<port-name>
	roleScopedKey := fmt.Sprintf("%s.%s", ref.Component, ref.PortName)

	if v, ok := instance.Annotations[podScopedKey]; ok {
		return v, nil
	}
	if v, ok := instance.Annotations[roleScopedKey]; ok {
		return v, nil
	}
	return "", fmt.Errorf("component-discovery portRef: port %q for component %q not found in instance annotations (tried keys: %q, %q)",
		ref.PortName, ref.Component, podScopedKey, roleScopedKey)
}

// injectEnvVarIntoPod appends or replaces an env var in every container and init container.
func injectEnvVarIntoPod(pod *corev1.Pod, name, value string) {
	env := corev1.EnvVar{Name: name, Value: value}
	for i := range pod.Spec.Containers {
		pod.Spec.Containers[i].Env = appendOrReplaceEnv(pod.Spec.Containers[i].Env, env)
	}
	for i := range pod.Spec.InitContainers {
		pod.Spec.InitContainers[i].Env = appendOrReplaceEnv(pod.Spec.InitContainers[i].Env, env)
	}
}

func appendOrReplaceEnv(envs []corev1.EnvVar, newEnv corev1.EnvVar) []corev1.EnvVar {
	for i, e := range envs {
		if e.Name == newEnv.Name {
			envs[i] = newEnv
			return envs
		}
	}
	return append(envs, newEnv)
}
