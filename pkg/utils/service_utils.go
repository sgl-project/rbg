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

package utils

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// GetCompatibleHeadlessServiceName returns the headless service name for a v1alpha2 RoleBasedGroup role.
func GetCompatibleHeadlessServiceName(
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
