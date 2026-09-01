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

package volcano

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scheduler/common"
	volcanoschedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

func standaloneRole(name string, replicas int32, workloadType string) workloadsv1alpha2.RoleSpec {
	role := workloadsv1alpha2.RoleSpec{
		Name:     name,
		Replicas: ptr.To(replicas),
		Pattern: workloadsv1alpha2.Pattern{
			StandalonePattern: &workloadsv1alpha2.StandalonePattern{},
		},
	}
	if workloadType != "" {
		role.Annotations = map[string]string{constants.RoleWorkloadTypeAnnotationKey: workloadType}
	}
	return role
}

func rbgWithRoles(roles ...workloadsv1alpha2.RoleSpec) *workloadsv1alpha2.RoleBasedGroup {
	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
		Spec:       workloadsv1alpha2.RoleBasedGroupSpec{Roles: roles},
	}
}

func TestBuildGangSpec(t *testing.T) {
	tests := []struct {
		name             string
		rbg              *workloadsv1alpha2.RoleBasedGroup
		minReplicas      map[string]int32
		extraRoles       []string
		wantMinMember    int32
		wantSubGroups    []string
		wantMinSubGroups map[string]int32
		wantErrContain   string
	}{
		{
			name:          "per-role minimums over standalone roles",
			rbg:           rbgWithRoles(standaloneRole("prefill", 4, ""), standaloneRole("decode", 6, "")),
			minReplicas:   map[string]int32{"prefill": 2, "decode": 3},
			wantMinMember: 5,
			wantSubGroups: []string{"prefill", "decode"},
		},
		{
			name: "subGroupSize follows the leaderWorker size",
			rbg: rbgWithRoles(workloadsv1alpha2.RoleSpec{
				Name:     "worker",
				Replicas: ptr.To(int32(3)),
				Pattern: workloadsv1alpha2.Pattern{
					LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{Size: ptr.To(int32(4))},
				},
			}),
			minReplicas:   map[string]int32{"worker": 2},
			wantMinMember: 8,
			wantSubGroups: []string{"worker"},
		},
		{
			name:          "roles absent from the strategy do not contribute",
			rbg:           rbgWithRoles(standaloneRole("prefill", 4, ""), standaloneRole("decode", 6, "")),
			minReplicas:   map[string]int32{"prefill": 1},
			wantMinMember: 1,
			wantSubGroups: []string{"prefill"},
		},
		{
			// A role covered by an all-or-nothing rule has no minimum of its own and
			// participates in full: its subGroup needs every replica.
			name:             "all-or-nothing roles contribute their full replicas",
			rbg:              rbgWithRoles(standaloneRole("prefill", 4, ""), standaloneRole("decode", 6, "")),
			minReplicas:      map[string]int32{"decode": 2},
			extraRoles:       []string{"prefill"},
			wantMinMember:    6,
			wantSubGroups:    []string{"prefill", "decode"},
			wantMinSubGroups: map[string]int32{"prefill": 4, "decode": 2},
		},
		{
			name:           "minReplicas below one",
			rbg:            rbgWithRoles(standaloneRole("prefill", 4, "")),
			minReplicas:    map[string]int32{"prefill": 0},
			wantErrContain: "must be at least 1",
		},
		{
			name:           "minReplicas above replicas is unsatisfiable",
			rbg:            rbgWithRoles(standaloneRole("prefill", 2, "")),
			minReplicas:    map[string]int32{"prefill": 3},
			wantErrContain: "can never be satisfied",
		},
		{
			name:           "workload backing does not label pods per instance",
			rbg:            rbgWithRoles(standaloneRole("prefill", 4, constants.StatefulSetWorkloadType)),
			minReplicas:    map[string]int32{"prefill": 2},
			wantErrContain: "pod label is needed to partition",
		},
		{
			// The label requirement also applies to all-or-nothing roles in a gang
			// that uses subGroupPolicy: their full-replica subGroups need it too.
			name: "all-or-nothing role without the instance label is unsupported",
			rbg: rbgWithRoles(
				standaloneRole("prefill", 4, constants.StatefulSetWorkloadType),
				standaloneRole("decode", 6, ""),
			),
			minReplicas:    map[string]int32{"decode": 2},
			extraRoles:     []string{"prefill"},
			wantErrContain: "pod label is needed to partition",
		},
		{
			name:           "minReplicas names an unknown role",
			rbg:            rbgWithRoles(standaloneRole("prefill", 4, "")),
			minReplicas:    map[string]int32{"prefill": 1, "ghost": 1, "phantom": 1},
			wantErrContain: "[ghost phantom]",
		},
		{
			name:           "gang covering only zero-replica roles",
			rbg:            rbgWithRoles(standaloneRole("prefill", 0, "")),
			minReplicas:    map[string]int32{},
			wantErrContain: "all scaled to zero",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roles := sets.KeySet(tt.minReplicas)
			roles.Insert(tt.extraRoles...)
			minMember, policies, err := buildGangSpec(tt.rbg, &common.GangStrategy{
				Roles:       roles,
				MinReplicas: tt.minReplicas,
			})
			if tt.wantErrContain != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContain)
				// The reconciler requeues on a fixed interval instead of the error
				// backoff only for this classification.
				assert.True(t, common.IsIncompatibleGangConfig(err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantMinMember, minMember)

			names := make([]string, 0, len(policies))
			for _, p := range policies {
				names = append(names, p.Name)
				assert.Equal(t, []string{constants.RoleInstanceNameLabelKey}, p.MatchLabelKeys)
				require.NotNil(t, p.LabelSelector)
				assert.Equal(t, "rbg", p.LabelSelector.MatchLabels[constants.GroupNameLabelKey])
				assert.Equal(t, p.Name, p.LabelSelector.MatchLabels[constants.RoleNameLabelKey])
				if tt.wantMinSubGroups != nil {
					require.NotNil(t, p.MinSubGroups)
					assert.Equal(t, tt.wantMinSubGroups[p.Name], ptr.Deref(p.MinSubGroups, 0))
				}
			}
			assert.Equal(t, tt.wantSubGroups, names)
		})
	}
}

func ptsAnnotations(pts *coreapplyv1.PodTemplateSpecApplyConfiguration) map[string]string {
	if pts.ObjectMetaApplyConfiguration == nil {
		return nil
	}
	return pts.Annotations
}

func TestInjectPodSchedulingFields(t *testing.T) {
	rbg := rbgWithRoles(standaloneRole("prefill", 2, ""), standaloneRole("decode", 2, ""))
	prefill := &rbg.Spec.Roles[0]
	decode := &rbg.Spec.Roles[1]
	m := New(nil)

	t.Run("gang disabled injects nothing", func(t *testing.T) {
		pts := &coreapplyv1.PodTemplateSpecApplyConfiguration{}
		m.InjectPodSchedulingFields(rbg, prefill, nil, pts)
		assert.Nil(t, pts.Spec)
		assert.Empty(t, ptsAnnotations(pts))
	})

	t.Run("gang covering every role enrolls every role", func(t *testing.T) {
		pts := &coreapplyv1.PodTemplateSpecApplyConfiguration{}
		m.InjectPodSchedulingFields(rbg, prefill, &common.GangStrategy{}, pts)
		require.NotNil(t, pts.Spec)
		assert.Equal(t, SchedulerName, ptr.Deref(pts.Spec.SchedulerName, ""))
		assert.Equal(t, "rbg", ptsAnnotations(pts)[AnnotationKey])
	})

	t.Run("role outside a per-role gang gets schedulerName but not the PodGroup annotation", func(t *testing.T) {
		strategy := &common.GangStrategy{
			Roles:       sets.New("prefill"),
			MinReplicas: map[string]int32{"prefill": 1},
		}
		pts := &coreapplyv1.PodTemplateSpecApplyConfiguration{}
		m.InjectPodSchedulingFields(rbg, decode, strategy, pts)
		require.NotNil(t, pts.Spec)
		assert.Equal(t, SchedulerName, ptr.Deref(pts.Spec.SchedulerName, ""))
		assert.NotContains(t, ptsAnnotations(pts), AnnotationKey)
	})

	// An all-or-nothing rule still scopes itself with spec.policies[].roles, so a role
	// the rule leaves out must not be enrolled either.
	t.Run("role outside an all-or-nothing gang is not enrolled", func(t *testing.T) {
		strategy := &common.GangStrategy{Roles: sets.New("prefill")}
		pts := &coreapplyv1.PodTemplateSpecApplyConfiguration{}
		m.InjectPodSchedulingFields(rbg, decode, strategy, pts)
		require.NotNil(t, pts.Spec)
		assert.Equal(t, SchedulerName, ptr.Deref(pts.Spec.SchedulerName, ""))
		assert.NotContains(t, ptsAnnotations(pts), AnnotationKey)
	})
}

func podGroupCRD(withSubGroupPolicy bool) *apiextensionsv1.CustomResourceDefinition {
	specProps := apiextensionsv1.JSONSchemaProps{
		Type:       "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{"minMember": {Type: "integer"}},
	}
	if withSubGroupPolicy {
		specProps.Properties["subGroupPolicy"] = apiextensionsv1.JSONSchemaProps{Type: "array"}
	}
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: CrdName},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1beta1", Served: true, Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type:       "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": specProps},
					},
				}},
			},
		},
	}
}

// TestCreateOrUpdateSubGroupPolicyDrift pins that a redistributed minReplicas map is
// pushed to the PodGroup. Moving a minimum between roles can leave minMember identical,
// so a comparison that only looked at minMember would keep the old per-role split.
func TestCreateOrUpdateSubGroupPolicyDrift(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(scheme))
	require.NoError(t, volcanoschedulingv1beta1.AddToScheme(scheme))

	rbg := rbgWithRoles(standaloneRole("prefill", 4, ""), standaloneRole("decode", 6, ""))
	before := &common.GangStrategy{
		Roles:       sets.New("prefill", "decode"),
		MinReplicas: map[string]int32{"prefill": 1, "decode": 2},
	}
	after := &common.GangStrategy{
		Roles:       sets.New("prefill", "decode"),
		MinReplicas: map[string]int32{"prefill": 2, "decode": 1},
	}

	minMemberBefore, policiesBefore, err := buildGangSpec(rbg, before)
	require.NoError(t, err)
	minMemberAfter, _, err := buildGangSpec(rbg, after)
	require.NoError(t, err)
	require.Equal(t, minMemberBefore, minMemberAfter, "the two strategies must share a minMember for this test to be meaningful")

	existing := &volcanoschedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Name: rbg.Name, Namespace: rbg.Namespace},
		Spec: volcanoschedulingv1beta1.PodGroupSpec{
			MinMember:      minMemberBefore,
			SubGroupPolicy: policiesBefore,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(podGroupCRD(true), existing).Build()

	require.NoError(t, New(c).createOrUpdate(context.Background(), rbg, after, c))

	updated := &volcanoschedulingv1beta1.PodGroup{}
	require.NoError(t, c.Get(context.Background(),
		client.ObjectKey{Name: rbg.Name, Namespace: rbg.Namespace}, updated))
	require.Len(t, updated.Spec.SubGroupPolicy, 2)
	got := map[string]int32{}
	for _, p := range updated.Spec.SubGroupPolicy {
		got[p.Name] = ptr.Deref(p.MinSubGroups, 0)
	}
	assert.Equal(t, map[string]int32{"prefill": 2, "decode": 1}, got)
}

// TestCheckPodGroupCRDHasSubGroup pins that a failed read is reported as an error
// rather than as "this Volcano is too old", which would send the user chasing an
// upgrade that changes nothing.
func TestCheckPodGroupCRDHasSubGroup(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(scheme))

	t.Run("subGroupPolicy present", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(podGroupCRD(true)).Build()
		supported, err := checkPodGroupCRDHasSubGroup(context.Background(), c)
		require.NoError(t, err)
		assert.True(t, supported)
	})

	t.Run("subGroupPolicy absent", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(podGroupCRD(false)).Build()
		supported, err := checkPodGroupCRDHasSubGroup(context.Background(), c)
		require.NoError(t, err)
		assert.False(t, supported)
	})

	t.Run("subGroupPolicy only on another version is not support", func(t *testing.T) {
		crd := podGroupCRD(true)
		crd.Spec.Versions[0].Name = "v1beta2"
		crd.Spec.Versions[0].Served = false
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd).Build()
		supported, err := checkPodGroupCRDHasSubGroup(context.Background(), c)
		require.NoError(t, err)
		assert.False(t, supported)
	})

	t.Run("read failure surfaces as an error", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return errors.New("boom")
			},
		}).Build()
		supported, err := checkPodGroupCRDHasSubGroup(context.Background(), c)
		require.Error(t, err)
		assert.Contains(t, err.Error(), CrdName)
		assert.False(t, supported)
	})
}

// TestSupportsSubGroupPolicyCaching pins the TTL cache around the CRD probe: the probe
// is an uncached apiserver read, so it must not run on every reconcile, yet it must
// still notice a Volcano upgrade once the entry expires.
func TestSupportsSubGroupPolicyCaching(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(scheme))

	countingReader := func(crd *apiextensionsv1.CustomResourceDefinition, failWith error) (client.Reader, *int) {
		var calls int
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
					obj client.Object, opts ...client.GetOption) error {
					calls++
					if failWith != nil {
						return failWith
					}
					return cl.Get(ctx, key, obj, opts...)
				},
			}).Build()
		return c, &calls
	}

	t.Run("repeated calls within the TTL probe once", func(t *testing.T) {
		reader, calls := countingReader(podGroupCRD(true), nil)
		m := New(nil)

		for range 3 {
			supported, err := m.supportsSubGroupPolicy(context.Background(), reader)
			require.NoError(t, err)
			assert.True(t, supported)
		}
		assert.Equal(t, 1, *calls)
	})

	t.Run("a failed probe is not cached", func(t *testing.T) {
		reader, calls := countingReader(podGroupCRD(true), errors.New("boom"))
		m := New(nil)

		_, err := m.supportsSubGroupPolicy(context.Background(), reader)
		require.Error(t, err)
		assert.False(t, common.IsIncompatibleGangConfig(err))
		_, err = m.supportsSubGroupPolicy(context.Background(), reader)
		require.Error(t, err)
		assert.Equal(t, 2, *calls)
	})

	t.Run("an expired entry is re-probed", func(t *testing.T) {
		reader, calls := countingReader(podGroupCRD(true), nil)
		m := New(nil)

		supported, err := m.supportsSubGroupPolicy(context.Background(), reader)
		require.NoError(t, err)
		assert.True(t, supported)

		m.subGroupProbedAt = time.Now().Add(-subGroupProbeTTL - time.Second)

		supported, err = m.supportsSubGroupPolicy(context.Background(), reader)
		require.NoError(t, err)
		assert.True(t, supported)
		assert.Equal(t, 2, *calls)
	})
}
