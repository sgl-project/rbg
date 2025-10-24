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

package util

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	rbgclient "sigs.k8s.io/rbgs/client-go/clientset/versioned"
)

func GetRBGClient(cf *genericclioptions.ConfigFlags) (*rbgclient.Clientset, error) {
	config, err := cf.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}
	return rbgclient.NewForConfig(config)
}

func GetK8SClientSet(cf *genericclioptions.ConfigFlags) (*kubernetes.Clientset, error) {
	config, err := cf.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}
	return kubernetes.NewForConfig(config)
}

func GetDefaultDynamicClient(cf *genericclioptions.ConfigFlags) (*dynamic.DynamicClient, error) {
	// Fetch Kubernetes configuration
	config, err := cf.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	// Create a dynamic client
	dynamicClient, err := dynamic.NewForConfig(config)

	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}
	return dynamicClient, nil
}

func GetRBGObjectByDynamicClient(ctx context.Context, name, namespace string, dynamicClient dynamic.Interface) (*unstructured.Unstructured, error) {
	// Define GVR
	gvr := schema.GroupVersionResource{
		Group:    "workloads.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "rolebasedgroups",
	}

	// Fetch the resource object
	resource, err := dynamicClient.Resource(gvr).Namespace(namespace).Get(
		ctx,
		name,
		metav1.GetOptions{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get RoleBasedGroup: %w", err)
	}
	return resource, nil
}
