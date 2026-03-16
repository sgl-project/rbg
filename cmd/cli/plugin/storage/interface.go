/*
Copyright 2026.

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

package storage

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/cmd/cli/plugin/util"
)

// ModelInfo contains information about a downloaded model.
type ModelInfo struct {
	// ModelID is the original model identifier (e.g., "organization/model-name")
	ModelID string `json:"modelID"`
	// Revision is the model revision (e.g., "main", "v1.0")
	Revision string `json:"revision"`
	// DownloadedAt is the timestamp when the model was downloaded (optional)
	DownloadedAt string `json:"downloadedAt,omitempty"`
}

// Plugin defines the interface for storage backends.
type Plugin interface {
	Name() string

	// ConfigFields returns the config fields this plugin accepts.
	ConfigFields() []util.ConfigField

	// Init initializes storage with config.
	Init(config map[string]interface{}) error

	// Exists checks if the model already exists in storage.
	Exists(modelID string) (bool, error)

	// PreMount is called before mounting, e.g. to create a PVC/PV.
	PreMount(client client.Client, modelID string, revision string) error

	// MountStorage modifies the PodTemplateSpec to add volumes and mounts.
	// The volume is mounted at the path returned by MountPath().
	MountStorage(podTemplate *corev1.PodTemplateSpec) error

	// MountPath returns the base path where storage is mounted in the container.
	// The full model path is constructed by the caller as: MountPath() + "/" + sanitized(modelID)
	MountPath() string
}

// Factory is a constructor for a storage plugin.
type Factory func() Plugin

var registry = make(map[string]Factory)

// Register registers a storage plugin factory under the given type name.
func Register(name string, factory Factory) {
	registry[name] = factory
}

// Get returns an initialized storage plugin instance for the given type and config.
func Get(pluginType string, config map[string]interface{}) (Plugin, error) {
	factory, ok := registry[pluginType]
	if !ok {
		return nil, fmt.Errorf("unknown storage type %q", pluginType)
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
		return fmt.Errorf("unknown storage type %q", pluginType)
	}
	return util.ValidateConfig(factory().ConfigFields(), config)
}

// RegisteredNames returns all registered storage plugin type names.
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
