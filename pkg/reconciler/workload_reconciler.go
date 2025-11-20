package reconciler

import (
	"context"
	"fmt"
	"reflect"

	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

type WorkloadReconciler interface {
	Validate(ctx context.Context, role *workloadsv1alpha1.RoleSpec) error
	Reconciler(
		ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
		rollingUpdateStrategy *workloadsv1alpha1.RollingUpdate, revisionKey string) error
	ConstructRoleStatus(
		ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
	) (workloadsv1alpha1.RoleStatus, bool, error)
	CheckWorkloadReady(
		ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
	) (bool, error)
	CleanupOrphanedWorkloads(ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup) error
	RecreateWorkload(ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec) error
}

func NewWorkloadReconciler(
	workload workloadsv1alpha1.WorkloadSpec, scheme *runtime.Scheme, client client.Client,
) (WorkloadReconciler, error) {
	switch {
	case workload.String() == workloadsv1alpha1.DeploymentWorkloadType:
		return NewDeploymentReconciler(scheme, client), nil
	case workload.String() == workloadsv1alpha1.StatefulSetWorkloadType:
		return NewStatefulSetReconciler(scheme, client), nil
	case workload.String() == workloadsv1alpha1.LeaderWorkerSetWorkloadType:
		return NewLeaderWorkerSetReconciler(scheme, client), nil
	default:
		return nil, fmt.Errorf("unsupported workload type: %s", workload.String())
	}
}

// WorkloadEqual determines whether the workload needs reconciliation
func WorkloadEqual(obj1, obj2 interface{}) (bool, error) {
	switch o1 := obj1.(type) {
	case *appsv1.Deployment:
		if o2, ok := obj2.(*appsv1.Deployment); ok {
			if equal, err := semanticallyEqualDeployment(o1, o2, true); !equal {
				return false, fmt.Errorf("deploy not equal, error: %s", err.Error())
			}
			return true, nil
		}
	case *appsv1.StatefulSet:
		if o2, ok := obj2.(*appsv1.StatefulSet); ok {
			if equal, err := semanticallyEqualStatefulSet(o1, o2, true); !equal {
				return false, fmt.Errorf("sts: %s/%s not equal, error: %s", o1.Namespace, o1.Name, err.Error())
			}
			if o1.Generation != o2.Generation {
				return false, fmt.Errorf("sts: %s/%s generation not equal", o1.Namespace, o1.Name)
			}
			return true, nil
		}
	case *lwsv1.LeaderWorkerSet:
		if o2, ok := obj2.(*lwsv1.LeaderWorkerSet); ok {
			if equal, err := semanticallyEqualLeaderWorkerSet(o1, o2, true); !equal {
				return false, fmt.Errorf("lws not equal, error: %s", err.Error())
			}
			return true, nil
		}
	}

	return false, fmt.Errorf("not support workload: %v", reflect.TypeOf(obj1))
}
