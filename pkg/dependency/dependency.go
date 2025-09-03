package dependency

import (
	"context"
	"errors"
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
) ([]*workloadsv1alpha.RoleSpec, error) {
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

	var ret []*workloadsv1alpha.RoleSpec
	for _, roleName := range roleOrder {
		for i := range rbg.Spec.Roles {
			if rbg.Spec.Roles[i].Name == roleName {
				ret = append(ret, &rbg.Spec.Roles[i])
				break
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

// Use Depth-First Search (DFS) to build a topological sort and check for cycles
func dependencyOrder(ctx context.Context, dependencies map[string][]string) ([]string, error) {
	logger := log.FromContext(ctx)

	// sort map by keys to avoid random order
	keys := make([]string, 0, len(dependencies))
	for k := range dependencies {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Track visited nodes and detect cycles
	completed := make(map[string]bool)
	temp := make(map[string]bool)
	order := make([]string, 0)
	path := make([]string, 0) // Stack to keep track of current path

	var visit func(string) error
	visit = func(role string) error {
		// Check if already in temp (indicates cycle)
		if temp[role] {
			// Cycle detected, print roles in path
			err := errors.New("cycle detected")
			logger.Error(err, "cycle detected", "cycle", append(path, role))
			return err
		}
		// Skip if already visited
		if completed[role] {
			return nil
		}
		// Mark as temp visited
		temp[role] = true
		path = append(path, role) // Add role to path

		// Visit all dependencies first
		for _, dep := range dependencies[role] {
			if err := visit(dep); err != nil {
				return err // Cycle detected
			}
		}

		// Mark as permanently visited and add to order
		temp[role] = false
		completed[role] = true
		order = append(order, role)
		path = path[:len(path)-1] // Remove role from path
		return nil
	}

	// Visit all roles
	for _, role := range keys {
		if !completed[role] {
			if err := visit(role); err != nil {
				return nil, err // Return nil if cycle detected
			}
		}
	}

	return order, nil
}
