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

package v1alpha2

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/rbgs/api/workloads/constants"
)

func defaultingContext(t *testing.T, operation admissionv1.Operation) context.Context {
	t.Helper()
	return admission.NewContextWithRequest(context.Background(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{Operation: operation},
	})
}

func TestRoleBasedGroupDefaulterDiscoveryConfigVersion(t *testing.T) {
	t.Run("CREATE defaults a missing annotation to v1", func(t *testing.T) {
		rbg := &RoleBasedGroup{}
		require.NoError(t, (&RoleBasedGroupDefaulter{}).Default(defaultingContext(t, admissionv1.Create), rbg))
		assert.Equal(t, "v1", rbg.Annotations["rbg.workloads.x-k8s.io/discovery-config-version"])
	})

	t.Run("CREATE keeps an explicit v1alpha1 opt-out", func(t *testing.T) {
		rbg := &RoleBasedGroup{}
		rbg.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersionV1Alpha1)
		require.NoError(t, (&RoleBasedGroupDefaulter{}).Default(defaultingContext(t, admissionv1.Create), rbg))
		assert.Equal(t, "v1alpha1", rbg.Annotations["rbg.workloads.x-k8s.io/discovery-config-version"])
	})

	t.Run("CREATE defaults a deprecated stateful role to v1alpha1", func(t *testing.T) {
		rbg := &RoleBasedGroup{
			Spec: RoleBasedGroupSpec{
				Roles: []RoleSpec{{
					Name: "worker",
					Annotations: map[string]string{
						constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType,
					},
				}},
			},
		}
		require.NoError(t, (&RoleBasedGroupDefaulter{}).Default(defaultingContext(t, admissionv1.Create), rbg))
		assert.Equal(t, "v1alpha1", rbg.Annotations["rbg.workloads.x-k8s.io/discovery-config-version"])
	})

	t.Run("UPDATE never defaults a missing annotation", func(t *testing.T) {
		rbg := &RoleBasedGroup{}
		require.NoError(t, (&RoleBasedGroupDefaulter{}).Default(defaultingContext(t, admissionv1.Update), rbg))
		assert.NotContains(t, rbg.Annotations, "rbg.workloads.x-k8s.io/discovery-config-version")
	})

	t.Run("UPDATE keeps the selected version", func(t *testing.T) {
		rbg := &RoleBasedGroup{}
		rbg.SetDiscoveryConfigVersion(constants.DiscoveryConfigVersionV1)
		require.NoError(t, (&RoleBasedGroupDefaulter{}).Default(defaultingContext(t, admissionv1.Update), rbg))
		assert.Equal(t, "v1", rbg.Annotations["rbg.workloads.x-k8s.io/discovery-config-version"])
	})

	t.Run("an UPDATE object without a creation timestamp is not defaulted", func(t *testing.T) {
		rbg := &RoleBasedGroup{}
		require.NoError(t, (&RoleBasedGroupDefaulter{}).Default(defaultingContext(t, admissionv1.Update), rbg))
		assert.NotContains(t, rbg.Annotations, "rbg.workloads.x-k8s.io/discovery-config-version")
	})

	t.Run("a context without an admission request never defaults", func(t *testing.T) {
		rbg := &RoleBasedGroup{}
		require.NoError(t, (&RoleBasedGroupDefaulter{}).Default(context.Background(), rbg))
		assert.NotContains(t, rbg.Annotations, "rbg.workloads.x-k8s.io/discovery-config-version")
	})
}

func TestRoleBasedGroupDefaulter_Default(t *testing.T) {
	tests := []struct {
		name string
		typ  UpdateStrategyType
		want UpdateStrategyType
	}{
		{"empty defaults to InPlaceIfPossible", "", InPlaceIfPossibleUpdateStrategyType},
		{"legacy Recreate maps to RecreatePod", LegacyRecreateUpdateStrategyType, RecreatePodUpdateStrategyType},
		{"valid value passes through", InPlaceOnlyUpdateStrategyType, InPlaceOnlyUpdateStrategyType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rbg := &RoleBasedGroup{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{
							Name: "worker",
							RolloutStrategy: &RolloutStrategy{
								Type:          RollingUpdateStrategyType,
								RollingUpdate: &RollingUpdate{Type: tt.typ},
							},
						},
						{Name: "bare"},
					},
				},
			}

			require.NoError(t, (&RoleBasedGroupDefaulter{}).Default(context.Background(), rbg))
			assert.Equal(t, tt.want, rbg.Spec.Roles[0].RolloutStrategy.RollingUpdate.Type)
		})
	}

	t.Run("nil RolloutStrategy and RollingUpdate do not panic", func(t *testing.T) {
		rbg := &RoleBasedGroup{
			Spec: RoleBasedGroupSpec{
				Roles: []RoleSpec{
					{Name: "no-strategy"},
					{Name: "no-rolling-update", RolloutStrategy: &RolloutStrategy{Type: RollingUpdateStrategyType}},
				},
			},
		}
		require.NoError(t, (&RoleBasedGroupDefaulter{}).Default(context.Background(), rbg))
	})

	t.Run("wrong object type returns error", func(t *testing.T) {
		err := (&RoleBasedGroupDefaulter{}).Default(context.Background(), &RoleInstanceSet{})
		assert.Error(t, err)
	})
}

func TestRoleInstanceSetDefaulter_Default(t *testing.T) {
	tests := []struct {
		name string
		typ  UpdateStrategyType
		want UpdateStrategyType
	}{
		{"empty defaults to InPlaceIfPossible", "", InPlaceIfPossibleUpdateStrategyType},
		{"legacy Recreate maps to RecreatePod", LegacyRecreateUpdateStrategyType, RecreatePodUpdateStrategyType},
		{"valid value passes through", RecreatePodUpdateStrategyType, RecreatePodUpdateStrategyType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ris := &RoleInstanceSet{
				Spec: RoleInstanceSetSpec{
					UpdateStrategy: RoleInstanceSetUpdateStrategy{Type: tt.typ},
				},
			}

			require.NoError(t, (&RoleInstanceSetDefaulter{}).Default(context.Background(), ris))
			assert.Equal(t, tt.want, ris.Spec.UpdateStrategy.Type)
		})
	}
}

func TestRoleBasedGroupSetDefaulter_Default(t *testing.T) {
	rbgset := &RoleBasedGroupSet{
		Spec: RoleBasedGroupSetSpec{
			GroupTemplate: RoleBasedGroupTemplateSpec{
				Spec: RoleBasedGroupSpec{
					Roles: []RoleSpec{
						{
							Name: "worker",
							RolloutStrategy: &RolloutStrategy{
								Type:          RollingUpdateStrategyType,
								RollingUpdate: &RollingUpdate{Type: LegacyRecreateUpdateStrategyType},
							},
						},
						{
							Name: "decode",
							RolloutStrategy: &RolloutStrategy{
								Type:          RollingUpdateStrategyType,
								RollingUpdate: &RollingUpdate{Type: ""},
							},
						},
						{Name: "bare"},
					},
				},
			},
		},
	}

	require.NoError(t, (&RoleBasedGroupSetDefaulter{}).Default(context.Background(), rbgset))
	roles := rbgset.Spec.GroupTemplate.Spec.Roles
	assert.Equal(t, RecreatePodUpdateStrategyType, roles[0].RolloutStrategy.RollingUpdate.Type)
	assert.Equal(t, InPlaceIfPossibleUpdateStrategyType, roles[1].RolloutStrategy.RollingUpdate.Type)
}

func TestRoleBasedGroupSetDefaulterDiscoveryConfigVersion(t *testing.T) {
	t.Run("CREATE defaults the template annotation to v1", func(t *testing.T) {
		rbgset := &RoleBasedGroupSet{}
		require.NoError(t, (&RoleBasedGroupSetDefaulter{}).Default(defaultingContext(t, admissionv1.Create), rbgset))
		assert.Equal(
			t,
			"v1",
			rbgset.Spec.GroupTemplate.Annotations["rbg.workloads.x-k8s.io/discovery-config-version"],
		)
	})

	t.Run("CREATE keeps an explicit v1alpha1 opt-out", func(t *testing.T) {
		rbgset := &RoleBasedGroupSet{}
		rbgset.Spec.GroupTemplate.Annotations = map[string]string{
			"rbg.workloads.x-k8s.io/discovery-config-version": "v1alpha1",
		}
		require.NoError(t, (&RoleBasedGroupSetDefaulter{}).Default(defaultingContext(t, admissionv1.Create), rbgset))
		assert.Equal(
			t,
			"v1alpha1",
			rbgset.Spec.GroupTemplate.Annotations["rbg.workloads.x-k8s.io/discovery-config-version"],
		)
	})

	t.Run("CREATE defaults a deprecated stateful template role to v1alpha1", func(t *testing.T) {
		rbgset := &RoleBasedGroupSet{
			Spec: RoleBasedGroupSetSpec{
				GroupTemplate: RoleBasedGroupTemplateSpec{
					Spec: RoleBasedGroupSpec{
						Roles: []RoleSpec{{
							Name: "worker",
							Annotations: map[string]string{
								constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType,
							},
						}},
					},
				},
			},
		}
		require.NoError(t, (&RoleBasedGroupSetDefaulter{}).Default(defaultingContext(t, admissionv1.Create), rbgset))
		assert.Equal(
			t,
			"v1alpha1",
			rbgset.Spec.GroupTemplate.Annotations["rbg.workloads.x-k8s.io/discovery-config-version"],
		)
	})

	t.Run("UPDATE never defaults a missing template annotation", func(t *testing.T) {
		rbgset := &RoleBasedGroupSet{}
		require.NoError(t, (&RoleBasedGroupSetDefaulter{}).Default(defaultingContext(t, admissionv1.Update), rbgset))
		assert.Nil(t, rbgset.Spec.GroupTemplate.Annotations)
	})

	t.Run("a context without an admission request never defaults", func(t *testing.T) {
		rbgset := &RoleBasedGroupSet{}
		require.NoError(t, (&RoleBasedGroupSetDefaulter{}).Default(context.Background(), rbgset))
		assert.Nil(t, rbgset.Spec.GroupTemplate.Annotations)
	})

	t.Run("CREATE defaults an explicitly empty template annotation", func(t *testing.T) {
		rbgset := &RoleBasedGroupSet{}
		rbgset.Spec.GroupTemplate.Annotations = map[string]string{
			"rbg.workloads.x-k8s.io/discovery-config-version": "",
		}
		require.NoError(t, (&RoleBasedGroupSetDefaulter{}).Default(defaultingContext(t, admissionv1.Create), rbgset))
		assert.Equal(
			t,
			"v1",
			rbgset.Spec.GroupTemplate.Annotations["rbg.workloads.x-k8s.io/discovery-config-version"],
		)
	})
}
