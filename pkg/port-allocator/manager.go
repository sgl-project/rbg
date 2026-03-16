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
// It handles both Dynamic and Static port allocation
// Dynamic ports are always stored in the Instance-level ConfigMap
// Static ports are stored in the InstanceSet-level ConfigMap if the Instance is managed by an InstanceSet,
// otherwise they are stored in the Instance-level ConfigMap
type PortManager struct {
	client   client.Client
	instance client.Object
	// instanceCM stores Dynamic ports (always instance-<instance-name>-ports)
	instanceCM *corev1.ConfigMap
	// instanceSetCM stores Static ports (only set if Instance is managed by InstanceSet)
	// Format: instanceset-<instanceset-name>-ports
	instanceSetCM *corev1.ConfigMap
	// instanceSetName is the name of the InstanceSet that owns this Instance (empty if standalone)
	instanceSetName string
}

// NewPortManager creates a new PortManager for an Instance
// It automatically determines the ConfigMap(s) needed based on Instance's OwnerReferences
func NewPortManager(ctx context.Context, k8sClient client.Client, instance client.Object) (*PortManager, error) {
	instanceName := instance.GetName()
	namespace := instance.GetNamespace()

	// Get or create the Instance-level ConfigMap for Dynamic ports
	instanceCMName := GetInstancePortConfigMapName(instanceName)
	instanceCM, err := GetOrCreatePortConfigMap(ctx, k8sClient, instanceCMName, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get/create instance port ConfigMap: %w", err)
	}

	// Check if Instance is managed by an InstanceSet
	instanceSetName := GetInstanceSetOwnerName(instance)
	var instanceSetCM *corev1.ConfigMap

	if instanceSetName != "" {
		// Get or create the InstanceSet-level ConfigMap for Static ports
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

// AllocatePortsForPod allocates both dynamic and static ports for a pod
// componentName is the component name of the pod, used for static port key
func (m *PortManager) AllocatePortsForPod(ctx context.Context, pod *corev1.Pod, config *PortAllocatorConfig, componentName string) error {
	if config == nil {
		return nil
	}

	var dynamicPortsToAllocate []PortAllocation
	var staticPortsToAllocate []PortAllocation
	var portsToPreAllocate []PortReference

	// Process Dynamic allocations - stored in instanceCM
	for _, alloc := range config.GetDynamicAllocations() {
		key := FormatDynamicPortKey(pod.Name, alloc.Name)
		if _, exists := GetPortFromConfigMap(m.instanceCM, key); !exists {
			dynamicPortsToAllocate = append(dynamicPortsToAllocate, alloc)
		}
	}

	// Process Static allocations - stored in instanceSetCM if managed, otherwise instanceCM
	staticCM := m.getStaticPortConfigMap()
	for _, alloc := range config.GetStaticAllocations() {
		key := FormatStaticPortKey(componentName, alloc.Name)
		if _, exists := GetPortFromConfigMap(staticCM, key); !exists {
			staticPortsToAllocate = append(staticPortsToAllocate, alloc)
		}
	}

	// Process References - pre-allocate if not exists (stored in instanceCM as dynamic ports)
	for _, ref := range config.References {
		componentName, portName, err := ParseReference(ref.From)
		if err != nil {
			klog.V(2).InfoS("Failed to parse reference", "from", ref.From, "error", err)
			continue
		}

		// Build the key for the referenced port
		// We need to find the pod name for the referenced component
		refPodName := m.getReferencePodName(pod, componentName)
		if refPodName == "" {
			// Cannot determine pod name, skip for now
			klog.V(2).InfoS("Cannot determine reference pod name", "component", componentName, "pod", klog.KObj(pod))
			continue
		}

		key := FormatDynamicPortKey(refPodName, portName)
		if _, exists := GetPortFromConfigMap(m.instanceCM, key); !exists {
			portsToPreAllocate = append(portsToPreAllocate, ref)
		}
	}

	// Allocate new dynamic ports
	if len(dynamicPortsToAllocate) > 0 {
		allocatedPorts, err := AllocateBatch(int32(len(dynamicPortsToAllocate)))
		if err != nil {
			return fmt.Errorf("failed to allocate dynamic ports: %w", err)
		}

		for i, alloc := range dynamicPortsToAllocate {
			key := FormatDynamicPortKey(pod.Name, alloc.Name)
			SetPortInConfigMap(m.instanceCM, key, strconv.Itoa(int(allocatedPorts[i])))
		}
	}

	// Allocate new static ports
	if len(staticPortsToAllocate) > 0 {
		allocatedPorts, err := AllocateBatch(int32(len(staticPortsToAllocate)))
		if err != nil {
			return fmt.Errorf("failed to allocate static ports: %w", err)
		}

		for i, alloc := range staticPortsToAllocate {
			key := FormatStaticPortKey(componentName, alloc.Name)
			SetPortInConfigMap(staticCM, key, strconv.Itoa(int(allocatedPorts[i])))
		}
	}

	// Pre-allocate reference ports
	if len(portsToPreAllocate) > 0 {
		allocatedPorts, err := AllocateBatch(int32(len(portsToPreAllocate)))
		if err != nil {
			return fmt.Errorf("failed to allocate reference ports: %w", err)
		}

		for i, ref := range portsToPreAllocate {
			refComponentName, portName, _ := ParseReference(ref.From)
			refPodName := m.getReferencePodName(pod, refComponentName)
			if refPodName != "" {
				key := FormatDynamicPortKey(refPodName, portName)
				SetPortInConfigMap(m.instanceCM, key, strconv.Itoa(int(allocatedPorts[i])))
			}
		}
	}

	// Update ConfigMaps if there were changes
	if len(dynamicPortsToAllocate) > 0 || len(portsToPreAllocate) > 0 {
		if err := UpdatePortConfigMap(ctx, m.client, m.instanceCM); err != nil {
			return fmt.Errorf("failed to update instance port ConfigMap: %w", err)
		}
	}

	if len(staticPortsToAllocate) > 0 {
		if err := UpdatePortConfigMap(ctx, m.client, staticCM); err != nil {
			return fmt.Errorf("failed to update static port ConfigMap: %w", err)
		}
	}

	return nil
}

// InjectPortsIntoPod injects allocated ports into the pod spec
// componentName is the component name of the pod, used for static port key
func (m *PortManager) InjectPortsIntoPod(pod *corev1.Pod, config *PortAllocatorConfig, componentName string) error {
	if config == nil {
		return nil
	}

	staticCM := m.getStaticPortConfigMap()

	// Process allocations
	for _, alloc := range config.Allocations {
		var key string
		var cm *corev1.ConfigMap

		if alloc.Policy == Dynamic {
			key = FormatDynamicPortKey(pod.Name, alloc.Name)
			cm = m.instanceCM
		} else {
			key = FormatStaticPortKey(componentName, alloc.Name)
			cm = staticCM
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

		key := FormatDynamicPortKey(refPodName, portName)
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

// ReleasePodDynamicPorts releases dynamic ports for a deleted pod
// Static ports are NOT released here (they are shared)
func (m *PortManager) ReleasePodDynamicPorts(ctx context.Context, pod *corev1.Pod, config *PortAllocatorConfig) error {
	if config == nil {
		return nil
	}

	dynamicAllocations := config.GetDynamicAllocations()
	if len(dynamicAllocations) == 0 {
		return nil
	}

	for _, alloc := range dynamicAllocations {
		key := FormatDynamicPortKey(pod.Name, alloc.Name)
		portStr, exists := GetPortFromConfigMap(m.instanceCM, key)
		if !exists {
			continue
		}

		port, err := strconv.ParseInt(portStr, 10, 32)
		if err != nil {
			continue
		}

		// Release the port
		if err := Release(int32(port)); err != nil {
			klog.V(2).InfoS("Failed to release port", "port", port, "error", err)
		}

		// Remove from ConfigMap
		RemovePortFromConfigMap(m.instanceCM, key)
	}

	// Update ConfigMap
	if err := UpdatePortConfigMap(ctx, m.client, m.instanceCM); err != nil {
		return fmt.Errorf("failed to update instance port ConfigMap: %w", err)
	}

	return nil
}

// SyncPortAllocations handles port allocation changes during updates
// It compares current ConfigMap state with new config and allocates/releases ports accordingly
func (m *PortManager) SyncPortAllocations(ctx context.Context, pod *corev1.Pod, oldConfig, newConfig *PortAllocatorConfig, componentName string) error {
	if oldConfig == nil && newConfig == nil {
		return nil
	}

	// Build sets of old and new port names
	oldDynamicPorts := sets.NewString()
	oldStaticPorts := sets.NewString()
	newDynamicPorts := sets.NewString()
	newStaticPorts := sets.NewString()

	if oldConfig != nil {
		for _, alloc := range oldConfig.GetDynamicAllocations() {
			oldDynamicPorts.Insert(alloc.Name)
		}
		for _, alloc := range oldConfig.GetStaticAllocations() {
			oldStaticPorts.Insert(alloc.Name)
		}
	}

	if newConfig != nil {
		for _, alloc := range newConfig.GetDynamicAllocations() {
			newDynamicPorts.Insert(alloc.Name)
		}
		for _, alloc := range newConfig.GetStaticAllocations() {
			newStaticPorts.Insert(alloc.Name)
		}
	}

	// Find ports to add and remove
	dynamicToAdd := newDynamicPorts.Difference(oldDynamicPorts)
	dynamicToRemove := oldDynamicPorts.Difference(newDynamicPorts)
	staticToAdd := newStaticPorts.Difference(oldStaticPorts)
	staticToRemove := oldStaticPorts.Difference(newStaticPorts)

	staticCM := m.getStaticPortConfigMap()

	// Allocate new dynamic ports
	if dynamicToAdd.Len() > 0 {
		ports, err := AllocateBatch(int32(dynamicToAdd.Len()))
		if err != nil {
			return fmt.Errorf("failed to allocate new dynamic ports: %w", err)
		}

		i := 0
		for _, name := range dynamicToAdd.List() {
			key := FormatDynamicPortKey(pod.Name, name)
			SetPortInConfigMap(m.instanceCM, key, strconv.Itoa(int(ports[i])))
			i++
		}
	}

	// Allocate new static ports
	if staticToAdd.Len() > 0 {
		ports, err := AllocateBatch(int32(staticToAdd.Len()))
		if err != nil {
			return fmt.Errorf("failed to allocate new static ports: %w", err)
		}

		i := 0
		for _, name := range staticToAdd.List() {
			key := FormatStaticPortKey(componentName, name)
			SetPortInConfigMap(staticCM, key, strconv.Itoa(int(ports[i])))
			i++
		}
	}

	// Release removed dynamic ports
	for _, name := range dynamicToRemove.List() {
		key := FormatDynamicPortKey(pod.Name, name)
		if portStr, exists := GetPortFromConfigMap(m.instanceCM, key); exists {
			if port, err := strconv.ParseInt(portStr, 10, 32); err == nil {
				Release(int32(port))
			}
			RemovePortFromConfigMap(m.instanceCM, key)
		}
	}

	// Release removed static ports
	for _, name := range staticToRemove.List() {
		key := FormatStaticPortKey(componentName, name)
		if portStr, exists := GetPortFromConfigMap(staticCM, key); exists {
			if port, err := strconv.ParseInt(portStr, 10, 32); err == nil {
				Release(int32(port))
			}
			RemovePortFromConfigMap(staticCM, key)
		}
	}

	// Update ConfigMaps if there were changes
	if dynamicToAdd.Len() > 0 || dynamicToRemove.Len() > 0 {
		if err := UpdatePortConfigMap(ctx, m.client, m.instanceCM); err != nil {
			return fmt.Errorf("failed to update instance port ConfigMap: %w", err)
		}
	}

	if staticToAdd.Len() > 0 || staticToRemove.Len() > 0 {
		if err := UpdatePortConfigMap(ctx, m.client, staticCM); err != nil {
			return fmt.Errorf("failed to update static port ConfigMap: %w", err)
		}
	}

	return nil
}

// getStaticPortConfigMap returns the ConfigMap for static ports
// If Instance is managed by InstanceSet, returns instanceSetCM; otherwise returns instanceCM
func (m *PortManager) getStaticPortConfigMap() *corev1.ConfigMap {
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

// GetInstanceSetOwnerName returns the name of the InstanceSet that owns this Instance
func GetInstanceSetOwnerName(instance client.Object) string {
	if instance == nil {
		return ""
	}

	ownerRefs := instance.GetOwnerReferences()
	for _, ref := range ownerRefs {
		if ref.Kind == "InstanceSet" && ref.APIVersion == "workloads.x-k8s.io/v1alpha1" {
			return ref.Name
		}
	}

	return ""
}

// Backward compatibility - InstancePortManager alias
type InstancePortManager = PortManager

// NewInstancePortManager is an alias for NewPortManager
var NewInstancePortManager = NewPortManager
