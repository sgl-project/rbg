package dependency

import (
	"context"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

type DependencyManager interface {
	SortRoles(ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup) ([][]*workloadsv1alpha2.RoleSpec, error)
	CheckDependencyReady(
		ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec,
	) (bool, error)
}
