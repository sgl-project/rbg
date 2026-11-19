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
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/rbgs/api/workloads/constants"
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
	require.NotNil(t, role.StandalonePattern)
	require.NotNil(t, role.StandalonePattern.Template)
	assert.Equal(t, "worker", role.StandalonePattern.Template.Spec.Containers[0].Name)
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
	require.NotNil(t, role.Template)
	assert.Equal(t, "decode", role.Template.Spec.Containers[0].Name)
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

// TestRoleBasedGroupSet_RoundTrip_PreservesRolloutStrategy pins the blocker scenario: a set on
// the rolling path is read through v1alpha1 and written back with a full-object update, and the
// strategy plus the v1alpha2-only rollout status fields must survive the trip.
func TestRoleBasedGroupSet_RoundTrip_PreservesRolloutStrategy(t *testing.T) {
	hub := &v2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "rbgs", Namespace: "ns"},
		Spec: v2.RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(3)),
			RolloutStrategy: &v2.GroupSetRolloutStrategy{
				Type:           v2.RecreateStrategyType,
				Partition:      ptr.To(intstr.FromInt32(1)),
				MaxUnavailable: ptr.To(intstr.FromInt32(1)),
				MaxSurge:       ptr.To(intstr.FromString("25%")),
			},
			GroupTemplate: v2.RoleBasedGroupTemplateSpec{
				Spec: v2.RoleBasedGroupSpec{
					Roles: []v2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}},
				},
			},
		},
		Status: v2.RoleBasedGroupSetStatus{
			Replicas:                3,
			ReadyReplicas:           3,
			CurrentReplicas:         1,
			UpdatedReplicas:         2,
			UpdatedReadyReplicas:    2,
			ExpectedUpdatedReplicas: 2,
			CurrentRevision:         "rbgs-old",
			UpdateRevision:          "rbgs-new",
		},
	}

	for _, strategyType := range []v2.GroupUpdateStrategyType{v2.RecreateStrategyType, v2.InPlaceUpdateStrategyType} {
		t.Run(string(strategyType), func(t *testing.T) {
			original := hub.DeepCopy()
			original.Spec.RolloutStrategy.Type = strategyType
			view := &RoleBasedGroupSet{}
			require.NoError(t, view.ConvertFrom(original))
			assert.Contains(t, view.Annotations, annotationV1alpha2RolloutStrategy)
			assert.Contains(t, view.Annotations, annotationV1alpha2RolloutStatus)
			assert.NotContains(t, view.Annotations, annotationV1alpha1PodGroupPolicy,
				"hub-carried v1alpha1 stash keys must stay out of the v1alpha1 view")

			view.Labels = map[string]string{"synced": "true"}
			roundTripped := &v2.RoleBasedGroupSet{}
			require.NoError(t, view.ConvertTo(roundTripped))

			assert.Equal(t, original.Spec.RolloutStrategy, roundTripped.Spec.RolloutStrategy)
			assert.Equal(t, original.Status.CurrentReplicas, roundTripped.Status.CurrentReplicas)
			assert.Equal(t, original.Status.UpdatedReplicas, roundTripped.Status.UpdatedReplicas)
			assert.Equal(t, original.Status.UpdatedReadyReplicas, roundTripped.Status.UpdatedReadyReplicas)
			assert.Equal(t, original.Status.ExpectedUpdatedReplicas, roundTripped.Status.ExpectedUpdatedReplicas)
			assert.Equal(t, original.Status.CurrentRevision, roundTripped.Status.CurrentRevision)
			assert.Equal(t, original.Status.UpdateRevision, roundTripped.Status.UpdateRevision)
			assert.NotContains(t, roundTripped.Annotations, annotationV1alpha2RolloutStrategy,
				"the stash must not leak into storage; the hub keeps the values in native fields")
			assert.NotContains(t, roundTripped.Annotations, annotationV1alpha2RolloutStatus)
		})
	}
}

// TestRoleBasedGroupSet_ConvertTo_NativeV1alpha1HasNoStrategy covers the opposite direction: an
// object that only ever existed as v1alpha1 carries no stash, and conversion must leave the
// strategy unset so the set stays on the legacy path.
func TestRoleBasedGroupSet_ConvertTo_NativeV1alpha1HasNoStrategy(t *testing.T) {
	src := &RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "rbgs", Namespace: "ns"},
		Spec: RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(1)),
			Template: RoleBasedGroupSpec{
				Roles: []RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}},
			},
		},
	}

	dst := &v2.RoleBasedGroupSet{}
	require.NoError(t, src.ConvertTo(dst))
	assert.Nil(t, dst.Spec.RolloutStrategy)
	assert.Zero(t, dst.Status.CurrentRevision)
	assert.Zero(t, dst.Status.UpdateRevision)
}

// TestRoleBasedGroupSet_ConvertFrom_NoStrategyNoStash: a set on the legacy path produces no
// v1alpha2 stash annotations, keeping the v1alpha1 view free of conversion noise.
func TestRoleBasedGroupSet_ConvertFrom_NoStrategyNoStash(t *testing.T) {
	hub := &v2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "rbgs", Namespace: "ns"},
		Spec: v2.RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(1)),
			GroupTemplate: v2.RoleBasedGroupTemplateSpec{
				Spec: v2.RoleBasedGroupSpec{
					Roles: []v2.RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}},
				},
			},
		},
		Status: v2.RoleBasedGroupSetStatus{Replicas: 1, ReadyReplicas: 1},
	}

	view := &RoleBasedGroupSet{}
	require.NoError(t, view.ConvertFrom(hub))
	assert.NotContains(t, view.Annotations, annotationV1alpha2RolloutStrategy)
	assert.NotContains(t, view.Annotations, annotationV1alpha2RolloutStatus)
}

// TestRoleBasedGroupSet_ConvertTo_CorruptStash surfaces a hand-corrupted stash as an error
// instead of silently dropping it.
func TestRoleBasedGroupSet_ConvertTo_CorruptStash(t *testing.T) {
	src := &RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rbgs",
			Namespace: "ns",
			Annotations: map[string]string{
				annotationV1alpha2RolloutStrategy: "{not-json",
			},
		},
	}

	err := src.ConvertTo(&v2.RoleBasedGroupSet{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), annotationV1alpha2RolloutStrategy)
}

func TestRoleBasedGroupSet_PodGroupPolicyAnnotations(t *testing.T) {
	type policyCase struct {
		name   string
		policy *PodGroupPolicy
		gang   map[string]string
	}
	cases := []policyCase{
		{name: "nil"},
		{name: "empty", policy: &PodGroupPolicy{}},
		{
			name: "kube-explicit-timeout",
			policy: &PodGroupPolicy{PodGroupPolicySource: PodGroupPolicySource{
				KubeScheduling: &KubeSchedulingPodGroupPolicySource{ScheduleTimeoutSeconds: ptr.To(int32(90))},
			}},
			gang: map[string]string{
				constants.GangSchedulingAnnotationKey:             "true",
				constants.GangSchedulingScheduleTimeoutSecondsKey: "90",
			},
		},
		{
			name: "kube-default-timeout",
			policy: &PodGroupPolicy{PodGroupPolicySource: PodGroupPolicySource{
				KubeScheduling: &KubeSchedulingPodGroupPolicySource{},
			}},
			gang: map[string]string{constants.GangSchedulingAnnotationKey: "true"},
		},
		{
			name: "kube-zero-timeout",
			policy: &PodGroupPolicy{PodGroupPolicySource: PodGroupPolicySource{
				KubeScheduling: &KubeSchedulingPodGroupPolicySource{ScheduleTimeoutSeconds: ptr.To(int32(0))},
			}},
			gang: map[string]string{
				constants.GangSchedulingAnnotationKey:             "true",
				constants.GangSchedulingScheduleTimeoutSecondsKey: "0",
			},
		},
		{
			name: "volcano-queue-and-priority",
			policy: &PodGroupPolicy{PodGroupPolicySource: PodGroupPolicySource{
				VolcanoScheduling: &VolcanoSchedulingPodGroupPolicySource{Queue: "training", PriorityClassName: "high"},
			}},
			gang: map[string]string{
				constants.GangSchedulingAnnotationKey:           "true",
				constants.GangSchedulingVolcanoQueueKey:         "training",
				constants.GangSchedulingVolcanoPriorityClassKey: "high",
			},
		},
		{
			name: "volcano-queue-only",
			policy: &PodGroupPolicy{PodGroupPolicySource: PodGroupPolicySource{
				VolcanoScheduling: &VolcanoSchedulingPodGroupPolicySource{Queue: "batch"},
			}},
			gang: map[string]string{
				constants.GangSchedulingAnnotationKey:   "true",
				constants.GangSchedulingVolcanoQueueKey: "batch",
			},
		},
		{
			name: "volcano-priority-only",
			policy: &PodGroupPolicy{PodGroupPolicySource: PodGroupPolicySource{
				VolcanoScheduling: &VolcanoSchedulingPodGroupPolicySource{PriorityClassName: "low"},
			}},
			gang: map[string]string{
				constants.GangSchedulingAnnotationKey:           "true",
				constants.GangSchedulingVolcanoPriorityClassKey: "low",
			},
		},
		{
			name: "volcano-defaults",
			policy: &PodGroupPolicy{PodGroupPolicySource: PodGroupPolicySource{
				VolcanoScheduling: &VolcanoSchedulingPodGroupPolicySource{},
			}},
			gang: map[string]string{constants.GangSchedulingAnnotationKey: "true"},
		},
	}

	coordination := []Coordination{{Name: "workers", Roles: []string{"worker"}}}
	coordinationJSON, err := json.Marshal(coordination)
	require.NoError(t, err)
	wantAnnotations := func(t *testing.T, tc policyCase) map[string]string {
		t.Helper()
		want := map[string]string{annotationV1alpha1Coordination: string(coordinationJSON)}
		for key, value := range tc.gang {
			want[key] = value
		}
		if tc.policy != nil {
			data, err := json.Marshal(tc.policy)
			require.NoError(t, err)
			want[constants.ConversionV1alpha1PodGroupPolicyAnnotationKey] = string(data)
		}
		return want
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &RoleBasedGroupSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: "rbgs", Namespace: "ns", UID: "set-uid",
					Labels:      map[string]string{"example.com/parent-only": "label"},
					Annotations: map[string]string{"example.com/parent-only": "annotation"},
					Finalizers:  []string{"example.com/parent-only"},
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "v1", Kind: "ConfigMap", Name: "owner", UID: "owner-uid",
					}},
				},
				Spec: RoleBasedGroupSetSpec{
					Template: RoleBasedGroupSpec{
						Roles:                    []RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}},
						PodGroupPolicy:           tc.policy,
						CoordinationRequirements: coordination,
					},
				},
			}
			original := src.DeepCopy()
			hub := &v2.RoleBasedGroupSet{}
			require.NoError(t, src.ConvertTo(hub))
			assert.Equal(t, original, src, "ConvertTo must not mutate its input")
			assert.Equal(t, wantAnnotations(t, tc), hub.Spec.GroupTemplate.Annotations)
			assert.Empty(t, hub.Spec.GroupTemplate.Labels, "parent labels must not reach children")
			wantMeta := original.ObjectMeta.DeepCopy()
			for key, value := range wantAnnotations(t, tc) {
				wantMeta.Annotations[key] = value
			}
			assert.Equal(t, *wantMeta, hub.ObjectMeta, "parent metadata must remain on the set")

			// Round trips must clear annotations from the previous policy without mutating the hub.
			for _, next := range cases {
				t.Run("round-trip-to-"+next.name, func(t *testing.T) {
					inputHub := hub.DeepCopy()
					before := inputHub.DeepCopy()
					view := &RoleBasedGroupSet{}
					require.NoError(t, view.ConvertFrom(inputHub))
					assert.Equal(t, before, inputHub, "ConvertFrom must not mutate its input")
					assert.Equal(t, tc.policy, view.Spec.Template.PodGroupPolicy)
					assert.Equal(t, coordination, view.Spec.Template.CoordinationRequirements)
					assert.Equal(t, original.Annotations, view.Annotations, "derived annotations must not survive in the v1alpha1 view")

					view.Spec.Template.PodGroupPolicy = next.policy
					beforeView := view.DeepCopy()
					updated := &v2.RoleBasedGroupSet{}
					require.NoError(t, view.ConvertTo(updated))
					assert.Equal(t, beforeView, view, "ConvertTo must not mutate the edited view")
					assert.Equal(t, wantAnnotations(t, next), updated.Spec.GroupTemplate.Annotations,
						"only the current policy's stash and derived annotations may reach children")
					assert.Empty(t, updated.Spec.GroupTemplate.Labels)
					wantParent := wantAnnotations(t, next)
					wantParent["example.com/parent-only"] = "annotation"
					assert.Equal(t, wantParent, updated.Annotations, "stale policy annotations must not remain on the set")
				})
			}
		})
	}
}
