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

package engine

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/rbgs/cmd/cli/plugin/util"
)

// Plugin defines the interface for inference engines.
type Plugin interface {
	Name() string

	// ConfigFields returns the config fields this plugin accepts.
	ConfigFields() []util.ConfigField

	// Init initializes the engine with credentials/config.
	Init(config map[string]interface{}) error

	// GenerateTemplate generates a PodTemplateSpec used to start the model engine.
	GenerateTemplate(name string, modelID string, modelPath string) (*corev1.PodTemplateSpec, error)
}

// Factory is a constructor for an engine plugin.
type Factory func() Plugin

var registry = make(map[string]Factory)

// Register registers an engine plugin factory under the given type name.
func Register(name string, factory Factory) {
	registry[name] = factory
}

// Get returns an initialized engine plugin instance for the given type and config.
func Get(pluginType string, config map[string]interface{}) (Plugin, error) {
	factory, ok := registry[pluginType]
	if !ok {
		return nil, fmt.Errorf("unknown engine type %q", pluginType)
	}
	p := factory()
	if err := p.Init(config); err != nil {
		return nil, err
	}
	return p, nil
}

// ValidateConfig validates the provided config against the declared fields of the named plugin.
func ValidateConfig(pluginType string, config map[string]interface{}) error {
	factory, ok := registry[pluginType]
	if !ok {
		return fmt.Errorf("unknown engine type %q", pluginType)
	}
	return util.ValidateConfig(factory().ConfigFields(), config)
}

// RegisteredNames returns all registered engine plugin type names.
func RegisteredNames() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

// GetFields returns the config fields for a plugin type without initializing it.
func GetFields(pluginType string) []util.ConfigField {
	factory, ok := registry[pluginType]
	if !ok {
		return nil
	}
	return factory().ConfigFields()
}

// IsRegistered checks if a plugin type is registered.
func IsRegistered(pluginType string) bool {
	_, ok := registry[pluginType]
	return ok
}
