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
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestEnvBuilder_Build(t *testing.T) {
	tests := []struct {
		name     string
		rbg      *workloadsv1alpha2.RoleBasedGroup
		role     *workloadsv1alpha2.RoleSpec
		expected []corev1.EnvVar
	}{
		{
			name: "StatefulSet role",
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-group",
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name: "stateful-role",
				Annotations: map[string]string{
					constants.RoleWorkloadTypeAnnotationKey: "apps/v1/StatefulSet",
				},
			},
			expected: []corev1.EnvVar{
				{
					Name:  "RBG_GROUP_NAME",
					Value: "test-group",
				},
				{
					Name:  "RBG_ROLE_NAME",
					Value: "stateful-role",
				},
				{
					Name: "RBG_ROLE_INDEX",
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
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-group",
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name: "lws-role",
				Annotations: map[string]string{
					constants.RoleWorkloadTypeAnnotationKey: "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet",
				},
			},
			expected: []corev1.EnvVar{
				{
					Name:  "RBG_GROUP_NAME",
					Value: "test-group",
				},
				{
					Name:  "RBG_ROLE_NAME",
					Value: "lws-role",
				},
				{
					Name: "RBG_ROLE_INDEX",
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
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-group",
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name: "deployment-role",
				Annotations: map[string]string{
					constants.RoleWorkloadTypeAnnotationKey: "apps/v1/Deployment",
				},
			},
			expected: []corev1.EnvVar{
				{
					Name:  "RBG_GROUP_NAME",
					Value: "test-group",
				},
				{
					Name:  "RBG_ROLE_NAME",
					Value: "deployment-role",
				},
			},
		},
		{
			name: "RoleInstanceSet role",
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-group",
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name: "role-instance-role",
				Annotations: map[string]string{
					constants.RoleWorkloadTypeAnnotationKey: "workloads.x-k8s.io/v1alpha2/RoleInstanceSet",
				},
			},
			expected: []corev1.EnvVar{
				{
					Name:  "RBG_GROUP_NAME",
					Value: "test-group",
				},
				{
					Name:  "RBG_ROLE_NAME",
					Value: "role-instance-role",
				},
				{
					Name: "RBG_ROLE_INDEX",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['rbg.workloads.x-k8s.io/role-instance-index']",
						},
					},
				},
				{
					Name: "RBG_ROLE_INSTANCE_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['rbg.workloads.x-k8s.io/role-instance-name']",
						},
					},
				},
				{
					Name: "RBG_COMPONENT_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['rbg.workloads.x-k8s.io/component-name']",
						},
					},
				},
				{
					Name: "RBG_COMPONENT_INDEX",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['rbg.workloads.x-k8s.io/component-id']",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				g := &EnvBuilder{
					rbg:  tt.rbg,
					role: tt.role,
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
		rbg      *workloadsv1alpha2.RoleBasedGroup
		role     *workloadsv1alpha2.RoleSpec
		expected []corev1.EnvVar
	}{
		{
			name: "StatefulSet role vars",
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-group",
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name: "stateful-role",
				Annotations: map[string]string{
					constants.RoleWorkloadTypeAnnotationKey: "apps/v1/StatefulSet",
				},
			},
			expected: []corev1.EnvVar{
				{
					Name:  "RBG_GROUP_NAME",
					Value: "test-group",
				},
				{
					Name:  "RBG_ROLE_NAME",
					Value: "stateful-role",
				},
				{
					Name: "RBG_ROLE_INDEX",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['apps.kubernetes.io/pod-index']",
						},
					},
				},
			},
		},
		{
			name: "RoleInstanceSet role vars",
			rbg: &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-group",
				},
			},
			role: &workloadsv1alpha2.RoleSpec{
				Name: "role-instance-role",
				Annotations: map[string]string{
					constants.RoleWorkloadTypeAnnotationKey: "workloads.x-k8s.io/v1alpha2/RoleInstanceSet",
				},
			},
			expected: []corev1.EnvVar{
				{
					Name:  "RBG_GROUP_NAME",
					Value: "test-group",
				},
				{
					Name:  "RBG_ROLE_NAME",
					Value: "role-instance-role",
				},
				{
					Name: "RBG_ROLE_INDEX",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['" + constants.RoleInstanceIndexLabelKey + "']",
						},
					},
				},
				{
					Name: "RBG_ROLE_INSTANCE_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['" + constants.RoleInstanceNameLabelKey + "']",
						},
					},
				},
				{
					Name: "RBG_COMPONENT_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['" + constants.ComponentNameLabelKey + "']",
						},
					},
				},
				{
					Name: "RBG_COMPONENT_INDEX",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.labels['" + constants.ComponentIDLabelKey + "']",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				g := &EnvBuilder{
					rbg:  tt.rbg,
					role: tt.role,
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
