package dependency

import (
	"context"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

type DependencyManager interface {
	SortRoles(ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup) ([]*workloadsv1alpha1.RoleSpec, error)
	CheckDependencyReady(
		ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
	) (bool, error)
}
