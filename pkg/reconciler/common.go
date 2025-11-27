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
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func ConstructRoleStatue(rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec, currentReplicas, currentReady, updatedReplicas int32) (workloadsv1alpha1.RoleStatus, bool) {
	updateStatus := false
	status, found := rbg.GetRoleStatus(role.Name)
	if !found || status.Replicas != currentReplicas ||
		status.ReadyReplicas != currentReady ||
		status.UpdatedReplicas != updatedReplicas {
		status = workloadsv1alpha1.RoleStatus{
			Name:            role.Name,
			Replicas:        currentReplicas,
			ReadyReplicas:   currentReady,
			UpdatedReplicas: updatedReplicas,
		}
		updateStatus = true
	}
	return status, updateStatus
}

func CleanupOrphanedObjs(ctx context.Context, c client.Client, rbg *workloadsv1alpha1.RoleBasedGroup, gvk schema.GroupVersionKind) error {
	logger := log.FromContext(ctx)

	objList := &unstructured.UnstructuredList{}
	objList.SetGroupVersionKind(gvk)
	// list obj managed by rbg
	if err := c.List(
		ctx, objList, client.InNamespace(rbg.Namespace),
		client.MatchingLabels(
			map[string]string{
				workloadsv1alpha1.SetNameLabelKey: rbg.Name,
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
			if role.Workload.Kind == obj.GetObjectKind().GroupVersionKind().Kind && rbg.GetWorkloadName(&role) == obj.GetName() {
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

func RecreateObj(ctx context.Context, c client.Client, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec, gvk schema.GroupVersionKind) error {
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
