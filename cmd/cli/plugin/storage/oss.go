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
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/cmd/cli/plugin/util"
)

const ossStorageType = "oss"

func init() {
	Register(ossStorageType, func() Plugin {
		return &OSSStorage{}
	})
}

// OSSStorage implements the StoragePlugin interface for Alibaba Cloud OSS storage
type OSSStorage struct {
	// storageName is set during PreMount, used for PVC name in MountStorage
	storageName string
	// config fields
	storageSize string
	url         string
	bucket      string
	subpath     string
	akId        string
	akSecret    string
}

// Name returns the plugin name
func (o *OSSStorage) Name() string {
	return ossStorageType
}

// ConfigFields returns the config fields this plugin accepts
func (o *OSSStorage) ConfigFields() []util.ConfigField {
	return []util.ConfigField{
		{Key: "storageSize", Description: "storage size for the PV (e.g., 100Gi)", Required: true},
		{Key: "url", Description: "OSS endpoint URL (e.g., oss-cn-hangzhou.aliyuncs.com)", Required: true},
		{Key: "bucket", Description: "OSS bucket name", Required: true},
		{Key: "subpath", Description: "subpath within the bucket", Required: false},
		{Key: "akId", Description: "Alibaba Cloud AccessKey ID", Required: true},
		{Key: "akSecret", Description: "Alibaba Cloud AccessKey Secret", Required: true, Masked: util.MaskAll},
	}
}

// Init initializes the plugin with config
func (o *OSSStorage) Init(config map[string]interface{}) error {
	var ok bool

	if o.storageSize, ok = config["storageSize"].(string); !ok || o.storageSize == "" {
		return fmt.Errorf("storageSize is required in storage config for oss type")
	}
	if o.url, ok = config["url"].(string); !ok || o.url == "" {
		return fmt.Errorf("url is required in storage config for oss type")
	}
	if o.bucket, ok = config["bucket"].(string); !ok || o.bucket == "" {
		return fmt.Errorf("bucket is required in storage config for oss type")
	}
	// subpath is optional
	o.subpath, _ = config["subpath"].(string)
	if o.subpath == "" {
		o.subpath = "/"
	}

	if o.akId, ok = config["akId"].(string); !ok || o.akId == "" {
		return fmt.Errorf("akId is required in storage config for oss type")
	}
	if o.akSecret, ok = config["akSecret"].(string); !ok || o.akSecret == "" {
		return fmt.Errorf("akSecret is required in storage config for oss type")
	}

	return nil
}

// Exists checks if the model exists in storage
func (o *OSSStorage) Exists(modelID string) (bool, error) {
	// TODO: Implement actual check via OSS API
	return false, nil
}

// PreMount creates Secret, PV and PVC resources if they don't exist
func (o *OSSStorage) PreMount(c client.Client, opts PreMountOptions) error {
	// Store storageName for use in MountStorage
	o.storageName = opts.StorageName

	ctx := context.Background()
	secretName := o.secretName(opts.StorageName)
	pvName := opts.StorageName
	pvcName := opts.StorageName

	// Step 1: Create or verify Secret
	if err := o.createOrVerifySecret(ctx, c, secretName, opts.Namespace); err != nil {
		return fmt.Errorf("failed to create/verify secret: %w", err)
	}

	// Step 2: Create or verify PV
	if err := o.createOrVerifyPV(ctx, c, pvName, secretName, opts.Namespace); err != nil {
		return fmt.Errorf("failed to create/verify PV: %w", err)
	}

	// Step 3: Create or verify PVC
	if err := o.createOrVerifyPVC(ctx, c, pvcName, pvName, opts.Namespace); err != nil {
		return fmt.Errorf("failed to create/verify PVC: %w", err)
	}

	return nil
}

// secretName returns the name of the secret
func (o *OSSStorage) secretName(storageName string) string {
	return storageName + "-oss-secret"
}

// createOrVerifySecret creates the secret or verifies it if already exists
func (o *OSSStorage) createOrVerifySecret(ctx context.Context, c client.Client, secretName, namespace string) error {
	secret := &corev1.Secret{}
	err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret)
	if err == nil {
		// Secret exists, verify it
		return o.verifySecret(secret)
	}
	if !errors.IsNotFound(err) {
		return err
	}

	// Create new secret
	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"akId":     []byte(o.akId),
			"akSecret": []byte(o.akSecret),
		},
	}
	return c.Create(ctx, newSecret)
}

// verifySecret verifies that the existing secret has correct akId and akSecret
func (o *OSSStorage) verifySecret(secret *corev1.Secret) error {
	akIdBytes, ok := secret.Data["akId"]
	if !ok {
		return fmt.Errorf("secret %s already exists but missing akId field", secret.Name)
	}
	akSecretBytes, ok := secret.Data["akSecret"]
	if !ok {
		return fmt.Errorf("secret %s already exists but missing akSecret field", secret.Name)
	}

	// Compare directly (Data field stores base64-encoded values, but client-go handles encoding)
	if string(akIdBytes) != o.akId {
		return fmt.Errorf("secret %s already exists with different akId", secret.Name)
	}
	if string(akSecretBytes) != o.akSecret {
		return fmt.Errorf("secret %s already exists with different akSecret", secret.Name)
	}

	return nil
}

// createOrVerifyPV creates the PV or verifies it if already exists
func (o *OSSStorage) createOrVerifyPV(ctx context.Context, c client.Client, pvName, secretName, namespace string) error {
	pv := &corev1.PersistentVolume{}
	err := c.Get(ctx, types.NamespacedName{Name: pvName}, pv)
	if err == nil {
		// PV exists, verify it
		return o.verifyPV(pv, secretName, namespace)
	}
	if !errors.IsNotFound(err) {
		return err
	}

	// Create new PV
	storageQuantity, err := resource.ParseQuantity(o.storageSize)
	if err != nil {
		return fmt.Errorf("invalid storageSize %q: %w", o.storageSize, err)
	}

	newPV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvName,
			Labels: map[string]string{
				"alicloud-pvname": pvName,
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: storageQuantity,
			},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "ossplugin.csi.alibabacloud.com",
					VolumeHandle: pvName,
					NodePublishSecretRef: &corev1.SecretReference{
						Name:      secretName,
						Namespace: namespace,
					},
					VolumeAttributes: map[string]string{
						"bucket":    o.bucket,
						"otherOpts": "",
						"path":      o.subpath,
						"url":       o.url,
					},
				},
			},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              ossStorageType,
			VolumeMode:                    func() *corev1.PersistentVolumeMode { v := corev1.PersistentVolumeFilesystem; return &v }(),
		},
	}
	return c.Create(ctx, newPV)
}

// verifyPV verifies that the existing PV has correct configuration
func (o *OSSStorage) verifyPV(pv *corev1.PersistentVolume, secretName, namespace string) error {
	if pv.Spec.CSI == nil {
		return fmt.Errorf("PV %s already exists but is not a CSI volume", pv.Name)
	}
	if pv.Spec.CSI.Driver != "ossplugin.csi.alibabacloud.com" {
		return fmt.Errorf("PV %s already exists but uses different CSI driver", pv.Name)
	}
	if pv.Spec.CSI.NodePublishSecretRef == nil {
		return fmt.Errorf("PV %s already exists but has no NodePublishSecretRef", pv.Name)
	}
	if pv.Spec.CSI.NodePublishSecretRef.Name != secretName {
		return fmt.Errorf("PV %s already exists but references different secret %q (expected %q)",
			pv.Name, pv.Spec.CSI.NodePublishSecretRef.Name, secretName)
	}
	if pv.Spec.CSI.NodePublishSecretRef.Namespace != namespace {
		return fmt.Errorf("PV %s already exists but references secret in different namespace %q (expected %q)",
			pv.Name, pv.Spec.CSI.NodePublishSecretRef.Namespace, namespace)
	}
	return nil
}

// createOrVerifyPVC creates the PVC or verifies it if already exists
func (o *OSSStorage) createOrVerifyPVC(ctx context.Context, c client.Client, pvcName, pvName, namespace string) error {
	pvc := &corev1.PersistentVolumeClaim{}
	err := c.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc)
	if err == nil {
		// PVC exists, verify it
		return o.verifyPVC(pvc, pvName)
	}
	if !errors.IsNotFound(err) {
		return err
	}

	// Create new PVC
	storageQuantity, err := resource.ParseQuantity(o.storageSize)
	if err != nil {
		return fmt.Errorf("invalid storageSize %q: %w", o.storageSize, err)
	}

	newPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageQuantity,
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"alicloud-pvname": pvName,
				},
			},
			StorageClassName: func() *string { s := ossStorageType; return &s }(),
			VolumeMode:       func() *corev1.PersistentVolumeMode { v := corev1.PersistentVolumeFilesystem; return &v }(),
			VolumeName:       pvName,
		},
	}
	return c.Create(ctx, newPVC)
}

// verifyPVC verifies that the existing PVC has correct configuration
func (o *OSSStorage) verifyPVC(pvc *corev1.PersistentVolumeClaim, pvName string) error {
	if pvc.Spec.VolumeName != pvName {
		return fmt.Errorf("PVC %s already exists but references different PV %q (expected %q)",
			pvc.Name, pvc.Spec.VolumeName, pvName)
	}
	return nil
}

// MountStorage mounts the storage to the pod template
func (o *OSSStorage) MountStorage(podTemplate *corev1.PodTemplateSpec) error {
	pvcName := o.storageName
	mountPath := o.MountPath()

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

// MountPath returns the base mount path for the storage
func (o *OSSStorage) MountPath() string {
	return "/models"
}
