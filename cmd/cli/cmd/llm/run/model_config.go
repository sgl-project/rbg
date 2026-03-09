package run

import (
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

// ModelConfig describes a model and its available run modes.
type ModelConfig struct {
	ID    string       `yaml:"id"`
	Name  string       `yaml:"name"`
	Modes []ModeConfig `yaml:"modes"`
}

// ModeConfig describes a single run mode for a model.
type ModeConfig struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Engine      string         `yaml:"engine"`
	Image       string         `yaml:"image"`
	Resources   ResourceConfig `yaml:"resources"`
	Args        []string       `yaml:"args"`
	Env         []EnvVar       `yaml:"env"`
}

// ResourceConfig describes compute resources for a mode.
type ResourceConfig struct {
	GPU    int    `yaml:"gpu"`
	CPU    int    `yaml:"cpu"`
	Memory string `yaml:"memory"`
}

// EnvVar describes an environment variable.
type EnvVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

// LoadBuiltinModels loads the embedded model configurations.
// If the environment variable RBG_MODELS_CONFIG is set, it loads from
// that file instead (useful for local debugging/testing).
func LoadBuiltinModels() ([]ModelConfig, error) {
	data := embeddedModelsYAML
	if overrideFile := os.Getenv("RBG_MODELS_CONFIG"); overrideFile != "" {
		var err error
		data, err = os.ReadFile(overrideFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read RBG_MODELS_CONFIG %q: %w", overrideFile, err)
		}
	}
	var configs []ModelConfig
	if err := yaml.Unmarshal(data, &configs); err != nil {
		return nil, fmt.Errorf("failed to parse model configs: %w", err)
	}
	return configs, nil
}

// FindModelConfig finds the best matching ModelConfig for modelID using:
//  1. Exact match
//  2. Wildcard match (e.g. "Qwen/*")
//  3. Default config ("*")
func FindModelConfig(models []ModelConfig, modelID string) (*ModelConfig, error) {
	var wildcardMatch *ModelConfig
	var defaultMatch *ModelConfig

	for i := range models {
		mc := &models[i]
		if mc.ID == modelID {
			return mc, nil
		}
		if mc.ID == "*" {
			defaultMatch = mc
			continue
		}
		if matched, _ := filepath.Match(mc.ID, modelID); matched {
			if wildcardMatch == nil {
				wildcardMatch = mc
			}
		}
	}

	if wildcardMatch != nil {
		return wildcardMatch, nil
	}
	if defaultMatch != nil {
		return defaultMatch, nil
	}

	return nil, fmt.Errorf("no configuration found for model %q", modelID)
}

// FindModeConfig finds a named mode within a ModelConfig.
// If mode is empty, the first mode in the list is used.
func FindModeConfig(mc *ModelConfig, mode string) (*ModeConfig, error) {
	if len(mc.Modes) == 0 {
		return nil, fmt.Errorf("no modes defined for model %q", mc.ID)
	}
	if mode == "" {
		return &mc.Modes[0], nil
	}

	var modeNames []string
	for i := range mc.Modes {
		m := &mc.Modes[i]
		if m.Name == mode {
			return m, nil
		}
		modeNames = append(modeNames, m.Name)
	}

	return nil, fmt.Errorf("mode %q not found for model %q, available modes: %v", mode, mc.ID, modeNames)
}
