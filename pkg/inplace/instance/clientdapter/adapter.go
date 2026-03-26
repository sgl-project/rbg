/*
Copyright 2025 The RBG Authors.

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

package clientdapter

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

type Adapter interface {
	GetRoleInstance(namespace, name string) (*workloadsv1alpha2.RoleInstance, error)
	UpdateRoleInstance(instance *workloadsv1alpha2.RoleInstance) (*workloadsv1alpha2.RoleInstance, error)
	UpdateRoleInstanceStatus(instance *workloadsv1alpha2.RoleInstance) error
}

type AdapterWithPatch interface {
	Adapter
	PatchRoleInstance(instance *workloadsv1alpha2.RoleInstance, patch client.Patch) (*workloadsv1alpha2.RoleInstance, error)
}

type AdapterRuntimeClient struct {
	client.Client
}

func (c *AdapterRuntimeClient) GetRoleInstance(namespace, name string) (*workloadsv1alpha2.RoleInstance, error) {
	instance := &workloadsv1alpha2.RoleInstance{}
	err := c.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, instance)
	return instance, err
}

func (c *AdapterRuntimeClient) UpdateRoleInstance(instance *workloadsv1alpha2.RoleInstance) (*workloadsv1alpha2.RoleInstance, error) {
	return instance, c.Update(context.TODO(), instance)
}

func (c *AdapterRuntimeClient) UpdateRoleInstanceStatus(instance *workloadsv1alpha2.RoleInstance) error {
	return c.Status().Update(context.TODO(), instance)
}

func (c *AdapterRuntimeClient) PatchRoleInstance(instance *workloadsv1alpha2.RoleInstance, patch client.Patch) (*workloadsv1alpha2.RoleInstance, error) {
	return instance, c.Patch(context.TODO(), instance, patch)
}
