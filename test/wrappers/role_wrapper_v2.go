package wrappers

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

type RoleWrapperV2 struct {
	workloadsv1alpha2.RoleSpec
}

func (roleWrapper *RoleWrapperV2) Obj() workloadsv1alpha2.RoleSpec {
	return roleWrapper.RoleSpec
}

func (roleWrapper *RoleWrapperV2) WithName(name string) *RoleWrapperV2 {
	roleWrapper.Name = name
	return roleWrapper
}

func (roleWrapper *RoleWrapperV2) WithReplicas(size int32) *RoleWrapperV2 {
	roleWrapper.Replicas = ptr.To(size)
	return roleWrapper
}

func (roleWrapper *RoleWrapperV2) WithMaxUnavailable(value int32) *RoleWrapperV2 {
	if roleWrapper.RolloutStrategy == nil {
		roleWrapper.RolloutStrategy = &workloadsv1alpha2.RolloutStrategy{}
	}
	if roleWrapper.RolloutStrategy.RollingUpdate == nil {
		roleWrapper.RolloutStrategy.RollingUpdate = &workloadsv1alpha2.RollingUpdate{}
	}
	roleWrapper.RolloutStrategy.RollingUpdate.MaxUnavailable = ptr.To(intstr.FromInt32(value))
	return roleWrapper
}

func (roleWrapper *RoleWrapperV2) WithMaxSurge(value int32) *RoleWrapperV2 {
	if roleWrapper.RolloutStrategy == nil {
		roleWrapper.RolloutStrategy = &workloadsv1alpha2.RolloutStrategy{}
	}
	if roleWrapper.RolloutStrategy.RollingUpdate == nil {
		roleWrapper.RolloutStrategy.RollingUpdate = &workloadsv1alpha2.RollingUpdate{}
	}
	roleWrapper.RolloutStrategy.RollingUpdate.MaxSurge = ptr.To(intstr.FromInt32(value))
	return roleWrapper
}

func (roleWrapper *RoleWrapperV2) WithTemplate(template corev1.PodTemplateSpec) *RoleWrapperV2 {
	if roleWrapper.Pattern == nil {
		roleWrapper.Pattern = &workloadsv1alpha2.Pattern{}
	}
	if roleWrapper.Pattern.StandalonePattern == nil {
		roleWrapper.Pattern.StandalonePattern = &workloadsv1alpha2.StandalonePattern{}
	}
	roleWrapper.Pattern.StandalonePattern.TemplateSource.Template = &template
	return roleWrapper
}

func (roleWrapper *RoleWrapperV2) WithDependencies(dependencies []string) *RoleWrapperV2 {
	roleWrapper.Dependencies = dependencies
	return roleWrapper
}

func (roleWrapper *RoleWrapperV2) WithRollingUpdate(rollingUpdate workloadsv1alpha2.RollingUpdate) *RoleWrapperV2 {
	roleWrapper.RolloutStrategy = &workloadsv1alpha2.RolloutStrategy{
		Type:          workloadsv1alpha2.RollingUpdateStrategyType,
		RollingUpdate: &rollingUpdate,
	}
	return roleWrapper
}

func (roleWrapper *RoleWrapperV2) WithRestartPolicy(restartPolicy workloadsv1alpha2.RestartPolicyType) *RoleWrapperV2 {
	roleWrapper.RestartPolicy = restartPolicy
	return roleWrapper
}

func (roleWrapper *RoleWrapperV2) WithLabels(labels map[string]string) *RoleWrapperV2 {
	roleWrapper.Labels = labels
	return roleWrapper
}

func (roleWrapper *RoleWrapperV2) WithWorkload(workloadType string) *RoleWrapperV2 {
	switch workloadType {
	case workloadsv1alpha2.DeploymentWorkloadType:
		roleWrapper.Workload = workloadsv1alpha2.WorkloadSpec{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		}
	case workloadsv1alpha2.StatefulSetWorkloadType:
		roleWrapper.Workload = workloadsv1alpha2.WorkloadSpec{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		}
	case workloadsv1alpha2.LeaderWorkerSetWorkloadType:
		roleWrapper.Workload = workloadsv1alpha2.WorkloadSpec{
			APIVersion: "leaderworkerset.x-k8s.io/v1",
			Kind:       "LeaderWorkerSet",
		}
	case workloadsv1alpha2.InstanceSetWorkloadType:
		roleWrapper.Workload = workloadsv1alpha2.WorkloadSpec{
			APIVersion: "workloads.x-k8s.io/v1alpha1",
			Kind:       "InstanceSet",
		}
	default:
		panic(fmt.Sprintf("workload type not supported: %s", workloadType))
	}
	return roleWrapper
}

func (roleWrapper *RoleWrapperV2) WithEngineRuntime(engineRuntimes []workloadsv1alpha2.EngineRuntime) *RoleWrapperV2 {
	roleWrapper.EngineRuntimes = engineRuntimes
	return roleWrapper
}

func (roleWrapper *RoleWrapperV2) WithLeaderWorkerTemplate(leaderPatch, workerPatch *runtime.RawExtension) *RoleWrapperV2 {
	if roleWrapper.Pattern == nil {
		roleWrapper.Pattern = &workloadsv1alpha2.Pattern{}
	}
	roleWrapper.Pattern.LeaderWorkerPattern = &workloadsv1alpha2.LeaderWorkerPattern{
		LeaderTemplatePatch: leaderPatch,
		WorkerTemplatePatch: workerPatch,
	}
	return roleWrapper
}

func (roleWrapper *RoleWrapperV2) WithScalingAdapter(enable bool) *RoleWrapperV2 {
	roleWrapper.ScalingAdapter = &workloadsv1alpha2.ScalingAdapter{
		Enable: enable,
	}
	return roleWrapper
}

// WithTemplateRef sets the role to use a template reference by name.
func (roleWrapper *RoleWrapperV2) WithTemplateRef(name string) *RoleWrapperV2 {
	if roleWrapper.Pattern == nil {
		roleWrapper.Pattern = &workloadsv1alpha2.Pattern{}
	}
	if roleWrapper.Pattern.StandalonePattern == nil {
		roleWrapper.Pattern.StandalonePattern = &workloadsv1alpha2.StandalonePattern{}
	}
	roleWrapper.Pattern.StandalonePattern.TemplateSource.TemplateRef = &workloadsv1alpha2.TemplateRef{
		Name: name,
	}
	// Clear Template to satisfy mutual exclusivity
	roleWrapper.Pattern.StandalonePattern.TemplateSource.Template = nil
	return roleWrapper
}

func (roleWrapper *RoleWrapperV2) WithTemplatePatch(patch runtime.RawExtension) *RoleWrapperV2 {
	roleWrapper.TemplatePatch = patch
	return roleWrapper
}

func BuildBasicRoleV2(name string) *RoleWrapperV2 {
	template := BuildBasicPodTemplateSpec().Obj()
	return &RoleWrapperV2{
		workloadsv1alpha2.RoleSpec{
			Name:     name,
			Replicas: ptr.To(int32(1)),
			RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
				Type: workloadsv1alpha2.RollingUpdateStrategyType,
			},
			Workload: workloadsv1alpha2.WorkloadSpec{
				APIVersion: "apps/v1",
				Kind:       "StatefulSet",
			},
			Pattern: &workloadsv1alpha2.Pattern{
				StandalonePattern: &workloadsv1alpha2.StandalonePattern{
					TemplateSource: workloadsv1alpha2.TemplateSource{
						Template: &template,
					},
				},
			},
		},
	}
}

func BuildLwsRoleV2(name string) *RoleWrapperV2 {
	leaderPatch := BuildLWSTemplatePatch(map[string]string{"role": "leader"})
	workerPatch := BuildLWSTemplatePatch(map[string]string{"role": "worker"})
	template := BuildBasicPodTemplateSpec().Obj()

	return &RoleWrapperV2{
		workloadsv1alpha2.RoleSpec{
			Name:     name,
			Replicas: ptr.To(int32(1)),
			RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
				Type: workloadsv1alpha2.RollingUpdateStrategyType,
			},
			Workload: workloadsv1alpha2.WorkloadSpec{
				APIVersion: "leaderworkerset.x-k8s.io/v1",
				Kind:       "LeaderWorkerSet",
			},
			Pattern: &workloadsv1alpha2.Pattern{
				LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
					Size: ptr.To(int32(2)),
					TemplateSource: workloadsv1alpha2.TemplateSource{
						Template: &template,
					},
					LeaderTemplatePatch: &leaderPatch,
					WorkerTemplatePatch: &workerPatch,
				},
			},
		},
	}
}

// GetTemplate returns the template from the role wrapper.
// It handles both standalone and leader-worker patterns.
func (roleWrapper *RoleWrapperV2) GetTemplate() *corev1.PodTemplateSpec {
	if roleWrapper.Pattern == nil {
		return nil
	}
	if roleWrapper.Pattern.StandalonePattern != nil {
		return roleWrapper.Pattern.StandalonePattern.Template
	}
	if roleWrapper.Pattern.LeaderWorkerPattern != nil {
		return roleWrapper.Pattern.LeaderWorkerPattern.Template
	}
	return nil
}

// SetTemplate sets the template for the role wrapper.
// It handles standalone pattern by default.
func (roleWrapper *RoleWrapperV2) SetTemplate(template *corev1.PodTemplateSpec) {
	if roleWrapper.Pattern == nil {
		roleWrapper.Pattern = &workloadsv1alpha2.Pattern{}
	}
	if roleWrapper.Pattern.StandalonePattern == nil {
		roleWrapper.Pattern.StandalonePattern = &workloadsv1alpha2.StandalonePattern{}
	}
	roleWrapper.Pattern.StandalonePattern.Template = template
}
