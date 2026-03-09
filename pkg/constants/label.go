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

// ========== Labels ==========

// GroupSet level labels
const (
	// GroupSetNameLabelKey identifies resources belonging to a specific RoleBasedGroupSet
	GroupSetNameLabelKey = RBGPrefix + "groupset-name"

	// GroupSetIndexLabelKey identifies the index of the RBG within the RBGSet
	GroupSetIndexLabelKey = RBGPrefix + "groupset-index"
)

// Group level labels
const (
	// GroupNameLabelKey identifies resources belonging to a specific RoleBasedGroup
	GroupNameLabelKey = RBGPrefix + "group-name"

	// GroupUIDLabelKey is set on every Pod and carries a short hash
	// that identifies all Pods belonging to the same RoleBasedGroup instance.
	// Used as the match label for topology affinity.
	GroupUIDLabelKey = RBGPrefix + "group-uid"

	// GroupRevisionLabelKey is the labels key used to store the revision hash of the
	// RoleBasedGroup Roles's template.
	GroupRevisionLabelKey = RBGPrefix + "group-revision"
)

// Role level labels
const (
	// RoleNameLabelKey identifies resources belonging to a specific role
	RoleNameLabelKey = RBGPrefix + "role-name"

	// RoleTypeLabelKey identifies the role template type
	RoleTypeLabelKey = RBGPrefix + "role-type"

	// RoleRevisionLabelKeyFmt is the labels key used to store the revision hash of
	// a specific Role template.
	RoleRevisionLabelKeyFmt = RBGPrefix + "role-revision-%s"
)

// RoleInstance level labels
const (
	// RoleInstanceOwnerLabelKey identifies RoleInstance belonging to a specific controller UID
	RoleInstanceOwnerLabelKey = RBGPrefix + "role-instance-owner"

	// RoleInstanceIDLabelKey is a unique id for RoleInstance and its Pods.
	RoleInstanceIDLabelKey = RBGPrefix + "role-instance-id"

	// RoleInstanceNameLabelKey is the name of the RoleInstance.
	RoleInstanceNameLabelKey = RBGPrefix + "role-instance-name"

	// RoleInstanceIndexLabelKey identifies the index of RoleInstance in Role (for ordered scenarios)
	RoleInstanceIndexLabelKey = RBGPrefix + "role-instance-index"

	// RoleInstanceDeleteLabelKey is a label used to mark that the RoleInstance should be deleted.
	RoleInstanceDeleteLabelKey = RBGPrefix + "role-instance-delete"
)

// Component level labels
const (
	// ComponentNameLabelKey identifies the component name (e.g., leader/worker/coordinator)
	ComponentNameLabelKey = RBGPrefix + "component-name"

	// ComponentIDLabelKey identifies the component instance index within the Instance
	ComponentIDLabelKey = RBGPrefix + "component-id"

	// ComponentSizeLabelKey identifies the component replica count
	ComponentSizeLabelKey = RBGPrefix + "component-size"
)
