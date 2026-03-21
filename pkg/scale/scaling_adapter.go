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
