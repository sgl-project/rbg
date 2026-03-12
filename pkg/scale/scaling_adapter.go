package scale

import (
	"fmt"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func GenerateScalingAdapterName(rbgName, roleName string) string {
	return fmt.Sprintf("%s-%s", rbgName, roleName)
}

func IsScalingAdapterManagedByRBG(
	scalingAdapter *workloadsv1alpha2.RoleBasedGroupScalingAdapter,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) bool {
	if scalingAdapter == nil || rbg == nil {
		return false
	}

	return scalingAdapter.ContainsRBGOwner(rbg)
}

func IsScalingAdapterEnable(roleSpec *workloadsv1alpha2.RoleSpec) bool {
	if roleSpec == nil || roleSpec.ScalingAdapter == nil {
		return false
	}
	return roleSpec.ScalingAdapter.Enable
}
