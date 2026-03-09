package discovery

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/constants"
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

	var configMapName string
	mode := rbg.GetDiscoveryConfigMode()
	switch mode {
	case constants.RefineDiscoveryConfigMode:
		if !workloadsv1alpha2.IsStatefulRole(role) {
			return nil
		}
		configMapName = rbg.Name
	default:
		// legacy and unknown modes keep role-level mount for backward compatibility.
		configMapName = rbg.GetWorkloadName(role)
	}

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
		// 1. Convert env to Map to remove duplicates
		existingEnv := make(map[string]corev1.EnvVar)
		for _, e := range container.Env {
			existingEnv[e.Name] = e
		}
		for _, newEnv := range envVars {
			existingEnv[newEnv.Name] = newEnv // Overwrite env.Value if the name exists
		}
		// 2. Convert back to slice
		mergedEnv := make([]corev1.EnvVar, 0, len(existingEnv))
		for _, env := range existingEnv {
			mergedEnv = append(mergedEnv, env)
		}
		// Avoid sts updates caused by env order changes
		sort.Slice(
			mergedEnv, func(i, j int) bool {
				return mergedEnv[i].Name < mergedEnv[j].Name
			},
		)
		container.Env = mergedEnv
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
	svcName, err := utils.GetCompatibleHeadlessServiceNameV2(ctx, i.client, rbg, role)
	if err != nil {
		return err
	}

	envVars := builder.BuildLwsEnv(svcName)
	for idx := range podSpec.Spec.Containers {
		container := &podSpec.Spec.Containers[idx]
		// 1. Convert env to Map to remove duplicates
		existingEnv := make(map[string]corev1.EnvVar)
		for _, e := range container.Env {
			existingEnv[e.Name] = e
		}
		for _, newEnv := range envVars {
			existingEnv[newEnv.Name] = newEnv // Overwrite env.Value if the name exists
		}
		// 2. Convert back to slice
		mergedEnv := make([]corev1.EnvVar, 0, len(existingEnv))
		for _, env := range existingEnv {
			mergedEnv = append(mergedEnv, env)
		}
		// Avoid sts updates caused by env order changes
		sort.Slice(
			mergedEnv, func(i, j int) bool {
				return mergedEnv[i].Name < mergedEnv[j].Name
			},
		)
		container.Env = mergedEnv
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
