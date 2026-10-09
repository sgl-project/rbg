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
	"strings"
	"sync"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scheduler/common"
	"sigs.k8s.io/rbgs/pkg/utils"
	volcanoschedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

	"github.com/stretchr/testify/require"
)

func TestNetworkTopologyForGroupRequiredAndPreferred(t *testing.T) {
	group := &common.PlacementGroup{
		Topology: &workloadsv1alpha2.TopologyConstraint{
			Pack: &workloadsv1alpha2.TopologyPackConstraint{
				Required:  ptr.To("block"),
				Preferred: ptr.To("rack"),
			},
		},
	}
	got, absorbed := networkTopologyForGroup(group)
	if got == nil || got.Mode != volcanoschedulingv1beta1.HardNetworkTopologyMode || got.HighestTierName != "block" {
		t.Fatalf("unexpected topology: %#v", got)
	}
	if !absorbed {
		t.Fatal("expected preferred level to be reported as absorbed")
	}
}

func TestNetworkTopologyForGroupPreferredOnly(t *testing.T) {
	group := &common.PlacementGroup{
		Topology: &workloadsv1alpha2.TopologyConstraint{
			Pack: &workloadsv1alpha2.TopologyPackConstraint{Preferred: ptr.To("rack")},
		},
	}
	got, absorbed := networkTopologyForGroup(group)
	if got == nil || got.Mode != volcanoschedulingv1beta1.SoftNetworkTopologyMode {
		t.Fatalf("unexpected topology: %#v", got)
	}
	if !absorbed {
		t.Fatal("expected preferred level to be reported as absorbed")
	}
}

func TestBuildPlacementPodGroupSeparatesRootAndInstanceTopology(t *testing.T) {
	standalone := standaloneRole("prefill", 2, constants.RoleInstanceSetWorkloadType)
	leaderWorker := standaloneRole("prefill", 2, constants.RoleInstanceSetWorkloadType)
	leaderWorker.Pattern = workloadsv1alpha2.Pattern{
		LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{Size: ptr.To(int32(4))},
	}

	tests := []struct {
		name             string
		role             workloadsv1alpha2.RoleSpec
		group            *common.PlacementGroup
		wantRoot         *volcanoschedulingv1beta1.NetworkTopologySpec
		wantSubGroupSize int32
		wantSubTopology  string
	}{
		{
			name: "standalone instance topology stays on the subgroup",
			role: standalone,
			group: &common.PlacementGroup{
				Scope: common.PlacementScope{
					Roles:       []string{"prefill"},
					PartitionBy: common.PartitionByRoleInstance,
				},
				Topology: &workloadsv1alpha2.TopologyConstraint{
					Pack: &workloadsv1alpha2.TopologyPackConstraint{Required: ptr.To("rack")},
				},
			},
			wantSubGroupSize: 1,
			wantSubTopology:  "rack",
		},
		{
			name: "leader-worker instance topology stays on the subgroup",
			role: leaderWorker,
			group: &common.PlacementGroup{
				Scope: common.PlacementScope{
					Roles:       []string{"prefill"},
					PartitionBy: common.PartitionByRoleInstance,
				},
				Topology: &workloadsv1alpha2.TopologyConstraint{
					Pack: &workloadsv1alpha2.TopologyPackConstraint{Required: ptr.To("rack")},
				},
			},
			wantSubGroupSize: 4,
			wantSubTopology:  "rack",
		},
		{
			name: "whole-group topology stays on the PodGroup root",
			role: standalone,
			group: &common.PlacementGroup{
				Scope: common.PlacementScope{
					Roles:       []string{"prefill", "decode"},
					PartitionBy: common.PartitionByNone,
				},
				Topology: &workloadsv1alpha2.TopologyConstraint{
					Pack: &workloadsv1alpha2.TopologyPackConstraint{Required: ptr.To("block")},
				},
			},
			wantRoot: &volcanoschedulingv1beta1.NetworkTopologySpec{
				Mode:            volcanoschedulingv1beta1.HardNetworkTopologyMode,
				HighestTierName: "block",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rbg := rbgWithRoles(tt.role)
			m := New(nil)
			m.networkTopologySupported = true
			m.networkTopologyProbedAt = time.Now()
			m.topologySubGroupSupported = true
			m.topologySubGroupProbedAt = time.Now()

			got, _, err := m.buildPlacementPodGroup(context.Background(), rbg, tt.group, "test-pg", nil)
			require.NoError(t, err)

			if tt.wantRoot == nil {
				require.Nil(t, got.Spec.NetworkTopology)
			} else {
				require.Equal(t, tt.wantRoot, got.Spec.NetworkTopology)
			}

			if tt.wantSubTopology == "" {
				require.Empty(t, got.Spec.SubGroupPolicy)
				return
			}
			require.Len(t, got.Spec.SubGroupPolicy, 1)
			policy := got.Spec.SubGroupPolicy[0]
			require.Equal(t, tt.wantSubGroupSize, ptr.Deref(policy.SubGroupSize, 0))
			require.NotNil(t, policy.NetworkTopology)
			require.Equal(t, tt.wantSubTopology, policy.NetworkTopology.HighestTierName)
		})
	}
}

func TestInjectPlacementSchedulingFieldsUsesPlanMembership(t *testing.T) {
	rbg := rbgWithRoles(standaloneRole("prefill", 2, ""), standaloneRole("router", 1, ""))
	plan := &common.PlacementPlan{
		Root: &common.PlacementGroup{
			ID: "prefill",
			Scope: common.PlacementScope{
				Roles:       []string{"prefill"},
				PartitionBy: common.PartitionByNone,
			},
		},
	}
	pts := &coreapplyv1.PodTemplateSpecApplyConfiguration{}
	New(nil).InjectPlacementSchedulingFields(rbg, &rbg.Spec.Roles[0], plan, pts)

	if pts.Spec == nil || pts.Spec.SchedulerName == nil || *pts.Spec.SchedulerName != SchedulerName {
		t.Fatalf("expected schedulerName %q, got %#v", SchedulerName, pts.Spec)
	}
	if pts.Annotations == nil || pts.Annotations[AnnotationKey] == "" {
		t.Fatalf("expected PodGroup annotation, got %#v", pts.Annotations)
	}
}

func TestInjectPlacementSchedulingFieldsRoleOutsidePlanGetsNoMembership(t *testing.T) {
	rbg := rbgWithRoles(standaloneRole("prefill", 2, ""), standaloneRole("router", 1, ""))
	plan := &common.PlacementPlan{
		Root: &common.PlacementGroup{
			ID: "prefill",
			Scope: common.PlacementScope{
				Roles:       []string{"prefill"},
				PartitionBy: common.PartitionByNone,
			},
		},
	}
	pts := &coreapplyv1.PodTemplateSpecApplyConfiguration{}
	New(nil).InjectPlacementSchedulingFields(rbg, &rbg.Spec.Roles[1], plan, pts)

	if pts.ObjectMetaApplyConfiguration != nil && pts.Annotations[AnnotationKey] != "" {
		t.Fatalf("expected no PodGroup annotation, got %#v", pts.Annotations)
	}
}

func TestMergeSubGroupPolicies(t *testing.T) {
	gang := []volcanoschedulingv1beta1.SubGroupPolicySpec{{
		Name:         "prefill",
		MinSubGroups: ptr.To(int32(2)),
	}}
	topology := []volcanoschedulingv1beta1.SubGroupPolicySpec{{
		Name:            "prefill",
		SubGroupSize:    ptr.To(int32(4)),
		NetworkTopology: &volcanoschedulingv1beta1.NetworkTopologySpec{HighestTierName: "rack"},
	}}
	got := mergeSubGroupPolicies(gang, topology)
	if len(got) != 1 {
		t.Fatalf("expected one policy, got %d", len(got))
	}
	if ptr.Deref(got[0].MinSubGroups, 0) != 2 || ptr.Deref(got[0].SubGroupSize, 0) != 4 {
		t.Fatalf("unexpected merged policy: %#v", got[0])
	}
	if got[0].NetworkTopology == nil || got[0].NetworkTopology.HighestTierName != "rack" {
		t.Fatalf("expected topology on merged policy: %#v", got[0])
	}
}

func TestReconcilePlacementNoTopologyDeletesStaleTopologyPodGroups(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, volcanoschedulingv1beta1.AddToScheme(scheme))
	rbg := rbgWithRoles(standaloneRole("prefill", 2, ""), standaloneRole("decode", 2, ""))
	rbg.UID = "rbg-uid"

	controllerRef := *metav1.NewControllerRef(rbg, utils.GetRbgGVK())
	stale := &volcanoschedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:            rbg.Name + "-prefill",
			Namespace:       rbg.Namespace,
			OwnerReferences: []metav1.OwnerReference{controllerRef},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stale).Build()

	watched := sync.Map{}
	watched.Store(CrdName, struct{}{})
	plan := &common.PlacementPlan{
		Root: &common.PlacementGroup{
			ID:    rbg.Name,
			Scope: common.PlacementScope{Roles: []string{"prefill", "decode"}},
			Gang:  &common.GangStrategy{Roles: sets.New("prefill", "decode")},
		},
	}
	_, err := New(c).ReconcilePlacement(
		context.Background(),
		rbg,
		plan,
		&builder.TypedBuilder[reconcile.Request]{},
		&watched,
		c,
	)
	require.NoError(t, err)

	getErr := c.Get(context.Background(), client.ObjectKey{Name: stale.Name, Namespace: stale.Namespace}, stale)
	if !apierrors.IsNotFound(getErr) {
		t.Fatalf("expected stale topology PodGroup to be deleted, got %v", getErr)
	}
}

func TestBoundedPlacementPodGroupName(t *testing.T) {
	longName := strings.Repeat("a", 100)
	got := boundedPlacementPodGroupName(longName, "r-prefill", "uid-a")
	if len(got) != 26+1+len("r-prefill")+1+12 {
		t.Fatalf("unexpected bounded name length %d: %q", len(got), got)
	}
	if !strings.HasPrefix(got, longName[:26]+"-r-prefill-") {
		t.Fatalf("unexpected bounded name %q", got)
	}

	otherRBG := boundedPlacementPodGroupName(longName, "r-prefill", "uid-b")
	if got == otherRBG {
		t.Fatalf("expected different RBG UIDs to produce different names, got %q", got)
	}

	otherPlacement := boundedPlacementPodGroupName(longName, "p-pd", "uid-a")
	if got == otherPlacement {
		t.Fatalf("expected different placement names to produce different names, got %q", got)
	}

	longSource := boundedPlacementPodGroupName(longName, "r-"+strings.Repeat("x", 100), "uid-a")
	if !strings.Contains(longSource, "-r-"+strings.Repeat("x", 20)+"-") {
		t.Fatalf("expected source name to be truncated to 20 characters, got %q", longSource)
	}
}

func TestValidateTopologyLevelOrderDoesNotCompareRequiredAndPreferred(t *testing.T) {
	levels := map[string]int{"block": 2, "rack": 1}
	constraint := &workloadsv1alpha2.TopologyConstraint{
		Pack: &workloadsv1alpha2.TopologyPackConstraint{
			Required:  ptr.To("block"),
			Preferred: ptr.To("rack"),
		},
	}
	if err := validateTopologyLevelOrder(constraint, levels); err != nil {
		t.Fatalf("expected required=block/preferred=rack to be accepted, got %v", err)
	}
}

func TestLoadTopologyLevelsReportsMissingHyperNodeCRD(t *testing.T) {
	testScheme := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(testScheme))
	c := fake.NewClientBuilder().WithScheme(testScheme).Build()

	_, err := New(c).loadTopologyLevels(context.Background(), c)
	require.Error(t, err)
	require.True(t, common.IsSchedulerUnsupported(err), "expected SchedulerUnsupportedError, got %v", err)
	require.Contains(t, err.Error(), HyperNodeCrdName)
}

func TestLoadTopologyLevelsReportsInconsistentTiers(t *testing.T) {
	testScheme := runtime.NewScheme()
	require.NoError(t, apiextensionsv1.AddToScheme(testScheme))
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: HyperNodeCrdName},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{{
				Type:   apiextensionsv1.Established,
				Status: apiextensionsv1.ConditionTrue,
			}},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(crd).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(_ context.Context, _ client.WithWatch, list client.ObjectList, _ ...client.ListOption) error {
				hyperNodes := list.(*unstructured.UnstructuredList)
				hyperNodes.SetAPIVersion("topology.volcano.sh/v1alpha1")
				hyperNodes.SetKind("HyperNodeList")
				hyperNodes.Items = []unstructured.Unstructured{
					testHyperNode("rack-a", "rack", 1),
					testHyperNode("rack-b", "rack", 2),
				}
				return nil
			},
		}).
		Build()

	_, err := New(c).loadTopologyLevels(context.Background(), c)
	require.Error(t, err)
	require.True(t, common.IsTopologyTranslationError(err), "expected TopologyTranslationError, got %v", err)
	require.Contains(t, err.Error(), "maps to different tiers")
}

func testHyperNode(name, tierName string, tier int64) unstructured.Unstructured {
	return unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "topology.volcano.sh/v1alpha1",
			"kind":       "HyperNode",
			"metadata":   map[string]interface{}{"name": name},
			"spec": map[string]interface{}{
				"tier":     tier,
				"tierName": tierName,
			},
		},
	}
}

func TestBuildTopologySubGroupsRejectsZeroSizeRole(t *testing.T) {
	role := standaloneRole("prefill", 1, constants.RoleInstanceSetWorkloadType)
	role.Pattern = workloadsv1alpha2.Pattern{
		CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
			Components: []workloadsv1alpha2.InstanceComponent{{Name: "engine", Size: ptr.To(int32(0))}},
		},
	}
	rbg := rbgWithRoles(role)
	group := &common.PlacementGroup{
		Scope: common.PlacementScope{
			Roles:       []string{"prefill"},
			PartitionBy: common.PartitionByRoleInstance,
		},
		Topology: &workloadsv1alpha2.TopologyConstraint{
			Pack: &workloadsv1alpha2.TopologyPackConstraint{Required: ptr.To("rack")},
		},
	}

	_, _, err := buildTopologySubGroups(rbg, group)
	require.Error(t, err)
	require.True(t, common.IsTopologyTranslationError(err), "expected TopologyTranslationError, got %v", err)
	require.Contains(t, err.Error(), "subgroup size must be at least 1")
}

func TestPlacementPodGroupNameUsesReadableSourceAndRBGIdentity(t *testing.T) {
	rbgName := strings.Repeat("infer", 20)
	first := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: rbgName, Namespace: "default", UID: "uid-a"},
	}
	second := first.DeepCopy()
	second.UID = "uid-b"
	group := &common.PlacementGroup{
		ID:   "scope-id",
		Name: "r-prefill",
		Scope: common.PlacementScope{
			Roles:       []string{"prefill"},
			PartitionBy: common.PartitionByRoleInstance,
		},
		Topology: &workloadsv1alpha2.TopologyConstraint{
			Pack: &workloadsv1alpha2.TopologyPackConstraint{Required: ptr.To("rack")},
		},
	}

	firstName := placementPodGroupName(first, group, 0)
	secondName := placementPodGroupName(second, group, 0)
	if firstName == secondName {
		t.Fatalf("expected RBG UID to distinguish same-named scopes, got %q", firstName)
	}
	if !strings.Contains(firstName, "-r-prefill-") {
		t.Fatalf("expected readable placement name in %q", firstName)
	}
	if len(firstName) > 63 {
		t.Fatalf("expected DNS-label-compatible name, got %q", firstName)
	}
}
