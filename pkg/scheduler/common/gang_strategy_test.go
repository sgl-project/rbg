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

package common

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func gangRule(roles []string, minReplicas map[string]int32) workloadsv1alpha2.CoordinatedPolicyRule {
	return workloadsv1alpha2.CoordinatedPolicyRule{
		Roles: roles,
		Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
			Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
				Gang: &workloadsv1alpha2.GangSchedulingStrategy{MinReplicas: minReplicas},
			},
		},
	}
}

func TestMergeGangStrategies(t *testing.T) {
	tests := []struct {
		name             string
		rules            []workloadsv1alpha2.CoordinatedPolicyRule
		want             *GangStrategy
		wantIncompatible bool
	}{
		{
			name:  "no rules",
			rules: nil,
			want:  nil,
		},
		{
			name:  "rule without a scheduling strategy",
			rules: []workloadsv1alpha2.CoordinatedPolicyRule{{Roles: []string{"prefill"}}},
			want:  nil,
		},
		{
			name:  "gang without minReplicas covers only the roles the rule names",
			rules: []workloadsv1alpha2.CoordinatedPolicyRule{gangRule([]string{"prefill"}, nil)},
			want:  &GangStrategy{Roles: sets.New("prefill")},
		},
		{
			name: "per-role minimums are merged across rules",
			rules: []workloadsv1alpha2.CoordinatedPolicyRule{
				gangRule([]string{"prefill"}, map[string]int32{"prefill": 2}),
				gangRule([]string{"decode"}, map[string]int32{"decode": 3}),
			},
			want: &GangStrategy{
				Roles:       sets.New("prefill", "decode"),
				MinReplicas: map[string]int32{"prefill": 2, "decode": 3},
			},
		},
		{
			name: "largest minimum wins for a duplicated role",
			rules: []workloadsv1alpha2.CoordinatedPolicyRule{
				gangRule([]string{"prefill"}, map[string]int32{"prefill": 2}),
				gangRule([]string{"prefill"}, map[string]int32{"prefill": 5}),
				gangRule([]string{"prefill"}, map[string]int32{"prefill": 3}),
			},
			want: &GangStrategy{Roles: sets.New("prefill"), MinReplicas: map[string]int32{"prefill": 5}},
		},
		{
			name: "minimums for roles outside the declaring rule scope are dropped",
			rules: []workloadsv1alpha2.CoordinatedPolicyRule{
				gangRule([]string{"prefill"}, map[string]int32{"prefill": 2, "decode": 9}),
			},
			want: &GangStrategy{Roles: sets.New("prefill"), MinReplicas: map[string]int32{"prefill": 2}},
		},
		{
			// Widening to an all-or-nothing gang would be stricter than the policy asked
			// for, so the configuration is reported instead of reinterpreted.
			name: "all minimums out of scope is an incompatible configuration",
			rules: []workloadsv1alpha2.CoordinatedPolicyRule{
				gangRule([]string{"prefill"}, map[string]int32{"decode": 9}),
			},
			wantIncompatible: true,
		},
		{
			// The all-or-nothing rule only subsumes minimums for its own roles:
			// prefill's minimum survives, decode participates in full.
			name: "all-or-nothing gang keeps minimums for roles it does not cover",
			rules: []workloadsv1alpha2.CoordinatedPolicyRule{
				gangRule([]string{"prefill"}, map[string]int32{"prefill": 2}),
				gangRule([]string{"decode"}, nil),
			},
			want: &GangStrategy{
				Roles:       sets.New("prefill", "decode"),
				MinReplicas: map[string]int32{"prefill": 2},
			},
		},
		{
			// One rule declares an all-or-nothing gang over prefill, another a
			// per-role minimum over decode. Both constraints must hold: prefill
			// participates in full, decode is held to its minimum.
			name: "all-or-nothing role and per-role minimum coexist",
			rules: []workloadsv1alpha2.CoordinatedPolicyRule{
				gangRule([]string{"prefill"}, nil),
				gangRule([]string{"decode"}, map[string]int32{"decode": 2}),
			},
			want: &GangStrategy{
				Roles:       sets.New("prefill", "decode"),
				MinReplicas: map[string]int32{"decode": 2},
			},
		},
		{
			// An all-or-nothing rule over a role subsumes that role's own minimum:
			// holding it to a lower count would weaken the all-or-nothing rule.
			name: "all-or-nothing gang subsumes the same role's minimum",
			rules: []workloadsv1alpha2.CoordinatedPolicyRule{
				gangRule([]string{"prefill"}, map[string]int32{"prefill": 2}),
				gangRule([]string{"prefill"}, nil),
			},
			want: &GangStrategy{Roles: sets.New("prefill")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := &workloadsv1alpha2.CoordinatedPolicy{
				Spec: workloadsv1alpha2.CoordinatedPolicySpec{Policies: tt.rules},
			}
			got, err := MergeGangStrategies(policy)
			if tt.wantIncompatible {
				assert.True(t, IsIncompatibleGangConfig(err))
				assert.Nil(t, got)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRoleInGang(t *testing.T) {
	role := &workloadsv1alpha2.RoleSpec{Name: "prefill"}

	assert.False(t, RoleInGang(role, nil))
	assert.False(t, RoleInGang(nil, &GangStrategy{}))
	// An empty role set is the legacy annotation's gang: it names no roles, so it covers all.
	assert.True(t, RoleInGang(role, &GangStrategy{}))
	assert.True(t, RoleInGang(role, &GangStrategy{Roles: sets.New("prefill", "decode")}))
	assert.False(t, RoleInGang(role, &GangStrategy{Roles: sets.New("decode")}))
}

func TestGangSize(t *testing.T) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{Name: "prefill", Replicas: ptr.To[int32](2)},
				{Name: "decode", Replicas: ptr.To[int32](3)},
				{Name: "router", Replicas: ptr.To[int32](1)},
			},
		},
	}

	t.Run("empty role set covers the whole group", func(t *testing.T) {
		size, err := GangSize(rbg, &GangStrategy{})
		require.NoError(t, err)
		assert.Equal(t, int32(6), size)
	})

	// The point of scoping: a role the policy leaves out must not be demanded by
	// minMember, or the gang waits for pods it never enrolls.
	t.Run("only covered roles are counted", func(t *testing.T) {
		size, err := GangSize(rbg, &GangStrategy{Roles: sets.New("prefill", "decode")})
		require.NoError(t, err)
		assert.Equal(t, int32(5), size)
	})

	t.Run("unknown role is an incompatible configuration", func(t *testing.T) {
		_, err := GangSize(rbg, &GangStrategy{Roles: sets.New("prefill", "typo")})
		assert.True(t, IsIncompatibleGangConfig(err))
		assert.ErrorContains(t, err, "typo")
	})

	t.Run("covered roles all scaled to zero is an incompatible configuration", func(t *testing.T) {
		scaledDown := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
			Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{
					{Name: "prefill", Replicas: ptr.To[int32](0)},
					{Name: "decode", Replicas: ptr.To[int32](3)},
				},
			},
		}
		_, err := GangSize(scaledDown, &GangStrategy{Roles: sets.New("prefill")})
		assert.True(t, IsIncompatibleGangConfig(err))
	})
}

func TestGangMinimumReplicas(t *testing.T) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{Name: "prefill", Replicas: ptr.To[int32](4)},
				{Name: "decode", Replicas: ptr.To[int32](2)},
			},
		},
	}

	assert.Nil(t, GangMinimumReplicas(rbg, nil))

	// An all-or-nothing gang needs every replica of the roles it covers.
	assert.Equal(
		t, map[string]int32{"prefill": 4},
		GangMinimumReplicas(rbg, &GangStrategy{Roles: sets.New("prefill")}),
	)
	assert.Equal(
		t, map[string]int32{"prefill": 4, "decode": 2},
		GangMinimumReplicas(rbg, &GangStrategy{}),
	)
	// A per-role gang needs exactly its minimums, not the full replica count.
	assert.Equal(
		t, map[string]int32{"prefill": 2},
		GangMinimumReplicas(rbg, &GangStrategy{
			Roles:       sets.New("prefill"),
			MinReplicas: map[string]int32{"prefill": 2},
		}),
	)

	// A covered role without a configured minimum participates in full, while
	// configured minimums still apply to the roles they name.
	mixed := &workloadsv1alpha2.RoleBasedGroup{
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{
				{Name: "prefill", Replicas: ptr.To[int32](4)},
				{Name: "decode", Replicas: ptr.To[int32](6)},
			},
		},
	}
	assert.Equal(
		t, map[string]int32{"prefill": 4, "decode": 2},
		GangMinimumReplicas(mixed, &GangStrategy{
			Roles:       sets.New("prefill", "decode"),
			MinReplicas: map[string]int32{"decode": 2},
		}),
	)
}

func TestUnknownGangRoles(t *testing.T) {
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{{Name: "prefill"}, {Name: "decode"}},
		},
	}

	assert.Nil(t, UnknownGangRoles(rbg, nil))
	assert.Nil(t, UnknownGangRoles(rbg, &GangStrategy{}))
	assert.Empty(t, UnknownGangRoles(rbg, &GangStrategy{Roles: sets.New("prefill")}))
	assert.Equal(
		t, []string{"proxy", "typo"},
		UnknownGangRoles(rbg, &GangStrategy{Roles: sets.New("prefill", "typo", "proxy")}),
	)
}

func TestGetGangStrategy(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))

	rbg := func(annotations map[string]string) *workloadsv1alpha2.RoleBasedGroup {
		return &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default", Annotations: annotations},
		}
	}

	t.Run("no policy and no annotation disables gang", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		strategy, err := GetGangStrategy(context.Background(), c, rbg(nil))
		require.NoError(t, err)
		assert.Nil(t, strategy)
	})

	t.Run("legacy annotation enables whole-group gang", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		strategy, err := GetGangStrategy(
			context.Background(), c, rbg(map[string]string{constants.GangSchedulingAnnotationKey: "true"}),
		)
		require.NoError(t, err)
		assert.Equal(t, &GangStrategy{}, strategy)
	})

	t.Run("CoordinatedPolicy takes precedence over the annotation", func(t *testing.T) {
		policy := &workloadsv1alpha2.CoordinatedPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
			Spec: workloadsv1alpha2.CoordinatedPolicySpec{
				Policies: []workloadsv1alpha2.CoordinatedPolicyRule{
					gangRule([]string{"prefill"}, map[string]int32{"prefill": 2}),
				},
			},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()
		strategy, err := GetGangStrategy(
			context.Background(), c, rbg(map[string]string{constants.GangSchedulingAnnotationKey: "true"}),
		)
		require.NoError(t, err)
		assert.Equal(
			t, &GangStrategy{Roles: sets.New("prefill"), MinReplicas: map[string]int32{"prefill": 2}}, strategy,
		)
	})

	t.Run("policy without a gang rule falls back to the annotation", func(t *testing.T) {
		policy := &workloadsv1alpha2.CoordinatedPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
			Spec: workloadsv1alpha2.CoordinatedPolicySpec{
				Policies: []workloadsv1alpha2.CoordinatedPolicyRule{{Roles: []string{"prefill"}}},
			},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).Build()
		strategy, err := GetGangStrategy(
			context.Background(), c, rbg(map[string]string{constants.GangSchedulingAnnotationKey: "true"}),
		)
		require.NoError(t, err)
		assert.Equal(t, &GangStrategy{}, strategy)
	})

	// A read failure must not be mistaken for "no policy": that would silently
	// downgrade gang scheduling to no gang scheduling at all.
	t.Run("read error is propagated", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(
			interceptor.Funcs{
				Get: func(
					_ context.Context, _ client.WithWatch, _ client.ObjectKey, obj client.Object, _ ...client.GetOption,
				) error {
					if _, ok := obj.(*workloadsv1alpha2.CoordinatedPolicy); ok {
						return apierrors.NewInternalError(errors.New("etcd unavailable"))
					}
					return nil
				},
			},
		).Build()
		strategy, err := GetGangStrategy(
			context.Background(), c, rbg(map[string]string{constants.GangSchedulingAnnotationKey: "true"}),
		)
		assert.ErrorContains(t, err, "get CoordinatedPolicy default/rbg")
		assert.Nil(t, strategy)
	})

	t.Run("policy CRD not registered is treated as absence", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(
			interceptor.Funcs{
				Get: func(
					_ context.Context, _ client.WithWatch, key client.ObjectKey, obj client.Object,
					_ ...client.GetOption,
				) error {
					if _, ok := obj.(*workloadsv1alpha2.CoordinatedPolicy); ok {
						return apierrors.NewNotFound(
							schema.GroupResource{Group: "workloads.x-k8s.io", Resource: "coordinatedpolicies"},
							key.Name,
						)
					}
					return nil
				},
			},
		).Build()
		strategy, err := GetGangStrategy(context.Background(), c, rbg(nil))
		require.NoError(t, err)
		assert.Nil(t, strategy)
	})
}

func TestResolveGangStrategy(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))

	newRBG := func(name string) *workloadsv1alpha2.RoleBasedGroup {
		return &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		}
	}
	policy := func(name string, minReplicas map[string]int32) *workloadsv1alpha2.CoordinatedPolicy {
		return &workloadsv1alpha2.CoordinatedPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: workloadsv1alpha2.CoordinatedPolicySpec{
				Policies: []workloadsv1alpha2.CoordinatedPolicyRule{
					gangRule([]string{"prefill"}, minReplicas),
				},
			},
		}
	}

	// Every consumer in one reconcile must see the same strategy even if the
	// CoordinatedPolicy changes underneath, otherwise the PodGroup and the pod
	// templates can disagree about the gang.
	t.Run("resolved strategy is reused for the same RBG", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(policy("rbg", map[string]int32{"prefill": 2})).Build()

		ctx, strategy, err := ResolveGangStrategy(context.Background(), c, newRBG("rbg"))
		require.NoError(t, err)
		assert.Equal(
			t, &GangStrategy{Roles: sets.New("prefill"), MinReplicas: map[string]int32{"prefill": 2}}, strategy,
		)

		stored := &workloadsv1alpha2.CoordinatedPolicy{}
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "rbg", Namespace: "default"}, stored))
		stored.Spec.Policies[0].Strategy.Scheduling.Gang.MinReplicas = map[string]int32{"prefill": 5}
		require.NoError(t, c.Update(ctx, stored))

		cached, err := GetGangStrategy(ctx, c, newRBG("rbg"))
		require.NoError(t, err)
		assert.Equal(
			t, &GangStrategy{Roles: sets.New("prefill"), MinReplicas: map[string]int32{"prefill": 2}}, cached,
		)
	})

	t.Run("cached strategy is not reused for another RBG", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(policy("rbg", map[string]int32{"prefill": 2})).Build()

		ctx, _, err := ResolveGangStrategy(context.Background(), c, newRBG("rbg"))
		require.NoError(t, err)

		other, err := GetGangStrategy(ctx, c, newRBG("other"))
		require.NoError(t, err)
		assert.Nil(t, other)
	})

	t.Run("read error is propagated", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(
			interceptor.Funcs{
				Get: func(
					_ context.Context, _ client.WithWatch, _ client.ObjectKey, obj client.Object, _ ...client.GetOption,
				) error {
					if _, ok := obj.(*workloadsv1alpha2.CoordinatedPolicy); ok {
						return apierrors.NewInternalError(errors.New("etcd unavailable"))
					}
					return nil
				},
			},
		).Build()

		ctx, strategy, err := ResolveGangStrategy(context.Background(), c, newRBG("rbg"))
		assert.ErrorContains(t, err, "get CoordinatedPolicy default/rbg")
		assert.Nil(t, strategy)
		assert.Nil(t, ctx.Value(gangStrategyContextKey{}))
	})
}
