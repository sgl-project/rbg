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

package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestPVCStorage_Name(t *testing.T) {
	p := &PVCStorage{}
	assert.Equal(t, "pvc", p.Name())
}

func TestPVCStorage_ConfigFields(t *testing.T) {
	p := &PVCStorage{}
	fields := p.ConfigFields()
	require.Len(t, fields, 1)
	assert.Equal(t, "pvcName", fields[0].Key)
	assert.True(t, fields[0].Required)
}

func TestPVCStorage_Init_MissingPVCName(t *testing.T) {
	p := &PVCStorage{}
	err := p.Init(map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pvcName")
}

func TestPVCStorage_Init_EmptyPVCName(t *testing.T) {
	p := &PVCStorage{}
	err := p.Init(map[string]interface{}{"pvcName": ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pvcName")
}

func TestPVCStorage_Init_OK(t *testing.T) {
	p := &PVCStorage{}
	err := p.Init(map[string]interface{}{"pvcName": "my-pvc"})
	require.NoError(t, err)
	assert.Equal(t, "my-pvc", p.pvcName)
}

func TestPVCStorage_MountPath(t *testing.T) {
	p := &PVCStorage{}
	assert.Equal(t, "/models", p.MountPath())
}

func TestPVCStorage_Exists(t *testing.T) {
	p := &PVCStorage{pvcName: "my-pvc"}
	exists, err := p.Exists("any-model")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestPVCStorage_PreMount(t *testing.T) {
	p := &PVCStorage{pvcName: "my-pvc"}
	err := p.PreMount(nil, "org/model", "main")
	assert.NoError(t, err)
}

func TestPVCStorage_ListModels_Unsupported(t *testing.T) {
	p := &PVCStorage{pvcName: "my-pvc"}
	models, err := p.ListModels()
	require.Error(t, err)
	assert.Nil(t, models)
	assert.Contains(t, err.Error(), "not supported")
}

func TestPVCStorage_MountStorage_AddsVolumeAndMount(t *testing.T) {
	p := &PVCStorage{pvcName: "my-pvc"}

	tpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "main"},
			},
		},
	}

	err := p.MountStorage(tpl)
	require.NoError(t, err)

	require.Len(t, tpl.Spec.Volumes, 1)
	vol := tpl.Spec.Volumes[0]
	assert.Equal(t, "model-storage", vol.Name)
	require.NotNil(t, vol.VolumeSource.PersistentVolumeClaim)
	assert.Equal(t, "my-pvc", vol.VolumeSource.PersistentVolumeClaim.ClaimName)

	require.Len(t, tpl.Spec.Containers[0].VolumeMounts, 1)
	vm := tpl.Spec.Containers[0].VolumeMounts[0]
	assert.Equal(t, "model-storage", vm.Name)
	assert.Equal(t, "/models", vm.MountPath)
}

func TestPVCStorage_MountStorage_MultipleContainers(t *testing.T) {
	p := &PVCStorage{pvcName: "my-pvc"}

	tpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "c1"},
				{Name: "c2"},
			},
		},
	}

	require.NoError(t, p.MountStorage(tpl))
	for _, c := range tpl.Spec.Containers {
		require.Len(t, c.VolumeMounts, 1)
		assert.Equal(t, "model-storage", c.VolumeMounts[0].Name)
	}
}

func TestPVCStorage_MountStorage_InitContainers(t *testing.T) {
	p := &PVCStorage{pvcName: "my-pvc"}

	tpl := &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{Name: "init"},
			},
			Containers: []corev1.Container{
				{Name: "main"},
			},
		},
	}

	require.NoError(t, p.MountStorage(tpl))
	require.Len(t, tpl.Spec.InitContainers[0].VolumeMounts, 1)
	assert.Equal(t, "/models", tpl.Spec.InitContainers[0].VolumeMounts[0].MountPath)
}

func TestGet_PVC_RequiresPVCName(t *testing.T) {
	_, err := Get("pvc", map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pvcName")
}

func TestGet_PVC_OK(t *testing.T) {
	p, err := Get("pvc", map[string]interface{}{"pvcName": "test-pvc"})
	require.NoError(t, err)
	assert.Equal(t, "pvc", p.Name())
}

func TestValidateConfig_PVC_MissingRequired(t *testing.T) {
	err := ValidateConfig("pvc", map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pvcName")
}

func TestValidateConfig_PVC_OK(t *testing.T) {
	err := ValidateConfig("pvc", map[string]interface{}{"pvcName": "my-pvc"})
	assert.NoError(t, err)
}

func TestValidateConfig_PVC_UnknownField(t *testing.T) {
	err := ValidateConfig("pvc", map[string]interface{}{"pvcName": "my-pvc", "bad": "x"})
	assert.Error(t, err)
}

func TestGetFields_PVC(t *testing.T) {
	fields := GetFields("pvc")
	require.NotNil(t, fields)
	assert.Len(t, fields, 1)
}
