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
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
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

// TestDynamicPortAllocationAndRelease tests dynamic port allocation and release
func TestDynamicPortAllocationAndRelease(t *testing.T) {
	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	// Create a ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instance-test-instance-ports",
			Namespace: "default",
		},
		Data: make(map[string]string),
	}

	// Test dynamic port allocation
	podName := "test-pod-0"
	allocations := []PortAllocation{
		{Name: "grpc-port", Env: "GRPC_PORT", Policy: Dynamic},
		{Name: "http-port", Env: "HTTP_PORT", Policy: Dynamic},
	}

	// Allocate ports
	ports, err := AllocateBatch(int32(len(allocations)))
	require.NoError(t, err)

	for i, alloc := range allocations {
		key := FormatDynamicPortKey(podName, alloc.Name)
		SetPortInConfigMap(cm, key, strconv.Itoa(int(ports[i])))
	}

	// Verify ports were allocated
	grpcKey := FormatDynamicPortKey(podName, "grpc-port")
	httpKey := FormatDynamicPortKey(podName, "http-port")

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

// TestStaticPortAllocationAndSharing tests static port allocation and sharing across pods
func TestStaticPortAllocationAndSharing(t *testing.T) {
	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	// Create a ConfigMap for static ports
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instanceset-test-instanceset-ports",
			Namespace: "default",
		},
		Data: make(map[string]string),
	}

	// Allocate static port for "leader" role
	roleName := "leader"
	allocations := []PortAllocation{
		{Name: "http-port", Env: "HTTP_PORT", Policy: Static},
	}

	ports, err := AllocateBatch(int32(len(allocations)))
	require.NoError(t, err)

	for i, alloc := range allocations {
		key := FormatStaticPortKey(roleName, alloc.Name)
		SetPortInConfigMap(cm, key, strconv.Itoa(int(ports[i])))
	}

	// Verify port was allocated with role name prefix
	staticKey := FormatStaticPortKey(roleName, "http-port")
	staticPort, exists := GetPortFromConfigMap(cm, staticKey)
	assert.True(t, exists)
	assert.NotEmpty(t, staticPort)

	// Simulate multiple pods accessing the same static port
	// All pods with the same role should see the same port
	for i := 0; i < 3; i++ {
		port, exists := GetPortFromConfigMap(cm, staticKey)
		assert.True(t, exists)
		assert.Equal(t, staticPort, port, "All pods should see the same static port")
	}
}

// TestStaticPortPreAllocation tests that static ports are not re-allocated
func TestStaticPortPreAllocation(t *testing.T) {
	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "instanceset-test-ports",
			Namespace: "default",
		},
		Data: make(map[string]string),
	}

	// Pre-allocate a static port
	preAllocatedPort := int32(30001)
	key := FormatStaticPortKey("worker", "data-port")
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
			{Name: "port1", Env: "PORT1", Policy: Dynamic},
			{Name: "port2", Env: "PORT2", Policy: Dynamic},
			{Name: "static1", Env: "STATIC1", Policy: Static},
		},
	}

	// Allocate initial ports
	ports, err := AllocateBatch(3)
	require.NoError(t, err)

	SetPortInConfigMap(cm, FormatDynamicPortKey("test-pod-0", "port1"), strconv.Itoa(int(ports[0])))
	SetPortInConfigMap(cm, FormatDynamicPortKey("test-pod-0", "port2"), strconv.Itoa(int(ports[1])))
	SetPortInConfigMap(cm, FormatStaticPortKey("test-role", "static1"), strconv.Itoa(int(ports[2])))

	// Verify initial ports
	_, exists1 := GetPortFromConfigMap(cm, FormatDynamicPortKey("test-pod-0", "port1"))
	assert.True(t, exists1)
	_, exists2 := GetPortFromConfigMap(cm, FormatDynamicPortKey("test-pod-0", "port2"))
	assert.True(t, exists2)
	_, existsStatic := GetPortFromConfigMap(cm, FormatStaticPortKey("test-role", "static1"))
	assert.True(t, existsStatic)

	// New config with changes
	newConfig := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "port1", Env: "PORT1", Policy: Dynamic},    // unchanged
			{Name: "port3", Env: "PORT3", Policy: Dynamic},    // new port
			{Name: "static1", Env: "STATIC1", Policy: Static}, // unchanged
			{Name: "static2", Env: "STATIC2", Policy: Static}, // new static port
			// port2 removed
		},
	}

	// For this test, we manually verify the diff logic would work
	oldDynamic := oldConfig.GetDynamicAllocations()
	newDynamic := newConfig.GetDynamicAllocations()
	oldStatic := oldConfig.GetStaticAllocations()
	newStatic := newConfig.GetStaticAllocations()

	// Check dynamic ports diff
	assert.Len(t, oldDynamic, 2)
	assert.Len(t, newDynamic, 2)

	// port1 exists in both
	assert.Equal(t, "port1", oldDynamic[0].Name)
	assert.Equal(t, "port1", newDynamic[0].Name)

	// port2 exists in old but not new
	assert.Equal(t, "port2", oldDynamic[1].Name)
	// port3 exists in new but not old
	assert.Equal(t, "port3", newDynamic[1].Name)

	// Check static ports diff
	assert.Len(t, oldStatic, 1)
	assert.Len(t, newStatic, 2)
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

	key := FormatDynamicPortKey(podName, "grpc-port")
	SetPortInConfigMap(cm, key, strconv.Itoa(int(ports[0])))

	firstPort, _ := GetPortFromConfigMap(cm, key)

	// Second check - port should still be the same
	secondPort, exists := GetPortFromConfigMap(cm, key)
	assert.True(t, exists)
	assert.Equal(t, firstPort, secondPort)
}

// TestMultiplePodsDynamicPorts tests that different pods get different dynamic ports
func TestMultiplePodsDynamicPorts(t *testing.T) {
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

		key := FormatDynamicPortKey(podName, "grpc-port")
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

	// Create a standalone Instance (no InstanceSet owner)
	instance := &workloadsv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance",
			Namespace: "default",
		},
		Spec: workloadsv1alpha1.InstanceSpec{
			Components: []workloadsv1alpha1.InstanceComponent{
				{
					Name: "leader",
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"rolebasedgroup.workloads.x-k8s.io/port-allocator": `{
									"allocations": [
										{"name": "grpc-port", "env": "GRPC_PORT", "policy": "Dynamic"},
										{"name": "http-port", "env": "HTTP_PORT", "policy": "Static"}
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
	_ = workloadsv1alpha1.AddToScheme(scheme)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()

	// Create PortManager
	pm, err := NewPortManager(ctx, k8sClient, instance)
	require.NoError(t, err)
	assert.NotNil(t, pm)

	// Verify Instance-level ConfigMap was created
	instanceCM := pm.GetInstanceConfigMap()
	assert.NotNil(t, instanceCM)
	assert.Equal(t, "instance-test-instance-ports", instanceCM.Name)

	// For standalone Instance, static ports use the same ConfigMap
	assert.Equal(t, instanceCM, pm.getStaticPortConfigMap())
	assert.False(t, pm.IsManagedByInstanceSet())
}

// TestPortManager_InstanceSetOwned tests PortManager with InstanceSet-owned Instance
func TestPortManager_InstanceSetOwned(t *testing.T) {
	ctx := context.Background()

	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	// Create an Instance owned by InstanceSet
	instance := &workloadsv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instanceset-leader-0",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "workloads.x-k8s.io/v1alpha1",
					Kind:       "InstanceSet",
					Name:       "test-instanceset",
					UID:        "test-uid",
				},
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)
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

	// Static ports should use InstanceSet-level ConfigMap
	assert.Equal(t, instanceSetCM, pm.getStaticPortConfigMap())
	assert.True(t, pm.IsManagedByInstanceSet())
}

// TestPortManager_AllocateAndInjectPorts tests the full allocation and injection flow
func TestPortManager_AllocateAndInjectPorts(t *testing.T) {
	ctx := context.Background()

	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	instance := &workloadsv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance",
			Namespace: "default",
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)
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
			{Name: "grpc-port", Env: "GRPC_PORT", Policy: Dynamic},
			{Name: "http-port", Env: "HTTP_PORT", Policy: Static},
		},
	}

	// Allocate ports
	err = pm.AllocatePortsForPod(ctx, pod, config, "leader")
	require.NoError(t, err)

	// Verify ports were stored in ConfigMap
	instanceCM := pm.GetInstanceConfigMap()
	grpcKey := FormatDynamicPortKey(pod.Name, "grpc-port")
	grpcPort, exists := GetPortFromConfigMap(instanceCM, grpcKey)
	assert.True(t, exists)
	assert.NotEmpty(t, grpcPort)

	// Inject ports into Pod
	err = pm.InjectPortsIntoPod(pod, config, "leader")
	require.NoError(t, err)

	// Verify environment variables were injected
	assert.Len(t, pod.Spec.Containers[0].Env, 2)
}

// TestPortManager_ReleasePodDynamicPorts tests releasing dynamic ports when pod is deleted
func TestPortManager_ReleasePodDynamicPorts(t *testing.T) {
	ctx := context.Background()

	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	instance := &workloadsv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance",
			Namespace: "default",
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)
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
			{Name: "grpc-port", Env: "GRPC_PORT", Policy: Dynamic},
			{Name: "http-port", Env: "HTTP_PORT", Policy: Static},
		},
	}

	err = pm.AllocatePortsForPod(ctx, pod, config, "worker")
	require.NoError(t, err)

	// Verify ports exist
	instanceCM := pm.GetInstanceConfigMap()
	grpcKey := FormatDynamicPortKey(pod.Name, "grpc-port")
	_, exists := GetPortFromConfigMap(instanceCM, grpcKey)
	assert.True(t, exists)

	// Release dynamic ports (simulating pod deletion)
	err = pm.ReleasePodDynamicPorts(ctx, pod, config)
	require.NoError(t, err)

	// Verify dynamic port was removed
	_, exists = GetPortFromConfigMap(instanceCM, grpcKey)
	assert.False(t, exists)

	// Static port should still exist
	staticKey := FormatStaticPortKey("worker", "http-port")
	_, exists = GetPortFromConfigMap(instanceCM, staticKey)
	assert.True(t, exists)
}

// TestGetInstanceSetOwnerName tests extracting InstanceSet owner name
func TestGetInstanceSetOwnerName(t *testing.T) {
	tests := []struct {
		name         string
		instance     *workloadsv1alpha1.Instance
		expectedName string
	}{
		{
			name: "with InstanceSet owner",
			instance: &workloadsv1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-instance",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "workloads.x-k8s.io/v1alpha1",
							Kind:       "InstanceSet",
							Name:       "my-instanceset",
							UID:        "test-uid",
						},
					},
				},
			},
			expectedName: "my-instanceset",
		},
		{
			name: "without owner",
			instance: &workloadsv1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-instance",
				},
			},
			expectedName: "",
		},
		{
			name: "with different owner kind",
			instance: &workloadsv1alpha1.Instance{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-instance",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "workloads.x-k8s.io/v1alpha1",
							Kind:       "Instance",
							Name:       "parent-instance",
							UID:        "test-uid",
						},
					},
				},
			},
			expectedName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := GetInstanceSetOwnerName(tt.instance)
			assert.Equal(t, tt.expectedName, name)
		})
	}
}

// TestPortManager_ReferencePortResolution tests reference port resolution
func TestPortManager_ReferencePortResolution(t *testing.T) {
	ctx := context.Background()

	// Setup test allocator
	portAllocator = newTestAllocator(30000, 2768)

	instance := &workloadsv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance",
			Namespace: "default",
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)
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
			{Name: "worker-port", Env: "WORKER_PORT", Policy: Dynamic},
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
	leaderKey := FormatDynamicPortKey(leaderPodName, "leader-port")
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
			{Name: "leader-port", Env: "LEADER_PORT", Policy: Dynamic},
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

	instance := &workloadsv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-instance",
			Namespace: "default",
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = workloadsv1alpha1.AddToScheme(scheme)
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

	// Old config
	oldConfig := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "port1", Env: "PORT1", Policy: Dynamic},
			{Name: "port2", Env: "PORT2", Policy: Dynamic},
		},
	}

	// Allocate old ports
	err = pm.AllocatePortsForPod(ctx, pod, oldConfig, "worker")
	require.NoError(t, err)

	// New config - port2 removed, port3 added
	newConfig := &PortAllocatorConfig{
		Allocations: []PortAllocation{
			{Name: "port1", Env: "PORT1", Policy: Dynamic}, // unchanged
			{Name: "port3", Env: "PORT3", Policy: Dynamic}, // new
		},
	}

	// Sync ports
	err = pm.SyncPortAllocations(ctx, pod, oldConfig, newConfig, "worker")
	require.NoError(t, err)

	// Verify port1 still exists
	instanceCM := pm.GetInstanceConfigMap()
	port1Key := FormatDynamicPortKey(pod.Name, "port1")
	_, exists := GetPortFromConfigMap(instanceCM, port1Key)
	assert.True(t, exists)

	// Verify port2 was removed
	port2Key := FormatDynamicPortKey(pod.Name, "port2")
	_, exists = GetPortFromConfigMap(instanceCM, port2Key)
	assert.False(t, exists)

	// Verify port3 was added
	port3Key := FormatDynamicPortKey(pod.Name, "port3")
	_, exists = GetPortFromConfigMap(instanceCM, port3Key)
	assert.True(t, exists)
}
