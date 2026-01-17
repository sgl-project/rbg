package utils

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

func GetRBG(ctx context.Context, client client.Client, key types.NamespacedName) (rbg *workloadsv1alpha1.RoleBasedGroup, err error) {
	rbg = &workloadsv1alpha1.RoleBasedGroup{}
	err = client.Get(ctx, key, rbg)
	return rbg, err
}
