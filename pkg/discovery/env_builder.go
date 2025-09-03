package discovery

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

type EnvBuilder struct {
	rbg  *workloadsv1alpha1.RoleBasedGroup
	role *workloadsv1alpha1.RoleSpec
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

func (g *EnvBuilder) buildLocalRoleVars() []corev1.EnvVar {

	// Inject environment variables for service discovery
	// MUST NOT inject size envs to avoid pod recreated when scale up/down
	envVars := []corev1.EnvVar{
		{
			Name:  "GROUP_NAME",
			Value: g.rbg.Name,
		},
		{
			Name:  "ROLE_NAME",
			Value: g.role.Name,
		},
	}

	if g.role.Workload.Kind == "StatefulSet" || g.role.Workload.Kind == "LeaderWorkerSet" {
		envVars = append(envVars,
			corev1.EnvVar{
				Name: "ROLE_INDEX",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.labels['apps.kubernetes.io/pod-index']",
					},
				},
			})
	}

	return envVars
}
