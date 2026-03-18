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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ConfigMap name prefix and suffix
	InstanceConfigMapPrefix    = "instance-"
	InstanceSetConfigMapPrefix = "instanceset-"
	PortConfigMapSuffix        = "-ports"

	// Labels for port ConfigMaps
	PortConfigMapLabelKey   = "port-allocator.workloads.x-k8s.io/managed-by"
	PortConfigMapLabelValue = "rbgs-controller-manager"
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

// ParsePortKey parses a port key and returns the owner name and port name
// For both pod-scoped and role-scoped ports: returns (ownerName, portName)
// - PodScoped ports: ownerName is pod name
// - RoleScoped ports: ownerName is component name
func ParsePortKey(key string) (ownerName, portName string, err error) {
	dotIndex := -1
	for i, c := range key {
		if c == '.' {
			dotIndex = i
			break
		}
	}

	if dotIndex == -1 {
		// No dot found - invalid key format
		return "", "", fmt.Errorf("invalid port key format '%s': expected format '<owner>.<port-name>'", key)
	}

	if dotIndex == 0 || dotIndex == len(key)-1 {
		return "", "", fmt.Errorf("invalid port key format '%s'", key)
	}

	return key[:dotIndex], key[dotIndex+1:], nil
}

// CreatePortConfigMap creates a ConfigMap for storing ports
// Note: No OwnerReference is set because resources don't have finalizers.
// ConfigMaps must be explicitly deleted when the parent resource is deleted.
func CreatePortConfigMap(ctx context.Context, k8sClient client.Client, cmName, namespace string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: namespace,
			Labels: map[string]string{
				PortConfigMapLabelKey: PortConfigMapLabelValue,
			},
		},
		Data: make(map[string]string),
	}

	if err := k8sClient.Create(ctx, cm); err != nil {
		return nil, fmt.Errorf("failed to create port ConfigMap: %w", err)
	}

	return cm, nil
}

// GetOrCreatePortConfigMap gets or creates the port ConfigMap with the given name
func GetOrCreatePortConfigMap(ctx context.Context, k8sClient client.Client, cmName, namespace string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, cm)
	if err == nil {
		return cm, nil
	}

	// ConfigMap doesn't exist, create it
	return CreatePortConfigMap(ctx, k8sClient, cmName, namespace)
}

// UpdatePortConfigMap updates the ConfigMap using retry-on-conflict to handle concurrent reconciles
func UpdatePortConfigMap(ctx context.Context, k8sClient client.Client, cm *corev1.ConfigMap, mutateFn func(*corev1.ConfigMap)) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		latest := &corev1.ConfigMap{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: cm.Name, Namespace: cm.Namespace}, latest); err != nil {
			return err
		}
		mutateFn(latest)
		if err := k8sClient.Update(ctx, latest); err != nil {
			return err
		}
		// Reflect the latest state back to the caller so cached references stay current.
		*cm = *latest
		return nil
	})
}

// DeletePortConfigMap deletes the port ConfigMap
func DeletePortConfigMap(ctx context.Context, k8sClient client.Client, namespace, name string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	return k8sClient.Delete(ctx, cm)
}

// GetPortFromConfigMap retrieves a port value from ConfigMap
func GetPortFromConfigMap(cm *corev1.ConfigMap, key string) (string, bool) {
	if cm == nil || cm.Data == nil {
		return "", false
	}
	port, exists := cm.Data[key]
	return port, exists
}

// SetPortInConfigMap sets a port value in ConfigMap data
func SetPortInConfigMap(cm *corev1.ConfigMap, key, value string) {
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[key] = value
}

// RemovePortFromConfigMap removes a port entry from ConfigMap
func RemovePortFromConfigMap(cm *corev1.ConfigMap, key string) {
	if cm != nil && cm.Data != nil {
		delete(cm.Data, key)
	}
}

// GetPortConfigMapByName retrieves the port ConfigMap by its full name
func GetPortConfigMapByName(ctx context.Context, k8sClient client.Client, namespace, cmName string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, cm)
	if err != nil {
		return nil, err
	}
	return cm, nil
}

// GetInstancePortConfigMapName returns the ConfigMap name for dynamic port allocation
func GetInstancePortConfigMapName(instanceName string) string {
	return InstanceConfigMapPrefix + instanceName + PortConfigMapSuffix
}

// GetInstanceSetPortConfigMapName returns the ConfigMap name for static port allocation
// Format: instanceset-<name>-ports
func GetInstanceSetPortConfigMapName(instanceSetName string) string {
	return InstanceSetConfigMapPrefix + instanceSetName + PortConfigMapSuffix
}

// ReleasePortsAndDeleteCM releases all ports from a ConfigMap and deletes it
// This is used when a resource (Instance or InstanceSet) is deleted
func ReleasePortsAndDeleteCM(ctx context.Context, k8sClient client.Client, namespace, cmName string) error {
	// Get the ConfigMap
	cm, err := GetPortConfigMapByName(ctx, k8sClient, namespace, cmName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// ConfigMap doesn't exist, nothing to do
			return nil
		}
		return fmt.Errorf("failed to get port ConfigMap %s: %w", cmName, err)
	}

	// Release all ports from the ConfigMap
	if cm.Data != nil {
		for key, portStr := range cm.Data {
			port, err := strconv.ParseInt(portStr, 10, 32)
			if err != nil {
				klog.V(2).InfoS("Failed to parse port value", "key", key, "value", portStr, "error", err)
				continue
			}

			// Release the port back to the allocator
			if err := Release(int32(port)); err != nil {
				klog.V(2).InfoS("Failed to release port", "port", port, "key", key, "error", err)
			}
		}
	}

	// Delete the ConfigMap
	if err := DeletePortConfigMap(ctx, k8sClient, namespace, cmName); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete port ConfigMap %s: %w", cmName, err)
	}

	klog.InfoS("Successfully released ports and deleted ConfigMap", "configMap", cmName, "namespace", namespace)
	return nil
}

// CleanupInstancePorts releases all ports and deletes the ConfigMap for an Instance.
// This should be called when an Instance is being deleted.
func CleanupInstancePorts(ctx context.Context, k8sClient client.Client, namespace, instanceName string) error {
	cmName := GetInstancePortConfigMapName(instanceName)
	return ReleasePortsAndDeleteCM(ctx, k8sClient, namespace, cmName)
}

// CleanupInstanceSetPorts releases all ports and deletes the ConfigMap for an InstanceSet.
// This should be called when an InstanceSet is being deleted.
func CleanupInstanceSetPorts(ctx context.Context, k8sClient client.Client, namespace, instanceSetName string) error {
	cmName := GetInstanceSetPortConfigMapName(instanceSetName)
	return ReleasePortsAndDeleteCM(ctx, k8sClient, namespace, cmName)
}
