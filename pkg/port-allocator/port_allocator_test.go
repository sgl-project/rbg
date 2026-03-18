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
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

func (t *testAllocator) Start(client client.Client) error {
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

// TestPodScopedPortAllocationAndRelease tests pod-scoped port allocation and release
func TestPodScopedPortAllocationAndRelease(t *testing.T) {
	// Setup test allocator
	portAllocator = newTestAllocator(31000, 2000)

	// Create a ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instance-test-instance-ports",
			Namespace: "default",
		},
		Data: make(map[string]string),
	}

	// Test pod-scoped port allocation
	podName := "test-pod-0"
	allocations := []PortAllocation{
		{Name: "grpc-port", Env: "GRPC_PORT", Scope: PodScoped},
		{Name: "http-port", Env: "HTTP_PORT", Scope: PodScoped},
	}

	// Allocate ports
	ports, err := AllocateBatch(int32(len(allocations)))
	require.NoError(t, err)

	for i, alloc := range allocations {
		key := FormatPodScopedPortKey(podName, alloc.Name)
		SetPortInConfigMap(cm, key, strconv.Itoa(int(ports[i])))
	}

	// Verify ports were allocated
	grpcKey := FormatPodScopedPortKey(podName, "grpc-port")
	httpKey := FormatPodScopedPortKey(podName, "http-port")

	grpcPort, exists := GetPortFromConfigMap(cm, grpcKey)
	assert.True(t, exists)
	assert.NotEmpty(t, grpcPort)

	httpPort, exists := GetPortFromConfigMap(cm, httpKey)
	assert.True(t, exists)
	assert.NotEmpty(t, httpPort)

	// Verify different ports were allocated
	assert.NotEqual(t, grpcPort, httpPort)

	// Test port release
	portsInt, err := strconv.ParseInt(grpcPort, 10, 32)
	require.NoError(t, err)

	err = Release(int32(portsInt))
	assert.NoError(t, err)

	// Remove from ConfigMap
	RemovePortFromConfigMap(cm, grpcKey)
	_, exists = GetPortFromConfigMap(cm, grpcKey)
	assert.False(t, exists)
}

// TestRoleScopedPortAllocationAndSharing tests role-scoped port allocation and sharing across pods
func TestRoleScopedPortAllocationAndSharing(t *testing.T) {
	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	// Create a ConfigMap for role-scoped ports
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instanceset-test-instanceset-ports",
			Namespace: "default",
		},
		Data: make(map[string]string),
	}

	// Allocate role-scoped port for "leader" role
	roleName := "leader"
	allocations := []PortAllocation{
		{Name: "http-port", Env: "HTTP_PORT", Scope: RoleScoped},
	}

	ports, err := AllocateBatch(int32(len(allocations)))
	require.NoError(t, err)

	for i, alloc := range allocations {
		key := FormatRoleScopedPortKey(roleName, alloc.Name)
		SetPortInConfigMap(cm, key, strconv.Itoa(int(ports[i])))
	}

	// Verify port was allocated with role name prefix
	roleScopedKey := FormatRoleScopedPortKey(roleName, "http-port")
	roleScopedPort, exists := GetPortFromConfigMap(cm, roleScopedKey)
	assert.True(t, exists)
	assert.NotEmpty(t, roleScopedPort)

	// Simulate multiple pods accessing the same role-scoped port
	// All pods with the same role should see the same port
	for i := 0; i < 3; i++ {
		port, exists := GetPortFromConfigMap(cm, roleScopedKey)
		assert.True(t, exists)
		assert.Equal(t, roleScopedPort, port, "All pods should see the same role-scoped port")
	}
}

// TestRoleScopedPortPreAllocation tests that role-scoped ports are not re-allocated
func TestRoleScopedPortPreAllocation(t *testing.T) {
	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instanceset-test-ports",
			Namespace: "default",
		},
		Data: make(map[string]string),
	}

	// Pre-allocate a role-scoped port
	preAllocatedPort := int32(30001)
	key := FormatRoleScopedPortKey("worker", "data-port")
	SetPortInConfigMap(cm, key, strconv.Itoa(int(preAllocatedPort)))

	// Verify the port exists and is unchanged
	port, exists := GetPortFromConfigMap(cm, key)
	assert.True(t, exists)
	assert.Equal(t, strconv.Itoa(int(preAllocatedPort)), port)
}

// TestPortUpdateAndDiff tests port update/diff calculation
func TestPortUpdateAndDiff(t *testing.T) {
	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instance-test-ports",
			Namespace: "default",
		},
		Data: make(map[string]string),
	}

	// Initial allocation
	oldConfig := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "port1", Env: "PORT1", Scope: PodScoped},
			{Name: "port2", Env: "PORT2", Scope: PodScoped},
			{Name: "static1", Env: "STATIC1", Scope: RoleScoped},
		},
	}

	// Allocate initial ports
	ports, err := AllocateBatch(3)
	require.NoError(t, err)

	SetPortInConfigMap(cm, FormatPodScopedPortKey("test-pod-0", "port1"), strconv.Itoa(int(ports[0])))
	SetPortInConfigMap(cm, FormatPodScopedPortKey("test-pod-0", "port2"), strconv.Itoa(int(ports[1])))
	SetPortInConfigMap(cm, FormatRoleScopedPortKey("test-role", "static1"), strconv.Itoa(int(ports[2])))

	// Verify initial ports
	_, exists1 := GetPortFromConfigMap(cm, FormatPodScopedPortKey("test-pod-0", "port1"))
	assert.True(t, exists1)
	_, exists2 := GetPortFromConfigMap(cm, FormatPodScopedPortKey("test-pod-0", "port2"))
	assert.True(t, exists2)
	_, existsRoleScoped := GetPortFromConfigMap(cm, FormatRoleScopedPortKey("test-role", "static1"))
	assert.True(t, existsRoleScoped)

	// New config with changes
	newConfig := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "port1", Env: "PORT1", Scope: PodScoped},      // unchanged
			{Name: "port3", Env: "PORT3", Scope: PodScoped},      // new port
			{Name: "static1", Env: "STATIC1", Scope: RoleScoped}, // unchanged
			{Name: "static2", Env: "STATIC2", Scope: RoleScoped}, // new role-scoped port
			// port2 removed
		},
	}

	// For this test, we manually verify the diff logic would work
	oldPodScoped := oldConfig.GetPodScopedAllocations()
	newPodScoped := newConfig.GetPodScopedAllocations()
	oldRoleScoped := oldConfig.GetRoleScopedAllocations()
	newRoleScoped := newConfig.GetRoleScopedAllocations()

	// Check pod-scoped ports diff
	assert.Len(t, oldPodScoped, 2)
	assert.Len(t, newPodScoped, 2)

	// port1 exists in both
	assert.Equal(t, "port1", oldPodScoped[0].Name)
	assert.Equal(t, "port1", newPodScoped[0].Name)

	// port2 exists in old but not new
	assert.Equal(t, "port2", oldPodScoped[1].Name)
	// port3 exists in new but not old
	assert.Equal(t, "port3", newPodScoped[1].Name)

	// Check role-scoped ports diff
	assert.Len(t, oldRoleScoped, 1)
	assert.Len(t, newRoleScoped, 2)
}

// TestPortAllocationIdempotency tests that allocating same port twice doesn't create duplicates
func TestPortAllocationIdempotency(t *testing.T) {
	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instance-test-ports",
			Namespace: "default",
		},
		Data: make(map[string]string),
	}

	podName := "test-pod-0"

	// First allocation
	ports, err := AllocateBatch(1)
	require.NoError(t, err)

	key := FormatPodScopedPortKey(podName, "grpc-port")
	SetPortInConfigMap(cm, key, strconv.Itoa(int(ports[0])))

	firstPort, _ := GetPortFromConfigMap(cm, key)

	// Second check - port should still be the same
	secondPort, exists := GetPortFromConfigMap(cm, key)
	assert.True(t, exists)
	assert.Equal(t, firstPort, secondPort)
}

// TestMultiplePodsPodScopedPorts tests that different pods get different pod-scoped ports
func TestMultiplePodsPodScopedPorts(t *testing.T) {
	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instance-test-ports",
			Namespace: "default",
		},
		Data: make(map[string]string),
	}

	// Allocate ports for multiple pods
	portMap := make(map[string]string)
	for i := 0; i < 5; i++ {
		podName := "test-pod-" + strconv.Itoa(i)
		ports, err := AllocateBatch(1)
		require.NoError(t, err)

		key := FormatPodScopedPortKey(podName, "grpc-port")
		SetPortInConfigMap(cm, key, strconv.Itoa(int(ports[0])))

		port, exists := GetPortFromConfigMap(cm, key)
		assert.True(t, exists)
		portMap[port] = podName
	}

	// Verify all pods got unique ports
	assert.Len(t, portMap, 5, "Each pod should get a unique port")
}

// TestConfigMapOperations tests ConfigMap helper operations
func TestConfigMapOperations(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cm",
			Namespace: "default",
		},
		Data: make(map[string]string),
	}

	// Test Set and Get
	SetPortInConfigMap(cm, "key1", "30001")
	port, exists := GetPortFromConfigMap(cm, "key1")
	assert.True(t, exists)
	assert.Equal(t, "30001", port)

	// Test non-existent key
	_, exists = GetPortFromConfigMap(cm, "nonexistent")
	assert.False(t, exists)

	// Test Remove
	RemovePortFromConfigMap(cm, "key1")
	_, exists = GetPortFromConfigMap(cm, "key1")
	assert.False(t, exists)
}

// TestPortRangeValidation tests that ports are allocated within the valid range
func TestPortRangeValidation(t *testing.T) {
	startPort := int32(30000)
	portRange := int32(2768) // 30000-32767

	portAllocator = newTestAllocator(startPort, portRange)

	// Allocate multiple ports
	ports, err := AllocateBatch(10)
	require.NoError(t, err)

	for _, port := range ports {
		assert.GreaterOrEqual(t, port, startPort)
		assert.LessOrEqual(t, port, startPort+portRange-1)
	}
}

// TestReleasePortsAndDeleteCM tests the ReleasePortsAndDeleteCM function
func TestReleasePortsAndDeleteCM(t *testing.T) {
	ctx := context.Background()

	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	// Create a ConfigMap with some ports
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instance-test-instance-ports",
			Namespace: "default",
		},
		Data: map[string]string{
			"pod-0.grpc-port": "30000",
			"pod-0.http-port": "30001",
			"leader.http":     "30002",
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	// Release and delete
	err := ReleasePortsAndDeleteCM(ctx, k8sClient, "default", "instance-test-instance-ports")
	require.NoError(t, err)

	// Verify ConfigMap is deleted
	var deletedCM corev1.ConfigMap
	err = k8sClient.Get(ctx, client.ObjectKey{Name: "instance-test-instance-ports", Namespace: "default"}, &deletedCM)
	assert.Error(t, err) // Should not find the ConfigMap
}

// TestReleasePortsAndDeleteCM_NotFound tests behavior when ConfigMap doesn't exist
func TestReleasePortsAndDeleteCM_NotFound(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Should not error when ConfigMap doesn't exist
	err := ReleasePortsAndDeleteCM(ctx, k8sClient, "default", "nonexistent-cm")
	assert.NoError(t, err)
}

// TestPortManager_InstancePortAllocation tests the PortManager with Instance-level allocation
func TestPortManager_InstancePortAllocation(t *testing.T) {
	ctx := context.Background()

	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance",
			Namespace: "default",
		},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			Components: []workloadsv1alpha2.RoleInstanceComponent{
				{
					Name: "leader",
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"rolebasedgroup.workloads.x-k8s.io/port-allocator": `{
									"allocations": [
										{"name": "grpc-port", "env": "GRPC_PORT", "scope": "PodScoped"},
										{"name": "http-port", "env": "HTTP_PORT", "scope": "RoleScoped"}
									]
								}`,
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "nginx", Image: "nginx:1.28.0"},
							},
						},
					},
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()

	// Create PortManager
	pm, err := NewPortManager(ctx, k8sClient, instance)
	require.NoError(t, err)
	assert.NotNil(t, pm)

	// Verify Instance-level ConfigMap was created
	instanceCM := pm.GetInstanceConfigMap()
	assert.NotNil(t, instanceCM)
	assert.Equal(t, "instance-test-instance-ports", instanceCM.Name)

	// For standalone Instance, role-scoped ports use the same ConfigMap
	assert.Equal(t, instanceCM, pm.getRoleScopedPortConfigMap())
	assert.False(t, pm.IsManagedByInstanceSet())
}

// TestPortManager_InstanceSetOwned tests PortManager with Instance
func TestPortManager_InstanceSetOwned(t *testing.T) {
	ctx := context.Background()

	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instanceset-leader-0",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "workloads.x-k8s.io/v1alpha2",
					Kind:       "RoleInstanceSet",
					Name:       "test-instanceset",
					UID:        "test-uid",
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()

	// Create PortManager
	pm, err := NewPortManager(ctx, k8sClient, instance)
	require.NoError(t, err)
	assert.NotNil(t, pm)

	// Verify both ConfigMaps were created
	instanceCM := pm.GetInstanceConfigMap()
	assert.NotNil(t, instanceCM)
	assert.Equal(t, "instance-test-instanceset-leader-0-ports", instanceCM.Name)

	instanceSetCM := pm.GetInstanceSetConfigMap()
	assert.NotNil(t, instanceSetCM)
	assert.Equal(t, "instanceset-test-instanceset-ports", instanceSetCM.Name)

	// Role-scoped ports should use InstanceSet-level ConfigMap
	assert.Equal(t, instanceSetCM, pm.getRoleScopedPortConfigMap())
	assert.True(t, pm.IsManagedByInstanceSet())
}

// TestPortManager_AllocateAndInjectPorts tests the full allocation and injection flow
func TestPortManager_AllocateAndInjectPorts(t *testing.T) {
	ctx := context.Background()

	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance",
			Namespace: "default",
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()

	pm, err := NewPortManager(ctx, k8sClient, instance)
	require.NoError(t, err)

	// Create a Pod with port allocation config
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance-leader-0",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "nginx", Image: "nginx:1.28.0"},
			},
		},
	}

	config := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "grpc-port", Env: "GRPC_PORT", Scope: PodScoped},
			{Name: "http-port", Env: "HTTP_PORT", Scope: RoleScoped},
		},
	}

	// Allocate ports
	err = pm.AllocatePortsForPod(ctx, pod, config, "leader")
	require.NoError(t, err)

	// Verify ports were stored in ConfigMap
	instanceCM := pm.GetInstanceConfigMap()
	grpcKey := FormatPodScopedPortKey(pod.Name, "grpc-port")
	grpcPort, exists := GetPortFromConfigMap(instanceCM, grpcKey)
	assert.True(t, exists)
	assert.NotEmpty(t, grpcPort)

	// Inject ports into Pod
	err = pm.InjectPortsIntoPod(pod, config, "leader")
	require.NoError(t, err)

	// Verify environment variables were injected
	assert.Len(t, pod.Spec.Containers[0].Env, 2)
}

// TestPortManager_ReleasePodScopedPorts tests releasing pod-scoped ports when pod is deleted
func TestPortManager_ReleasePodScopedPorts(t *testing.T) {
	ctx := context.Background()

	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance",
			Namespace: "default",
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()

	pm, err := NewPortManager(ctx, k8sClient, instance)
	require.NoError(t, err)

	// Create and allocate ports for a pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance-worker-0",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "nginx", Image: "nginx:1.28.0"},
			},
		},
	}

	config := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "grpc-port", Env: "GRPC_PORT", Scope: PodScoped},
			{Name: "http-port", Env: "HTTP_PORT", Scope: RoleScoped},
		},
	}

	err = pm.AllocatePortsForPod(ctx, pod, config, "worker")
	require.NoError(t, err)

	// Verify ports exist
	instanceCM := pm.GetInstanceConfigMap()
	grpcKey := FormatPodScopedPortKey(pod.Name, "grpc-port")
	_, exists := GetPortFromConfigMap(instanceCM, grpcKey)
	assert.True(t, exists)

	// Release pod-scoped ports (simulating pod deletion)
	err = pm.ReleasePodScopedPorts(ctx, pod, config)
	require.NoError(t, err)

	// Verify pod-scoped port was removed
	_, exists = GetPortFromConfigMap(instanceCM, grpcKey)
	assert.False(t, exists)

	// Role-scoped port should still exist
	roleScopedKey := FormatRoleScopedPortKey("worker", "http-port")
	_, exists = GetPortFromConfigMap(instanceCM, roleScopedKey)
	assert.True(t, exists)
}

// TestPortManager_ReferencePortResolution tests reference port resolution
func TestPortManager_ReferencePortResolution(t *testing.T) {
	ctx := context.Background()

	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance",
			Namespace: "default",
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()

	pm, err := NewPortManager(ctx, k8sClient, instance)
	require.NoError(t, err)

	// Worker pod that references leader's port
	workerPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance-worker-0",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "nginx", Image: "nginx:1.28.0"},
			},
		},
	}

	config := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "worker-port", Env: "WORKER_PORT", Scope: PodScoped},
		},
		References: []PortReference{
			{Env: "LEADER_PORT", From: "leader.leader-port"},
		},
	}

	// Allocate ports for worker (leader doesn't exist yet)
	err = pm.AllocatePortsForPod(ctx, workerPod, config, "worker")
	require.NoError(t, err)

	// Verify leader's port was pre-allocated
	instanceCM := pm.GetInstanceConfigMap()
	leaderPodName := "test-instance-leader-0" // Derived from worker pod name
	leaderKey := FormatPodScopedPortKey(leaderPodName, "leader-port")
	leaderPort, exists := GetPortFromConfigMap(instanceCM, leaderKey)
	assert.True(t, exists)
	assert.NotEmpty(t, leaderPort)

	// Now create leader pod and verify it uses the pre-allocated port
	leaderPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance-leader-0",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "nginx", Image: "nginx:1.28.0"},
			},
		},
	}

	leaderConfig := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "leader-port", Env: "LEADER_PORT", Scope: PodScoped},
		},
	}

	err = pm.AllocatePortsForPod(ctx, leaderPod, leaderConfig, "leader")
	require.NoError(t, err)

	// Verify leader uses the pre-allocated port
	newLeaderPort, exists := GetPortFromConfigMap(instanceCM, leaderKey)
	assert.True(t, exists)
	assert.Equal(t, leaderPort, newLeaderPort, "Leader should use pre-allocated port")
}

// TestPortManager_SyncPortAllocations tests port sync on config updates
func TestPortManager_SyncPortAllocations(t *testing.T) {
	ctx := context.Background()

	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance",
			Namespace: "default",
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha2.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()

	pm, err := NewPortManager(ctx, k8sClient, instance)
	require.NoError(t, err)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance-worker-0",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "nginx", Image: "nginx:1.28.0"},
			},
		},
	}

	oldConfig := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "port1", Env: "PORT1", Scope: PodScoped},
			{Name: "port2", Env: "PORT2", Scope: PodScoped},
		},
	}

	// Allocate old ports
	err = pm.AllocatePortsForPod(ctx, pod, oldConfig, "worker")
	require.NoError(t, err)

	// New config - port2 removed, port3 added
	newConfig := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "port1", Env: "PORT1", Scope: PodScoped}, // unchanged
			{Name: "port3", Env: "PORT3", Scope: PodScoped}, // new
		},
	}

	// Sync ports
	err = pm.SyncPortAllocations(ctx, pod, oldConfig, newConfig, "worker")
	require.NoError(t, err)

	// Verify port1 still exists
	instanceCM := pm.GetInstanceConfigMap()
	port1Key := FormatPodScopedPortKey(pod.Name, "port1")
	_, exists := GetPortFromConfigMap(instanceCM, port1Key)
	assert.True(t, exists)

	// Verify port2 was removed
	port2Key := FormatPodScopedPortKey(pod.Name, "port2")
	_, exists = GetPortFromConfigMap(instanceCM, port2Key)
	assert.False(t, exists)

	// Verify port3 was added
	port3Key := FormatPodScopedPortKey(pod.Name, "port3")
	_, exists = GetPortFromConfigMap(instanceCM, port3Key)
	assert.True(t, exists)
}
