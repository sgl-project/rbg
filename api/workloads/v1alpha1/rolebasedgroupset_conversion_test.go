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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	v2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// ── ConvertTo (v1alpha1 → v1alpha2) ──────────────────────────────────────────

func TestRoleBasedGroupSet_ConvertTo(t *testing.T) {
	src := &RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "rbgs", Namespace: "ns"},
		Spec: RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(3)),
			Template: RoleBasedGroupSpec{
				Roles: []RoleSpec{
					{
						Name:     "worker",
						Replicas: ptr.To(int32(2)),
						TemplateSource: TemplateSource{
							Template: podTemplate("worker"),
						},
					},
				},
			},
		},
		Status: RoleBasedGroupSetStatus{
			ObservedGeneration: 5,
			Replicas:           3,
			ReadyReplicas:      2,
		},
	}

	dst := &v2.RoleBasedGroupSet{}
	require.NoError(t, src.ConvertTo(dst))

	assert.Equal(t, "rbgs", dst.Name)
	assert.Equal(t, ptr.To(int32(3)), dst.Spec.Replicas)
	require.Len(t, dst.Spec.GroupTemplate.Spec.Roles, 1)
	role := dst.Spec.GroupTemplate.Spec.Roles[0]
	assert.Equal(t, "worker", role.Name)
	require.NotNil(t, role.Pattern.StandalonePattern)
	require.NotNil(t, role.Pattern.StandalonePattern.Template)
	assert.Equal(t, "worker", role.Pattern.StandalonePattern.Template.Spec.Containers[0].Name)
	assert.Equal(t, int64(5), dst.Status.ObservedGeneration)
	assert.Equal(t, int32(3), dst.Status.Replicas)
	assert.Equal(t, int32(2), dst.Status.ReadyReplicas)
}

func TestRoleBasedGroupSet_ConvertTo_PreservesLossyAnnotations(t *testing.T) {
	timeout := int32(30)
	src := &RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "rbgs", Namespace: "ns"},
		Spec: RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(1)),
			Template: RoleBasedGroupSpec{
				Roles: []RoleSpec{{Name: "w", Replicas: ptr.To(int32(1)), TemplateSource: TemplateSource{Template: podTemplate("app")}}},
				PodGroupPolicy: &PodGroupPolicy{
					PodGroupPolicySource: PodGroupPolicySource{
						KubeScheduling: &KubeSchedulingPodGroupPolicySource{ScheduleTimeoutSeconds: &timeout},
					},
				},
			},
		},
	}

	dst := &v2.RoleBasedGroupSet{}
	require.NoError(t, src.ConvertTo(dst))

	assert.Contains(t, dst.Annotations, annotationV1alpha1PodGroupPolicy)
	var pgp PodGroupPolicy
	require.NoError(t, json.Unmarshal([]byte(dst.Annotations[annotationV1alpha1PodGroupPolicy]), &pgp))
	assert.Equal(t, &timeout, pgp.KubeScheduling.ScheduleTimeoutSeconds)
}

func TestRoleBasedGroupSet_ConvertTo_MergesExistingAnnotations(t *testing.T) {
	// Existing annotations on dst (from ObjectMeta copy) must be preserved when
	// preserve logic adds new conversion keys.
	src := &RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "rbgs",
			Namespace:   "ns",
			Annotations: map[string]string{"existing-key": "existing-value"},
		},
		Spec: RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(1)),
			Template: RoleBasedGroupSpec{
				Roles: []RoleSpec{{Name: "w", Replicas: ptr.To(int32(1)), TemplateSource: TemplateSource{Template: podTemplate("app")}}},
				PodGroupPolicy: &PodGroupPolicy{
					PodGroupPolicySource: PodGroupPolicySource{
						KubeScheduling: &KubeSchedulingPodGroupPolicySource{},
					},
				},
			},
		},
	}

	dst := &v2.RoleBasedGroupSet{}
	require.NoError(t, src.ConvertTo(dst))

	// Both the original and the new conversion annotation must be present.
	assert.Equal(t, "existing-value", dst.Annotations["existing-key"])
	assert.Contains(t, dst.Annotations, annotationV1alpha1PodGroupPolicy)
}

func TestRoleBasedGroupSet_ConvertTo_WrongType(t *testing.T) {
	src := &RoleBasedGroupSet{}
	err := src.ConvertTo(&v2.RoleBasedGroup{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected *v1alpha2.RoleBasedGroupSet")
}

// ── ConvertFrom (v1alpha2 → v1alpha1) ────────────────────────────────────────

func TestRoleBasedGroupSet_ConvertFrom(t *testing.T) {
	src := &v2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "rbgs", Namespace: "ns"},
		Spec: v2.RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(5)),
			GroupTemplate: v2.RoleBasedGroupTemplateSpec{
				Spec: v2.RoleBasedGroupSpec{
					Roles: []v2.RoleSpec{
						{
							Name:     "decode",
							Replicas: ptr.To(int32(2)),
							Pattern: v2.Pattern{
								StandalonePattern: &v2.StandalonePattern{
									TemplateSource: v2.TemplateSource{
										Template: podTemplate("decode"),
									},
								},
							},
						},
					},
				},
			},
		},
		Status: v2.RoleBasedGroupSetStatus{
			ObservedGeneration: 7,
			Replicas:           5,
			ReadyReplicas:      4,
		},
	}

	dst := &RoleBasedGroupSet{}
	require.NoError(t, dst.ConvertFrom(src))

	assert.Equal(t, "rbgs", dst.Name)
	assert.Equal(t, ptr.To(int32(5)), dst.Spec.Replicas)
	require.Len(t, dst.Spec.Template.Roles, 1)
	role := dst.Spec.Template.Roles[0]
	assert.Equal(t, "decode", role.Name)
	require.NotNil(t, role.TemplateSource.Template)
	assert.Equal(t, "decode", role.TemplateSource.Template.Spec.Containers[0].Name)
	assert.Equal(t, int64(7), dst.Status.ObservedGeneration)
	assert.Equal(t, int32(5), dst.Status.Replicas)
	assert.Equal(t, int32(4), dst.Status.ReadyReplicas)
}

func TestRoleBasedGroupSet_ConvertFrom_RestoresLossyFields(t *testing.T) {
	timeout := int32(90)
	pgp := PodGroupPolicy{
		PodGroupPolicySource: PodGroupPolicySource{
			KubeScheduling: &KubeSchedulingPodGroupPolicySource{ScheduleTimeoutSeconds: &timeout},
		},
	}
	pgpJSON, err := json.Marshal(pgp)
	require.NoError(t, err)

	src := &v2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rbgs",
			Namespace: "ns",
			Annotations: map[string]string{
				annotationV1alpha1PodGroupPolicy: string(pgpJSON),
			},
		},
		Spec: v2.RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(1)),
			GroupTemplate: v2.RoleBasedGroupTemplateSpec{
				Spec: v2.RoleBasedGroupSpec{
					Roles: []v2.RoleSpec{{
						Name:     "w",
						Replicas: ptr.To(int32(1)),
						Pattern:  v2.Pattern{StandalonePattern: &v2.StandalonePattern{}},
					}},
				},
			},
		},
	}

	dst := &RoleBasedGroupSet{}
	require.NoError(t, dst.ConvertFrom(src))

	require.NotNil(t, dst.Spec.Template.PodGroupPolicy)
	assert.Equal(t, &timeout, dst.Spec.Template.PodGroupPolicy.KubeScheduling.ScheduleTimeoutSeconds)
	// Conversion annotations must be cleaned up.
	assert.NotContains(t, dst.Annotations, annotationV1alpha1PodGroupPolicy)
}

func TestRoleBasedGroupSet_ConvertFrom_WrongType(t *testing.T) {
	dst := &RoleBasedGroupSet{}
	err := dst.ConvertFrom(&v2.RoleBasedGroup{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected *v1alpha2.RoleBasedGroupSet")
}

// ── Round-trip ────────────────────────────────────────────────────────────────

func TestRoleBasedGroupSet_RoundTrip(t *testing.T) {
	timeout := int32(60)
	original := &RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rbgs",
			Namespace: "ns",
			Labels:    map[string]string{"team": "infra"},
		},
		Spec: RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(4)),
			Template: RoleBasedGroupSpec{
				Roles: []RoleSpec{
					{
						Name:     "prefill",
						Replicas: ptr.To(int32(2)),
						TemplateSource: TemplateSource{
							Template: podTemplate("prefill"),
						},
					},
				},
				PodGroupPolicy: &PodGroupPolicy{
					PodGroupPolicySource: PodGroupPolicySource{
						KubeScheduling: &KubeSchedulingPodGroupPolicySource{ScheduleTimeoutSeconds: &timeout},
					},
				},
			},
		},
	}

	hub := &v2.RoleBasedGroupSet{}
	require.NoError(t, original.ConvertTo(hub))
	restored := &RoleBasedGroupSet{}
	require.NoError(t, restored.ConvertFrom(hub))

	assert.Equal(t, original.Name, restored.Name)
	assert.Equal(t, original.Labels, restored.Labels)
	assert.Equal(t, original.Spec.Replicas, restored.Spec.Replicas)
	require.Len(t, restored.Spec.Template.Roles, 1)
	assert.Equal(t, "prefill", restored.Spec.Template.Roles[0].Name)
	require.NotNil(t, restored.Spec.Template.PodGroupPolicy)
	assert.Equal(t, &timeout, restored.Spec.Template.PodGroupPolicy.KubeScheduling.ScheduleTimeoutSeconds)
}
