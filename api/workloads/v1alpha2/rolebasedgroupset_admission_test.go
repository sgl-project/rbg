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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// TestValidateGroupSetRolloutStrategy covers the rollout budget rules the CRD cannot express:
// an IntOrString field has no minimum, so admission is the only gate against negative budgets,
// which would otherwise wedge a rollout silently.
func TestValidateGroupSetRolloutStrategy(t *testing.T) {
	tests := []struct {
		name     string
		replicas int32
		strategy *GroupSetRolloutStrategy
		wantErr  string
	}{
		{name: "unset strategy is not this validator's concern", replicas: 4},
		{
			name:     "empty strategy takes the documented defaults",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{},
		},
		{
			name:     "recreate with explicit budgets",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{
				Type:           RecreateStrategyType,
				Partition:      ptr.To(intstr.FromInt32(2)),
				MaxUnavailable: ptr.To(intstr.FromInt32(1)),
				MaxSurge:       ptr.To(intstr.FromInt32(1)),
			},
		},
		{
			name:     "recreate percentages",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{
				Type:           RecreateStrategyType,
				Partition:      ptr.To(intstr.FromString("25%")),
				MaxUnavailable: ptr.To(intstr.FromString("0%")),
				MaxSurge:       ptr.To(intstr.FromString("25%")),
			},
		},
		{
			name:     "in-place update is accepted",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{Type: InPlaceUpdateStrategyType},
		},
		{
			name:     "unknown strategy is rejected",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{Type: "Unknown"},
			wantErr:  `unsupported value "Unknown"; supported values are Recreate and InPlaceUpdate`,
		},
		{
			name:     "negative maxUnavailable",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{MaxUnavailable: ptr.To(intstr.FromInt32(-1))},
			wantErr:  "maxUnavailable: invalid value",
		},
		{
			name:     "negative maxSurge",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{MaxSurge: ptr.To(intstr.FromInt32(-1))},
			wantErr:  "maxSurge: invalid value",
		},
		{
			name:     "negative partition",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{Partition: ptr.To(intstr.FromInt32(-1))},
			wantErr:  "partition: invalid value",
		},
		{
			name:     "negative percentage",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{MaxSurge: ptr.To(intstr.FromString("-10%"))},
			wantErr:  "maxSurge: invalid value",
		},
		{
			name:     "partition percentage above 100",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{Partition: ptr.To(intstr.FromString("101%"))},
			wantErr:  "spec.rolloutStrategy.partition: percentage must be between 0% and 100%",
		},
		{
			name:     "maxUnavailable percentage above 100",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{MaxUnavailable: ptr.To(intstr.FromString("101%"))},
			wantErr:  "spec.rolloutStrategy.maxUnavailable: percentage must be between 0% and 100%",
		},
		{
			name:     "maxSurge percentage above 100",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{MaxSurge: ptr.To(intstr.FromString("101%"))},
			wantErr:  "spec.rolloutStrategy.maxSurge: percentage must be between 0% and 100%",
		},
		{
			name:     "100 percent is the maximum valid percentage",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{
				Type:           RecreateStrategyType,
				Partition:      ptr.To(intstr.FromString("100%")),
				MaxUnavailable: ptr.To(intstr.FromString("100%")),
				MaxSurge:       ptr.To(intstr.FromString("100%")),
			},
		},
		{
			name:     "partition beyond replicas",
			replicas: 3,
			strategy: &GroupSetRolloutStrategy{Partition: ptr.To(intstr.FromInt32(4))},
			wantErr:  "must be between 0 and replicas (3)",
		},
		{
			name:     "partition equal to replicas is the full hold-back",
			replicas: 3,
			strategy: &GroupSetRolloutStrategy{Partition: ptr.To(intstr.FromString("100%"))},
		},
		{
			name:     "recreate integer zero budgets cannot make progress",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{
				Type:           RecreateStrategyType,
				MaxUnavailable: ptr.To(intstr.FromInt32(0)),
				MaxSurge:       ptr.To(intstr.FromInt32(0)),
			},
			wantErr: "maxUnavailable and maxSurge cannot both be 0 or 0% for Recreate",
		},
		{
			name:     "empty type rejects zero percentage budgets as in-place",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{
				MaxUnavailable: ptr.To(intstr.FromString("0%")),
				MaxSurge:       ptr.To(intstr.FromString("0%")),
			},
			wantErr: "maxUnavailable: must be greater than 0 for InPlaceUpdate",
		},
		{
			name:     "recreate zero maxUnavailable with surge is the no-downtime shape",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{
				Type:           RecreateStrategyType,
				MaxUnavailable: ptr.To(intstr.FromInt32(0)),
				MaxSurge:       ptr.To(intstr.FromInt32(2)),
			},
		},
		{
			name:     "zero surge with unavailable is accepted for empty type",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{
				MaxUnavailable: ptr.To(intstr.FromInt32(1)),
				MaxSurge:       ptr.To(intstr.FromInt32(0)),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &RoleBasedGroupSetSpec{
				Replicas:        ptr.To(tt.replicas),
				RolloutStrategy: tt.strategy,
			}
			err := validateGroupSetRolloutStrategy(spec)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestRoleBasedGroupSetValidator_RolloutStrategyOnBothVerbs checks the complete budget
// matrix, including mixed zero forms and positive percentages that round down to zero.
func TestRoleBasedGroupSetValidator_RolloutStrategyOnBothVerbs(t *testing.T) {
	validator := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: true}
	build := func(strategy *GroupSetRolloutStrategy) *RoleBasedGroupSet {
		return &RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs", Namespace: "default"},
			Spec: RoleBasedGroupSetSpec{
				Replicas:        ptr.To(int32(1)),
				RolloutStrategy: strategy,
				GroupTemplate: RoleBasedGroupTemplateSpec{
					Spec: RoleBasedGroupSpec{
						Roles: []RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}},
					},
				},
			},
		}
	}
	validateBoth := func(t *testing.T, strategy *GroupSetRolloutStrategy, wantErrs ...string) {
		t.Helper()
		for _, verb := range []string{"create", "update"} {
			t.Run(verb, func(t *testing.T) {
				var err error
				if verb == "create" {
					_, err = validator.ValidateCreate(context.Background(), build(strategy))
				} else {
					_, err = validator.ValidateUpdate(context.Background(), build(nil), build(strategy))
				}
				if len(wantErrs) == 0 {
					require.NoError(t, err)
					return
				}
				require.Error(t, err)
				for _, wantErr := range wantErrs {
					assert.Contains(t, err.Error(), wantErr)
				}
			})
		}
	}
	t.Run("legacy", func(t *testing.T) { validateBoth(t, nil) })
	t.Run("unknown strategy", func(t *testing.T) {
		validateBoth(t, &GroupSetRolloutStrategy{Type: "Unknown"},
			`unsupported value "Unknown"; supported values are Recreate and InPlaceUpdate`)
	})
	budgets := []struct {
		name     string
		value    *intstr.IntOrString
		zero     bool
		positive bool
	}{
		{name: "omitted"},
		{name: "0", value: ptr.To(intstr.FromInt32(0)), zero: true},
		{name: "0%", value: ptr.To(intstr.FromString("0%")), zero: true},
		{name: "1", value: ptr.To(intstr.FromInt32(1)), positive: true},
		{name: "1%", value: ptr.To(intstr.FromString("1%")), positive: true},
	}
	for _, strategyType := range []GroupUpdateStrategyType{"", RecreateStrategyType, InPlaceUpdateStrategyType} {
		t.Run("type="+string(strategyType), func(t *testing.T) {
			for _, field := range []struct {
				name   string
				assign func(*GroupSetRolloutStrategy)
			}{
				{"partition", func(s *GroupSetRolloutStrategy) { s.Partition = ptr.To(intstr.FromString("101%")) }},
				{"maxUnavailable", func(s *GroupSetRolloutStrategy) { s.MaxUnavailable = ptr.To(intstr.FromString("101%")) }},
				{"maxSurge", func(s *GroupSetRolloutStrategy) { s.MaxSurge = ptr.To(intstr.FromString("101%")) }},
			} {
				t.Run(field.name+"/percentage above 100", func(t *testing.T) {
					strategy := &GroupSetRolloutStrategy{Type: strategyType}
					field.assign(strategy)
					validateBoth(t, strategy, field.name+": percentage must be between 0% and 100%")
				})
			}
			for _, unavailable := range budgets {
				for _, surge := range budgets {
					t.Run("unavailable="+unavailable.name+"/surge="+surge.name, func(t *testing.T) {
						var wantErrs []string
						if strategyType == RecreateStrategyType {
							if unavailable.zero && !surge.positive {
								wantErrs = append(wantErrs, "maxUnavailable and maxSurge cannot both be 0 or 0% for Recreate")
							}
						} else {
							if unavailable.zero {
								wantErrs = append(wantErrs, "maxUnavailable: must be greater than 0 for InPlaceUpdate")
							}
							if surge.positive {
								wantErrs = append(wantErrs, "maxSurge: must be omitted, 0 or 0% for InPlaceUpdate")
							}
						}
						validateBoth(t, &GroupSetRolloutStrategy{
							Type: strategyType, MaxUnavailable: unavailable.value, MaxSurge: surge.value,
						}, wantErrs...)
					})
				}
			}
			for _, invalid := range []intstr.IntOrString{
				intstr.FromInt32(-1), intstr.FromString("-1%"), intstr.FromString(""),
				intstr.FromString("1"), intstr.FromString("1.5%"), intstr.FromString("%"),
				intstr.FromString("1%%"), intstr.FromString("9223372036854775808%"),
				{Type: intstr.Type(2), IntVal: 1},
			} {
				for _, field := range []string{"maxUnavailable", "maxSurge", "partition"} {
					t.Run(field+"/invalid="+invalid.String(), func(t *testing.T) {
						strategy := &GroupSetRolloutStrategy{Type: strategyType}
						switch field {
						case "maxUnavailable":
							strategy.MaxUnavailable = ptr.To(invalid)
						case "maxSurge":
							strategy.MaxSurge = ptr.To(invalid)
						case "partition":
							strategy.Partition = ptr.To(invalid)
						}
						validateBoth(t, strategy, field+": invalid")
					})
				}
			}
		})
	}
}

func TestRoleBasedGroupSetValidator_RolloutStrategyTransitions(t *testing.T) {
	validator := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: true}
	for _, surge := range []intstr.IntOrString{intstr.FromInt32(1), intstr.FromString("1%")} {
		for _, targetType := range []GroupUpdateStrategyType{InPlaceUpdateStrategyType, ""} {
			t.Run("recreate to type="+string(targetType)+"/surge="+surge.String(), func(t *testing.T) {
				old := &RoleBasedGroupSet{Spec: RoleBasedGroupSetSpec{
					Replicas: ptr.To(int32(1)),
					RolloutStrategy: &GroupSetRolloutStrategy{
						Type: RecreateStrategyType, MaxSurge: ptr.To(surge),
					},
				}}
				updated := old.DeepCopy()
				updated.Spec.RolloutStrategy.Type = targetType
				_, err := validator.ValidateUpdate(context.Background(), old, updated)
				require.ErrorContains(t, err, "maxSurge: must be omitted, 0 or 0% for InPlaceUpdate")

				for _, cleared := range []*intstr.IntOrString{nil, ptr.To(intstr.FromInt32(0)), ptr.To(intstr.FromString("0%"))} {
					updated.Spec.RolloutStrategy.MaxSurge = cleared
					_, err = validator.ValidateUpdate(context.Background(), old, updated)
					require.NoError(t, err)
				}
			})
		}
		for _, unavailable := range []intstr.IntOrString{intstr.FromInt32(0), intstr.FromString("0%")} {
			t.Run("in-place to recreate/unavailable="+unavailable.String()+"/surge="+surge.String(), func(t *testing.T) {
				old := &RoleBasedGroupSet{Spec: RoleBasedGroupSetSpec{
					Replicas:        ptr.To(int32(1)),
					RolloutStrategy: &GroupSetRolloutStrategy{Type: InPlaceUpdateStrategyType},
				}}
				updated := old.DeepCopy()
				updated.Spec.RolloutStrategy = &GroupSetRolloutStrategy{
					Type: RecreateStrategyType, MaxUnavailable: ptr.To(unavailable), MaxSurge: ptr.To(surge),
				}
				_, err := validator.ValidateUpdate(context.Background(), old, updated)
				require.NoError(t, err)
			})
		}
	}
}

func TestRoleBasedGroupSetDefaulter_RolloutStrategy(t *testing.T) {
	tests := []struct {
		name     string
		strategy *GroupSetRolloutStrategy
		wantType GroupUpdateStrategyType
	}{
		{name: "unset strategy stays legacy"},
		{name: "empty strategy", strategy: &GroupSetRolloutStrategy{}, wantType: InPlaceUpdateStrategyType},
		{
			name: "empty type preserves budgets",
			strategy: &GroupSetRolloutStrategy{
				Partition:      ptr.To(intstr.FromString("25%")),
				MaxUnavailable: ptr.To(intstr.FromString("1%")), MaxSurge: ptr.To(intstr.FromString("0%")),
			},
			wantType: InPlaceUpdateStrategyType,
		},
		{name: "recreate stays explicit", strategy: &GroupSetRolloutStrategy{Type: RecreateStrategyType}, wantType: RecreateStrategyType},
		{name: "in-place stays explicit", strategy: &GroupSetRolloutStrategy{Type: InPlaceUpdateStrategyType}, wantType: InPlaceUpdateStrategyType},
		{name: "unknown type is not healed", strategy: &GroupSetRolloutStrategy{Type: "Unknown"}, wantType: "Unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rbgs := &RoleBasedGroupSet{Spec: RoleBasedGroupSetSpec{RolloutStrategy: tt.strategy.DeepCopy()}}
			want := rbgs.DeepCopy()
			if want.Spec.RolloutStrategy != nil {
				want.Spec.RolloutStrategy.Type = tt.wantType
			}
			defaulter := &RoleBasedGroupSetDefaulter{}
			require.NoError(t, defaulter.Default(context.Background(), rbgs))
			assert.Equal(t, want, rbgs)
			require.NoError(t, defaulter.Default(context.Background(), rbgs))
			assert.Equal(t, want, rbgs, "defaulting must be idempotent")
		})
	}
}

// TestRoleBasedGroupSetValidator_ScalingAdapterTransitions covers the compatibility split:
// enabling an adapter is never allowed, but a set that already carries one stays updatable, so
// upgrading the operator cannot lock pre-existing objects out of unrelated changes.
func TestRoleBasedGroupSetValidator_ScalingAdapterTransitions(t *testing.T) {
	validator := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: true}
	build := func(roles ...RoleSpec) *RoleBasedGroupSet {
		return &RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs", Namespace: "default"},
			Spec: RoleBasedGroupSetSpec{
				Replicas: ptr.To(int32(2)),
				GroupTemplate: RoleBasedGroupTemplateSpec{
					Spec: RoleBasedGroupSpec{Roles: roles},
				},
			},
		}
	}
	plain := RoleSpec{Name: "worker", Replicas: ptr.To(int32(1))}
	adapterOff := RoleSpec{
		Name: "worker", Replicas: ptr.To(int32(1)),
		ScalingAdapter: &ScalingAdapter{Enable: false},
	}
	adapterOn := RoleSpec{
		Name: "worker", Replicas: ptr.To(int32(1)),
		ScalingAdapter: &ScalingAdapter{Enable: true},
	}
	addedAdapterOn := RoleSpec{
		Name: "extra", Replicas: ptr.To(int32(1)),
		ScalingAdapter: &ScalingAdapter{Enable: true},
	}

	tests := []struct {
		name    string
		old     *RoleBasedGroupSet
		new     *RoleBasedGroupSet
		wantErr string
	}{
		{
			name:    "create with an enabled adapter is rejected",
			old:     nil,
			new:     build(adapterOn),
			wantErr: `cannot enable scaling adapter in a RoleBasedGroupSet`,
		},
		{
			name: "create with an explicitly disabled adapter is allowed",
			old:  nil,
			new:  build(adapterOff),
		},
		{
			name:    "update that enables an existing role's adapter is rejected",
			old:     build(adapterOff),
			new:     build(adapterOn),
			wantErr: `roles[0].scalingAdapter.enable (role "worker"): cannot enable scaling adapter`,
		},
		{
			name:    "update that adds a role with an enabled adapter is rejected",
			old:     build(plain),
			new:     build(plain, addedAdapterOn),
			wantErr: `roles[1].scalingAdapter.enable (role "extra"): cannot enable scaling adapter`,
		},
		{
			name: "an already enabled adapter does not block an unrelated update",
			old:  build(adapterOn),
			new: func() *RoleBasedGroupSet {
				scaled := build(adapterOn)
				scaled.Spec.Replicas = ptr.To(int32(4))
				return scaled
			}(),
		},
		{
			name: "disabling an adapter is allowed",
			old:  build(adapterOn),
			new:  build(adapterOff),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.old == nil {
				_, err = validator.ValidateCreate(context.Background(), tt.new)
			} else {
				_, err = validator.ValidateUpdate(context.Background(), tt.old, tt.new)
			}
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
