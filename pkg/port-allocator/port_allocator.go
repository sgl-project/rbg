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
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AllocateStrategy program startup flags
type AllocateStrategy string

// ErrPortAllocatorDisabled is returned when the port allocator is not enabled.
// Callers can use errors.Is() to check for this specific condition.
var ErrPortAllocatorDisabled = errors.New("port allocator is not enabled")

// Singleton pattern, always initialized to ensure IsEnabled() can be queried safely.
var portAllocator = &PortAllocator{
	enabled: false,
}

var paFactory = make(map[AllocateStrategy]func(startPort, portRange int32) (PortAllocatorInterface, error))

type PortAllocator struct {
	enabled  bool
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

// IsEnabled returns true if the port allocator is enabled.
func IsEnabled() bool {
	if portAllocator == nil {
		return false
	}
	return portAllocator.enabled
}

func Release(port int32) error {
	if portAllocator == nil || !IsEnabled() {
		return ErrPortAllocatorDisabled
	}
	return portAllocator.pa.Release(port)
}

func AllocateBatch(num int32) ([]int32, error) {
	if portAllocator == nil || !IsEnabled() {
		return nil, ErrPortAllocatorDisabled
	}
	return portAllocator.pa.AllocateBatch(num)
}

func HasPolicy(policy AllocateStrategy) bool {
	_, ok := paFactory[policy]
	return ok
}

// SetupPortAllocator initializes the global singleton port allocator.
// The enabled flag controls whether the allocator is active.
// The allocator object is always initialized so that IsEnabled() can be queried safely.
func SetupPortAllocator(startPort int, portRange int, allocateStrategy string, enabled bool, client client.Client) error {
	if !enabled {
		return nil
	}

	if err := portAllocator.configure(client, AllocateStrategy(allocateStrategy), int32(startPort), int32(portRange)); err != nil {
		return fmt.Errorf("failed to configure port allocator: %w", err)
	}

	if err := portAllocator.start(); err != nil {
		return fmt.Errorf("failed to start port allocator: %w", err)
	}

	return nil
}

// configure sets up the port allocator with the given strategy and port range.
// It sets enabled=true only after successful configuration.
func (alloc *PortAllocator) configure(client client.Client, strategy AllocateStrategy, startPort, portRange int32) error {
	factory, ok := paFactory[strategy]
	if !ok {
		return fmt.Errorf("unsupported port allocator strategy: %s", strategy)
	}

	alloc.client = client
	alloc.strategy = strategy

	pa, err := factory(startPort, portRange)
	if err != nil {
		return fmt.Errorf("failed to create port allocator instance: %w", err)
	}
	alloc.pa = pa
	alloc.enabled = true
	return nil
}

// start initializes the underlying port allocator.
func (alloc *PortAllocator) start() error {
	return alloc.pa.Start(alloc.client)
}
