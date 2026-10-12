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

package constants

// CoordinatedPolicyTopologyFinalizer prevents deletion of a topology-bearing
// CoordinatedPolicy while its matching RoleBasedGroup still exists. Delete and
// recreate the RBG first; the controller removes this finalizer as part of RBG cleanup.
const CoordinatedPolicyTopologyFinalizer = "workloads.x-k8s.io/topology-policy-protection"

// CoordinatedPolicyTopologyRBGUIDAnnotation records the UID of the RoleBasedGroup
// whose lifecycle is protected by CoordinatedPolicyTopologyFinalizer. Release is
// keyed to this UID so deleting and recreating an RBG with the same name cannot
// satisfy the release condition for the old lifecycle.
const CoordinatedPolicyTopologyRBGUIDAnnotation = "workloads.x-k8s.io/topology-policy-protection-uid"

// RBGTopologyContractAnnotation records the topology contract accepted by the
// RoleBasedGroup when its placement plan was first accepted. The value is the
// canonical PlacementPlan contract ID, or "none" when no topology is configured.
// A later topology-bearing CoordinatedPolicy, or any change to the accepted
// contract, is rejected as TopologyPolicyLate rather than applied to running pods.
const RBGTopologyContractAnnotation = RBGPrefix + "topology-contract"
