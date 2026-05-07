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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func ConstructRoleStatue(rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec, currentReplicas, currentReady, updatedReplicas int32) workloadsv1alpha2.RoleStatus {
	status, found := rbg.GetRoleStatus(role.Name)
	if !found || status.Replicas != currentReplicas ||
		status.ReadyReplicas != currentReady ||
		status.UpdatedReplicas != updatedReplicas {
		status = workloadsv1alpha2.RoleStatus{
			Name:            role.Name,
			Replicas:        currentReplicas,
			ReadyReplicas:   currentReady,
			UpdatedReplicas: updatedReplicas,
		}
	}
	return status
}

// ConstructWorkloadRoleStatus handles the common pattern of constructing a role status
// from a workload that may not have observed the latest generation yet. If the
// workload's controller hasn't observed the latest generation, it returns an empty
// status (don't treat this as an error, otherwise constructAndUpdateRoleStatuses
// would bail out before calling updateRBGStatus). Otherwise it delegates to
// ConstructRoleStatue.
func ConstructWorkloadRoleStatus(
	ctx context.Context,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	role *workloadsv1alpha2.RoleSpec,
	replicas, readyReplicas, updatedReplicas int32,
	generation, observedGeneration int64,
) workloadsv1alpha2.RoleStatus {
	if observedGeneration < generation {
		// Don't return a zeroed RoleStatus here: doing so would overwrite the
		// previously recorded replicas/readyReplicas with zeros in updateRBGStatus,
		// causing transient but incorrect status flickering. Instead, return the
		// last known status from RBG to preserve the current observed state.
		logger := log.FromContext(ctx)
		logger.V(1).Info("workload status not yet observed, preserving last known status for this role",
			"role", role.Name,
			"generation", generation,
			"observedGeneration", observedGeneration,
		)
		if status, found := rbg.GetRoleStatus(role.Name); found {
			return status
		}
		return workloadsv1alpha2.RoleStatus{Name: role.Name}
	}
	return ConstructRoleStatue(rbg, role, replicas, readyReplicas, updatedReplicas)
}

func CleanupOrphanedObjs(ctx context.Context, c client.Client, rbg *workloadsv1alpha2.RoleBasedGroup, gvk schema.GroupVersionKind) error {
	logger := log.FromContext(ctx)

	objList := &unstructured.UnstructuredList{}
	objList.SetGroupVersionKind(gvk)
	// list obj managed by rbg
	if err := c.List(
		ctx, objList, client.InNamespace(rbg.Namespace),
		client.MatchingLabels(
			map[string]string{
				constants.GroupNameLabelKey: rbg.Name,
			},
		),
	); err != nil {
		return err
	}

	for _, obj := range objList.Items {
		if !v1.IsControlledBy(&obj, rbg) {
			continue
		}
		found := false
		for _, role := range rbg.Spec.Roles {
			if role.GetWorkloadSpec().Kind == obj.GetObjectKind().GroupVersionKind().Kind && rbg.GetWorkloadName(&role) == obj.GetName() {
				found = true
				break
			}
		}
		if !found {
			if err := c.Delete(ctx, &obj); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete obj %s error: %s", obj.GetName(), err.Error())
			}
			// The deletion of headless services depends on its own reference
			logger.Info("delete obj",
				"gvk", obj.GetObjectKind().GroupVersionKind().String(),
				"namespace", obj.GetNamespace(),
				"obj", obj.GetName())
		}
	}

	return nil
}

func RecreateObj(ctx context.Context, c client.Client, rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec, gvk schema.GroupVersionKind) error {
	logger := log.FromContext(ctx)
	if rbg == nil || role == nil {
		return nil
	}

	objName := rbg.GetWorkloadName(role)
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)

	err := c.Get(ctx, types.NamespacedName{Name: objName, Namespace: rbg.Namespace}, obj)
	// if obj is not found, skip delete obj
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	logger.Info(fmt.Sprintf("Recreate obj workload, delete obj kind: %s, namespace: %s, name: %s", gvk.Kind, rbg.Namespace, objName))
	if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	// wait new obj create
	var retErr error
	err = wait.PollUntilContextTimeout(
		ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
			newObj := &unstructured.Unstructured{}
			newObj.SetGroupVersionKind(gvk)
			retErr = c.Get(ctx, types.NamespacedName{Name: objName, Namespace: rbg.Namespace}, newObj)
			if retErr != nil {
				if apierrors.IsNotFound(retErr) {
					return false, nil
				}
				return false, retErr
			}
			return true, nil
		},
	)

	if err != nil {
		logger.Error(retErr, "wait new obj creating error")
		return retErr
	}

	return nil
}
