package benchmark

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

const (
	// pvcStoragePrefix is the prefix for PVC storage URIs.
	pvcStoragePrefix = "pvc://"
	// tokenizerVolumeName is the name of the volume for PVC-based tokenizer.
	tokenizerVolumeName = "tokenizer-volume"
	// tokenizerMountPath is the mount path for tokenizer inside the container.
	tokenizerMountPath = "/tokenizer"
	// outputVolumeName is the name of the volume for PVC-based output storage.
	outputVolumeName = "output-volume"
	// outputMountPath is the mount path for experiment output inside the container.
	outputMountPath = "/output"
)

// PVCComponents holds the parsed components of a PVC storage URI.
type PVCComponents struct {
	PVCName string
	SubPath string
}

// isPVCStorageURI checks if the given URI is a PVC storage URI.
func isPVCStorageURI(uri string) bool {
	return strings.HasPrefix(uri, pvcStoragePrefix)
}

// parsePVCStorageURI parses a PVC storage URI into its components.
// Supported formats:
//   - pvc://{pvc-name}/{sub-path}
//   - pvc://{pvc-name}/
//
// The namespace for the PVC is determined by the current kubeconfig context,
// not specified in the URI.
func parsePVCStorageURI(uri string) (*PVCComponents, error) {
	if !strings.HasPrefix(uri, pvcStoragePrefix) {
		return nil, fmt.Errorf("invalid PVC storage URI format: missing %s prefix", pvcStoragePrefix)
	}

	// Remove prefix
	path := strings.TrimPrefix(uri, pvcStoragePrefix)
	if path == "" {
		return nil, fmt.Errorf("invalid PVC storage URI format: missing content after prefix")
	}

	// Find the first slash to separate pvc-name from sub-path
	firstSlashIdx := strings.Index(path, "/")
	if firstSlashIdx == -1 {
		return nil, fmt.Errorf("invalid PVC storage URI format: missing trailing slash")
	}

	pvcName := path[:firstSlashIdx]
	subPath := path[firstSlashIdx+1:]

	if pvcName == "" {
		return nil, fmt.Errorf("invalid PVC storage URI format: missing PVC name")
	}

	return &PVCComponents{
		PVCName: pvcName,
		SubPath: subPath,
	}, nil
}

// buildTokenizerConfig builds the tokenizer configuration for the benchmark job.
// If the tokenizer is a PVC URI, it returns the mount path and the corresponding volume/mount configs.
// Otherwise, it returns the tokenizer value directly (e.g., Huggingface model name).
func buildTokenizerConfig(tokenizer string) (string, []corev1.Volume, []corev1.VolumeMount, error) {
	if !isPVCStorageURI(tokenizer) {
		// Not a PVC URI, return the tokenizer value directly
		return tokenizer, nil, nil, nil
	}

	pvcComponents, err := parsePVCStorageURI(tokenizer)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to parse PVC storage URI: %w", err)
	}

	volumes := []corev1.Volume{
		{
			Name: tokenizerVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcComponents.PVCName,
				},
			},
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      tokenizerVolumeName,
			MountPath: tokenizerMountPath,
			ReadOnly:  true,
		},
	}

	// Only set SubPath if it's not empty
	if pvcComponents.SubPath != "" {
		volumeMounts[0].SubPath = pvcComponents.SubPath
	}

	return tokenizerMountPath, volumes, volumeMounts, nil
}

// buildOutputConfig builds the output directory configuration for the benchmark job.
// The output directory must be a PVC URI.
func buildOutputConfig(outputDir string) (corev1.Volume, corev1.VolumeMount, error) {
	pvcComponents, err := parsePVCStorageURI(outputDir)
	if err != nil {
		return corev1.Volume{}, corev1.VolumeMount{}, fmt.Errorf("failed to parse output PVC storage URI: %w", err)
	}

	volume := corev1.Volume{
		Name: outputVolumeName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcComponents.PVCName,
			},
		},
	}

	volumeMount := corev1.VolumeMount{
		Name:      outputVolumeName,
		MountPath: outputMountPath,
	}

	// Only set SubPath if it's not empty
	if pvcComponents.SubPath != "" {
		volumeMount.SubPath = pvcComponents.SubPath
	}

	return volume, volumeMount, nil
}
