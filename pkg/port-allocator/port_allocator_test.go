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

package port_allocator

import (
	"maps"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// testAllocator is a simple test implementation of PortAllocatorInterface
type testAllocator struct {
	startPort int32
	portRange int32
	usedPorts map[int32]bool
	nextPort  int32
}

func newTestAllocator(startPort, portRange int32) *PortAllocator {
	return &PortAllocator{
		pa: &testAllocator{
			startPort: startPort,
			portRange: portRange,
			usedPorts: make(map[int32]bool),
			nextPort:  startPort,
		},
	}
}

func (t *testAllocator) Start(_ client.Client) error {
	return nil
}

func (t *testAllocator) Release(port int32) error {
	delete(t.usedPorts, port)
	return nil
}

func (t *testAllocator) AllocateBatch(num int32) ([]int32, error) {
	result := make([]int32, num)
	for i := int32(0); i < num; i++ {
		port := t.nextPort
		t.usedPorts[port] = true
		t.nextPort++
		result[i] = port
	}
	return result, nil
}

func TestPodScopedPortAllocation(t *testing.T) {
	portAllocator = newTestAllocator(31000, 2000)

	podName := "test-pod-0"
	config := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "grpc-port", Env: "GRPC_PORT", Scope: PodScoped},
			{Name: "http-port", Env: "HTTP_PORT", Scope: PodScoped},
		},
	}

	annotations, err := AllocatePodScopedPorts(config, podName)
	require.NoError(t, err)
	assert.Len(t, annotations, 2)

	// Verify keys are formatted correctly
	grpcKey := FormatPodScopedPortKey(podName, "grpc-port")
	httpKey := FormatPodScopedPortKey(podName, "http-port")

	_, exists := annotations[grpcKey]
	assert.True(t, exists)
	_, exists = annotations[httpKey]
	assert.True(t, exists)

	// Verify different ports were allocated
	assert.NotEqual(t, annotations[grpcKey], annotations[httpKey])
}

func TestRoleScopedPortAllocation(t *testing.T) {
	portAllocator = newTestAllocator(30000, 2768)

	componentName := "leader"
	config := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "http-port", Env: "HTTP_PORT", Scope: RoleScoped},
		},
	}

	annotations, err := AllocateRoleScopedPorts(config, componentName)
	require.NoError(t, err)
	assert.Len(t, annotations, 1)

	// Verify key is formatted correctly
	key := FormatRoleScopedPortKey(componentName, "http-port")
	port, exists := annotations[key]
	assert.True(t, exists)
	assert.NotEmpty(t, port)
}

func TestInjectPortsIntoPod(t *testing.T) {
	portAllocator = newTestAllocator(30000, 2768)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-instance-leader-0",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "nginx", Image: "nginx:1.28.0"},
			},
		},
	}

	config := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "grpc-port", Env: "GRPC_PORT", AnnotationKey: "test/grpc-port", Scope: PodScoped},
			{Name: "http-port", Env: "HTTP_PORT", AnnotationKey: "test/http-port", Scope: RoleScoped},
		},
	}

	podAnnotations, err := AllocatePodScopedPorts(config, pod.Name)
	require.NoError(t, err)

	roleScopedAnnotations, err := AllocateRoleScopedPorts(config, "leader")
	require.NoError(t, err)

	// Merge annotations
	allAnnotations := maps.Clone(podAnnotations)
	maps.Copy(allAnnotations, roleScopedAnnotations)

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-instance",
			Labels:      map[string]string{constants.RoleTypeLabelKey: string(constants.ComponentsTemplateType)},
			Annotations: allAnnotations,
		},
	}

	err = InjectPortsIntoPod(pod, instance, config, "leader")
	require.NoError(t, err)

	// Verify environment variables were injected
	assert.Len(t, pod.Spec.Containers[0].Env, 2)

	// Verify annotations were injected
	assert.NotEmpty(t, pod.Annotations["test/grpc-port"])
	assert.NotEmpty(t, pod.Annotations["test/http-port"])
}

func TestGetReferencePodName(t *testing.T) {
	tests := []struct {
		name           string
		instance       *workloadsv1alpha2.RoleInstance
		componentName  string
		expectedResult string
	}{
		// CustomComponentsPattern
		{
			name: "worker to leader reference (ComponentsTemplateType)",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "rbg-prefill-0",
					Labels: map[string]string{constants.RoleTypeLabelKey: string(constants.ComponentsTemplateType)},
				},
			},
			componentName:  "leader",
			expectedResult: "rbg-prefill-0-leader-0",
		},
		{
			name: "leader to worker reference (ComponentsTemplateType)",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "rbg-prefill-0",
					Labels: map[string]string{constants.RoleTypeLabelKey: string(constants.ComponentsTemplateType)},
				},
			},
			componentName:  "worker",
			expectedResult: "rbg-prefill-0-worker-0",
		},
		// LeaderWorkerPattern
		{
			name: "worker to leader reference (LWS pattern)",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-prefill-0",
					Labels: map[string]string{constants.RoleTypeLabelKey: string(constants.LeaderWorkerSetTemplateType)},
				},
			},
			componentName:  "leader",
			expectedResult: "test-prefill-0-0",
		},
		{
			name: "leader reference from worker pod (LWS pattern)",
			instance: &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "test-prefill-0",
					Labels: map[string]string{constants.RoleTypeLabelKey: string(constants.LeaderWorkerSetTemplateType)},
				},
			},
			componentName:  "leader",
			expectedResult: "test-prefill-0-0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetReferencePodName(tt.instance, tt.componentName)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestPortRangeValidation(t *testing.T) {
	startPort := int32(30000)
	portRange := int32(2768)

	portAllocator = newTestAllocator(startPort, portRange)

	ports, err := AllocateBatch(10)
	require.NoError(t, err)

	for _, port := range ports {
		assert.GreaterOrEqual(t, port, startPort)
		assert.LessOrEqual(t, port, startPort+portRange-1)
	}
}

func TestMultiplePodsPodScopedPorts(t *testing.T) {
	portAllocator = newTestAllocator(30000, 2768)

	config := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "grpc-port", Env: "GRPC_PORT", Scope: PodScoped},
		},
	}

	portMap := make(map[string]string)
	for i := 0; i < 5; i++ {
		podName := "test-pod-" + strconv.Itoa(i)
		annotations, err := AllocatePodScopedPorts(config, podName)
		require.NoError(t, err)

		key := FormatPodScopedPortKey(podName, "grpc-port")
		port := annotations[key]
		portMap[port] = podName
	}

	assert.Len(t, portMap, 5, "Each pod should get a unique port")
}

func TestParsePortAllocatorConfigFromAnnotations(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		expectNil   bool
		expectError bool
		checkResult func(t *testing.T, config *PortAllocatorConfig)
	}{
		{
			name:        "nil annotations",
			annotations: nil,
			expectNil:   true,
		},
		{
			name:        "no port allocator annotation",
			annotations: map[string]string{"other-key": "value"},
			expectNil:   true,
		},
		{
			name: "valid config",
			annotations: map[string]string{
				"rolebasedgroup.workloads.x-k8s.io/port-allocator": `{
					"allocations": [
						{"name": "grpc", "env": "GRPC_PORT", "scope": "PodScoped"}
					]
				}`,
			},
			checkResult: func(t *testing.T, config *PortAllocatorConfig) {
				assert.NotNil(t, config)
				assert.Len(t, config.Allocations, 1)
				assert.Equal(t, "grpc", config.Allocations[0].Name)
			},
		},
		{
			name: "invalid JSON",
			annotations: map[string]string{
				"rolebasedgroup.workloads.x-k8s.io/port-allocator": `{invalid}`,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := ParsePortAllocatorConfigFromAnnotations(tt.annotations)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			if tt.expectNil {
				assert.Nil(t, config)
			}
			if tt.checkResult != nil {
				tt.checkResult(t, config)
			}
		})
	}
}
