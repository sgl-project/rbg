package v1alpha2

import (
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/constants"
)

// WithGangScheduling enables kube scheduler-plugins based gang scheduling
// by setting the GangSchedulingAnnotationKey annotation to "true".
func (rbgWrapper *RoleBasedGroupWrapper) WithGangScheduling() *RoleBasedGroupWrapper {
	if rbgWrapper.Annotations == nil {
		rbgWrapper.Annotations = make(map[string]string)
	}
	rbgWrapper.Annotations[constants.GangSchedulingAnnotationKey] = "true"
	return rbgWrapper
}

// WithVolcanoGangScheduling enables Volcano based gang scheduling
// by setting the GangSchedulingAnnotationKey annotation to "true" and
// the GangSchedulingVolcanoQueueKey annotation to the given queue.
func (rbgWrapper *RoleBasedGroupWrapper) WithVolcanoGangScheduling(queue string) *RoleBasedGroupWrapper {
	if rbgWrapper.Annotations == nil {
		rbgWrapper.Annotations = make(map[string]string)
	}
	rbgWrapper.Annotations[constants.GangSchedulingAnnotationKey] = "true"
	rbgWrapper.Annotations[constants.GangSchedulingVolcanoQueueKey] = queue
	return rbgWrapper
}

type RoleBasedGroupWrapper struct {
	workloadsv1alpha2.RoleBasedGroup
}

func (rbgWrapper *RoleBasedGroupWrapper) Obj() *workloadsv1alpha2.RoleBasedGroup {
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

func (rbgWrapper *RoleBasedGroupWrapper) WithRoles(roles []workloadsv1alpha2.RoleSpec) *RoleBasedGroupWrapper {
	rbgWrapper.Spec.Roles = roles
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupWrapper) AddRole(role workloadsv1alpha2.RoleSpec) *RoleBasedGroupWrapper {
	rbgWrapper.Spec.Roles = append(rbgWrapper.Spec.Roles, role)
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupWrapper) WithRoleTemplates(
	templates []workloadsv1alpha2.RoleTemplate,
) *RoleBasedGroupWrapper {
	rbgWrapper.Spec.RoleTemplates = templates
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupWrapper) AddRoleTemplate(
	template workloadsv1alpha2.RoleTemplate,
) *RoleBasedGroupWrapper {
	rbgWrapper.Spec.RoleTemplates = append(rbgWrapper.Spec.RoleTemplates, template)
	return rbgWrapper
}

func (rbgWrapper *RoleBasedGroupWrapper) WithStatus(
	status workloadsv1alpha2.RoleBasedGroupStatus,
) *RoleBasedGroupWrapper {
	rbgWrapper.Status = status
	return rbgWrapper
}

func BuildBasicRoleBasedGroup(name, ns string) *RoleBasedGroupWrapper {
	return &RoleBasedGroupWrapper{
		workloadsv1alpha2.RoleBasedGroup{
			TypeMeta: v1.TypeMeta{
				APIVersion: "workloads.x-k8s.io/v1alpha2",
				Kind:       "RoleBasedGroup",
			},
			ObjectMeta: v1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels: map[string]string{
					constants.GroupNameLabelKey: name,
				},
				UID: "rbg-test-uid",
			},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{
					BuildStandaloneRole("test-role").Obj(),
				},
			},
		},
	}
}
