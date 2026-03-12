/*
Copyright 2025.

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

package constants

// ========== Environment Variables ==========
// Environment variables are used for runtime service discovery and configuration passing,
// injected into Pod containers by the Controller.

// Basic environment variables (all Workload types)
const (
	// EnvRBGGroupName is the environment variable for RBG name
	EnvRBGGroupName = "RBG_GROUP_NAME"

	// EnvRBGRoleName is the environment variable for Role name
	EnvRBGRoleName = "RBG_ROLE_NAME"
)

// StatefulSet / LeaderWorkerSet specific environment variables
const (
	// EnvRBGRoleIndex is the ordered index of Pod in Role
	// Source: Downward API from metadata.labels['apps.kubernetes.io/pod-index']
	EnvRBGRoleIndex = "RBG_ROLE_INDEX"
)

// InstanceSet specific environment variables
const (
	// EnvRBGRoleInstanceName is the name of RoleInstance
	// Source: Downward API from metadata.labels['rbg.workloads.x-k8s.io/role-instance-name']
	EnvRBGRoleInstanceName = "RBG_ROLE_INSTANCE_NAME"

	// EnvRBGComponentName is the component name
	// Source: Downward API from metadata.labels['rbg.workloads.x-k8s.io/component-name']
	EnvRBGComponentName = "RBG_COMPONENT_NAME"

	// EnvRBGComponentIndex is the component index within the Instance
	// Source: Downward API from metadata.labels['rbg.workloads.x-k8s.io/component-id']
	EnvRBGComponentIndex = "RBG_COMPONENT_INDEX"
)

// Multi-node distributed environment variables (LWS replacement)
const (
	// EnvRBGLeaderAddress is the DNS address of the Leader component
	// Source: Computed as $(INSTANCE_NAME)-0.{svcName}.{namespace}
	EnvRBGLeaderAddress = "RBG_LWP_LEADER_ADDRESS"

	// EnvRBGIndex is the Component index within the Instance
	EnvRBGIndex = "RBG_LWP_WORKER_INDEX"

	// EnvRBGSize is the total number of Components in the Instance
	// Source: Downward API from label rbg.workloads.x-k8s.io/component-size
	EnvRBGSize = "RBG_LWP_GROUP_SIZE"
)

// System environment variable prefix for filtering
const (
	// EnvRBGPrefix is the prefix for all RBG system environment variables
	// Used by controller to filter system env vars during Pod spec comparison
	EnvRBGPrefix = "RBG_"
)
