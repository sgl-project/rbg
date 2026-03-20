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

package reconciler

import (
	"context"
	"fmt"
	"reflect"

	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scheduler"
)

type WorkloadReconciler interface {
	Validate(ctx context.Context, role *workloadsv1alpha2.RoleSpec) error
	Reconciler(
		ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec,
		rollingUpdateStrategy *workloadsv1alpha2.RollingUpdate, revisionKey string) error
	ConstructRoleStatus(
		ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec,
	) (workloadsv1alpha2.RoleStatus, bool, error)
	CheckWorkloadReady(
		ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec,
	) (bool, error)
	CleanupOrphanedWorkloads(ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup) error
	RecreateWorkload(ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec) error
}

// PodGroupManagerSetter is an optional interface implemented by WorkloadReconcilers
// that support injecting a PodGroupManager for gang-scheduling label injection.
type PodGroupManagerSetter interface {
	SetPodGroupManager(m scheduler.PodGroupManager)
}

func NewWorkloadReconciler(
	workload workloadsv1alpha2.WorkloadSpec, scheme *runtime.Scheme, client client.Client,
) (WorkloadReconciler, error) {
	switch {
	case workload.String() == constants.DeploymentWorkloadType:
		return NewDeploymentReconciler(scheme, client), nil
	case workload.String() == constants.StatefulSetWorkloadType:
		return NewStatefulSetReconciler(scheme, client), nil
	case workload.String() == constants.LeaderWorkerSetWorkloadType:
		return NewLeaderWorkerSetReconciler(scheme, client), nil
	case workload.String() == constants.RoleInstanceSetWorkloadType:
		return NewRoleInstanceSetReconciler(scheme, client), nil
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
	case *workloadsv1alpha2.RoleInstanceSet:
		if o2, ok := obj2.(*workloadsv1alpha2.RoleInstanceSet); ok {
			if o1.Generation == o2.Generation && reflect.DeepEqual(o1.Status, o2.Status) {
				return true, nil
			}
			return false, fmt.Errorf("roleInstanceSet generation or status not equal")
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
