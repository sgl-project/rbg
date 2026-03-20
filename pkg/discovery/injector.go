/*
Copyright 2026 The RBG Authors.

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

package discovery

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type GroupInfoInjector interface {
	InjectConfig(
		context context.Context, podSpec *corev1.PodTemplateSpec, rbg *workloadsv1alpha2.RoleBasedGroup,
		role *workloadsv1alpha2.RoleSpec,
	) error
	InjectEnv(
		context context.Context, podSpec *corev1.PodTemplateSpec, rbg *workloadsv1alpha2.RoleBasedGroup,
		role *workloadsv1alpha2.RoleSpec,
	) error
	InjectSidecar(
		context context.Context, podSpec *corev1.PodTemplateSpec, rbg *workloadsv1alpha2.RoleBasedGroup,
		role *workloadsv1alpha2.RoleSpec,
	) error
}

type DefaultInjector struct {
	scheme *runtime.Scheme
	client client.Client
}

var _ GroupInfoInjector = &DefaultInjector{}

func NewDefaultInjector(scheme *runtime.Scheme, client client.Client) *DefaultInjector {
	return &DefaultInjector{
		client: client,
		scheme: scheme,
	}
}

func (i *DefaultInjector) InjectConfig(
	ctx context.Context, podSpec *corev1.PodTemplateSpec, rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
) error {
	const (
		volumeName = "rbg-cluster-config"
		mountPath  = "/etc/rbg"
		configKey  = "config.yaml"
	)

	// Only stateful roles have a discovery ConfigMap.
	// The controller (reconcileDiscoveryConfigMap) always creates a single RBG-level
	// ConfigMap named after the RBG itself, regardless of discovery config mode.
	if !workloadsv1alpha2.IsStatefulRole(role) {
		return nil
	}
	configMapName := rbg.Name

	volumeExists := false
	for _, vol := range podSpec.Spec.Volumes {
		if vol.Name == volumeName {
			volumeExists = true
			break
		}
	}
	if !volumeExists {
		podSpec.Spec.Volumes = append(
			podSpec.Spec.Volumes, corev1.Volume{
				Name: volumeName,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: configMapName,
						},
						Items: []corev1.KeyToPath{
							{Key: configKey, Path: configKey},
						},
					},
				},
			},
		)
	}

	for i := range podSpec.Spec.Containers {
		container := &podSpec.Spec.Containers[i]
		mountExists := false
		for _, vm := range container.VolumeMounts {
			if vm.Name == volumeName && vm.MountPath == mountPath {
				mountExists = true
				break
			}
		}
		if !mountExists {
			container.VolumeMounts = append(
				container.VolumeMounts, corev1.VolumeMount{
					Name:      volumeName,
					MountPath: mountPath,
					ReadOnly:  true,
				},
			)
		}
	}
	return nil
}

func (i *DefaultInjector) InjectEnv(
	ctx context.Context, podSpec *corev1.PodTemplateSpec, rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
) error {
	builder := &EnvBuilder{
		rbg:  rbg,
		role: role,
	}

	envVars := builder.Build()

	for idx := range podSpec.Spec.Containers {
		container := &podSpec.Spec.Containers[idx]
		container.Env = mergeEnvVars(container.Env, envVars)
	}
	return nil
}

func (i *DefaultInjector) InjectLeaderWorkerSetEnv(ctx context.Context,
	podSpec *corev1.PodTemplateSpec,
	rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec) error {

	builder := &EnvBuilder{
		rbg:  rbg,
		role: role,
	}
	svcName, err := utils.GetCompatibleHeadlessServiceName(ctx, i.client, rbg, role)
	if err != nil {
		return err
	}

	envVars := builder.BuildLwsEnv(svcName)
	for idx := range podSpec.Spec.Containers {
		container := &podSpec.Spec.Containers[idx]
		container.Env = mergeEnvVars(container.Env, envVars)
	}

	return nil
}

func (i *DefaultInjector) InjectSidecar(
	ctx context.Context, podSpec *corev1.PodTemplateSpec,
	rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec,
) error {
	builder := NewSidecarBuilder(i.client, rbg, role)
	return builder.Build(ctx, podSpec)
}

func mergeEnvVars(existing, injected []corev1.EnvVar) []corev1.EnvVar {
	existing = dedupeEnvVarsPreserveLast(existing)
	injected = dedupeEnvVarsPreserveLast(injected)

	injectedNames := make(map[string]struct{}, len(injected))
	for _, env := range injected {
		injectedNames[env.Name] = struct{}{}
	}

	merged := make([]corev1.EnvVar, 0, len(existing)+len(injected))
	for _, env := range existing {
		if _, ok := injectedNames[env.Name]; ok {
			continue
		}
		merged = append(merged, env)
	}

	return append(merged, injected...)
}

func dedupeEnvVarsPreserveLast(envs []corev1.EnvVar) []corev1.EnvVar {
	if len(envs) < 2 {
		return append([]corev1.EnvVar(nil), envs...)
	}

	seen := make(map[string]struct{}, len(envs))
	deduped := make([]corev1.EnvVar, 0, len(envs))
	for i := len(envs) - 1; i >= 0; i-- {
		if _, ok := seen[envs[i].Name]; ok {
			continue
		}
		seen[envs[i].Name] = struct{}{}
		deduped = append(deduped, envs[i])
	}

	for left, right := 0, len(deduped)-1; left < right; left, right = left+1, right-1 {
		deduped[left], deduped[right] = deduped[right], deduped[left]
	}

	return deduped
}
