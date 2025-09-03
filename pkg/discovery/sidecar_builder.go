package discovery

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type SidecarBuilder struct {
	rbg    *workloadsv1alpha.RoleBasedGroup
	role   *workloadsv1alpha.RoleSpec
	client client.Client
}

func NewSidecarBuilder(
	k8sClient client.Client, rbg *workloadsv1alpha.RoleBasedGroup, role *workloadsv1alpha.RoleSpec,
) *SidecarBuilder {
	return &SidecarBuilder{
		rbg:    rbg,
		role:   role,
		client: k8sClient,
	}
}

func (b *SidecarBuilder) Build(ctx context.Context, podSpec *v1.PodTemplateSpec) error {
	logger := log.FromContext(ctx)

	curRole, err := b.rbg.GetRole(b.role.Name)
	if err != nil || curRole == nil {
		return err
	}

	if len(curRole.EngineRuntimes) == 0 {
		logger.V(1).Info("runtime is nil, skip inject sidecar")
		return nil
	}

	for _, runtime := range curRole.EngineRuntimes {
		if err := b.injectRuntime(ctx, podSpec, runtime); err != nil {
			return fmt.Errorf("reconcile engine runtime %s, error: %s", runtime.ProfileName, err.Error())
		}
	}

	return nil
}

func (b *SidecarBuilder) injectRuntime(
	ctx context.Context, podSpec *v1.PodTemplateSpec,
	runtime workloadsv1alpha.EngineRuntime,
) error {
	logger := log.FromContext(ctx)

	engineRuntime := &workloadsv1alpha.ClusterEngineRuntimeProfile{}
	if err := b.client.Get(
		ctx, types.NamespacedName{
			Name: runtime.ProfileName,
		}, engineRuntime,
	); err != nil {
		return fmt.Errorf("get engine runtime %s, error: %s", runtime.ProfileName, err.Error())
	}

	engineRuntimeContainerNames := make([]string, 0)

	// inject initContainers
	for _, initContainer := range engineRuntime.Spec.InitContainers {
		engineRuntimeContainerNames = append(engineRuntimeContainerNames, initContainer.Name)
		found := false
		for _, ic := range podSpec.Spec.InitContainers {
			if ic.Name == initContainer.Name {
				found = true
				break
			}
		}
		if !found {
			podSpec.Spec.InitContainers = append(podSpec.Spec.InitContainers, initContainer)
		}
	}

	// inject containers
	for _, container := range engineRuntime.Spec.Containers {
		engineRuntimeContainerNames = append(engineRuntimeContainerNames, container.Name)
		found := false
		for _, c := range podSpec.Spec.Containers {
			if c.Name == container.Name {
				found = true
				break
			}
		}
		if !found {
			podSpec.Spec.Containers = append(podSpec.Spec.Containers, container)
		}
	}

	// inject volumes
	for _, vol := range engineRuntime.Spec.Volumes {
		found := false
		for _, oldV := range podSpec.Spec.Volumes {
			if oldV.Name == vol.Name {
				found = true
				break
			}
		}
		if !found {
			podSpec.Spec.Volumes = append(podSpec.Spec.Volumes, vol)
		}
	}

	// override container based on rbg cr
	for _, container := range runtime.Containers {

		if !utils.ContainsString(engineRuntimeContainerNames, container.Name) {
			logger.V(1).Info(
				fmt.Sprintf(
					"rbg runtime has container %s but not in clusterEngineRuntime, "+
						"skip override", container.Name,
				),
			)
			continue
		}

		for i, c := range podSpec.Spec.Containers {
			if c.Name == container.Name {
				if len(container.Env) > 0 {
					podSpec.Spec.Containers[i].Env = append(podSpec.Spec.Containers[i].Env, container.Env...)
				}
				if len(container.Args) > 0 {
					podSpec.Spec.Containers[i].Args = append(podSpec.Spec.Containers[i].Args, container.Args...)
				}
				break
			}
		}
	}

	return nil
}
