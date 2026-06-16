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

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var rbgGVR = schema.GroupVersionResource{
	Group:    "workloads.x-k8s.io",
	Version:  "v1alpha2",
	Resource: "rolebasedgroups",
}

// StressClient wraps kubernetes clients for stress testing.
type StressClient struct {
	dynamic   dynamic.Interface
	clientset kubernetes.Interface
}

// NewStressClient creates a new stress client.
func NewStressClient(kubeconfig string) (*StressClient, error) {
	config, err := buildConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("build config: %w", err)
	}

	// Use high QPS/Burst for the stress client itself
	config.QPS = 200
	config.Burst = 400

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create clientset: %w", err)
	}

	return &StressClient{
		dynamic:   dynClient,
		clientset: clientset,
	}, nil
}

func buildConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}

// EnsureNamespace creates the namespace if it doesn't exist.
func (c *StressClient) EnsureNamespace(ctx context.Context, namespace string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"rbg-stress-test": "true",
			},
		},
	}
	_, err := c.clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// CreateRBG creates an RBG resource.
func (c *StressClient) CreateRBG(ctx context.Context, namespace string, rbg *unstructured.Unstructured) error {
	_, err := c.dynamic.Resource(rbgGVR).Namespace(namespace).Create(ctx, rbg, metav1.CreateOptions{})
	return err
}

// UpdateRBG updates an RBG resource and returns the new metadata.generation.
func (c *StressClient) UpdateRBG(ctx context.Context, namespace string, rbg *unstructured.Unstructured) (int64, error) {
	result, err := c.dynamic.Resource(rbgGVR).Namespace(namespace).Update(ctx, rbg, metav1.UpdateOptions{})
	if err != nil {
		return 0, err
	}
	return result.GetGeneration(), nil
}

// GetRBG gets an RBG resource.
func (c *StressClient) GetRBG(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	return c.dynamic.Resource(rbgGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
}

// DeleteRBG deletes an RBG resource.
func (c *StressClient) DeleteRBG(ctx context.Context, namespace, name string) error {
	return c.dynamic.Resource(rbgGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// WaitForRBGReady polls until the RBG has Ready=True condition or context is done.
func (c *StressClient) WaitForRBGReady(ctx context.Context, namespace, name string) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		rbg, err := c.GetRBG(ctx, namespace, name)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if isRBGReady(rbg) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// WaitForRBGObservedGeneration polls until status.observedGeneration >= generation.
func (c *StressClient) WaitForRBGObservedGeneration(ctx context.Context, namespace, name string, generation int64) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		rbg, err := c.GetRBG(ctx, namespace, name)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		observed, found, _ := unstructured.NestedInt64(rbg.Object, "status", "observedGeneration")
		if found && observed >= generation {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// WaitForRBGDeleted polls until the RBG is gone or context is done.
func (c *StressClient) WaitForRBGDeleted(ctx context.Context, namespace, name string) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, err := c.GetRBG(ctx, namespace, name)
		if apierrors.IsNotFound(err) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// WatchRBGs returns a watch interface for RBGs in the namespace.
func (c *StressClient) WatchRBGs(ctx context.Context, namespace string) (watch.Interface, error) {
	return c.dynamic.Resource(rbgGVR).Namespace(namespace).Watch(ctx, metav1.ListOptions{})
}

// GetControllerInfo fetches details about the deployed RBG controller.
func (c *StressClient) GetControllerInfo(ctx context.Context, controllerNamespace, controllerLabel string) ControllerInfo {
	info := ControllerInfo{}

	deploy, err := c.clientset.AppsV1().Deployments(controllerNamespace).Get(ctx, "rbgs-controller-manager", metav1.GetOptions{})
	if err != nil {
		return info
	}

	info.Replicas = int(*deploy.Spec.Replicas)

	if len(deploy.Spec.Template.Spec.Containers) > 0 {
		container := deploy.Spec.Template.Spec.Containers[0]
		info.Image = container.Image
		info.Args = container.Args

		if lim := container.Resources.Limits; lim != nil {
			if cpu, ok := lim["cpu"]; ok {
				info.CPULimit = cpu.String()
			}
			if mem, ok := lim["memory"]; ok {
				info.MemoryLimit = mem.String()
			}
		}
		if req := container.Resources.Requests; req != nil {
			if cpu, ok := req["cpu"]; ok {
				info.CPURequest = cpu.String()
			}
			if mem, ok := req["memory"]; ok {
				info.MemoryRequest = mem.String()
			}
		}
	}

	// Get node where controller is running
	pods, err := c.clientset.CoreV1().Pods(controllerNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: controllerLabel,
	})
	if err == nil && len(pods.Items) > 0 {
		info.NodeName = pods.Items[0].Spec.NodeName
	}

	return info
}

// isRBGReady checks if the RBG has Ready=True condition.
func isRBGReady(rbg *unstructured.Unstructured) bool {
	conditions, found, err := unstructured.NestedSlice(rbg.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if cond["type"] == "Ready" && cond["status"] == "True" {
			return true
		}
	}
	return false
}
