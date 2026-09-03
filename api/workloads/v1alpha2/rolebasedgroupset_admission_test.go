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
			name:     "percentages",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{
				Partition:      ptr.To(intstr.FromString("25%")),
				MaxUnavailable: ptr.To(intstr.FromString("0%")),
				MaxSurge:       ptr.To(intstr.FromString("25%")),
			},
		},
		{
			name:     "reserved in-place update is rejected",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{Type: InPlaceUpdateStrategyType},
			wantErr:  `unsupported value "InPlaceUpdate"`,
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
			name:     "percentage above 100",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{Partition: ptr.To(intstr.FromString("200%"))},
			wantErr:  "percentage must be between 0% and 100%",
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
			name:     "both budgets integer zero cannot make progress",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{
				MaxUnavailable: ptr.To(intstr.FromInt32(0)),
				MaxSurge:       ptr.To(intstr.FromInt32(0)),
			},
			wantErr: "maxUnavailable and maxSurge cannot both be integer 0",
		},
		{
			name:     "zero percentages resolve to zero at runtime and fall back to maxUnavailable=1",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{
				MaxUnavailable: ptr.To(intstr.FromString("0%")),
				MaxSurge:       ptr.To(intstr.FromString("0%")),
			},
		},
		{
			name:     "zero maxUnavailable with surge is the no-downtime shape",
			replicas: 4,
			strategy: &GroupSetRolloutStrategy{
				MaxUnavailable: ptr.To(intstr.FromInt32(0)),
				MaxSurge:       ptr.To(intstr.FromInt32(2)),
			},
		},
		{
			name:     "zero surge with unavailable is the recreate shape",
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

// TestRoleBasedGroupSetValidator_RolloutStrategyOnBothVerbs pins that an invalid budget is
// rejected on update as well as create: a set that already exists must not be able to reach the
// wedged state by patching its strategy.
func TestRoleBasedGroupSetValidator_RolloutStrategyOnBothVerbs(t *testing.T) {
	validator := &RoleBasedGroupSetValidator{EnableDeprecatedWorkloadTypes: true}
	build := func(strategy *GroupSetRolloutStrategy) *RoleBasedGroupSet {
		return &RoleBasedGroupSet{
			ObjectMeta: metav1.ObjectMeta{Name: "test-rbgs", Namespace: "default"},
			Spec: RoleBasedGroupSetSpec{
				Replicas:        ptr.To(int32(3)),
				RolloutStrategy: strategy,
				GroupTemplate: RoleBasedGroupTemplateSpec{
					Spec: RoleBasedGroupSpec{
						Roles: []RoleSpec{{Name: "worker", Replicas: ptr.To(int32(1))}},
					},
				},
			},
		}
	}
	broken := build(&GroupSetRolloutStrategy{
		MaxUnavailable: ptr.To(intstr.FromInt32(-1)),
		MaxSurge:       ptr.To(intstr.FromInt32(1)),
	})

	_, err := validator.ValidateCreate(context.Background(), broken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maxUnavailable: invalid value")

	_, err = validator.ValidateUpdate(context.Background(), build(nil), broken)
	require.Error(t, err, "an update must not be able to introduce a negative budget")
	assert.Contains(t, err.Error(), "maxUnavailable: invalid value")
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
