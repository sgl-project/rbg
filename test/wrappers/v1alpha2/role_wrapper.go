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

package v1alpha2

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/utils"
)

// StandaloneRoleWrapper wraps a v1alpha2 RoleSpec with StandalonePattern.
type StandaloneRoleWrapper struct {
	workloadsv1alpha2.RoleSpec
}

func (rw *StandaloneRoleWrapper) Obj() workloadsv1alpha2.RoleSpec {
	return rw.RoleSpec
}

func (rw *StandaloneRoleWrapper) WithReplicas(size int32) *StandaloneRoleWrapper {
	rw.Replicas = ptr.To(size)
	return rw
}

func (rw *StandaloneRoleWrapper) WithDependencies(deps []string) *StandaloneRoleWrapper {
	rw.Dependencies = deps
	return rw
}

func (rw *StandaloneRoleWrapper) WithEngineRuntime(er []workloadsv1alpha2.EngineRuntime) *StandaloneRoleWrapper {
	rw.EngineRuntimes = er
	return rw
}

func (rw *StandaloneRoleWrapper) WithScalingAdapter(enable bool) *StandaloneRoleWrapper {
	rw.ScalingAdapter = &workloadsv1alpha2.ScalingAdapter{Enable: enable}
	return rw
}

func (rw *StandaloneRoleWrapper) WithRollingUpdate(ru workloadsv1alpha2.RollingUpdate) *StandaloneRoleWrapper {
	rw.RolloutStrategy = &workloadsv1alpha2.RolloutStrategy{
		Type:          workloadsv1alpha2.RollingUpdateStrategyType,
		RollingUpdate: &ru,
	}
	return rw
}

func (rw *StandaloneRoleWrapper) WithRestartPolicy(rp workloadsv1alpha2.RestartPolicyType) *StandaloneRoleWrapper {
	rw.RestartPolicy = rp
	return rw
}

func (rw *StandaloneRoleWrapper) WithWorkload(apiVersion, kind string) *StandaloneRoleWrapper {
	rw.Workload = workloadsv1alpha2.WorkloadSpec{
		APIVersion: apiVersion,
		Kind:       kind,
	}
	return rw
}

func (rw *StandaloneRoleWrapper) WithTemplate(template *corev1.PodTemplateSpec) *StandaloneRoleWrapper {
	rw.StandalonePattern.TemplateSource.Template = template
	rw.StandalonePattern.TemplateSource.TemplateRef = nil
	return rw
}

func (rw *StandaloneRoleWrapper) WithTemplateRef(name string) *StandaloneRoleWrapper {
	rw.StandalonePattern.TemplateSource.TemplateRef = &workloadsv1alpha2.TemplateRef{Name: name}
	rw.StandalonePattern.TemplateSource.Template = nil
	return rw
}

func (rw *StandaloneRoleWrapper) WithTemplatePatch(patch *workloadsv1alpha2.TemplateRef) *StandaloneRoleWrapper {
	rw.StandalonePattern.TemplateSource.TemplateRef = patch
	return rw
}

func (rw *StandaloneRoleWrapper) WithPatchRef(name string, patch *runtime.RawExtension) *StandaloneRoleWrapper {
	rw.StandalonePattern.TemplateSource.TemplateRef = &workloadsv1alpha2.TemplateRef{
		Name:  name,
		Patch: patch,
	}
	rw.StandalonePattern.TemplateSource.Template = nil
	return rw
}

// BuildStandaloneRole creates a basic standalone role.
// Sets the Workload to RoleInstanceSet (the v1alpha2 default stateful workload type).
func BuildStandaloneRole(name string) *StandaloneRoleWrapper {
	template := BuildBasicPodTemplateSpec()
	return &StandaloneRoleWrapper{
		workloadsv1alpha2.RoleSpec{
			Name:     name,
			Replicas: ptr.To(int32(1)),
			RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
				Type: workloadsv1alpha2.RollingUpdateStrategyType,
			},
			Workload: workloadsv1alpha2.WorkloadSpec{
				APIVersion: "workloads.x-k8s.io/v1alpha2",
				Kind:       "RoleInstanceSet",
			},
			Pattern: workloadsv1alpha2.Pattern{
				StandalonePattern: &workloadsv1alpha2.StandalonePattern{
					TemplateSource: workloadsv1alpha2.TemplateSource{
						Template: &template,
					},
				},
			},
		},
	}
}

// LeaderWorkerRoleWrapper wraps a v1alpha2 RoleSpec with LeaderWorkerPattern.
type LeaderWorkerRoleWrapper struct {
	workloadsv1alpha2.RoleSpec
}

func (rw *LeaderWorkerRoleWrapper) Obj() workloadsv1alpha2.RoleSpec {
	return rw.RoleSpec
}

func (rw *LeaderWorkerRoleWrapper) WithReplicas(size int32) *LeaderWorkerRoleWrapper {
	rw.Replicas = ptr.To(size)
	return rw
}

func (rw *LeaderWorkerRoleWrapper) WithDependencies(deps []string) *LeaderWorkerRoleWrapper {
	rw.Dependencies = deps
	return rw
}

func (rw *LeaderWorkerRoleWrapper) WithEngineRuntime(er []workloadsv1alpha2.EngineRuntime) *LeaderWorkerRoleWrapper {
	rw.EngineRuntimes = er
	return rw
}

func (rw *LeaderWorkerRoleWrapper) WithScalingAdapter(enable bool) *LeaderWorkerRoleWrapper {
	rw.ScalingAdapter = &workloadsv1alpha2.ScalingAdapter{Enable: enable}
	return rw
}

func (rw *LeaderWorkerRoleWrapper) WithRollingUpdate(ru workloadsv1alpha2.RollingUpdate) *LeaderWorkerRoleWrapper {
	rw.RolloutStrategy = &workloadsv1alpha2.RolloutStrategy{
		Type:          workloadsv1alpha2.RollingUpdateStrategyType,
		RollingUpdate: &ru,
	}
	return rw
}

func (rw *LeaderWorkerRoleWrapper) WithRestartPolicy(rp workloadsv1alpha2.RestartPolicyType) *LeaderWorkerRoleWrapper {
	rw.RestartPolicy = rp
	return rw
}

func (rw *LeaderWorkerRoleWrapper) WithSize(size int32) *LeaderWorkerRoleWrapper {
	rw.LeaderWorkerPattern.Size = ptr.To(size)
	return rw
}

func (rw *LeaderWorkerRoleWrapper) WithTemplateRef(name string) *LeaderWorkerRoleWrapper {
	rw.LeaderWorkerPattern.TemplateSource.TemplateRef = &workloadsv1alpha2.TemplateRef{Name: name}
	rw.LeaderWorkerPattern.TemplateSource.Template = nil
	return rw
}

// BuildLeaderWorkerRole creates a basic leader-worker role.
// Sets the Workload to LeaderWorkerSet.
func BuildLeaderWorkerRole(name string) *LeaderWorkerRoleWrapper {
	template := BuildBasicPodTemplateSpec()
	return &LeaderWorkerRoleWrapper{
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
			Pattern: workloadsv1alpha2.Pattern{
				LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
					Size: ptr.To(int32(2)),
					TemplateSource: workloadsv1alpha2.TemplateSource{
						Template: &template,
					},
					LeaderTemplatePatch: buildRawPatch(map[string]string{"role": "leader"}),
					WorkerTemplatePatch: buildRawPatch(map[string]string{"role": "worker"}),
				},
			},
		},
	}
}

// BuildBasicPodTemplateSpec builds a basic PodTemplateSpec for tests.
func BuildBasicPodTemplateSpec() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "nginx",
					Image: utils.DefaultImage,
				},
			},
		},
	}
}

// BuildRollingUpdate creates a RollingUpdate with common values.
func BuildRollingUpdate(maxUnavailable, maxSurge int32) workloadsv1alpha2.RollingUpdate {
	return workloadsv1alpha2.RollingUpdate{
		MaxUnavailable: ptr.To(intstr.FromInt32(maxUnavailable)),
		MaxSurge:       ptr.To(intstr.FromInt32(maxSurge)),
	}
}

// buildRawPatch creates a runtime.RawExtension patch with the given labels on the pod template metadata.
func buildRawPatch(labels map[string]string) *runtime.RawExtension {
	type metadata struct {
		Labels map[string]string `json:"labels"`
	}
	type patch struct {
		Metadata metadata `json:"metadata"`
	}
	b, _ := json.Marshal(patch{Metadata: metadata{Labels: labels}})
	return &runtime.RawExtension{Raw: b}
}
