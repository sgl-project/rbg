package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

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

	printReport(resource, roleStatuses, "")
}

func TestRun(t *testing.T) {
	// 创建测试数据
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

	// 创建 fake dynamic client
	scheme := runtime.NewScheme()
	client := fake.NewSimpleDynamicClient(scheme, roleBasedGroup)

	// 保存原始的全局变量以便恢复
	oldNamespace := namespace
	oldName := name
	defer func() {
		namespace = oldNamespace
		name = oldName
	}()

	// 设置测试参数
	namespace = "default"
	args := []string{"test-rbg"}

	err := runWithClient(nil, args, client)
	assert.NoError(t, err)
}
