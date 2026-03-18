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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestParsePortAllocatorConfig(t *testing.T) {
	tests := []struct {
		name        string
		pod         *corev1.Pod
		expectNil   bool
		expectError bool
		checkResult func(t *testing.T, config *PortAllocatorConfig)
	}{
		{
			name: "nil pod",
			pod:  nil,
			checkResult: func(t *testing.T, config *PortAllocatorConfig) {
				assert.Nil(t, config)
			},
		},
		{
			name: "no annotation",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-pod",
					Annotations: map[string]string{},
				},
			},
			checkResult: func(t *testing.T, config *PortAllocatorConfig) {
				assert.Nil(t, config)
			},
		},
		{
			name: "valid config with pod-scoped allocation",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Annotations: map[string]string{
						"rolebasedgroup.workloads.x-k8s.io/port-allocator": `{
							"allocations": [
								{
									"name": "grpc",
									"env": "GRPC_PORT",
									"annotationKey": "test/grpc-port",
									"scope": "PodScoped"
								}
							]
						}`,
					},
				},
			},
			checkResult: func(t *testing.T, config *PortAllocatorConfig) {
				assert.NotNil(t, config)
				assert.Len(t, config.Allocations, 1)
				assert.Equal(t, "grpc", config.Allocations[0].Name)
				assert.Equal(t, "GRPC_PORT", config.Allocations[0].Env)
				assert.Equal(t, "test/grpc-port", config.Allocations[0].AnnotationKey)
				assert.Equal(t, PodScoped, config.Allocations[0].Scope)
			},
		},
		{
			name: "valid config with role-scoped allocation",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Annotations: map[string]string{
						"rolebasedgroup.workloads.x-k8s.io/port-allocator": `{
							"allocations": [
								{
									"name": "http",
									"env": "HTTP_PORT",
									"scope": "RoleScoped"
								}
							]
						}`,
					},
				},
			},
			checkResult: func(t *testing.T, config *PortAllocatorConfig) {
				assert.NotNil(t, config)
				assert.Len(t, config.Allocations, 1)
				assert.Equal(t, RoleScoped, config.Allocations[0].Scope)
			},
		},
		{
			name: "config with references",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Annotations: map[string]string{
						"rolebasedgroup.workloads.x-k8s.io/port-allocator": `{
							"allocations": [
								{
									"name": "worker-port",
									"env": "WORKER_PORT",
									"scope": "PodScoped"
								}
							],
							"references": [
								{
									"env": "LEADER_PORT",
									"from": "leader.leader-port"
								}
							]
						}`,
					},
				},
			},
			checkResult: func(t *testing.T, config *PortAllocatorConfig) {
				assert.NotNil(t, config)
				assert.Len(t, config.Allocations, 1)
				assert.Len(t, config.References, 1)
				assert.Equal(t, "LEADER_PORT", config.References[0].Env)
				assert.Equal(t, "leader.leader-port", config.References[0].From)
			},
		},
		{
			name: "invalid JSON",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Annotations: map[string]string{
						"rolebasedgroup.workloads.x-k8s.io/port-allocator": `{invalid json}`,
					},
				},
			},
			expectError: true,
		},
		{
			name: "missing required name field",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Annotations: map[string]string{
						"rolebasedgroup.workloads.x-k8s.io/port-allocator": `{
							"allocations": [
								{
									"env": "GRPC_PORT",
									"scope": "PodScoped"
								}
							]
						}`,
					},
				},
			},
			expectError: true,
		},
		{
			name: "missing required env field",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Annotations: map[string]string{
						"rolebasedgroup.workloads.x-k8s.io/port-allocator": `{
							"allocations": [
								{
									"name": "grpc",
									"scope": "PodScoped"
								}
							]
						}`,
					},
				},
			},
			expectError: true,
		},
		{
			name: "default scope to PodScoped",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Annotations: map[string]string{
						"rolebasedgroup.workloads.x-k8s.io/port-allocator": `{
							"allocations": [
								{
									"name": "grpc",
									"env": "GRPC_PORT"
								}
							]
						}`,
					},
				},
			},
			checkResult: func(t *testing.T, config *PortAllocatorConfig) {
				assert.NotNil(t, config)
				assert.Equal(t, PodScoped, config.Allocations[0].Scope)
			},
		},
		{
			name: "invalid scope value",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Annotations: map[string]string{
						"rolebasedgroup.workloads.x-k8s.io/port-allocator": `{
							"allocations": [
								{
									"name": "grpc",
									"env": "GRPC_PORT",
									"scope": "Invalid"
								}
							]
						}`,
					},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := ParsePortAllocatorConfig(tt.pod)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			if tt.checkResult != nil {
				tt.checkResult(t, config)
			}
		})
	}
}

func TestGetPodScopedAllocations(t *testing.T) {
	config := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "port1", Env: "PORT1", Scope: PodScoped},
			{Name: "port2", Env: "PORT2", Scope: RoleScoped},
			{Name: "port3", Env: "PORT3", Scope: PodScoped},
		},
	}

	podScoped := config.GetPodScopedAllocations()
	assert.Len(t, podScoped, 2)
	assert.Equal(t, "port1", podScoped[0].Name)
	assert.Equal(t, "port3", podScoped[1].Name)
}

func TestGetRoleScopedAllocations(t *testing.T) {
	config := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "port1", Env: "PORT1", Scope: PodScoped},
			{Name: "port2", Env: "PORT2", Scope: RoleScoped},
			{Name: "port3", Env: "PORT3", Scope: RoleScoped},
		},
	}

	roleScoped := config.GetRoleScopedAllocations()
	assert.Len(t, roleScoped, 2)
	assert.Equal(t, "port2", roleScoped[0].Name)
	assert.Equal(t, "port3", roleScoped[1].Name)
}

func TestParseReference(t *testing.T) {
	tests := []struct {
		name            string
		from            string
		expectError     bool
		expectComponent string
		expectPort      string
	}{
		{
			name:            "valid reference",
			from:            "leader.leader-port",
			expectComponent: "leader",
			expectPort:      "leader-port",
		},
		{
			name:            "valid reference with numbers",
			from:            "worker-0.grpc-port",
			expectComponent: "worker-0",
			expectPort:      "grpc-port",
		},
		{
			name:        "empty reference",
			from:        "",
			expectError: true,
		},
		{
			name:        "missing port name",
			from:        "leader.",
			expectError: true,
		},
		{
			name:        "missing component name",
			from:        ".port",
			expectError: true,
		},
		{
			name:        "no dot",
			from:        "leaderport",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component, port, err := ParseReference(tt.from)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectComponent, component)
				assert.Equal(t, tt.expectPort, port)
			}
		})
	}
}

func TestFormatPortKey(t *testing.T) {
	// Test pod-scoped port key
	podScopedKey := FormatPodScopedPortKey("pod-0", "grpc-port")
	assert.Equal(t, "pod-0.grpc-port", podScopedKey)

	// Test role-scoped port key (now uses component name as prefix)
	roleScopedKey := FormatRoleScopedPortKey("leader", "http-port")
	assert.Equal(t, "leader.http-port", roleScopedKey)
}

func TestParsePortKey(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		expectOwner string
		expectPort  string
		expectError bool
	}{
		{
			name:        "valid key",
			key:         "pod-0.grpc-port",
			expectOwner: "pod-0",
			expectPort:  "grpc-port",
		},
		{
			name:        "valid key with hyphens",
			key:         "my-instance.http-port",
			expectOwner: "my-instance",
			expectPort:  "http-port",
		},
		{
			name:        "empty key",
			key:         "",
			expectError: true,
		},
		{
			name:        "no dot",
			key:         "invalidkey",
			expectError: true,
		},
		{
			name:        "dot at start",
			key:         ".port",
			expectError: true,
		},
		{
			name:        "dot at end",
			key:         "owner.",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, port, err := ParsePortKey(tt.key)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectOwner, owner)
				assert.Equal(t, tt.expectPort, port)
			}
		})
	}
}

func TestConfigMapNameGeneration(t *testing.T) {
	// Test instance port ConfigMap name
	instanceCMName := GetInstancePortConfigMapName("my-instance")
	assert.Equal(t, "instance-my-instance-ports", instanceCMName)

	// Test instanceset port ConfigMap name
	instanceSetCMName := GetInstanceSetPortConfigMapName("my-instanceset")
	assert.Equal(t, "instanceset-my-instanceset-ports", instanceSetCMName)
}

func TestHasPortAllocatorConfig(t *testing.T) {
	// Test with config
	templateWithConfig := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"rolebasedgroup.workloads.x-k8s.io/port-allocator": `{}`,
			},
		},
	}
	assert.True(t, HasPortAllocatorConfig(templateWithConfig))

	// Test without config
	templateWithoutConfig := &corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{},
		},
	}
	assert.False(t, HasPortAllocatorConfig(templateWithoutConfig))

	// Test nil template
	assert.False(t, HasPortAllocatorConfig(nil))
}
