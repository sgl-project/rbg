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

package v1alpha1

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	v2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func podTemplate(containerName string) *corev1.PodTemplateSpec {
	return &corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: containerName, Image: "nginx:latest"}},
		},
	}
}

func rawPatch(t *testing.T, v any) runtime.RawExtension {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return runtime.RawExtension{Raw: b}
}

// ── ConvertTo (v1alpha1 → v1alpha2) ──────────────────────────────────────────

func TestRoleBasedGroup_ConvertTo_StandalonePattern(t *testing.T) {
	src := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{
				{
					Name:     "worker",
					Replicas: ptr.To(int32(3)),
					TemplateSource: TemplateSource{
						Template: podTemplate("app"),
					},
				},
			},
		},
	}

	dst := &v2.RoleBasedGroup{}
	require.NoError(t, src.ConvertTo(dst))

	assert.Equal(t, "rbg", dst.Name)
	require.Len(t, dst.Spec.Roles, 1)
	role := dst.Spec.Roles[0]
	assert.Equal(t, "worker", role.Name)
	assert.Equal(t, ptr.To(int32(3)), role.Replicas)
	require.NotNil(t, role.Pattern.StandalonePattern)
	require.NotNil(t, role.Pattern.StandalonePattern.Template)
	assert.Equal(t, "app", role.Pattern.StandalonePattern.Template.Spec.Containers[0].Name)
	assert.Nil(t, role.Pattern.LeaderWorkerPattern)
	assert.Nil(t, role.Pattern.CustomComponentsPattern)
}

func TestRoleBasedGroup_ConvertTo_TemplateRef(t *testing.T) {
	patch := rawPatch(t, map[string]any{"metadata": map[string]any{"labels": map[string]string{"env": "prod"}}})
	src := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: RoleBasedGroupSpec{
			RoleTemplates: []RoleTemplate{
				{Name: "base", Template: *podTemplate("app")},
			},
			Roles: []RoleSpec{
				{
					Name:     "worker",
					Replicas: ptr.To(int32(1)),
					TemplateSource: TemplateSource{
						TemplateRef: &TemplateRef{Name: "base"},
					},
					TemplatePatch: patch,
				},
			},
		},
	}

	dst := &v2.RoleBasedGroup{}
	require.NoError(t, src.ConvertTo(dst))

	role := dst.Spec.Roles[0]
	require.NotNil(t, role.Pattern.StandalonePattern)
	ref := role.Pattern.StandalonePattern.TemplateRef
	require.NotNil(t, ref)
	assert.Equal(t, "base", ref.Name)
	require.NotNil(t, ref.Patch, "patch should be folded into TemplateRef.Patch")
	assert.JSONEq(t, string(patch.Raw), string(ref.Patch.Raw))
}

func TestRoleBasedGroup_ConvertTo_LeaderWorkerPattern(t *testing.T) {
	leaderPatch := rawPatch(t, map[string]any{"metadata": map[string]any{"labels": map[string]string{"role": "leader"}}})
	src := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{
				{
					Name:     "inference",
					Replicas: ptr.To(int32(2)),
					TemplateSource: TemplateSource{
						Template: podTemplate("leader"),
					},
					LeaderWorkerSet: &LeaderWorkerTemplate{
						Size:                ptr.To(int32(4)),
						PatchLeaderTemplate: &leaderPatch,
					},
				},
			},
		},
	}

	dst := &v2.RoleBasedGroup{}
	require.NoError(t, src.ConvertTo(dst))

	role := dst.Spec.Roles[0]
	require.NotNil(t, role.Pattern.LeaderWorkerPattern)
	lwp := role.Pattern.LeaderWorkerPattern
	assert.Equal(t, ptr.To(int32(4)), lwp.Size)
	require.NotNil(t, lwp.LeaderTemplatePatch)
	assert.JSONEq(t, string(leaderPatch.Raw), string(lwp.LeaderTemplatePatch.Raw))
	require.NotNil(t, lwp.Template)
	assert.Equal(t, "leader", lwp.Template.Spec.Containers[0].Name)
}

func TestRoleBasedGroup_ConvertTo_CustomComponentsPattern(t *testing.T) {
	src := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{
				{
					Name:     "pd",
					Replicas: ptr.To(int32(1)),
					Components: []InstanceComponent{
						{Name: "prefill", Size: ptr.To(int32(4)), Template: *podTemplate("prefill")},
						{Name: "decode", Size: ptr.To(int32(2)), Template: *podTemplate("decode")},
					},
				},
			},
		},
	}

	dst := &v2.RoleBasedGroup{}
	require.NoError(t, src.ConvertTo(dst))

	role := dst.Spec.Roles[0]
	require.NotNil(t, role.Pattern.CustomComponentsPattern)
	comps := role.Pattern.CustomComponentsPattern.Components
	require.Len(t, comps, 2)
	assert.Equal(t, "prefill", comps[0].Name)
	assert.Equal(t, "decode", comps[1].Name)
}

func TestRoleBasedGroup_ConvertTo_PreservesLossyFields(t *testing.T) {
	timeout := int32(30)
	src := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{{Name: "w", Replicas: ptr.To(int32(1)), TemplateSource: TemplateSource{Template: podTemplate("app")}}},
			PodGroupPolicy: &PodGroupPolicy{
				PodGroupPolicySource: PodGroupPolicySource{
					KubeScheduling: &KubeSchedulingPodGroupPolicySource{ScheduleTimeoutSeconds: &timeout},
				},
			},
			CoordinationRequirements: []Coordination{
				{Name: "coord1", Roles: []string{"w"}},
			},
		},
	}

	dst := &v2.RoleBasedGroup{}
	require.NoError(t, src.ConvertTo(dst))

	assert.Contains(t, dst.Annotations, annotationV1alpha1PodGroupPolicy)
	assert.Contains(t, dst.Annotations, annotationV1alpha1Coordination)

	// Verify the serialized JSON is parseable
	var pgp PodGroupPolicy
	require.NoError(t, json.Unmarshal([]byte(dst.Annotations[annotationV1alpha1PodGroupPolicy]), &pgp))
	assert.Equal(t, &timeout, pgp.KubeScheduling.ScheduleTimeoutSeconds)
}

func TestRoleBasedGroup_ConvertTo_WrongType(t *testing.T) {
	src := &RoleBasedGroup{}
	err := src.ConvertTo(&v2.RoleBasedGroupSet{}) // wrong type
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected *v1alpha2.RoleBasedGroup")
}

// TestRoleBasedGroup_ConvertTo_KubeGangScheduling verifies that a v1alpha1 PodGroupPolicy
// with KubeScheduling is translated into the controller-readable gang-scheduling annotations
// on the v1alpha2 hub object.
func TestRoleBasedGroup_ConvertTo_KubeGangScheduling(t *testing.T) {
	timeout := int32(30)
	src := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{{Name: "w", Replicas: ptr.To(int32(1)), TemplateSource: TemplateSource{Template: podTemplate("app")}}},
			PodGroupPolicy: &PodGroupPolicy{
				PodGroupPolicySource: PodGroupPolicySource{
					KubeScheduling: &KubeSchedulingPodGroupPolicySource{ScheduleTimeoutSeconds: &timeout},
				},
			},
		},
	}

	dst := &v2.RoleBasedGroup{}
	require.NoError(t, src.ConvertTo(dst))

	// Controller-readable annotations must be set.
	assert.Equal(t, "true", dst.Annotations[constants.GangSchedulingAnnotationKey])
	assert.Equal(t, "30", dst.Annotations[constants.GangSchedulingScheduleTimeoutSecondsKey])
	// Round-trip annotation must also be preserved.
	assert.Contains(t, dst.Annotations, annotationV1alpha1PodGroupPolicy)
}

// TestRoleBasedGroup_ConvertTo_KubeGangSchedulingNoTimeout verifies that when no timeout
// is specified, only GangSchedulingAnnotationKey is set (no timeout annotation).
func TestRoleBasedGroup_ConvertTo_KubeGangSchedulingNoTimeout(t *testing.T) {
	src := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{{Name: "w", Replicas: ptr.To(int32(1)), TemplateSource: TemplateSource{Template: podTemplate("app")}}},
			PodGroupPolicy: &PodGroupPolicy{
				PodGroupPolicySource: PodGroupPolicySource{
					KubeScheduling: &KubeSchedulingPodGroupPolicySource{},
				},
			},
		},
	}

	dst := &v2.RoleBasedGroup{}
	require.NoError(t, src.ConvertTo(dst))

	assert.Equal(t, "true", dst.Annotations[constants.GangSchedulingAnnotationKey])
	assert.NotContains(t, dst.Annotations, constants.GangSchedulingScheduleTimeoutSecondsKey)
}

// TestRoleBasedGroup_ConvertTo_VolcanoGangScheduling verifies that a v1alpha1 PodGroupPolicy
// with VolcanoScheduling is translated into the appropriate Volcano annotations.
func TestRoleBasedGroup_ConvertTo_VolcanoGangScheduling(t *testing.T) {
	src := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{{Name: "w", Replicas: ptr.To(int32(1)), TemplateSource: TemplateSource{Template: podTemplate("app")}}},
			PodGroupPolicy: &PodGroupPolicy{
				PodGroupPolicySource: PodGroupPolicySource{
					VolcanoScheduling: &VolcanoSchedulingPodGroupPolicySource{
						Queue:             "high-priority",
						PriorityClassName: "system-node-critical",
					},
				},
			},
		},
	}

	dst := &v2.RoleBasedGroup{}
	require.NoError(t, src.ConvertTo(dst))

	assert.Equal(t, "true", dst.Annotations[constants.GangSchedulingAnnotationKey])
	assert.Equal(t, "high-priority", dst.Annotations[constants.GangSchedulingVolcanoQueueKey])
	assert.Equal(t, "system-node-critical", dst.Annotations[constants.GangSchedulingVolcanoPriorityClassKey])
}

// TestRoleBasedGroup_ConvertFrom_GangSchedulingAnnotationsRemoved verifies that the
// translated gang-scheduling annotations are cleaned up when converting back to v1alpha1.
func TestRoleBasedGroup_ConvertFrom_GangSchedulingAnnotationsRemoved(t *testing.T) {
	timeout := int32(45)
	pgp := PodGroupPolicy{
		PodGroupPolicySource: PodGroupPolicySource{
			KubeScheduling: &KubeSchedulingPodGroupPolicySource{ScheduleTimeoutSeconds: &timeout},
		},
	}
	pgpJSON, err := json.Marshal(pgp)
	require.NoError(t, err)

	src := &v2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rbg",
			Namespace: "ns",
			Annotations: map[string]string{
				annotationV1alpha1PodGroupPolicy:                  string(pgpJSON),
				constants.GangSchedulingAnnotationKey:             "true",
				constants.GangSchedulingScheduleTimeoutSecondsKey: "45",
			},
		},
		Spec: v2.RoleBasedGroupSpec{
			Roles: []v2.RoleSpec{{
				Name:     "w",
				Replicas: ptr.To(int32(1)),
				Pattern:  v2.Pattern{StandalonePattern: &v2.StandalonePattern{}},
			}},
		},
	}

	dst := &RoleBasedGroup{}
	require.NoError(t, dst.ConvertFrom(src))

	// PodGroupPolicy must be restored.
	require.NotNil(t, dst.Spec.PodGroupPolicy)
	assert.Equal(t, &timeout, dst.Spec.PodGroupPolicy.KubeScheduling.ScheduleTimeoutSeconds)

	// All conversion-only and translated annotations must be stripped.
	assert.NotContains(t, dst.Annotations, annotationV1alpha1PodGroupPolicy)
	assert.NotContains(t, dst.Annotations, constants.GangSchedulingAnnotationKey)
	assert.NotContains(t, dst.Annotations, constants.GangSchedulingScheduleTimeoutSecondsKey)
}

// TestRoleBasedGroup_RoundTrip_KubeGangScheduling verifies that kube gang scheduling
// survives a v1alpha1 → v1alpha2 → v1alpha1 round-trip, and that the hub object
// carries the controller-readable annotation.
func TestRoleBasedGroup_RoundTrip_KubeGangScheduling(t *testing.T) {
	timeout := int32(60)
	original := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{{Name: "w", Replicas: ptr.To(int32(1)), TemplateSource: TemplateSource{Template: podTemplate("app")}}},
			PodGroupPolicy: &PodGroupPolicy{
				PodGroupPolicySource: PodGroupPolicySource{
					KubeScheduling: &KubeSchedulingPodGroupPolicySource{ScheduleTimeoutSeconds: &timeout},
				},
			},
		},
	}

	hub := &v2.RoleBasedGroup{}
	require.NoError(t, original.ConvertTo(hub))

	// Hub must have the controller-readable annotation so the gang-scheduling logic fires.
	assert.Equal(t, "true", hub.Annotations[constants.GangSchedulingAnnotationKey])
	assert.Equal(t, "60", hub.Annotations[constants.GangSchedulingScheduleTimeoutSecondsKey])

	restored := &RoleBasedGroup{}
	require.NoError(t, restored.ConvertFrom(hub))

	require.NotNil(t, restored.Spec.PodGroupPolicy)
	assert.Equal(t, &timeout, restored.Spec.PodGroupPolicy.KubeScheduling.ScheduleTimeoutSeconds)
	// Translated annotations must not leak into v1alpha1 object.
	assert.NotContains(t, restored.Annotations, constants.GangSchedulingAnnotationKey)
	assert.NotContains(t, restored.Annotations, constants.GangSchedulingScheduleTimeoutSecondsKey)
}

// TestRoleBasedGroup_ConvertTo_ExclusiveTopology verifies that the v1alpha1
// exclusive-topology annotation is translated to the controller-readable key on the hub.
func TestRoleBasedGroup_ConvertTo_ExclusiveTopology(t *testing.T) {
	src := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rbg",
			Namespace: "ns",
			Annotations: map[string]string{
				ExclusiveKeyAnnotationKey: "kubernetes.io/hostname",
			},
		},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{{Name: "w", Replicas: ptr.To(int32(1)), TemplateSource: TemplateSource{Template: podTemplate("app")}}},
		},
	}

	dst := &v2.RoleBasedGroup{}
	require.NoError(t, src.ConvertTo(dst))

	// Controller-readable key must be present with the correct value.
	assert.Equal(t, "kubernetes.io/hostname", dst.Annotations[constants.GroupExclusiveTopologyKey])
	// Original v1alpha1 key is preserved (it lives in ObjectMeta which is copied as-is).
	assert.Equal(t, "kubernetes.io/hostname", dst.Annotations[ExclusiveKeyAnnotationKey])
}

// TestRoleBasedGroup_ConvertFrom_ExclusiveTopologyAnnotationRemoved verifies that the
// translated exclusive-topology annotation is cleaned up when converting back to v1alpha1.
func TestRoleBasedGroup_ConvertFrom_ExclusiveTopologyAnnotationRemoved(t *testing.T) {
	src := &v2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rbg",
			Namespace: "ns",
			Annotations: map[string]string{
				ExclusiveKeyAnnotationKey:           "kubernetes.io/hostname",
				constants.GroupExclusiveTopologyKey: "kubernetes.io/hostname",
			},
		},
		Spec: v2.RoleBasedGroupSpec{
			Roles: []v2.RoleSpec{{
				Name:     "w",
				Replicas: ptr.To(int32(1)),
				Pattern:  v2.Pattern{StandalonePattern: &v2.StandalonePattern{}},
			}},
		},
	}

	dst := &RoleBasedGroup{}
	require.NoError(t, dst.ConvertFrom(src))

	// The v1alpha1 exclusive-topology key stays (it's in ObjectMeta from the original source).
	assert.Equal(t, "kubernetes.io/hostname", dst.Annotations[ExclusiveKeyAnnotationKey])
	// The translated constants key must be stripped.
	assert.NotContains(t, dst.Annotations, constants.GroupExclusiveTopologyKey)
}

// TestRoleBasedGroup_RoundTrip_ExclusiveTopology verifies that the exclusive-topology
// annotation survives a round-trip and the hub object carries the controller-readable key.
func TestRoleBasedGroup_RoundTrip_ExclusiveTopology(t *testing.T) {
	original := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rbg",
			Namespace: "ns",
			Annotations: map[string]string{
				ExclusiveKeyAnnotationKey: "kubernetes.io/hostname",
			},
		},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{{Name: "w", Replicas: ptr.To(int32(1)), TemplateSource: TemplateSource{Template: podTemplate("app")}}},
		},
	}

	hub := &v2.RoleBasedGroup{}
	require.NoError(t, original.ConvertTo(hub))

	// Hub must have the controller-readable key.
	assert.Equal(t, "kubernetes.io/hostname", hub.Annotations[constants.GroupExclusiveTopologyKey])

	restored := &RoleBasedGroup{}
	require.NoError(t, restored.ConvertFrom(hub))

	// v1alpha1 key preserved, translated key cleaned up.
	assert.Equal(t, "kubernetes.io/hostname", restored.Annotations[ExclusiveKeyAnnotationKey])
	assert.NotContains(t, restored.Annotations, constants.GroupExclusiveTopologyKey)
}

// ── ConvertFrom (v1alpha2 → v1alpha1) ────────────────────────────────────────

func TestRoleBasedGroup_ConvertFrom_StandalonePattern(t *testing.T) {
	src := &v2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: v2.RoleBasedGroupSpec{
			Roles: []v2.RoleSpec{
				{
					Name:     "worker",
					Replicas: ptr.To(int32(3)),
					Pattern: v2.Pattern{
						StandalonePattern: &v2.StandalonePattern{
							TemplateSource: v2.TemplateSource{
								Template: podTemplate("app"),
							},
						},
					},
				},
			},
		},
	}

	dst := &RoleBasedGroup{}
	require.NoError(t, dst.ConvertFrom(src))

	require.Len(t, dst.Spec.Roles, 1)
	role := dst.Spec.Roles[0]
	assert.Equal(t, "worker", role.Name)
	require.NotNil(t, role.TemplateSource.Template)
	assert.Equal(t, "app", role.TemplateSource.Template.Spec.Containers[0].Name)
	assert.Nil(t, role.LeaderWorkerSet)
	assert.Empty(t, role.Components)
}

func TestRoleBasedGroup_ConvertFrom_TemplateRef_PatchUnfolded(t *testing.T) {
	patch := rawPatch(t, map[string]any{"metadata": map[string]any{"labels": map[string]string{"env": "prod"}}})
	src := &v2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: v2.RoleBasedGroupSpec{
			Roles: []v2.RoleSpec{
				{
					Name:     "worker",
					Replicas: ptr.To(int32(1)),
					Pattern: v2.Pattern{
						StandalonePattern: &v2.StandalonePattern{
							TemplateSource: v2.TemplateSource{
								TemplateRef: &v2.TemplateRef{Name: "base", Patch: &patch},
							},
						},
					},
				},
			},
		},
	}

	dst := &RoleBasedGroup{}
	require.NoError(t, dst.ConvertFrom(src))

	role := dst.Spec.Roles[0]
	require.NotNil(t, role.TemplateSource.TemplateRef)
	assert.Equal(t, "base", role.TemplateSource.TemplateRef.Name)
	assert.JSONEq(t, string(patch.Raw), string(role.TemplatePatch.Raw), "patch should be unfolded back into TemplatePatch")
}

func TestRoleBasedGroup_ConvertFrom_LeaderWorkerPattern(t *testing.T) {
	workerPatch := rawPatch(t, map[string]any{"metadata": map[string]any{"labels": map[string]string{"role": "worker"}}})
	src := &v2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: v2.RoleBasedGroupSpec{
			Roles: []v2.RoleSpec{
				{
					Name:     "inference",
					Replicas: ptr.To(int32(2)),
					Pattern: v2.Pattern{
						LeaderWorkerPattern: &v2.LeaderWorkerPattern{
							Size:                ptr.To(int32(4)),
							WorkerTemplatePatch: &workerPatch,
							TemplateSource: v2.TemplateSource{
								Template: podTemplate("leader"),
							},
						},
					},
				},
			},
		},
	}

	dst := &RoleBasedGroup{}
	require.NoError(t, dst.ConvertFrom(src))

	role := dst.Spec.Roles[0]
	require.NotNil(t, role.LeaderWorkerSet)
	assert.Equal(t, ptr.To(int32(4)), role.LeaderWorkerSet.Size)
	require.NotNil(t, role.LeaderWorkerSet.PatchWorkerTemplate)
	assert.JSONEq(t, string(workerPatch.Raw), string(role.LeaderWorkerSet.PatchWorkerTemplate.Raw))
}

func TestRoleBasedGroup_ConvertFrom_RestoresLossyFields(t *testing.T) {
	timeout := int32(45)
	pgp := PodGroupPolicy{
		PodGroupPolicySource: PodGroupPolicySource{
			KubeScheduling: &KubeSchedulingPodGroupPolicySource{ScheduleTimeoutSeconds: &timeout},
		},
	}
	pgpJSON, err := json.Marshal(pgp)
	require.NoError(t, err)

	coord := []Coordination{{Name: "c1", Roles: []string{"worker"}}}
	coordJSON, err := json.Marshal(coord)
	require.NoError(t, err)

	src := &v2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rbg",
			Namespace: "ns",
			Annotations: map[string]string{
				annotationV1alpha1PodGroupPolicy: string(pgpJSON),
				annotationV1alpha1Coordination:   string(coordJSON),
			},
		},
		Spec: v2.RoleBasedGroupSpec{
			Roles: []v2.RoleSpec{{
				Name:     "worker",
				Replicas: ptr.To(int32(1)),
				Pattern:  v2.Pattern{StandalonePattern: &v2.StandalonePattern{}},
			}},
		},
	}

	dst := &RoleBasedGroup{}
	require.NoError(t, dst.ConvertFrom(src))

	require.NotNil(t, dst.Spec.PodGroupPolicy)
	assert.Equal(t, &timeout, dst.Spec.PodGroupPolicy.KubeScheduling.ScheduleTimeoutSeconds)
	require.Len(t, dst.Spec.CoordinationRequirements, 1)
	assert.Equal(t, "c1", dst.Spec.CoordinationRequirements[0].Name)

	// Conversion-only annotations must be stripped from the v1alpha1 object.
	assert.NotContains(t, dst.Annotations, annotationV1alpha1PodGroupPolicy)
	assert.NotContains(t, dst.Annotations, annotationV1alpha1Coordination)
}

func TestRoleBasedGroup_ConvertFrom_WrongType(t *testing.T) {
	dst := &RoleBasedGroup{}
	err := dst.ConvertFrom(&v2.RoleBasedGroupSet{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected *v1alpha2.RoleBasedGroup")
}

// ── Round-trip fidelity ───────────────────────────────────────────────────────

// TestRoleBasedGroup_RoundTrip verifies that converting v1alpha1→v1alpha2→v1alpha1
// preserves all fields that survive the round-trip (lossy fields are tested separately).
func TestRoleBasedGroup_RoundTrip_Standalone(t *testing.T) {
	original := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rbg",
			Namespace: "ns",
			Labels:    map[string]string{"app": "test"},
		},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{
				{
					Name:     "prefill",
					Replicas: ptr.To(int32(4)),
					TemplateSource: TemplateSource{
						Template: podTemplate("prefill"),
					},
					Workload: WorkloadSpec{APIVersion: "apps/v1", Kind: "StatefulSet"},
				},
			},
		},
	}

	hub := &v2.RoleBasedGroup{}
	require.NoError(t, original.ConvertTo(hub))

	restored := &RoleBasedGroup{}
	require.NoError(t, restored.ConvertFrom(hub))

	assert.Equal(t, original.Name, restored.Name)
	assert.Equal(t, original.Namespace, restored.Namespace)
	assert.Equal(t, original.Labels, restored.Labels)
	require.Len(t, restored.Spec.Roles, 1)
	r := restored.Spec.Roles[0]
	assert.Equal(t, "prefill", r.Name)
	assert.Equal(t, ptr.To(int32(4)), r.Replicas)
	require.NotNil(t, r.TemplateSource.Template)
	assert.Equal(t, "prefill", r.TemplateSource.Template.Spec.Containers[0].Name)
	assert.Equal(t, "apps/v1", r.Workload.APIVersion)
	assert.Equal(t, "StatefulSet", r.Workload.Kind)
}

func TestRoleBasedGroup_RoundTrip_LossyFieldsPreserved(t *testing.T) {
	timeout := int32(60)
	original := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{{Name: "w", Replicas: ptr.To(int32(1)), TemplateSource: TemplateSource{Template: podTemplate("app")}}},
			PodGroupPolicy: &PodGroupPolicy{
				PodGroupPolicySource: PodGroupPolicySource{
					KubeScheduling: &KubeSchedulingPodGroupPolicySource{ScheduleTimeoutSeconds: &timeout},
				},
			},
			CoordinationRequirements: []Coordination{
				{Name: "coord", Roles: []string{"w"}},
			},
		},
	}

	hub := &v2.RoleBasedGroup{}
	require.NoError(t, original.ConvertTo(hub))
	restored := &RoleBasedGroup{}
	require.NoError(t, restored.ConvertFrom(hub))

	require.NotNil(t, restored.Spec.PodGroupPolicy)
	assert.Equal(t, &timeout, restored.Spec.PodGroupPolicy.KubeScheduling.ScheduleTimeoutSeconds)
	require.Len(t, restored.Spec.CoordinationRequirements, 1)
	assert.Equal(t, "coord", restored.Spec.CoordinationRequirements[0].Name)
}

// TestRoleBasedGroup_ConvertTo_CoordinationStrategyRollingUpdate verifies that a
// v1alpha1 coordination with a RollingUpdate strategy is correctly serialized
// into the migration annotation, including all strategy fields.
func TestRoleBasedGroup_ConvertTo_CoordinationStrategyRollingUpdate(t *testing.T) {
	maxSkew := "10%"
	partition := "20%"
	maxUnavailable := "5%"
	src := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{{Name: "w", Replicas: ptr.To(int32(1)), TemplateSource: TemplateSource{Template: podTemplate("app")}}},
			CoordinationRequirements: []Coordination{
				{
					Name:  "update-coord",
					Roles: []string{"prefill", "decode"},
					Strategy: &CoordinationStrategy{
						RollingUpdate: &CoordinationRollingUpdate{
							MaxSkew:        &maxSkew,
							Partition:      &partition,
							MaxUnavailable: &maxUnavailable,
						},
					},
				},
			},
		},
	}

	dst := &v2.RoleBasedGroup{}
	require.NoError(t, src.ConvertTo(dst))

	raw := dst.Annotations[annotationV1alpha1Coordination]
	require.NotEmpty(t, raw)

	var coords []Coordination
	require.NoError(t, json.Unmarshal([]byte(raw), &coords))
	require.Len(t, coords, 1)
	coord := coords[0]
	assert.Equal(t, "update-coord", coord.Name)
	assert.Equal(t, []string{"prefill", "decode"}, coord.Roles)
	require.NotNil(t, coord.Strategy)
	require.NotNil(t, coord.Strategy.RollingUpdate)
	assert.Equal(t, &maxSkew, coord.Strategy.RollingUpdate.MaxSkew)
	assert.Equal(t, &partition, coord.Strategy.RollingUpdate.Partition)
	assert.Equal(t, &maxUnavailable, coord.Strategy.RollingUpdate.MaxUnavailable)
}

// TestRoleBasedGroup_ConvertTo_CoordinationStrategyScaling verifies that a
// v1alpha1 coordination with a Scaling strategy is correctly serialized.
func TestRoleBasedGroup_ConvertTo_CoordinationStrategyScaling(t *testing.T) {
	maxSkew := "5%"
	prog := OrderReady
	src := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{{Name: "w", Replicas: ptr.To(int32(1)), TemplateSource: TemplateSource{Template: podTemplate("app")}}},
			CoordinationRequirements: []Coordination{
				{
					Name:  "scale-coord",
					Roles: []string{"worker"},
					Strategy: &CoordinationStrategy{
						Scaling: &CoordinationScaling{
							MaxSkew:     &maxSkew,
							Progression: &prog,
						},
					},
				},
			},
		},
	}

	dst := &v2.RoleBasedGroup{}
	require.NoError(t, src.ConvertTo(dst))

	var coords []Coordination
	require.NoError(t, json.Unmarshal([]byte(dst.Annotations[annotationV1alpha1Coordination]), &coords))
	require.Len(t, coords, 1)
	require.NotNil(t, coords[0].Strategy.Scaling)
	assert.Equal(t, &maxSkew, coords[0].Strategy.Scaling.MaxSkew)
	assert.Equal(t, &prog, coords[0].Strategy.Scaling.Progression)
}

// TestRoleBasedGroup_RoundTrip_CoordinationFullStrategy verifies that all
// coordination strategy fields survive a v1alpha1 → v1alpha2 → v1alpha1 round-trip.
func TestRoleBasedGroup_RoundTrip_CoordinationFullStrategy(t *testing.T) {
	maxSkewRU := "10%"
	partition := "50%"
	maxUnavailable := "5%"
	maxSkewSC := "5%"
	prog := OrderScheduled

	original := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: RoleBasedGroupSpec{
			Roles: []RoleSpec{{Name: "w", Replicas: ptr.To(int32(1)), TemplateSource: TemplateSource{Template: podTemplate("app")}}},
			CoordinationRequirements: []Coordination{
				{
					Name:  "coord-ru",
					Roles: []string{"prefill", "decode"},
					Strategy: &CoordinationStrategy{
						RollingUpdate: &CoordinationRollingUpdate{
							MaxSkew:        &maxSkewRU,
							Partition:      &partition,
							MaxUnavailable: &maxUnavailable,
						},
					},
				},
				{
					Name:  "coord-sc",
					Roles: []string{"worker"},
					Strategy: &CoordinationStrategy{
						Scaling: &CoordinationScaling{
							MaxSkew:     &maxSkewSC,
							Progression: &prog,
						},
					},
				},
			},
		},
	}

	hub := &v2.RoleBasedGroup{}
	require.NoError(t, original.ConvertTo(hub))
	restored := &RoleBasedGroup{}
	require.NoError(t, restored.ConvertFrom(hub))

	require.Len(t, restored.Spec.CoordinationRequirements, 2)

	ru := restored.Spec.CoordinationRequirements[0]
	assert.Equal(t, "coord-ru", ru.Name)
	assert.Equal(t, []string{"prefill", "decode"}, ru.Roles)
	require.NotNil(t, ru.Strategy.RollingUpdate)
	assert.Equal(t, &maxSkewRU, ru.Strategy.RollingUpdate.MaxSkew)
	assert.Equal(t, &partition, ru.Strategy.RollingUpdate.Partition)
	assert.Equal(t, &maxUnavailable, ru.Strategy.RollingUpdate.MaxUnavailable)

	sc := restored.Spec.CoordinationRequirements[1]
	assert.Equal(t, "coord-sc", sc.Name)
	require.NotNil(t, sc.Strategy.Scaling)
	assert.Equal(t, &maxSkewSC, sc.Strategy.Scaling.MaxSkew)
	assert.Equal(t, &prog, sc.Strategy.Scaling.Progression)

	// Annotations must be cleaned up on the v1alpha1 object.
	assert.NotContains(t, restored.Annotations, annotationV1alpha1Coordination)
}

// TestRoleBasedGroup_RoundTrip_WorkloadAnnotation verifies that the Workload field
// survives a v1alpha1 → v1alpha2 → v1alpha1 round-trip via the role annotation.
func TestRoleBasedGroup_RoundTrip_WorkloadAnnotation(t *testing.T) {
	tests := []struct {
		name     string
		workload WorkloadSpec
	}{
		{
			name:     "StatefulSet",
			workload: WorkloadSpec{APIVersion: "apps/v1", Kind: "StatefulSet"},
		},
		{
			name:     "Deployment",
			workload: WorkloadSpec{APIVersion: "apps/v1", Kind: "Deployment"},
		},
		{
			name:     "LeaderWorkerSet",
			workload: WorkloadSpec{APIVersion: "leaderworkerset.x-k8s.io/v1", Kind: "LeaderWorkerSet"},
		},
		{
			name:     "InstanceSet (v1alpha1)",
			workload: WorkloadSpec{APIVersion: "workloads.x-k8s.io/v1alpha1", Kind: "InstanceSet"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := &RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(1)),
							TemplateSource: TemplateSource{
								Template: podTemplate("app"),
							},
							Workload: tt.workload,
						},
					},
				},
			}

			hub := &v2.RoleBasedGroup{}
			require.NoError(t, original.ConvertTo(hub))

			// Verify the annotation is set on the v1alpha2 role
			require.Len(t, hub.Spec.Roles, 1)
			role := hub.Spec.Roles[0]
			assert.Equal(t, fmt.Sprintf("%s/%s", tt.workload.APIVersion, tt.workload.Kind),
				role.Annotations[constants.RoleWorkloadTypeAnnotationKey])

			restored := &RoleBasedGroup{}
			require.NoError(t, restored.ConvertFrom(hub))

			// Verify Workload is correctly restored
			require.Len(t, restored.Spec.Roles, 1)
			r := restored.Spec.Roles[0]
			assert.Equal(t, tt.workload.APIVersion, r.Workload.APIVersion)
			assert.Equal(t, tt.workload.Kind, r.Workload.Kind)
		})
	}
}

// TestRoleBasedGroup_ConvertFrom_WorkloadDefaultFallback verifies that when a
// v1alpha2 RoleSpec has no workload-type annotation, the v1alpha1 Workload field
// is populated with the default (RoleInstanceSet) instead of being left empty.
func TestRoleBasedGroup_ConvertFrom_WorkloadDefaultFallback(t *testing.T) {
	src := &v2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: v2.RoleBasedGroupSpec{
			Roles: []v2.RoleSpec{
				{
					Name:     "worker",
					Replicas: ptr.To(int32(1)),
					Pattern: v2.Pattern{
						StandalonePattern: &v2.StandalonePattern{
							TemplateSource: v2.TemplateSource{
								Template: podTemplate("app"),
							},
						},
					},
					// No annotations set — should fallback to default RoleInstanceSet
				},
			},
		},
	}

	dst := &RoleBasedGroup{}
	require.NoError(t, dst.ConvertFrom(src))

	require.Len(t, dst.Spec.Roles, 1)
	role := dst.Spec.Roles[0]
	// Workload should be populated with the default, not left empty
	assert.NotEmpty(t, role.Workload.APIVersion, "Workload.APIVersion should not be empty")
	assert.NotEmpty(t, role.Workload.Kind, "Workload.Kind should not be empty")
	assert.Equal(t, "RoleInstanceSet", role.Workload.Kind)
}

// TestRoleBasedGroup_ConvertTo_WorkloadAnnotationPreserved verifies that the
// workload-type annotation is correctly set on the v1alpha2 role when converting
// from v1alpha1, and that empty Workload fields do not set the annotation.
func TestRoleBasedGroup_ConvertTo_WorkloadAnnotationPreserved(t *testing.T) {
	t.Run("non-empty workload sets annotation", func(t *testing.T) {
		src := &RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
			Spec: RoleBasedGroupSpec{
				Roles: []RoleSpec{
					{
						Name:     "worker",
						Replicas: ptr.To(int32(1)),
						TemplateSource: TemplateSource{
							Template: podTemplate("app"),
						},
						Workload: WorkloadSpec{APIVersion: "apps/v1", Kind: "StatefulSet"},
					},
				},
			},
		}

		dst := &v2.RoleBasedGroup{}
		require.NoError(t, src.ConvertTo(dst))

		require.Len(t, dst.Spec.Roles, 1)
		role := dst.Spec.Roles[0]
		assert.Equal(t, "apps/v1/StatefulSet", role.Annotations[constants.RoleWorkloadTypeAnnotationKey])
	})

	t.Run("empty workload does not set annotation", func(t *testing.T) {
		src := &RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
			Spec: RoleBasedGroupSpec{
				Roles: []RoleSpec{
					{
						Name:     "worker",
						Replicas: ptr.To(int32(1)),
						TemplateSource: TemplateSource{
							Template: podTemplate("app"),
						},
						// Workload is zero-value — annotation should not be set
					},
				},
			},
		}

		dst := &v2.RoleBasedGroup{}
		require.NoError(t, src.ConvertTo(dst))

		require.Len(t, dst.Spec.Roles, 1)
		role := dst.Spec.Roles[0]
		_, hasAnnotation := role.Annotations[constants.RoleWorkloadTypeAnnotationKey]
		assert.False(t, hasAnnotation, "annotation should not be set for empty workload")
	})
}

// TestRoleBasedGroup_ConvertFrom_MalformedWorkloadAnnotation verifies that a
// malformed workload-type annotation falls back to the default WorkloadSpec.
func TestRoleBasedGroup_ConvertFrom_MalformedWorkloadAnnotation(t *testing.T) {
	src := &v2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: v2.RoleBasedGroupSpec{
			Roles: []v2.RoleSpec{
				{
					Name:     "worker",
					Replicas: ptr.To(int32(1)),
					Annotations: map[string]string{
						constants.RoleWorkloadTypeAnnotationKey: "malformed-no-slash",
					},
					Pattern: v2.Pattern{
						StandalonePattern: &v2.StandalonePattern{
							TemplateSource: v2.TemplateSource{
								Template: podTemplate("app"),
							},
						},
					},
				},
			},
		},
	}

	dst := &RoleBasedGroup{}
	require.NoError(t, dst.ConvertFrom(src))

	require.Len(t, dst.Spec.Roles, 1)
	role := dst.Spec.Roles[0]
	// Should fall back to the default (from src.GetWorkloadSpec())
	assert.NotEmpty(t, role.Workload.APIVersion)
	assert.NotEmpty(t, role.Workload.Kind)
}

func TestRoleBasedGroup_RoundTrip_RoleTemplates(t *testing.T) {
	patch := rawPatch(t, map[string]any{"spec": map[string]any{"nodeSelector": map[string]string{"gpu": "true"}}})
	original := &RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "ns"},
		Spec: RoleBasedGroupSpec{
			RoleTemplates: []RoleTemplate{
				{Name: "gpu-base", Template: *podTemplate("app")},
			},
			Roles: []RoleSpec{
				{
					Name:     "decoder",
					Replicas: ptr.To(int32(2)),
					TemplateSource: TemplateSource{
						TemplateRef: &TemplateRef{Name: "gpu-base"},
					},
					TemplatePatch: patch,
				},
			},
		},
	}

	hub := &v2.RoleBasedGroup{}
	require.NoError(t, original.ConvertTo(hub))
	restored := &RoleBasedGroup{}
	require.NoError(t, restored.ConvertFrom(hub))

	require.Len(t, restored.Spec.RoleTemplates, 1)
	assert.Equal(t, "gpu-base", restored.Spec.RoleTemplates[0].Name)
	role := restored.Spec.Roles[0]
	require.NotNil(t, role.TemplateSource.TemplateRef)
	assert.Equal(t, "gpu-base", role.TemplateSource.TemplateRef.Name)
	assert.JSONEq(t, string(patch.Raw), string(role.TemplatePatch.Raw))
}
