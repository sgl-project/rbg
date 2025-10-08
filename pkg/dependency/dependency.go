package dependency

import (
	"context"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/reconciler"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type DefaultDependencyManager struct {
	scheme *runtime.Scheme
	client client.Client
}

var _ DependencyManager = &DefaultDependencyManager{}

func NewDefaultDependencyManager(scheme *runtime.Scheme, client client.Client) *DefaultDependencyManager {
	return &DefaultDependencyManager{scheme: scheme, client: client}
}

func (m *DefaultDependencyManager) SortRoles(
	ctx context.Context, rbg *workloadsv1alpha.RoleBasedGroup,
) ([][]*workloadsv1alpha.RoleSpec, error) {
	logger := log.FromContext(ctx)
	if len(rbg.Spec.Roles) == 0 {
		logger.Info("warning: rbg has no roles, skip")
		return nil, nil
	}

	roleNameList := make([]string, 0)
	for _, role := range rbg.Spec.Roles {
		roleNameList = append(roleNameList, role.Name)
	}
	roleDependency := make(map[string][]string)

	for _, role := range rbg.Spec.Roles {
		if len(role.Dependencies) > 0 {
			for _, d := range role.Dependencies {
				if !utils.ContainsString(roleNameList, d) {
					return nil, fmt.Errorf("role [%s] with dependency role [%s] not found in rbg", role.Name, d)
				}
			}

			roleDependency[role.Name] = role.Dependencies
		} else {
			roleDependency[role.Name] = []string{}
		}
	}

	roleOrder, err := dependencyOrder(ctx, roleDependency)
	if err != nil {
		return nil, fmt.Errorf("failed to sort roles by dependency order: %v", err)
	}
	logger.V(1).Info("roleOrder", "roleOrder", roleOrder)

	ret := make([][]*workloadsv1alpha.RoleSpec, len(roleOrder))
	for order, roles := range roleOrder {
		ret[order] = make([]*workloadsv1alpha.RoleSpec, 0, len(roles))
		for _, roleName := range roles {
			for i := range rbg.Spec.Roles {
				if rbg.Spec.Roles[i].Name == roleName {
					ret[order] = append(ret[order], &rbg.Spec.Roles[i])
					break
				}
			}
		}
	}
	return ret, nil

}

func (m *DefaultDependencyManager) CheckDependencyReady(
	ctx context.Context, rbg *workloadsv1alpha.RoleBasedGroup, role *workloadsv1alpha.RoleSpec,
) (bool, error) {

	for _, dep := range role.Dependencies {
		depRole, err := rbg.GetRole(dep)
		if err != nil {
			return false, err
		}
		r, err := reconciler.NewWorkloadReconciler(depRole.Workload, m.scheme, m.client)
		if err != nil {
			return false, err
		}
		ready, err := r.CheckWorkloadReady(ctx, rbg, depRole)
		if err != nil {
			return false, err
		}
		if !ready {
			return false, nil
		}
	}

	return true, nil
}

type roleWithOrder struct {
	name string
	// order is the order of the role in the topological sort
	// >=0 computed;
	// -1 progressing;
	// -2 not started;
	order int
}

// Use Depth-First Search (DFS) to build a topological sort and check for cycles
func dependencyOrder(ctx context.Context, dependencies map[string][]string) ([][]string, error) {
	logger := log.FromContext(ctx)

	// sort map by keys to avoid random order
	keys := make([]string, 0, len(dependencies))
	for k := range dependencies {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	roleWithOrderList := make([]*roleWithOrder, 0)
	for _, k := range keys {
		roleWithOrderList = append(roleWithOrderList, &roleWithOrder{name: k, order: -2})
	}

	roleWithOrderMap := make(map[string]*roleWithOrder)
	for _, roleWithOrder := range roleWithOrderList {
		roleWithOrderMap[roleWithOrder.name] = roleWithOrder
	}

	var visit func(role *roleWithOrder, paths []string) (int, error)
	visit = func(role *roleWithOrder, paths []string) (int, error) {
		// track the current path to detect cycles
		paths = append(paths, role.name)
		defer func() {
			paths = paths[:len(paths)-1]
		}()

		if role.order >= 0 {
			return role.order, nil
		}
		if role.order == -1 {
			err := fmt.Errorf("cycle detected for role '%s'", role.name)
			logger.Error(err, "cycle detected", "cycle", paths)
			return -1, err
		}
		role.order = -1
		maxOrder := 0
		for _, dep := range dependencies[role.name] {
			x, found := roleWithOrderMap[dep]
			if !found {
				err := fmt.Errorf("dependency '%s' not found for role '%s'", dep, role.name)
				logger.Error(err, "dependency not found", "dependency", dep)
				return -1, err
			}
			order, err := visit(x, paths)
			if err != nil {
				return -1, err
			}
			maxOrder = max(maxOrder, order+1)
		}
		role.order = maxOrder
		return role.order, nil
	}

	// Visit all roles
	for _, roleWithOrder := range roleWithOrderList {
		if roleWithOrder.order == -2 {
			_, err := visit(roleWithOrder, make([]string, 0))
			if err != nil {
				return nil, err
			}
		}
	}

	maxOrder := 0
	for _, roleWithOrder := range roleWithOrderList {
		maxOrder = max(maxOrder, roleWithOrder.order)
	}

	order := make([][]string, maxOrder+1)
	for _, roleWithOrder := range roleWithOrderList {
		order[roleWithOrder.order] = append(order[roleWithOrder.order], roleWithOrder.name)
	}

	return order, nil
}
