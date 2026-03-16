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

package metadata

import (
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

// Label and annotation keys for resources created by the run command
const (
	// RunCommandSourceLabelKey identifies resources created by the run command
	RunCommandSourceLabelKey = constants.RBGPrefix + "source"
	// RunCommandMetadataAnnotationKey stores CLI metadata as JSON
	RunCommandMetadataAnnotationKey = constants.RBGPrefix + "cli-metadata"
	// RunCommandSourceLabelValue is the value of the source label for CLI-created resources
	RunCommandSourceLabelValue = "rbgcli"
)

// RunMetadata stores CLI metadata attached to RBG resources created by the run command
type RunMetadata struct {
	ModelID  string `json:"modelID"`
	Engine   string `json:"engine"`
	Mode     string `json:"mode"`
	Revision string `json:"revision"`
	Port     int32  `json:"port"`
}
