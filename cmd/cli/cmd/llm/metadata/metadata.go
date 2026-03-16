package metadata

import (
	"sigs.k8s.io/rbgs/pkg/constants"
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
