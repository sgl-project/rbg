package v1alpha1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

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
