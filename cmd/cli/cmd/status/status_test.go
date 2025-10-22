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

package status

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic/fake"
)

var name string

func init() {
	name = "test-rbg"
}

func TestParseStatus(t *testing.T) {
	resource := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "workloads.x-k8s.io/v1alpha1",
			"kind":       "RoleBasedGroup",
			"metadata": map[string]interface{}{
				"name":      "test-rbg",
				"namespace": "default",
			},
			"status": map[string]interface{}{
				"roleStatuses": []interface{}{
					map[string]interface{}{
						"name":          "worker",
						"replicas":      int64(3),
						"readyReplicas": int64(2),
					},
					map[string]interface{}{
						"name":          "manager",
						"replicas":      int64(1),
						"readyReplicas": int64(1),
					},
				},
			},
		},
	}

	roleStatuses, err := parseStatus(resource)
	assert.NoError(t, err)
	assert.Len(t, roleStatuses, 2)
	assert.Equal(t, "worker", getString(roleStatuses[0], "name"))
	assert.Equal(t, int64(3), getInt64(roleStatuses[0], "replicas"))
	assert.Equal(t, int64(2), getInt64(roleStatuses[0], "readyReplicas"))
}

func TestParseStatusErrors(t *testing.T) {
	// no status
	resourceNoStatus := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "workloads.x-k8s.io/v1alpha1",
			"kind":       "RoleBasedGroup",
			"metadata": map[string]interface{}{
				"name":      "test-rbg",
				"namespace": "default",
			},
		},
	}

	_, err := parseStatus(resourceNoStatus)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "status not found")

	// no roleStatuses
	resourceNoRoleStatuses := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "workloads.x-k8s.io/v1alpha1",
			"kind":       "RoleBasedGroup",
			"metadata": map[string]interface{}{
				"name":      "test-rbg",
				"namespace": "default",
			},
			"status": map[string]interface{}{
				"phase": "Ready",
			},
		},
	}

	_, err = parseStatus(resourceNoRoleStatuses)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "roleStatuses not found")
}

func TestRunFunctionWithFakeClient(t *testing.T) {
	testRBG := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "workloads.x-k8s.io/v1alpha1",
			"kind":       "RoleBasedGroup",
			"metadata": map[string]interface{}{
				"name":              "test-rbg",
				"namespace":         "default",
				"creationTimestamp": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
			},
			"spec": map[string]interface{}{
				"roles": []interface{}{
					map[string]interface{}{
						"name": "worker",
					},
				},
			},
			"status": map[string]interface{}{
				"roleStatuses": []interface{}{
					map[string]interface{}{
						"name":          "worker",
						"replicas":      int64(3),
						"readyReplicas": int64(2),
					},
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	client := fake.NewSimpleDynamicClient(scheme, testRBG)

	resource, err := client.Resource(
		schema.GroupVersionResource{
			Group:    "workloads.x-k8s.io",
			Version:  "v1alpha1",
			Resource: "rolebasedgroups",
		},
	).Namespace("default").Get(context.TODO(), "test-rbg", metav1.GetOptions{})

	assert.NoError(t, err)
	assert.NotNil(t, resource)

	roleStatuses, err := parseStatus(resource)
	assert.NoError(t, err)
	assert.Len(t, roleStatuses, 1)
	old := statusOpts
	defer func() {
		statusOpts = old
	}()
	statusOpts = StatusOptions{cf: &genericclioptions.ConfigFlags{}}
	printReport(resource, roleStatuses, "")
}

func TestRun(t *testing.T) {
	roleBasedGroup := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "workloads.x-k8s.io/v1alpha1",
			"kind":       "RoleBasedGroup",
			"metadata": map[string]interface{}{
				"name":              "test-rbg",
				"namespace":         "default",
				"creationTimestamp": time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
			},
			"status": map[string]interface{}{
				"roleStatuses": []interface{}{
					map[string]interface{}{
						"name":          "worker",
						"replicas":      int64(3),
						"readyReplicas": int64(2),
					},
					map[string]interface{}{
						"name":          "manager",
						"replicas":      int64(1),
						"readyReplicas": int64(1),
					},
				},
			},
		},
	}

	// fake dynamic client
	scheme := runtime.NewScheme()
	client := fake.NewSimpleDynamicClient(scheme, roleBasedGroup)

	old := statusOpts
	defer func() {
		statusOpts = old
	}()
	statusOpts = StatusOptions{cf: &genericclioptions.ConfigFlags{}}

	ns := "default"
	statusOpts.cf.Namespace = &ns
	args := []string{"test-rbg"}

	err := runWithClient(nil, args[0], client)
	assert.NoError(t, err)
}
