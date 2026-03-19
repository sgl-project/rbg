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

package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	DefaultConfigDir  = ".rbg"
	DefaultConfigFile = "config"
	EnvConfigPath     = "RBG_CONFIG"
)

// Config represents the CLI configuration
type Config struct {
	APIVersion     string          `yaml:"apiVersion"`
	Kind           string          `yaml:"kind"`
	Storages       []StorageConfig `yaml:"storages,omitempty"`
	Sources        []SourceConfig  `yaml:"sources,omitempty"`
	Engines        []EngineConfig  `yaml:"engines,omitempty"`
	CurrentStorage string          `yaml:"current-storage,omitempty"`
	CurrentSource  string          `yaml:"current-source,omitempty"`
}

// StorageConfig represents a storage configuration
type StorageConfig struct {
	Name   string                 `yaml:"name"`
	Type   string                 `yaml:"type"`
	Config map[string]interface{} `yaml:"config,omitempty"`
}

// SourceConfig represents a source configuration
type SourceConfig struct {
	Name   string                 `yaml:"name"`
	Type   string                 `yaml:"type"`
	Config map[string]interface{} `yaml:"config,omitempty"`
}

// EngineConfig represents an engine configuration
type EngineConfig struct {
	Type   string                 `yaml:"type"`
	Config map[string]interface{} `yaml:"config,omitempty"`
}

var instance *Config

// GetConfigPath returns the path to the config file
func GetConfigPath() string {
	if envPath := os.Getenv(EnvConfigPath); envPath != "" {
		return envPath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, DefaultConfigDir, DefaultConfigFile)
}

// Load loads the configuration from file
func Load() (*Config, error) {
	if instance == nil {
		instance = &Config{
			APIVersion: "rbg/v1alpha1",
			Kind:       "Config",
		}
		if err := instance.loadFromFile(); err != nil {
			return nil, err
		}
	}
	return instance, nil
}

func (c *Config) loadFromFile() error {
	configPath := GetConfigPath()
	if configPath == "" {
		return nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, c); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	return nil
}

// Save saves the configuration to file
func (c *Config) Save() error {
	configPath := GetConfigPath()
	if configPath == "" {
		return fmt.Errorf("unable to determine config path")
	}

	// Ensure directory exists
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// AddStorage adds a new storage configuration.
// If this is the only storage after adding, it is automatically set as current.
func (c *Config) AddStorage(name, storageType string, cfg map[string]interface{}) error {
	for _, s := range c.Storages {
		if s.Name == name {
			return fmt.Errorf("storage '%s' already exists", name)
		}
	}
	c.Storages = append(c.Storages, StorageConfig{
		Name:   name,
		Type:   storageType,
		Config: cfg,
	})
	if len(c.Storages) == 1 {
		c.CurrentStorage = name
	}
	return nil
}

// GetStorage returns a storage configuration by name
func (c *Config) GetStorage(name string) (*StorageConfig, error) {
	for i := range c.Storages {
		if c.Storages[i].Name == name {
			return &c.Storages[i], nil
		}
	}
	return nil, fmt.Errorf("storage '%s' not found", name)
}

// UpdateStorage updates an existing storage configuration
func (c *Config) UpdateStorage(name string, cfg map[string]interface{}) error {
	for i := range c.Storages {
		if c.Storages[i].Name == name {
			for k, v := range cfg {
				c.Storages[i].Config[k] = v
			}
			return nil
		}
	}
	return fmt.Errorf("storage '%s' not found", name)
}

// DeleteStorage deletes a storage configuration
func (c *Config) DeleteStorage(name string) error {
	if c.CurrentStorage == name {
		return fmt.Errorf("cannot delete current storage '%s', switch to another storage first", name)
	}
	for i, s := range c.Storages {
		if s.Name == name {
			c.Storages = append(c.Storages[:i], c.Storages[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("storage '%s' not found", name)
}

// UseStorage sets the current storage
func (c *Config) UseStorage(name string) error {
	_, err := c.GetStorage(name)
	if err != nil {
		return err
	}
	c.CurrentStorage = name
	return nil
}

// AddSource adds a new source configuration.
// If this is the only source after adding, it is automatically set as current.
func (c *Config) AddSource(name, sourceType string, cfg map[string]interface{}) error {
	for _, s := range c.Sources {
		if s.Name == name {
			return fmt.Errorf("source '%s' already exists", name)
		}
	}
	c.Sources = append(c.Sources, SourceConfig{
		Name:   name,
		Type:   sourceType,
		Config: cfg,
	})
	if len(c.Sources) == 1 {
		c.CurrentSource = name
	}
	return nil
}

// GetSource returns a source configuration by name
func (c *Config) GetSource(name string) (*SourceConfig, error) {
	for i := range c.Sources {
		if c.Sources[i].Name == name {
			return &c.Sources[i], nil
		}
	}
	return nil, fmt.Errorf("source '%s' not found", name)
}

// UpdateSource updates an existing source configuration
func (c *Config) UpdateSource(name string, cfg map[string]interface{}) error {
	for i := range c.Sources {
		if c.Sources[i].Name == name {
			for k, v := range cfg {
				c.Sources[i].Config[k] = v
			}
			return nil
		}
	}
	return fmt.Errorf("source '%s' not found", name)
}

// DeleteSource deletes a source configuration
func (c *Config) DeleteSource(name string) error {
	if c.CurrentSource == name {
		return fmt.Errorf("cannot delete current source '%s', switch to another source first", name)
	}
	for i, s := range c.Sources {
		if s.Name == name {
			c.Sources = append(c.Sources[:i], c.Sources[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("source '%s' not found", name)
}

// UseSource sets the current source
func (c *Config) UseSource(name string) error {
	_, err := c.GetSource(name)
	if err != nil {
		return err
	}
	c.CurrentSource = name
	return nil
}

// SetEngine sets the engine configuration for the given type (create or update).
// Each engine type can only be configured once; calling SetEngine on an existing
// type replaces its configuration.
func (c *Config) SetEngine(engineType string, cfg map[string]interface{}) {
	for i := range c.Engines {
		if c.Engines[i].Type == engineType {
			c.Engines[i].Config = cfg
			return
		}
	}
	c.Engines = append(c.Engines, EngineConfig{
		Type:   engineType,
		Config: cfg,
	})
}

// GetEngine returns an engine configuration by type
func (c *Config) GetEngine(engineType string) (*EngineConfig, error) {
	for i := range c.Engines {
		if c.Engines[i].Type == engineType {
			return &c.Engines[i], nil
		}
	}
	return nil, fmt.Errorf("engine type '%s' not found", engineType)
}

// DeleteEngine deletes an engine configuration
func (c *Config) DeleteEngine(engineType string) error {
	for i, e := range c.Engines {
		if e.Type == engineType {
			c.Engines = append(c.Engines[:i], c.Engines[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("engine type '%s' not found", engineType)
}

// GetCurrentStorageConfig returns the current storage configuration
func (c *Config) GetCurrentStorageConfig() (map[string]interface{}, error) {
	if c.CurrentStorage == "" {
		return nil, fmt.Errorf("no storage configured")
	}
	s, err := c.GetStorage(c.CurrentStorage)
	if err != nil {
		return nil, err
	}
	return s.Config, nil
}

// GetCurrentSourceConfig returns the current source configuration
func (c *Config) GetCurrentSourceConfig() (map[string]interface{}, error) {
	if c.CurrentSource == "" {
		return nil, fmt.Errorf("no source configured")
	}
	s, err := c.GetSource(c.CurrentSource)
	if err != nil {
		return nil, err
	}
	return s.Config, nil
}
