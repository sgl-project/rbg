package reconciler

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type GroupInfoInjector interface {
	InjectEnv(
		context context.Context, podSpec *corev1.PodTemplateSpec, roleData *RoleData,
	) error
	InjectSidecar(
		context context.Context, podSpec *corev1.PodTemplateSpec, roleData *RoleData,
	) error
	InjectLeaderWorkerSetEnv(
		ctx context.Context, podSpec *corev1.PodTemplateSpec, roleData *RoleData,
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

func (i *DefaultInjector) InjectEnv(
	ctx context.Context, podSpec *corev1.PodTemplateSpec, roleData *RoleData,
) error {
	builder := &EnvBuilder{
		RoleData: roleData,
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
	roleData *RoleData) error {

	builder := &EnvBuilder{
		RoleData: roleData,
	}
	svcName, err := GetCompatibleHeadlessServiceNameFromRoleData(ctx, i.client, roleData)
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
	roleData *RoleData,
) error {
	builder := NewSidecarBuilder(i.client, roleData)
	return builder.Build(ctx, podSpec)
}
