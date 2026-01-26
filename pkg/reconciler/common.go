package reconciler

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

func CleanupOrphanedObjs(ctx context.Context, c client.Client, roles []*RoleData, gvk schema.GroupVersionKind) error {
	logger := log.FromContext(ctx)
	if len(roles) == 0 {
		return nil
	}
	// Get ownerInfo from the first role (all roles have the same owner)
	ownerInfo := roles[0].OwnerInfo

	objList := &unstructured.UnstructuredList{}
	objList.SetGroupVersionKind(gvk)
	// list obj managed by rbg
	if err := c.List(
		ctx, objList, client.InNamespace(ownerInfo.Namespace),
		client.MatchingLabels(
			map[string]string{
				workloadsv1alpha1.SetNameLabelKey: ownerInfo.Name,
			},
		),
	); err != nil {
		return err
	}

	for _, obj := range objList.Items {
		// Check if controlled by this RBG
		isControlled := false
		for _, owner := range obj.GetOwnerReferences() {
			if owner.UID == ownerInfo.UID {
				isControlled = true
				break
			}
		}
		if !isControlled {
			continue
		}

		found := false
		for _, roleData := range roles {
			if roleData.Spec.Workload.Kind == obj.GetObjectKind().GroupVersionKind().Kind && roleData.WorkloadName == obj.GetName() {
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

func RecreateObj(ctx context.Context, c client.Client, roleData *RoleData, gvk schema.GroupVersionKind) error {
	logger := log.FromContext(ctx)
	if roleData == nil || roleData.Spec == nil {
		return nil
	}

	objName := roleData.WorkloadName
	namespace := roleData.OwnerInfo.Namespace
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)

	err := c.Get(ctx, types.NamespacedName{Name: objName, Namespace: namespace}, obj)
	// if obj is not found, skip delete obj
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	logger.Info(fmt.Sprintf("Recreate obj workload, delete obj kind: %s, namespace: %s, name: %s", gvk.Kind, namespace, objName))
	if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	// wait new obj create
	var retErr error
	err = wait.PollUntilContextTimeout(
		ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
			newObj := &unstructured.Unstructured{}
			newObj.SetGroupVersionKind(gvk)
			retErr = c.Get(ctx, types.NamespacedName{Name: objName, Namespace: namespace}, newObj)
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
