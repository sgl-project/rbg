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
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

type EnvBuilder struct {
	rbg  *workloadsv1alpha2.RoleBasedGroup
	role *workloadsv1alpha2.RoleSpec
}

func (g *EnvBuilder) Build() []corev1.EnvVar {
	envMap := make(map[string]corev1.EnvVar)
	for _, env := range g.buildLocalRoleVars() {
		envMap[env.Name] = env
	}

	envVars := make([]corev1.EnvVar, 0, len(envMap))
	for _, env := range envMap {
		envVars = append(envVars, env)
	}

	sort.Slice(envVars, func(i, j int) bool {
		return envVars[i].Name < envVars[j].Name
	})
	return envVars
}

func (b *EnvBuilder) BuildLwsEnv(svcName string) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{
			Name:  constants.EnvRBGLeaderAddress,
			Value: fmt.Sprintf("$(%s)-0.%s.%s", constants.EnvRBGRoleInstanceName, svcName, b.rbg.Namespace),
		},
		{
			Name: constants.EnvRBGIndex,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: fmt.Sprintf("metadata.labels['%s']", constants.ComponentIndexLabelKey),
				},
			},
		},
		{
			Name: constants.EnvRBGSize,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: fmt.Sprintf("metadata.labels['%s']", constants.ComponentSizeLabelKey),
				},
			},
		},
	}
	return envVars
}

func (g *EnvBuilder) buildLocalRoleVars() []corev1.EnvVar {

	// Inject environment variables for service discovery
	// MUST NOT inject size envs to avoid pod recreated when scale up/down
	envVars := []corev1.EnvVar{
		{
			Name:  constants.EnvRBGGroupName,
			Value: g.rbg.Name,
		},
		{
			Name:  constants.EnvRBGRoleName,
			Value: g.role.Name,
		},
	}

	if workloadsv1alpha2.IsStatefulRole(g.role) {
		envVars = append(envVars,
			corev1.EnvVar{
				Name: constants.EnvRBGRoleIndex,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: g.roleIndexFieldPath(),
					},
				},
			})
	}

	if g.role.GetWorkloadSpec().Kind == "RoleInstanceSet" {
		envVars = append(envVars,
			corev1.EnvVar{
				Name: constants.EnvRBGRoleInstanceName,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.labels['%s']", constants.RoleInstanceNameLabelKey),
					},
				},
			},
			corev1.EnvVar{
				Name: constants.EnvRBGComponentName,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.labels['%s']", constants.ComponentNameLabelKey),
					},
				},
			},
			corev1.EnvVar{
				Name: constants.EnvRBGComponentIndex,
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: fmt.Sprintf("metadata.labels['%s']", constants.ComponentIDLabelKey),
					},
				},
			},
		)
	}

	return envVars
}

func (g *EnvBuilder) roleIndexFieldPath() string {
	if g.role != nil && g.role.GetWorkloadType() == constants.RoleInstanceSetWorkloadType {
		return fmt.Sprintf("metadata.labels['%s']", constants.RoleInstanceIndexLabelKey)
	}

	return "metadata.labels['apps.kubernetes.io/pod-index']"
}
