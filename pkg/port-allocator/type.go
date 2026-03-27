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

type PortScope string

const (
	// PodScoped Port is only valid for the current Pod
	PodScoped PortScope = "PodScoped"
	// RoleScoped Port is valid for all Pod replicas in the current role
	RoleScoped PortScope = "RoleScoped"
)

type PortAllocatorConfig struct {
	// Allocations specifies the ports to be allocated
	Allocations []PortAllocation `json:"allocations"`
	// References specifies the ports to be referenced from other pod
	References []PortReference `json:"references"`
}

type PortAllocation struct {
	// Not Empty
	// Name specifies the name of the port
	Name string `json:"name"`
	// Not Empty
	// Env specifies the name of the environment variable to be injected into the container
	Env string `json:"env"`
	// AnnotationKey specifies the key of the annotation to be injected into the Pod
	AnnotationKey string `json:"annotationKey"`
	// Not Empty
	// Default is PodScoped
	// Scope specifies the scope of the port
	Scope PortScope `json:"scope"`
}

type PortReference struct {
	// Not Empty
	// Env specifies the name of the environment variable to be injected into the container
	Env string `json:"env"`
	// Not Empty
	// From specifies the name of the port to be referenced
	From string `json:"from"`
}
