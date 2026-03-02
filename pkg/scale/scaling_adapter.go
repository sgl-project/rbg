package scale

import (
	"fmt"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func GenerateScalingAdapterName(rbgName, roleName string) string {
	return fmt.Sprintf("%s-%s", rbgName, roleName)
}

// IsScalingAdapterManagedByRBG checks if the scaling adapter is managed by the given RBG.
// Note: RoleBasedGroupScalingAdapter is a shared object between v1alpha1 and v1alpha2.
func IsScalingAdapterManagedByRBG(
	scalingAdapter *workloadsv1alpha1.RoleBasedGroupScalingAdapter,
	rbg *workloadsv1alpha2.RoleBasedGroup,
) bool {
	if scalingAdapter == nil || rbg == nil {
		return false
	}

	// Check owner references - we compare by UID
	for _, ref := range scalingAdapter.OwnerReferences {
		if ref.UID == rbg.UID {
			return true
		}
	}
	return false
}

// IsScalingAdapterManagedByRBGV1 checks if the scaling adapter is managed by the given v1alpha1 RBG.
// This is a compatibility wrapper for controllers still using v1alpha1.
func IsScalingAdapterManagedByRBGV1(
	scalingAdapter *workloadsv1alpha1.RoleBasedGroupScalingAdapter,
	rbg *workloadsv1alpha1.RoleBasedGroup,
) bool {
	if scalingAdapter == nil || rbg == nil {
		return false
	}

	// Check owner references - we compare by UID
	for _, ref := range scalingAdapter.OwnerReferences {
		if ref.UID == rbg.UID {
			return true
		}
	}
	return false
}

func IsScalingAdapterEnable(roleSpec *workloadsv1alpha2.RoleSpec) bool {
	if roleSpec == nil || roleSpec.ScalingAdapter == nil {
		return false
	}
	return roleSpec.ScalingAdapter.Enable
}
