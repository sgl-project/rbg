package wrappers

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	workloadsv1alpha "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type RoleWrapper struct {
	workloadsv1alpha.RoleSpec
}

func (roleWrapper *RoleWrapper) Obj() workloadsv1alpha.RoleSpec {
	return roleWrapper.RoleSpec
}

func (roleWrapper *RoleWrapper) WithName(name string) *RoleWrapper {
	roleWrapper.Name = name
	return roleWrapper
}

func (roleWrapper *RoleWrapper) WithReplicas(size int32) *RoleWrapper {
	roleWrapper.Replicas = ptr.To(size)
	return roleWrapper
}

func (roleWrapper *RoleWrapper) WithMaxUnavailable(value int32) *RoleWrapper {
	if roleWrapper.RolloutStrategy.RollingUpdate == nil {
		roleWrapper.RolloutStrategy.RollingUpdate = &workloadsv1alpha.RollingUpdate{}
	}
	roleWrapper.RolloutStrategy.RollingUpdate.MaxUnavailable = ptr.To(intstr.FromInt32(value))
	return roleWrapper
}

func (roleWrapper *RoleWrapper) WithMaxSurge(value int32) *RoleWrapper {
	if roleWrapper.RolloutStrategy.RollingUpdate == nil {
		roleWrapper.RolloutStrategy.RollingUpdate = &workloadsv1alpha.RollingUpdate{}
	}
	roleWrapper.RolloutStrategy.RollingUpdate.MaxSurge = ptr.To(intstr.FromInt32(value))
	return roleWrapper
}

func (roleWrapper *RoleWrapper) WithTemplate(template corev1.PodTemplateSpec) *RoleWrapper {
	roleWrapper.Template = &template
	return roleWrapper
}

func (roleWrapper *RoleWrapper) WithDependencies(dependencies []string) *RoleWrapper {
	roleWrapper.Dependencies = dependencies
	return roleWrapper
}

func (roleWrapper *RoleWrapper) WithRollingUpdate(rollingUpdate workloadsv1alpha.RollingUpdate) *RoleWrapper {
	roleWrapper.RolloutStrategy = &workloadsv1alpha.RolloutStrategy{
		Type:          workloadsv1alpha.RollingUpdateStrategyType,
		RollingUpdate: &rollingUpdate,
	}
	return roleWrapper
}

func (roleWrapper *RoleWrapper) WithRestartPolicy(restartPolicy workloadsv1alpha.RestartPolicyType) *RoleWrapper {
	roleWrapper.RestartPolicy = restartPolicy
	return roleWrapper
}

func (roleWrapper *RoleWrapper) WithWorkload(workloadType string) *RoleWrapper {
	switch workloadType {
	case workloadsv1alpha.DeploymentWorkloadType:
		roleWrapper.Workload = workloadsv1alpha.WorkloadSpec{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		}
	case workloadsv1alpha.StatefulSetWorkloadType:
		roleWrapper.Workload = workloadsv1alpha.WorkloadSpec{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		}
	case workloadsv1alpha.LeaderWorkerSetWorkloadType:
		roleWrapper.Workload = workloadsv1alpha.WorkloadSpec{
			APIVersion: "leaderworkerset.x-k8s.io/v1",
			Kind:       "LeaderWorkerSet",
		}
	case workloadsv1alpha.InstanceSetWorkloadType:
		roleWrapper.Workload = workloadsv1alpha.WorkloadSpec{
			APIVersion: "workloads.x-k8s.io/v1alpha1",
			Kind:       "InstanceSet",
		}
	default:
		panic(fmt.Sprintf("workload type not supported: %s", workloadType))
	}
	return roleWrapper
}

func (roleWrapper *RoleWrapper) WithEngineRuntime(engineRuntimes []workloadsv1alpha.EngineRuntime) *RoleWrapper {
	roleWrapper.EngineRuntimes = engineRuntimes
	return roleWrapper
}

func (roleWrapper *RoleWrapper) WithLeaderWorkerTemplate(leaderPatch, workerPatch *runtime.RawExtension) *RoleWrapper {
	roleWrapper.LeaderWorkerSet = &workloadsv1alpha.LeaderWorkerTemplate{
		PatchLeaderTemplate: leaderPatch,
		PatchWorkerTemplate: workerPatch,
	}
	return roleWrapper
}

func (roleWrapper *RoleWrapper) WithScalingAdapter(enable bool) *RoleWrapper {
	roleWrapper.ScalingAdapter = &workloadsv1alpha.ScalingAdapter{
		Enable: enable,
	}
	return roleWrapper
}

func (roleWrapper *RoleWrapper) WithTemplateRef(name string) *RoleWrapper {
	roleWrapper.TemplateRef = &workloadsv1alpha.TemplateRef{
		Name: name,
	}
	// Clear Template to satisfy mutual exclusivity (templateRef and template cannot both be set)
	roleWrapper.Template = nil
	return roleWrapper
}

func (roleWrapper *RoleWrapper) WithTemplatePatch(patch runtime.RawExtension) *RoleWrapper {
	roleWrapper.TemplatePatch = patch
	return roleWrapper
}

func BuildBasicRole(name string) *RoleWrapper {
	template := BuildBasicPodTemplateSpec().Obj()
	return &RoleWrapper{
		workloadsv1alpha.RoleSpec{
			Name:     name,
			Replicas: ptr.To(int32(1)),
			RolloutStrategy: &workloadsv1alpha.RolloutStrategy{
				Type: workloadsv1alpha.RollingUpdateStrategyType,
			},
			Workload: workloadsv1alpha.WorkloadSpec{
				APIVersion: "apps/v1",
				Kind:       "StatefulSet",
			},
			Template: &template,
		},
	}
}

func BuildLwsRole(name string) *RoleWrapper {
	leaderPatch := BuildLWSTemplatePatch(map[string]string{"role": "leader"})
	workerPatch := BuildLWSTemplatePatch(map[string]string{"role": "worker"})
	template := BuildBasicPodTemplateSpec().Obj()

	return &RoleWrapper{
		workloadsv1alpha.RoleSpec{
			Name:     name,
			Replicas: ptr.To(int32(1)),
			RolloutStrategy: &workloadsv1alpha.RolloutStrategy{
				Type: workloadsv1alpha.RollingUpdateStrategyType,
			},
			Workload: workloadsv1alpha.WorkloadSpec{
				APIVersion: "leaderworkerset.x-k8s.io/v1",
				Kind:       "LeaderWorkerSet",
			},
			Template: &template,
			LeaderWorkerSet: &workloadsv1alpha.LeaderWorkerTemplate{
				Size:                ptr.To(int32(2)),
				PatchLeaderTemplate: &leaderPatch,
				PatchWorkerTemplate: &workerPatch,
			},
		},
	}
}

func BuildLWSTemplatePatch(labels map[string]string) runtime.RawExtension {
	type metadata struct {
		Labels map[string]string `json:"labels"`
	}
	type labelsPatch struct {
		MetaData metadata `json:"metadata"`
	}

	patchContent := labelsPatch{
		MetaData: metadata{
			Labels: labels,
		},
	}

	return runtime.RawExtension{Raw: []byte(utils.DumpJSON(patchContent))}
}
