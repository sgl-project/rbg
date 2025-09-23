package wrappers

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

func BuildBasicRoleBasedGroup(name, ns string) *RoleBasedGroupWrapper {
	return &RoleBasedGroupWrapper{
		workloadsv1alpha.RoleBasedGroup{
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
