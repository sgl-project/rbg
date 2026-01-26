package reconciler

import (
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func TestEnvBuilder_Build(t *testing.T) {
	tests := []struct {
		name     string
		rbg      *workloadsv1alpha1.RoleBasedGroup
		role     *workloadsv1alpha1.RoleSpec
		expected []corev1.EnvVar
	}{
		{
			name: "StatefulSet role",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-group",
				},
			},
			role: &workloadsv1alpha1.RoleSpec{
				Name: "stateful-role",
				Workload: workloadsv1alpha1.WorkloadSpec{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
				},
			},
			expected: []corev1.EnvVar{
				{
					Name:  "GROUP_NAME",
					Value: "test-group",
				},
				{
					Name:  "ROLE_NAME",
					Value: "stateful-role",
				},
				{
					Name: "ROLE_INDEX",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['apps.kubernetes.io/pod-index']",
						},
					},
				},
			},
		},
		{
			name: "LeaderWorkerSet role",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-group",
				},
			},
			role: &workloadsv1alpha1.RoleSpec{
				Name: "lws-role",
				Workload: workloadsv1alpha1.WorkloadSpec{
					APIVersion: "leaderworkerset.x-k8s.io/v1",
					Kind:       "LeaderWorkerSet",
				},
			},
			expected: []corev1.EnvVar{
				{
					Name:  "GROUP_NAME",
					Value: "test-group",
				},
				{
					Name:  "ROLE_NAME",
					Value: "lws-role",
				},
				{
					Name: "ROLE_INDEX",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['apps.kubernetes.io/pod-index']",
						},
					},
				},
			},
		},
		{
			name: "Deployment role",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-group",
				},
			},
			role: &workloadsv1alpha1.RoleSpec{
				Name: "deployment-role",
				Workload: workloadsv1alpha1.WorkloadSpec{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
			},
			expected: []corev1.EnvVar{
				{
					Name:  "GROUP_NAME",
					Value: "test-group",
				},
				{
					Name:  "ROLE_NAME",
					Value: "deployment-role",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				// Create RoleData
				roleData := &RoleData{
					Spec: tt.role,
					OwnerInfo: OwnerInfo{
						Name:      tt.rbg.Name,
						Namespace: tt.rbg.Namespace,
					},
				}
				g := &EnvBuilder{
					RoleData: roleData,
				}
				got := g.Build()

				// Sort both slices for comparison
				sort.Slice(
					got, func(i, j int) bool {
						return got[i].Name < got[j].Name
					},
				)

				sort.Slice(
					tt.expected, func(i, j int) bool {
						return tt.expected[i].Name < tt.expected[j].Name
					},
				)

				if diff := cmp.Diff(got, tt.expected); diff != "" {
					t.Errorf("EnvBuilder.Build() mismatch (-want +got):\n%s", diff)
				}
			},
		)
	}
}

func TestEnvBuilder_buildLocalRoleVars(t *testing.T) {
	tests := []struct {
		name     string
		rbg      *workloadsv1alpha1.RoleBasedGroup
		role     *workloadsv1alpha1.RoleSpec
		expected []corev1.EnvVar
	}{
		{
			name: "StatefulSet role vars",
			rbg: &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-group",
				},
			},
			role: &workloadsv1alpha1.RoleSpec{
				Name: "stateful-role",
				Workload: workloadsv1alpha1.WorkloadSpec{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
				},
			},
			expected: []corev1.EnvVar{
				{
					Name:  "GROUP_NAME",
					Value: "test-group",
				},
				{
					Name:  "ROLE_NAME",
					Value: "stateful-role",
				},
				{
					Name: "ROLE_INDEX",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['apps.kubernetes.io/pod-index']",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				// Create RoleData
				roleData := &RoleData{
					Spec: tt.role,
					OwnerInfo: OwnerInfo{
						Name:      tt.rbg.Name,
						Namespace: tt.rbg.Namespace,
					},
				}
				g := &EnvBuilder{
					RoleData: roleData,
				}
				got := g.buildLocalRoleVars()

				// Sort both slices for comparison
				sort.Slice(
					got, func(i, j int) bool {
						return got[i].Name < got[j].Name
					},
				)

				sort.Slice(
					tt.expected, func(i, j int) bool {
						return tt.expected[i].Name < tt.expected[j].Name
					},
				)

				if diff := cmp.Diff(got, tt.expected); diff != "" {
					t.Errorf("EnvBuilder.buildLocalRoleVars() mismatch (-want +got):\n%s", diff)
				}
			},
		)
	}
}
