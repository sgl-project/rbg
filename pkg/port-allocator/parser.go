package port_allocator

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// ParsePortAllocatorConfig parses the PortAllocatorConfig from Pod annotations
func ParsePortAllocatorConfig(pod *corev1.Pod) (*PortAllocatorConfig, error) {
	if pod == nil {
		return nil, nil
	}

	annotationValue, exists := pod.Annotations[portAllocatorAnnotationKey]
	if !exists || annotationValue == "" {
		return nil, nil
	}

	config := &PortAllocatorConfig{}
	if err := json.Unmarshal([]byte(annotationValue), config); err != nil {
		return nil, fmt.Errorf("failed to parse port allocator config: %w", err)
	}

	if err := validatePortAllocatorConfig(config); err != nil {
		return nil, err
	}

	return config, nil
}

// ParsePortAllocatorConfigFromTemplate parses the PortAllocatorConfig from PodTemplateSpec annotations
func ParsePortAllocatorConfigFromTemplate(template *corev1.PodTemplateSpec) (*PortAllocatorConfig, error) {
	if template == nil {
		return nil, nil
	}

	annotationValue, exists := template.Annotations[portAllocatorAnnotationKey]
	if !exists || annotationValue == "" {
		return nil, nil
	}

	config := &PortAllocatorConfig{}
	if err := json.Unmarshal([]byte(annotationValue), config); err != nil {
		return nil, fmt.Errorf("failed to parse port allocator config: %w", err)
	}

	if err := validatePortAllocatorConfig(config); err != nil {
		return nil, err
	}

	return config, nil
}

// validatePortAllocatorConfig validates the PortAllocatorConfig
func validatePortAllocatorConfig(config *PortAllocatorConfig) error {
	if config == nil {
		return nil
	}

	// Validate allocations
	for i := range config.Allocations {
		if config.Allocations[i].Name == "" {
			return fmt.Errorf("allocations[%d].name is required", i)
		}
		if config.Allocations[i].Env == "" {
			return fmt.Errorf("allocations[%d].env is required", i)
		}
		if config.Allocations[i].Policy == "" {
			// Default to Dynamic
			config.Allocations[i].Policy = Dynamic
		}
		if config.Allocations[i].Policy != Dynamic && config.Allocations[i].Policy != Static {
			return fmt.Errorf("allocations[%d].policy must be 'Dynamic' or 'Static', got '%s'", i, config.Allocations[i].Policy)
		}
	}

	// Validate references
	for i := range config.References {
		if config.References[i].Env == "" {
			return fmt.Errorf("references[%d].env is required", i)
		}
		if config.References[i].From == "" {
			return fmt.Errorf("references[%d].from is required", i)
		}
	}

	return nil
}

// GetDynamicAllocations returns all allocations with Dynamic policy
func (c *PortAllocatorConfig) GetDynamicAllocations() []PortAllocation {
	if c == nil {
		return nil
	}

	var dynamic []PortAllocation
	for _, alloc := range c.Allocations {
		if alloc.Policy == Dynamic {
			dynamic = append(dynamic, alloc)
		}
	}
	return dynamic
}

// GetStaticAllocations returns all allocations with Static policy
func (c *PortAllocatorConfig) GetStaticAllocations() []PortAllocation {
	if c == nil {
		return nil
	}

	var static []PortAllocation
	for _, alloc := range c.Allocations {
		if alloc.Policy == Static {
			static = append(static, alloc)
		}
	}
	return static
}

// HasPortAllocatorConfig checks if the pod template has port allocator config
func HasPortAllocatorConfig(template *corev1.PodTemplateSpec) bool {
	if template == nil {
		return false
	}
	_, exists := template.Annotations[portAllocatorAnnotationKey]
	return exists
}

// ParseReference parses the "from" field of PortReference
// Format: "<component_name>.<port_name>"
func ParseReference(from string) (componentName, portName string, err error) {
	if from == "" {
		return "", "", fmt.Errorf("empty reference")
	}

	dotIndex := -1
	for i, c := range from {
		if c == '.' {
			dotIndex = i
			break
		}
	}

	if dotIndex == -1 || dotIndex == 0 || dotIndex == len(from)-1 {
		return "", "", fmt.Errorf("invalid reference format '%s', expected '<component_name>.<port_name>'", from)
	}

	return from[:dotIndex], from[dotIndex+1:], nil
}
