package discovery

import (
	"context"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	metaapplyv1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type GroupInfoInjector interface {
	InjectConfig(
		context context.Context, podSpec *corev1.PodTemplateSpec, rbg *workloadsv1alpha1.RoleBasedGroup,
		role *workloadsv1alpha1.RoleSpec,
	) error
	InjectEnv(
		context context.Context, podSpec *corev1.PodTemplateSpec, rbg *workloadsv1alpha1.RoleBasedGroup,
		role *workloadsv1alpha1.RoleSpec,
	) error
	InjectSidecar(
		context context.Context, podSpec *corev1.PodTemplateSpec, rbg *workloadsv1alpha1.RoleBasedGroup,
		role *workloadsv1alpha1.RoleSpec,
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
	ctx context.Context, podSpec *corev1.PodTemplateSpec, rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
) error {
	logger := log.FromContext(ctx)

	builder := &ConfigBuilder{
		client: i.client,
		rbg:    rbg,
		role:   role,
	}

	const (
		volumeName = "rbg-cluster-config"
		mountPath  = "/etc/rbg"
		configKey  = "config.yaml"
	)

	configData, err := builder.Build()
	if err != nil {
		return err
	}
	cmApplyConfig := coreapplyv1.ConfigMap(rbg.GetWorkloadName(role), rbg.Namespace).
		WithData(
			map[string]string{
				configKey: string(configData),
			},
		).
		WithOwnerReferences(
			metaapplyv1.OwnerReference().
				WithAPIVersion(rbg.APIVersion).
				WithKind(rbg.Kind).
				WithName(rbg.Name).
				WithUID(rbg.GetUID()).
				WithBlockOwnerDeletion(true).
				WithController(true),
		)

	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cmApplyConfig)
	if err != nil {
		logger.Error(err, "Converting obj apply configuration to json.")
		return err
	}
	newConfigmap := &corev1.ConfigMap{}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(obj, newConfigmap); err != nil {
		return fmt.Errorf("convert ConfigmapApplyConfig to deploy error: %s", err.Error())
	}

	oldConfigmap := &corev1.ConfigMap{}
	err = i.client.Get(
		ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, oldConfigmap,
	)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	equal, diff := semanticallyEqualConfigmap(oldConfigmap, newConfigmap)
	if equal {
		logger.V(1).Info("configmap equal, skip reconcile")
	} else {
		logger.V(1).Info(fmt.Sprintf("confgmap not equal, diff: %s", diff))
		if err := utils.PatchObjectApplyConfiguration(ctx, i.client, cmApplyConfig, utils.PatchSpec); err != nil {
			logger.Error(err, "Failed to patch ConfigMap")
			return err
		}
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
							Name: rbg.GetWorkloadName(role),
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
	ctx context.Context, podSpec *corev1.PodTemplateSpec, rbg *workloadsv1alpha1.RoleBasedGroup,
	role *workloadsv1alpha1.RoleSpec,
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
	rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec) error {

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
	rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
) error {
	builder := NewSidecarBuilder(i.client, rbg, role)
	return builder.Build(ctx, podSpec)
}
