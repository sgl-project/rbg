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

package v1alpha1

import (
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	workloadsv1alpha "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

type RoleBasedGroupWrapper struct {
	workloadsv1alpha.RoleBasedGroup
}

func (rbgWrapper *RoleBasedGroupWrapper) Obj() *workloadsv1alpha.RoleBasedGroup {
	return &rbgWrapper.RoleBasedGroup
}

func (rbgWrapper *RoleBasedGroupWrapper) WithName(name string) *RoleBasedGroupWrapper {
	rbgWrapper.Name = name
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupWrapper) WithAnnotations(annotations map[string]string) *RoleBasedGroupWrapper {
	rbgWrapper.Annotations = annotations
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupWrapper) WithRoles(roles []workloadsv1alpha.RoleSpec) *RoleBasedGroupWrapper {
	rbgWrapper.Spec.Roles = roles
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupWrapper) AddRole(role workloadsv1alpha.RoleSpec) *RoleBasedGroupWrapper {
	rbgWrapper.Spec.Roles = append(rbgWrapper.Spec.Roles, role)
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupWrapper) WithKubeGangScheduling(kubeGangScheduling bool) *RoleBasedGroupWrapper {
	if kubeGangScheduling {
		timeout := int32(60) // default timeout
		rbgWrapper.Spec.PodGroupPolicy = &workloadsv1alpha.PodGroupPolicy{
			PodGroupPolicySource: workloadsv1alpha.PodGroupPolicySource{
				KubeScheduling: &workloadsv1alpha.KubeSchedulingPodGroupPolicySource{
					ScheduleTimeoutSeconds: &timeout,
				},
			},
		}
	} else {
		rbgWrapper.Spec.PodGroupPolicy = nil
	}

	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupWrapper) WithVolcanoGangScheduling(priorityClassName, queue string) *RoleBasedGroupWrapper {
	rbgWrapper.Spec.PodGroupPolicy = &workloadsv1alpha.PodGroupPolicy{
		PodGroupPolicySource: workloadsv1alpha.PodGroupPolicySource{
			VolcanoScheduling: &workloadsv1alpha.VolcanoSchedulingPodGroupPolicySource{
				PriorityClassName: priorityClassName,
				Queue:             queue,
			},
		},
	}

	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupWrapper) WithDeletionTimestamp() *RoleBasedGroupWrapper {
	rbgWrapper.DeletionTimestamp = &v1.Time{Time: time.Now()}
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupWrapper) WithStatus(
	status workloadsv1alpha.RoleBasedGroupStatus,
) *RoleBasedGroupWrapper {
	rbgWrapper.Status = status
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupWrapper) WithRoleTemplates(
	templates []workloadsv1alpha.RoleTemplate,
) *RoleBasedGroupWrapper {
	rbgWrapper.Spec.RoleTemplates = templates
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupWrapper) AddRoleTemplate(
	template workloadsv1alpha.RoleTemplate,
) *RoleBasedGroupWrapper {
	rbgWrapper.Spec.RoleTemplates = append(rbgWrapper.Spec.RoleTemplates, template)
	return rbgWrapper
}

func BuildBasicRoleBasedGroup(name, ns string) *RoleBasedGroupWrapper {
	return &RoleBasedGroupWrapper{
		workloadsv1alpha.RoleBasedGroup{
			TypeMeta: v1.TypeMeta{
				APIVersion: "workloads.x-k8s.io/v1alpha1",
				Kind:       "RoleBasedGroup",
			},
			ObjectMeta: v1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels: map[string]string{
					workloadsv1alpha.SetNameLabelKey: name,
				},
				UID: "rbg-test-uid",
			},
			Spec: workloadsv1alpha.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha.RoleSpec{
					BuildBasicRole("test-role").Obj(),
				},
			},
		},
	}
}
