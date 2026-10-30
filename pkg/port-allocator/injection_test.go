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
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func injectionTestInstance(podName, portValue string) *workloadsv1alpha2.RoleInstance {
	return &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-instance",
			Labels:      map[string]string{constants.RoleTypeLabelKey: string(constants.ComponentsTemplateType)},
			Annotations: map[string]string{FormatPodScopedPortKey(podName, "metrics"): portValue},
		},
	}
}

func TestInjectPortsIntoPodRejectsInvalidPortValues(t *testing.T) {
	tests := []struct {
		name      string
		portValue string
	}{
		{name: "empty value", portValue: ""},
		{name: "zero", portValue: "0"},
		{name: "negative value", portValue: "-1"},
		{name: "above maximum", portValue: "65536"},
		{name: "non-numeric value", portValue: "invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance-worker-0"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "worker", Image: "worker:latest"}},
				},
			}
			config := &PortAllocatorConfig{
				Allocations: []PortAllocation{
					{Name: "metrics", Env: "METRICS_PORT", Scope: PodScoped},
				},
			}

			err := InjectPortsIntoPod(pod, injectionTestInstance(pod.Name, tt.portValue), config, "worker")

			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid port value")
			assert.Empty(t, pod.Spec.Containers[0].Ports)
			assert.Empty(t, pod.Spec.Containers[0].Env)
		})
	}
}

func TestInjectPortsIntoPodRequiresRegularContainer(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-instance-worker-0"},
	}
	config := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "metrics", Env: "METRICS_PORT", Scope: PodScoped},
		},
	}

	err := InjectPortsIntoPod(pod, injectionTestInstance(pod.Name, "30000"), config, "worker")

	require.ErrorContains(t, err, "pod has no regular containers")
}

func TestInjectPortsIntoPodHandlesExistingPortDeclarations(t *testing.T) {
	tests := []struct {
		name          string
		hostNetwork   bool
		existingPorts []corev1.ContainerPort
		expectError   string
		expectedPorts []corev1.ContainerPort
	}{
		{
			name:          "reuses an existing non-host-network container port",
			hostNetwork:   false,
			existingPorts: []corev1.ContainerPort{{ContainerPort: 30000, Protocol: corev1.ProtocolTCP}},
			expectedPorts: []corev1.ContainerPort{{ContainerPort: 30000, Protocol: corev1.ProtocolTCP}},
		},
		{
			name:          "fills the host port for an existing host-network container port",
			hostNetwork:   true,
			existingPorts: []corev1.ContainerPort{{ContainerPort: 30000, Protocol: corev1.ProtocolTCP}},
			expectedPorts: []corev1.ContainerPort{{ContainerPort: 30000, HostPort: 30000, Protocol: corev1.ProtocolTCP}},
		},
		{
			name:          "reuses an identical host-network port declaration",
			hostNetwork:   true,
			existingPorts: []corev1.ContainerPort{{ContainerPort: 30000, HostPort: 30000, Protocol: corev1.ProtocolTCP}},
			expectedPorts: []corev1.ContainerPort{{ContainerPort: 30000, HostPort: 30000, Protocol: corev1.ProtocolTCP}},
		},
		{
			name:          "rejects a host-network declaration with mismatched host and container ports",
			hostNetwork:   true,
			existingPorts: []corev1.ContainerPort{{ContainerPort: 30000, HostPort: 30001, Protocol: corev1.ProtocolTCP}},
			expectError:   "host port 30001 does not match container port 30000",
		},
		{
			name:          "rejects an existing host port mapped to another container port",
			hostNetwork:   true,
			existingPorts: []corev1.ContainerPort{{ContainerPort: 29999, HostPort: 30000, Protocol: corev1.ProtocolTCP}},
			expectError:   "host port 30000 with protocol TCP already exists",
		},
		{
			name:          "keeps declarations for different protocols separate",
			hostNetwork:   false,
			existingPorts: []corev1.ContainerPort{{ContainerPort: 30000, Protocol: corev1.ProtocolUDP}},
			expectedPorts: []corev1.ContainerPort{
				{ContainerPort: 30000, Protocol: corev1.ProtocolUDP},
				{ContainerPort: 30000, Protocol: corev1.ProtocolTCP},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "test-instance-worker-0"},
				Spec: corev1.PodSpec{
					HostNetwork: tt.hostNetwork,
					Containers: []corev1.Container{{
						Name:  "worker",
						Image: "worker:latest",
						Ports: tt.existingPorts,
					}},
				},
			}
			config := &PortAllocatorConfig{
				Allocations: []PortAllocation{
					{Name: "metrics", Env: "METRICS_PORT", Scope: PodScoped, Protocol: corev1.ProtocolTCP},
				},
			}

			err := InjectPortsIntoPod(pod, injectionTestInstance(pod.Name, "30000"), config, "worker")
			if tt.expectError != "" {
				require.ErrorContains(t, err, tt.expectError)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedPorts, pod.Spec.Containers[0].Ports)
		})
	}
}

func TestInjectPortsIntoPodOnlyDeclaresPortsOnFirstRegularContainer(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-instance-worker-0"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "engine", Image: "engine:latest"},
				{Name: "sidecar", Image: "sidecar:latest"},
			},
		},
	}
	config := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "metrics", Env: "METRICS_PORT", Scope: PodScoped, Protocol: corev1.ProtocolUDP},
		},
	}

	err := InjectPortsIntoPod(pod, injectionTestInstance(pod.Name, "30000"), config, "worker")
	require.NoError(t, err)

	require.Len(t, pod.Spec.Containers[0].Ports, 1)
	assert.Equal(t, corev1.ContainerPort{
		ContainerPort: 30000,
		Protocol:      corev1.ProtocolUDP,
	}, pod.Spec.Containers[0].Ports[0])
	assert.Empty(t, pod.Spec.Containers[1].Ports)

	for i := range pod.Spec.Containers {
		assert.Len(t, pod.Spec.Containers[i].Env, 1)
	}
}
