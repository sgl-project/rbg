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
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AllocateStrategy program startup flags
type AllocateStrategy string

// Singleton pattern, created at program startup based on the port allocation strategy
var portAllocator *PortAllocator

var paFactory = make(map[AllocateStrategy]func(startPort, portRange int32) (PortAllocatorInterface, error))

type PortAllocator struct {
	strategy AllocateStrategy
	pa       PortAllocatorInterface
	client   client.Client
}

type PortAllocatorInterface interface {
	// Start is used to initialize the port allocator when the program starts
	Start(client client.Client) error
	// Release releases a port, input the port to release
	Release(port int32) error
	// AllocateBatch allocates multiple ports, input the number of ports to allocate, output the list of allocated port numbers
	AllocateBatch(num int32) ([]int32, error)
}

// Register registers a port allocator
func Register(strategy AllocateStrategy, factory func(startPort, portRange int32) (PortAllocatorInterface, error)) {
	paFactory[strategy] = factory
}

// startPortAllocator start the port allocator
func (alloc *PortAllocator) startPortAllocator() error {
	return alloc.pa.Start(alloc.client)
}

func Release(port int32) error {
	return portAllocator.pa.Release(port)
}

func AllocateBatch(num int32) ([]int32, error) {
	return portAllocator.pa.AllocateBatch(num)
}

func HasPolicy(policy AllocateStrategy) bool {
	_, ok := paFactory[policy]
	return ok
}

// SetupPortAllocator instantiates the global singleton port allocator with specified port allocating strategy
func SetupPortAllocator(startPort int, portRange int, allocateStrategy string, client client.Client) error {
	if !HasPolicy(AllocateStrategy(allocateStrategy)) {
		return fmt.Errorf("don't have such port allocator strategy %s", allocateStrategy)
	}
	portAllocator = &PortAllocator{
		strategy: AllocateStrategy(allocateStrategy),
		client:   client,
	}

	if err := portAllocator.createAndRestorePortAllocator(int32(startPort), int32(portRange)); err != nil {
		return fmt.Errorf("failed to create port allocator: %v", err)
	}

	if err := portAllocator.startPortAllocator(); err != nil {
		return fmt.Errorf("failed to start port allocator: %v", err)
	}

	return nil
}

// createAndRestorePortAllocator creates and restores the port allocator
func (alloc *PortAllocator) createAndRestorePortAllocator(startPort, portRange int32) (err error) {
	f, ok := paFactory[alloc.strategy]
	if !ok {
		return fmt.Errorf("unsupported port allocator policy: %s", alloc.strategy)
	}
	alloc.pa, err = f(startPort, portRange)
	return err
}
