package wrappers

import (
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

type RoleBasedGroupV2Wrapper struct {
	workloadsv1alpha2.RoleBasedGroup
}

func (rbgWrapper *RoleBasedGroupV2Wrapper) Obj() *workloadsv1alpha2.RoleBasedGroup {
	return &rbgWrapper.RoleBasedGroup
}

func (rbgWrapper *RoleBasedGroupV2Wrapper) WithName(name string) *RoleBasedGroupV2Wrapper {
	rbgWrapper.Name = name
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupV2Wrapper) WithAnnotations(annotations map[string]string) *RoleBasedGroupV2Wrapper {
	rbgWrapper.Annotations = annotations
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupV2Wrapper) WithRoles(roles []workloadsv1alpha2.RoleSpec) *RoleBasedGroupV2Wrapper {
	rbgWrapper.Spec.Roles = roles
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupV2Wrapper) AddRole(role workloadsv1alpha2.RoleSpec) *RoleBasedGroupV2Wrapper {
	rbgWrapper.Spec.Roles = append(rbgWrapper.Spec.Roles, role)
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupV2Wrapper) WithKubeGangScheduling(kubeGangScheduling bool) *RoleBasedGroupV2Wrapper {
	if kubeGangScheduling {
		timeout := int32(60) // default timeout
		rbgWrapper.Spec.PodGroupPolicy = &workloadsv1alpha2.PodGroupPolicy{
			PodGroupPolicySource: workloadsv1alpha2.PodGroupPolicySource{
				KubeScheduling: &workloadsv1alpha2.KubeSchedulingPodGroupPolicySource{
					ScheduleTimeoutSeconds: &timeout,
				},
			},
		}
	} else {
		rbgWrapper.Spec.PodGroupPolicy = nil
	}

	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupV2Wrapper) WithVolcanoGangScheduling(priorityClassName, queue string) *RoleBasedGroupV2Wrapper {
	rbgWrapper.Spec.PodGroupPolicy = &workloadsv1alpha2.PodGroupPolicy{
		PodGroupPolicySource: workloadsv1alpha2.PodGroupPolicySource{
			VolcanoScheduling: &workloadsv1alpha2.VolcanoSchedulingPodGroupPolicySource{
				PriorityClassName: priorityClassName,
				Queue:             queue,
			},
		},
	}

	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupV2Wrapper) WithDeletionTimestamp() *RoleBasedGroupV2Wrapper {
	rbgWrapper.DeletionTimestamp = &v1.Time{Time: time.Now()}
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupV2Wrapper) WithStatus(
	status workloadsv1alpha2.RoleBasedGroupStatus,
) *RoleBasedGroupV2Wrapper {
	rbgWrapper.Status = status
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupV2Wrapper) WithRoleTemplates(
	templates []workloadsv1alpha2.RoleTemplate,
) *RoleBasedGroupV2Wrapper {
	rbgWrapper.Spec.RoleTemplates = templates
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupV2Wrapper) AddRoleTemplate(
	template workloadsv1alpha2.RoleTemplate,
) *RoleBasedGroupV2Wrapper {
	rbgWrapper.Spec.RoleTemplates = append(rbgWrapper.Spec.RoleTemplates, template)
	return rbgWrapper
}

func BuildBasicRoleBasedGroupV2(name, ns string) *RoleBasedGroupV2Wrapper {
	return &RoleBasedGroupV2Wrapper{
		workloadsv1alpha2.RoleBasedGroup{
			TypeMeta: v1.TypeMeta{
				APIVersion: "workloads.x-k8s.io/v1alpha2",
				Kind:       "RoleBasedGroup",
			},
			ObjectMeta: v1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels: map[string]string{
					workloadsv1alpha2.SetNameLabelKey: name,
				},
				UID: "rbg-test-uid",
			},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{
					BuildBasicRoleV2("test-role").Obj(),
				},
			},
		},
	}
}
