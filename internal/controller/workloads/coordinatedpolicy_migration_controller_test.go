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

package workloads

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func init() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
}

// buildMigrationScheme returns a runtime.Scheme with all required types registered.
func buildMigrationScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(s))
	return s
}

// coordAnnotation serializes coords to the migration annotation JSON string.
func coordAnnotation(t *testing.T, coords []workloadsv1alpha1.Coordination) string {
	t.Helper()
	b, err := json.Marshal(coords)
	require.NoError(t, err)
	return string(b)
}

// newRBGWithCoordAnnotation builds a v1alpha2 RoleBasedGroup carrying the migration annotation.
func newRBGWithCoordAnnotation(name, ns string, coords []workloadsv1alpha1.Coordination, t *testing.T) *workloadsv1alpha2.RoleBasedGroup {
	t.Helper()
	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Annotations: map[string]string{
				annotationCoordMigration: coordAnnotation(t, coords),
			},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{},
	}
}

// ── EnsureV1alpha1CoordinatedPolicy: create path ──────────────────────────────

func TestEnsureV1alpha1CoordinatedPolicy_CreatesPolicy(t *testing.T) {
	ns := "default"
	coords := []workloadsv1alpha1.Coordination{
		{Name: "c1", Roles: []string{"prefill", "decode"}},
	}
	rbg := newRBGWithCoordAnnotation("my-rbg", ns, coords, t)

	fc := fake.NewClientBuilder().WithScheme(buildMigrationScheme(t)).WithObjects(rbg).Build()

	require.NoError(t, EnsureV1alpha1CoordinatedPolicy(context.Background(), fc, rbg))

	policy := &workloadsv1alpha2.CoordinatedPolicy{}
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{Name: "my-rbg", Namespace: ns}, policy))
	require.Len(t, policy.Spec.Policies, 1)
	assert.Equal(t, []string{"prefill", "decode"}, policy.Spec.Policies[0].Roles)

	// OwnerReference must point to the RBG.
	require.Len(t, policy.OwnerReferences, 1)
	assert.Equal(t, "RoleBasedGroup", policy.OwnerReferences[0].Kind)
	assert.Equal(t, "my-rbg", policy.OwnerReferences[0].Name)

	// Annotation must still be present — it is never removed.
	updatedRBG := &workloadsv1alpha2.RoleBasedGroup{}
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{Name: "my-rbg", Namespace: ns}, updatedRBG))
	assert.Contains(t, updatedRBG.Annotations, annotationCoordMigration)
}

// ── EnsureV1alpha1CoordinatedPolicy: RollingUpdate strategy conversion ────────

func TestEnsureV1alpha1CoordinatedPolicy_RollingUpdateStrategy(t *testing.T) {
	ns := "default"
	maxSkew := "10%"
	partition := "20%"
	maxUnavail := "5%"
	coords := []workloadsv1alpha1.Coordination{
		{
			Name:  "ru-coord",
			Roles: []string{"prefill", "decode"},
			Strategy: &workloadsv1alpha1.CoordinationStrategy{
				RollingUpdate: &workloadsv1alpha1.CoordinationRollingUpdate{
					MaxSkew:        &maxSkew,
					Partition:      &partition,
					MaxUnavailable: &maxUnavail,
				},
			},
		},
	}
	rbg := newRBGWithCoordAnnotation("rbg-ru", ns, coords, t)

	fc := fake.NewClientBuilder().WithScheme(buildMigrationScheme(t)).WithObjects(rbg).Build()
	require.NoError(t, EnsureV1alpha1CoordinatedPolicy(context.Background(), fc, rbg))

	policy := &workloadsv1alpha2.CoordinatedPolicy{}
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{Name: "rbg-ru", Namespace: ns}, policy))
	require.Len(t, policy.Spec.Policies, 1)

	strat := policy.Spec.Policies[0].Strategy
	require.NotNil(t, strat.RollingUpdate)
	assert.Equal(t, intstr.FromString("10%"), *strat.RollingUpdate.MaxSkew)
	assert.Equal(t, intstr.FromString("20%"), *strat.RollingUpdate.Partition)
	assert.Equal(t, intstr.FromString("5%"), *strat.RollingUpdate.MaxUnavailable)
	assert.Nil(t, strat.Scaling)
}

// ── EnsureV1alpha1CoordinatedPolicy: Scaling strategy conversion ──────────────

func TestEnsureV1alpha1CoordinatedPolicy_ScalingStrategy_OrderScheduled(t *testing.T) {
	ns := "default"
	maxSkew := "5%"
	prog := workloadsv1alpha1.OrderScheduled
	coords := []workloadsv1alpha1.Coordination{
		{
			Name:  "sc-coord",
			Roles: []string{"worker"},
			Strategy: &workloadsv1alpha1.CoordinationStrategy{
				Scaling: &workloadsv1alpha1.CoordinationScaling{
					MaxSkew:     &maxSkew,
					Progression: &prog,
				},
			},
		},
	}
	rbg := newRBGWithCoordAnnotation("rbg-sc", ns, coords, t)

	fc := fake.NewClientBuilder().WithScheme(buildMigrationScheme(t)).WithObjects(rbg).Build()
	require.NoError(t, EnsureV1alpha1CoordinatedPolicy(context.Background(), fc, rbg))

	policy := &workloadsv1alpha2.CoordinatedPolicy{}
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{Name: "rbg-sc", Namespace: ns}, policy))

	strat := policy.Spec.Policies[0].Strategy
	require.NotNil(t, strat.Scaling)
	assert.Equal(t, intstr.FromString("5%"), *strat.Scaling.MaxSkew)
	assert.Equal(t, workloadsv1alpha2.OrderScheduledProgression, strat.Scaling.Progression)
	assert.Nil(t, strat.RollingUpdate)
}

func TestEnsureV1alpha1CoordinatedPolicy_ScalingStrategy_OrderReady(t *testing.T) {
	ns := "default"
	prog := workloadsv1alpha1.OrderReady
	coords := []workloadsv1alpha1.Coordination{
		{
			Name:  "sc-ready",
			Roles: []string{"worker"},
			Strategy: &workloadsv1alpha1.CoordinationStrategy{
				Scaling: &workloadsv1alpha1.CoordinationScaling{Progression: &prog},
			},
		},
	}
	rbg := newRBGWithCoordAnnotation("rbg-ready", ns, coords, t)

	fc := fake.NewClientBuilder().WithScheme(buildMigrationScheme(t)).WithObjects(rbg).Build()
	require.NoError(t, EnsureV1alpha1CoordinatedPolicy(context.Background(), fc, rbg))

	policy := &workloadsv1alpha2.CoordinatedPolicy{}
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{Name: "rbg-ready", Namespace: ns}, policy))
	// OrderReady has no equivalent in v1alpha2; progression is dropped (empty).
	assert.Equal(t, workloadsv1alpha2.ScalingProgression(""), policy.Spec.Policies[0].Strategy.Scaling.Progression)
}

// ── EnsureV1alpha1CoordinatedPolicy: multiple coordination rules ──────────────

func TestEnsureV1alpha1CoordinatedPolicy_MultipleRules(t *testing.T) {
	ns := "default"
	maxSkew := "10%"
	coords := []workloadsv1alpha1.Coordination{
		{Name: "ru", Roles: []string{"a", "b"}, Strategy: &workloadsv1alpha1.CoordinationStrategy{
			RollingUpdate: &workloadsv1alpha1.CoordinationRollingUpdate{MaxSkew: &maxSkew},
		}},
		{Name: "sc", Roles: []string{"c"}, Strategy: &workloadsv1alpha1.CoordinationStrategy{
			Scaling: &workloadsv1alpha1.CoordinationScaling{MaxSkew: ptr.To("5%")},
		}},
	}
	rbg := newRBGWithCoordAnnotation("rbg-multi", ns, coords, t)

	fc := fake.NewClientBuilder().WithScheme(buildMigrationScheme(t)).WithObjects(rbg).Build()
	require.NoError(t, EnsureV1alpha1CoordinatedPolicy(context.Background(), fc, rbg))

	policy := &workloadsv1alpha2.CoordinatedPolicy{}
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{Name: "rbg-multi", Namespace: ns}, policy))
	require.Len(t, policy.Spec.Policies, 2)
	assert.Equal(t, []string{"a", "b"}, policy.Spec.Policies[0].Roles)
	assert.Equal(t, []string{"c"}, policy.Spec.Policies[1].Roles)
}

// ── EnsureV1alpha1CoordinatedPolicy: idempotency (update path) ───────────────

func TestEnsureV1alpha1CoordinatedPolicy_UpdatesExistingPolicy(t *testing.T) {
	ns := "default"
	coords := []workloadsv1alpha1.Coordination{
		{Name: "c1", Roles: []string{"new-role"}},
	}
	rbg := newRBGWithCoordAnnotation("rbg-upd", ns, coords, t)

	// Pre-create a stale CoordinatedPolicy with different content.
	stale := &workloadsv1alpha2.CoordinatedPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg-upd", Namespace: ns},
		Spec: workloadsv1alpha2.CoordinatedPolicySpec{
			Policies: []workloadsv1alpha2.CoordinatedPolicyRule{
				{Roles: []string{"old-role"}, Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{}},
			},
		},
	}

	fc := fake.NewClientBuilder().WithScheme(buildMigrationScheme(t)).WithObjects(rbg, stale).Build()
	require.NoError(t, EnsureV1alpha1CoordinatedPolicy(context.Background(), fc, rbg))

	updated := &workloadsv1alpha2.CoordinatedPolicy{}
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{Name: "rbg-upd", Namespace: ns}, updated))
	require.Len(t, updated.Spec.Policies, 1)
	assert.Equal(t, []string{"new-role"}, updated.Spec.Policies[0].Roles, "stale policy must be overwritten")
}

// ── EnsureV1alpha1CoordinatedPolicy: no annotation → no-op ───────────────────

func TestEnsureV1alpha1CoordinatedPolicy_NoAnnotation_Noop(t *testing.T) {
	ns := "default"
	rbg := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "clean-rbg", Namespace: ns},
	}

	fc := fake.NewClientBuilder().WithScheme(buildMigrationScheme(t)).WithObjects(rbg).Build()
	require.NoError(t, EnsureV1alpha1CoordinatedPolicy(context.Background(), fc, rbg))

	// No CoordinatedPolicy should be created.
	policyList := &workloadsv1alpha2.CoordinatedPolicyList{}
	require.NoError(t, fc.List(context.Background(), policyList))
	assert.Empty(t, policyList.Items)
}

// ── EnsureV1alpha1CoordinatedPolicy: nil strategy → empty strategies ──────────

func TestEnsureV1alpha1CoordinatedPolicy_NilStrategy(t *testing.T) {
	ns := "default"
	coords := []workloadsv1alpha1.Coordination{
		{Name: "c-nil", Roles: []string{"role-a"}},
	}
	rbg := newRBGWithCoordAnnotation("rbg-nil", ns, coords, t)

	fc := fake.NewClientBuilder().WithScheme(buildMigrationScheme(t)).WithObjects(rbg).Build()
	require.NoError(t, EnsureV1alpha1CoordinatedPolicy(context.Background(), fc, rbg))

	policy := &workloadsv1alpha2.CoordinatedPolicy{}
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{Name: "rbg-nil", Namespace: ns}, policy))
	require.Len(t, policy.Spec.Policies, 1)
	assert.Nil(t, policy.Spec.Policies[0].Strategy.RollingUpdate)
	assert.Nil(t, policy.Spec.Policies[0].Strategy.Scaling)
}

// ── convertCoordinationStrategyApplyConfiguration unit tests ───────────────────

func TestConvertCoordinationStrategyApplyConfiguration_Nil(t *testing.T) {
	result := convertCoordinationStrategyApplyConfiguration(nil)
	assert.Nil(t, result)
}

func TestConvertCoordinationStrategyApplyConfiguration_RollingUpdate(t *testing.T) {
	maxSkew := "10%"
	partition := "50%"
	maxUnavail := "5%"
	src := &workloadsv1alpha1.CoordinationStrategy{
		RollingUpdate: &workloadsv1alpha1.CoordinationRollingUpdate{
			MaxSkew:        &maxSkew,
			Partition:      &partition,
			MaxUnavailable: &maxUnavail,
		},
	}

	got := convertCoordinationStrategyApplyConfiguration(src)
	require.NotNil(t, got)
	require.NotNil(t, got.RollingUpdate)
	assert.Equal(t, intstr.FromString("10%"), *got.RollingUpdate.MaxSkew)
	assert.Equal(t, intstr.FromString("50%"), *got.RollingUpdate.Partition)
	assert.Equal(t, intstr.FromString("5%"), *got.RollingUpdate.MaxUnavailable)
	assert.Nil(t, got.Scaling)
}

func TestConvertCoordinationStrategyApplyConfiguration_Scaling_OrderScheduled(t *testing.T) {
	maxSkew := "5%"
	prog := workloadsv1alpha1.OrderScheduled
	src := &workloadsv1alpha1.CoordinationStrategy{
		Scaling: &workloadsv1alpha1.CoordinationScaling{
			MaxSkew:     &maxSkew,
			Progression: &prog,
		},
	}

	got := convertCoordinationStrategyApplyConfiguration(src)
	require.NotNil(t, got)
	require.NotNil(t, got.Scaling)
	assert.Equal(t, intstr.FromString("5%"), *got.Scaling.MaxSkew)
	require.NotNil(t, got.Scaling.Progression)
	assert.Equal(t, workloadsv1alpha2.OrderScheduledProgression, *got.Scaling.Progression)
	assert.Nil(t, got.RollingUpdate)
}

func TestConvertCoordinationStrategyApplyConfiguration_Scaling_OrderReady(t *testing.T) {
	prog := workloadsv1alpha1.OrderReady
	src := &workloadsv1alpha1.CoordinationStrategy{
		Scaling: &workloadsv1alpha1.CoordinationScaling{Progression: &prog},
	}

	got := convertCoordinationStrategyApplyConfiguration(src)
	require.NotNil(t, got)
	require.NotNil(t, got.Scaling)
	// OrderReady has no equivalent in v1alpha2; progression is dropped (nil).
	assert.Nil(t, got.Scaling.Progression)
}
