/*
Copyright 2025.

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

package llm

import (
	"fmt"

	"github.com/spf13/cobra"
	"sigs.k8s.io/rbgs/cmd/cli/config"
	sourceplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/source"
	storageplugin "sigs.k8s.io/rbgs/cmd/cli/plugin/storage"
)

func newPullCmd() *cobra.Command {
	var (
		revision string
		source   string
		storage  string
	)

	cmd := &cobra.Command{
		Use:   "pull MODEL_ID",
		Short: "Pull a model from source to storage",
		Long:  `Download a model from the configured source to the configured storage`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			modelID := args[0]

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			// Determine source
			sourceName := cfg.CurrentSource
			if source != "" {
				sourceName = source
			}
			if sourceName == "" {
				return fmt.Errorf("no source configured, please run 'kubectl rbg llm config add-source' first")
			}

			sourceCfg, err := cfg.GetSource(sourceName)
			if err != nil {
				return err
			}

			// Determine storage
			storageName := cfg.CurrentStorage
			if storage != "" {
				storageName = storage
			}
			if storageName == "" {
				return fmt.Errorf("no storage configured, please run 'kubectl rbg llm config add-storage' first")
			}

			storageCfg, err := cfg.GetStorage(storageName)
			if err != nil {
				return err
			}

			// Initialize plugins
			sourcePlugin, err := sourceplugin.Get(sourceCfg.Type, sourceCfg.Config)
			if err != nil {
				return fmt.Errorf("failed to initialize source plugin: %w", err)
			}
			if sourcePlugin == nil {
				return fmt.Errorf("unknown source type: %s", sourceCfg.Type)
			}

			storagePlugin, err := storageplugin.Get(storageCfg.Type, storageCfg.Config)
			if err != nil {
				return fmt.Errorf("failed to initialize storage plugin: %w", err)
			}
			if storagePlugin == nil {
				return fmt.Errorf("unknown storage type: %s", storageCfg.Type)
			}

			// Get mount path and construct model path
			mountPath := storagePlugin.MountPath()
			modelPath := mountPath + "/" + sanitizeModelID(modelID)

			// Generate download template
			podTemplate, err := sourcePlugin.GenerateTemplate(modelID, modelPath)
			if err != nil {
				return fmt.Errorf("failed to generate download template: %w", err)
			}

			// Mount storage
			if err := storagePlugin.MountStorage(podTemplate); err != nil {
				return fmt.Errorf("failed to mount storage: %w", err)
			}

			// Print the generated template
			fmt.Println("# Generated Pod Template for Model Download")
			fmt.Printf("# Model: %s\n", modelID)
			fmt.Printf("# Source: %s\n", sourceName)
			fmt.Printf("# Storage: %s\n", storageName)
			fmt.Printf("# Revision: %s\n", revision)
			fmt.Println("#")
			fmt.Println("# TODO: Create Job from this template to actually download the model")
			fmt.Println()

			return printPodTemplate("download-"+sanitizeModelID(modelID), podTemplate)
		},
	}

	cmd.Flags().StringVar(&revision, "revision", "main", "Model revision to download")
	cmd.Flags().StringVar(&source, "source", "", "Source to use (overrides default)")
	cmd.Flags().StringVar(&storage, "storage", "", "Storage to use (overrides default)")

	return cmd
}
