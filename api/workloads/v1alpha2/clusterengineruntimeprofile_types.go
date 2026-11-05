/*
Copyright 2025 The RBG Authors.

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

package v1alpha2

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	NoUpdateStrategy      = "NoUpdate"
	RollingUpdateStrategy = "RollingUpdate"
)

// ClusterEngineRuntimeProfileSpec defines the desired state of ClusterEngineRuntimeProfile.
type ClusterEngineRuntimeProfileSpec struct {
	// +optional
	InitContainers []v1.Container `json:"initContainers,omitempty"`
	// +optional
	Containers []v1.Container `json:"containers,omitempty"`
	// +optional
	Volumes []v1.Volume `json:"volumes,omitempty"`
	// +kubebuilder:validation:Enum=NoUpdate;RollingUpdate
	// +kubebuilder:default=NoUpdate
	// Deprecated: updateStrategy was never consumed by the controller and
	// is removed at the next API version boundary alongside
	// injectContainers. Profile revisions are published as new profiles
	// and references migrate one at a time — see Profile Mutability in
	// KEP-531.
	UpdateStrategy string `json:"updateStrategy"`

	// ContainerPatches is the profile-side patch list: target-less
	// entries, each firing at every listed role-declared
	// targetContainers address of the referencing component, in list
	// order. The profile side may address
	// /env, /volumeMounts and /command/0 only; /args and /securityContext/capabilities/add
	// are role-side-only roots, and a profile-side entry addressing one
	// is a render error attributed to the profile. Absent, no container
	// is modified.
	//
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +listType=atomic
	ContainerPatches []ContainerPatch `json:"containerPatches,omitempty"`
}

// ContainerPatch is one ordered patch entry — the format-selection
// shell shared by both sides. Exactly one patch-format field is set,
// selected by optional sibling fields — the same variant-selection
// idiom as Pattern in RoleSpec and TemplateSource. Today `jsonPatch`
// is the only format and is required; a future format joins as a
// sibling field, `jsonPatch` relaxes to optional and a oneOf rule
// takes over — relaxing a required field is a compatible change.
type ContainerPatch struct {
	// JSONPatch carries the RFC 6902 format: `add`-only operations
	// with container-relative paths. See ContainerJSONPatch for its
	// grammar.
	// +kubebuilder:validation:Required
	JSONPatch *ContainerJSONPatch `json:"jsonPatch"`
}

// ContainerJSONPatch is a single JSON Patch (RFC 6902) operation
// applied to a container at render time. `value` is deliberately
// schemaless at write time — loose at write, exact at consumption:
// the render pass unmarshals it strictly into the concrete corev1
// type the path implies. The path allowlist is enforced twice — the
// CEL prefix rule below for immediate write-time feedback, and a
// grammar re-check at render (segment boundaries, element-pointer
// shape) that a CEL string match cannot guarantee. Two deliberate
// deviations from strict RFC 6902 accommodate the common nil-field
// container shape: a missing target list is treated as empty where
// insertion is allowed, and intermediate struct members along an
// allowlisted path are created when absent — except `command` and
// `args`, where creating the list can silently discard image-defined
// process definitions, so render errors instead: creating `args` is
// an error only when the container sets neither command nor args;
// creating `command` is always an error.
// +kubebuilder:validation:XValidation:rule="self.path.startsWith('/command/') || self.path.startsWith('/args/') || self.path.startsWith('/env/') || self.path.startsWith('/volumeMounts/') || self.path.startsWith('/securityContext/capabilities/add/')",message="path must be an element pointer under /command, /args, /env, /volumeMounts or /securityContext/capabilities/add"
type ContainerJSONPatch struct {
	// Op is the patch operation. Only `add` is accepted today;
	// `replace` is reserved as a future enum value and rejected.
	// +kubebuilder:validation:Enum=add
	// +kubebuilder:validation:Required
	Op string `json:"op"`

	// Path is an RFC 6901 pointer relative to the rendered target
	// container: `/command/0` prepends an argv element, `/env/0`
	// inserts an env var at the head, `/volumeMounts/-` appends a
	// mount, `/securityContext/capabilities/add/-` appends a
	// capability. Whole-list paths are forbidden: `add` onto an
	// existing location replaces it, which would overwrite user
	// values.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	Path string `json:"path"`

	// Value is the JSON value the operation adds, in the shape of
	// the path's element: a string for /command, /args and
	// /securityContext/capabilities/add elements; a corev1.EnvVar
	// object for /env; a corev1.VolumeMount object for
	// /volumeMounts. Accepted as arbitrary JSON at write time and
	// unmarshaled into the element's concrete corev1 type at render
	// time; a mismatched shape is a render error attributed to the
	// patch author's object.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Required
	Value runtime.RawExtension `json:"value"`
}

// ClusterEngineRuntimeProfileStatus defines the observed state of
// ClusterEngineRuntimeProfile. Every field is display and detection
// only: rendering reads the profile's spec, enforcement stays with the
// admission webhook, and status is never authoritative.
type ClusterEngineRuntimeProfileStatus struct {
	// ReferenceCount is the count of live references to this profile:
	// RoleBasedGroups plus the group templates of RoleBasedGroupSets,
	// a set's template counting even at zero replicas.
	// +optional
	ReferenceCount int32 `json:"referenceCount,omitempty"`

	// ReferencedGeneration is the profile's metadata.generation recorded
	// at the none-to-referenced transition and cleared when the last
	// reference leaves; it is never refreshed while referenced, so a
	// generation mismatch is drift evidence visible when the webhook is
	// bypassed.
	// +optional
	ReferencedGeneration int64 `json:"referencedGeneration,omitempty"`

	// Conditions carries the Frozen condition: True with reason
	// Referenced while a live reference exists, flipping to reason
	// SpecChangedWhileReferenced on a generation mismatch.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// Condition type and reasons carried on ClusterEngineRuntimeProfile
// status while the profile is referenced.
const (
	// FrozenConditionType is the Frozen condition on
	// ClusterEngineRuntimeProfile: True while a live reference exists.
	FrozenConditionType = "Frozen"

	// ReasonReferenced marks the Frozen condition while the profile's
	// spec matches the generation observed at the referencing
	// transition.
	ReasonReferenced = "Referenced"

	// ReasonSpecChangedWhileReferenced marks the Frozen condition when
	// the profile's generation drifted past the recorded
	// referencedGeneration — evidence of a mutation that bypassed the
	// admission webhook.
	ReasonSpecChangedWhileReferenced = "SpecChangedWhileReferenced"
)

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="REFERENCES",type="integer",JSONPath=".status.referenceCount"

// ClusterEngineRuntimeProfile is the Schema for the clusterengineruntimeprofiles API.
type ClusterEngineRuntimeProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterEngineRuntimeProfileSpec   `json:"spec,omitempty"`
	Status ClusterEngineRuntimeProfileStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterEngineRuntimeProfileList contains a list of ClusterEngineRuntimeProfile.
type ClusterEngineRuntimeProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterEngineRuntimeProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterEngineRuntimeProfile{}, &ClusterEngineRuntimeProfileList{})
}
