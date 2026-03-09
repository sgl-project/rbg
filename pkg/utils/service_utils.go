package utils

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func GetCompatibleHeadlessServiceName(
	ctx context.Context, kclient client.Client, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
) (string, error) {
	svc := &corev1.Service{}
	err := kclient.Get(ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, svc)
	if err == nil {
		// if oldService exists, use old ServiceName
		return rbg.GetWorkloadName(role), nil
	} else {
		// if oldService not exists, use new ServiceName
		if apierrors.IsNotFound(err) {
			return rbg.GetServiceName(role), nil
		}
	}
	return "", err
}

// GetCompatibleHeadlessServiceNameV2 is the v1alpha2 version of GetCompatibleHeadlessServiceName
func GetCompatibleHeadlessServiceNameV2(
	ctx context.Context, kclient client.Client, rbg *workloadsv1alpha2.RoleBasedGroup, role *workloadsv1alpha2.RoleSpec,
) (string, error) {
	svc := &corev1.Service{}
	err := kclient.Get(ctx, types.NamespacedName{Name: rbg.GetWorkloadName(role), Namespace: rbg.Namespace}, svc)
	if err == nil {
		// if oldService exists, use old ServiceName
		return rbg.GetWorkloadName(role), nil
	} else {
		// if oldService not exists, use new ServiceName
		if apierrors.IsNotFound(err) {
			return rbg.GetServiceName(role), nil
		}
	}
	return "", err
}
