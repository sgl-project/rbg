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

package v1alpha1

import (
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	workloadsv1alpha "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

type RoleBasedGroupSetWrapper struct {
	workloadsv1alpha.RoleBasedGroupSet
}

func (rsWrapper *RoleBasedGroupSetWrapper) Obj() *workloadsv1alpha.RoleBasedGroupSet {
	return &rsWrapper.RoleBasedGroupSet
}

func (rsWrapper *RoleBasedGroupSetWrapper) WithName(name string) *RoleBasedGroupSetWrapper {
	rsWrapper.Name = name
	return rsWrapper
}

func (rsWrapper *RoleBasedGroupSetWrapper) WithNamespace(namespace string) *RoleBasedGroupSetWrapper {
	rsWrapper.Namespace = namespace
	return rsWrapper
}

func (rsWrapper *RoleBasedGroupSetWrapper) WithAnnotations(annotations map[string]string) *RoleBasedGroupSetWrapper {
	rsWrapper.Annotations = annotations
	return rsWrapper
}

func (rsWrapper *RoleBasedGroupSetWrapper) WithReplicas(replicas int32) *RoleBasedGroupSetWrapper {
	rsWrapper.Spec.Replicas = &replicas
	return rsWrapper
}

func BuildBasicRoleBasedGroupSet(name, ns string) *RoleBasedGroupSetWrapper {
	return &RoleBasedGroupSetWrapper{
		workloadsv1alpha.RoleBasedGroupSet{
			ObjectMeta: v1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Spec: workloadsv1alpha.RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(1)),
				Template: BuildBasicRoleBasedGroup(name, ns).Obj().Spec,
			},
		},
	}
}
