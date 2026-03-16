package port_allocator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRandomAllocator_AllocateBatch(t *testing.T) {
	allocator, err := newRandomAllocator(30000, 2768)
	require.NoError(t, err)

	tests := []struct {
		name        string
		num         int32
		expectError bool
		expectLen   int
	}{
		{
			name:      "allocate single port",
			num:       1,
			expectLen: 1,
		},
		{
			name:      "allocate multiple ports",
			num:       10,
			expectLen: 10,
		},
		{
			name:      "allocate zero ports",
			num:       0,
			expectLen: 0,
		},
		{
			name:      "allocate negative ports",
			num:       -1,
			expectLen: 0,
		},
		{
			name:        "allocate more than range",
			num:         3000,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ports, err := allocator.AllocateBatch(tt.num)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, ports, tt.expectLen)
			}
		})
	}
}

func TestRandomAllocator_PortRange(t *testing.T) {
	startPort := int32(30000)
	portRange := int32(2768)

	allocator, err := newRandomAllocator(startPort, portRange)
	require.NoError(t, err)

	// Allocate multiple batches and verify all ports are within range
	for i := 0; i < 10; i++ {
		ports, err := allocator.AllocateBatch(100)
		require.NoError(t, err)

		for _, port := range ports {
			assert.GreaterOrEqual(t, port, startPort)
			assert.LessOrEqual(t, port, startPort+portRange-1)
		}
	}
}

func TestRandomAllocator_UniquePorts(t *testing.T) {
	allocator, err := newRandomAllocator(30000, 100)
	require.NoError(t, err)

	ports, err := allocator.AllocateBatch(50)
	require.NoError(t, err)

	// Verify all ports are unique within a single batch
	portSet := make(map[int32]bool)
	for _, port := range ports {
		assert.False(t, portSet[port], "Port %d is duplicated", port)
		portSet[port] = true
	}
}

func TestRandomAllocator_Release(t *testing.T) {
	allocator, err := newRandomAllocator(30000, 2768)
	require.NoError(t, err)

	// Release should always succeed (no-op in current implementation)
	err = allocator.Release(30000)
	assert.NoError(t, err)

	err = allocator.Release(12345)
	assert.NoError(t, err)
}

func TestRandomAllocator_Start(t *testing.T) {
	allocator, err := newRandomAllocator(30000, 2768)
	require.NoError(t, err)

	// Start should always succeed (no-op in current implementation)
	err = allocator.Start(nil)
	assert.NoError(t, err)
}

func TestRandomAllocator_Registration(t *testing.T) {
	// Verify that random allocator is registered
	assert.True(t, HasPolicy("random"))

	// Verify we can create an allocator through the factory
	factory, exists := paFactory["random"]
	assert.True(t, exists)

	allocator, err := factory(30000, 2768)
	require.NoError(t, err)
	require.NotNil(t, allocator)

	// Verify the allocator works
	ports, err := allocator.AllocateBatch(5)
	require.NoError(t, err)
	assert.Len(t, ports, 5)
}
