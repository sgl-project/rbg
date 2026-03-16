package port_allocator

import (
	"fmt"
	"math/rand"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type RandomAllocator struct {
	startPort int32
	portRange int32
	rand      *rand.Rand
}

func init() {
	Register("random", newRandomAllocator)
}

func newRandomAllocator(startPort, portRange int32) (PortAllocatorInterface, error) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return &RandomAllocator{
		startPort: startPort,
		portRange: portRange,
		rand:      r,
	}, nil
}

func (alloc *RandomAllocator) AllocateBatch(num int32) ([]int32, error) {
	if num <= 0 {
		return []int32{}, nil
	}
	if num > alloc.portRange {
		return nil, fmt.Errorf("requested %d ports, but only %d available in range", num, alloc.portRange)
	}

	result := make([]int32, num)
	used := make(map[int32]bool, num)

	for i := int32(0); i < num; i++ {
		var port int32
		for {
			port = alloc.startPort + alloc.rand.Int31n(alloc.portRange)
			if !used[port] {
				break
			}
		}
		used[port] = true
		result[i] = port
	}

	return result, nil
}

func (alloc *RandomAllocator) Release(port int32) error {
	return nil
}

func (alloc *RandomAllocator) Start(client client.Client) error {
	return nil
}
