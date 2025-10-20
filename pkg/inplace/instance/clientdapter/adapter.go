/*
Copyright 2025.

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

	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

type Adapter interface {
	GetInstance(namespace, name string) (*appsv1alpha1.Instance, error)
	UpdateInstance(instance *appsv1alpha1.Instance) (*appsv1alpha1.Instance, error)
	UpdateInstanceStatus(instance *appsv1alpha1.Instance) error
}

type AdapterWithPatch interface {
	Adapter
	PatchInstance(instance *appsv1alpha1.Instance, patch client.Patch) (*appsv1alpha1.Instance, error)
}

type AdapterRuntimeClient struct {
	client.Client
}

func (c *AdapterRuntimeClient) GetInstance(namespace, name string) (*appsv1alpha1.Instance, error) {
	instance := &appsv1alpha1.Instance{}
	err := c.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, instance)
	return instance, err
}

func (c *AdapterRuntimeClient) UpdateInstance(instance *appsv1alpha1.Instance) (*appsv1alpha1.Instance, error) {
	return instance, c.Update(context.TODO(), instance)
}

func (c *AdapterRuntimeClient) UpdateInstanceStatus(instance *appsv1alpha1.Instance) error {
	return c.Status().Update(context.TODO(), instance)
}

func (c *AdapterRuntimeClient) PatchInstance(instance *appsv1alpha1.Instance, patch client.Patch) (*appsv1alpha1.Instance, error) {
	return instance, c.Patch(context.TODO(), instance, patch)
}
