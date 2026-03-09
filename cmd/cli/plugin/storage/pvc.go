package storage

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/cmd/cli/plugin/util"
)

func init() {
	Register("pvc", func() Plugin {
		return &PVCStorage{}
	})
}

// PVCStorage implements the StoragePlugin interface for PVC storage
type PVCStorage struct {
	pvcName string
}

// Name returns the plugin name
func (p *PVCStorage) Name() string {
	return "pvc"
}

// ConfigFields returns the config fields this plugin accepts
func (p *PVCStorage) ConfigFields() []util.ConfigField {
	return []util.ConfigField{
		{Key: "pvcName", Description: "name of the pre-existing PersistentVolumeClaim to bind to", Required: true},
	}
}

// Init initializes the plugin with config
// The 'pvcName' field in config specifies the existing PVC to bind to
func (p *PVCStorage) Init(config map[string]interface{}) error {
	if pvcName, ok := config["pvcName"].(string); ok && pvcName != "" {
		p.pvcName = pvcName
	} else {
		return fmt.Errorf("pvcName is required in storage config for pvc type")
	}
	return nil
}

// Exists checks if the model exists in storage
func (p *PVCStorage) Exists(modelID string) (bool, error) {
	// TODO: Implement actual check
	return false, nil
}

// PreMount prepares the storage before mounting (e.g., creating PVC)
func (p *PVCStorage) PreMount(c client.Client, modelID string, revision string) error {
	// PVC should be created separately or already exist
	return nil
}

// MountStorage mounts the storage to the pod template.
func (p *PVCStorage) MountStorage(podTemplate *corev1.PodTemplateSpec) error {
	pvcName := p.pvcName
	mountPath := p.MountPath()

	// Add volume
	volume := corev1.Volume{
		Name: "model-storage",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcName,
			},
		},
	}
	podTemplate.Spec.Volumes = append(podTemplate.Spec.Volumes, volume)

	// Add volume mount to all containers
	volumeMount := corev1.VolumeMount{
		Name:      "model-storage",
		MountPath: mountPath,
	}

	for i := range podTemplate.Spec.Containers {
		podTemplate.Spec.Containers[i].VolumeMounts = append(
			podTemplate.Spec.Containers[i].VolumeMounts,
			volumeMount,
		)
	}

	// Add volume mount to init containers if any
	for i := range podTemplate.Spec.InitContainers {
		podTemplate.Spec.InitContainers[i].VolumeMounts = append(
			podTemplate.Spec.InitContainers[i].VolumeMounts,
			volumeMount,
		)
	}

	return nil
}

// MountPath returns the base mount path for the storage.
func (p *PVCStorage) MountPath() string {
	return "/models"
}
