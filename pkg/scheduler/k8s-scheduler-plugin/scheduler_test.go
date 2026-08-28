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

package kubeschedulerplugin

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	coreapplyv1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scheduler/common"
	schedv1alpha1 "sigs.k8s.io/scheduler-plugins/apis/scheduling/v1alpha1"
)

func testRBG() *workloadsv1alpha2.RoleBasedGroup {
	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "rbg", Namespace: "default"},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: []workloadsv1alpha2.RoleSpec{{
				Name:     "prefill",
				Replicas: ptr.To(int32(2)),
				Pattern: workloadsv1alpha2.Pattern{
					StandalonePattern: &workloadsv1alpha2.StandalonePattern{},
				},
			}},
		},
	}
}

func twoRoleRBG() *workloadsv1alpha2.RoleBasedGroup {
	rbg := testRBG()
	rbg.Spec.Roles = append(rbg.Spec.Roles, workloadsv1alpha2.RoleSpec{
		Name:     "decode",
		Replicas: ptr.To(int32(3)),
		Pattern: workloadsv1alpha2.Pattern{
			StandalonePattern: &workloadsv1alpha2.StandalonePattern{},
		},
	})
	return rbg
}

func ptsLabels(pts *coreapplyv1.PodTemplateSpecApplyConfiguration) map[string]string {
	if pts.ObjectMetaApplyConfiguration == nil {
		return nil
	}
	return pts.Labels
}

func TestInjectPodSchedulingFields(t *testing.T) {
	rbg := testRBG()
	role := &rbg.Spec.Roles[0]

	t.Run("gang disabled injects nothing", func(t *testing.T) {
		pts := &coreapplyv1.PodTemplateSpecApplyConfiguration{}
		New(nil, "custom-scheduler").InjectPodSchedulingFields(rbg, role, nil, pts)
		assert.Nil(t, pts.Spec)
		assert.Empty(t, ptsLabels(pts))
	})

	t.Run("profile name configured", func(t *testing.T) {
		pts := &coreapplyv1.PodTemplateSpecApplyConfiguration{}
		New(nil, "custom-scheduler").InjectPodSchedulingFields(
			rbg, role, &common.GangStrategy{}, pts)
		require.NotNil(t, pts.Spec)
		assert.Equal(t, "custom-scheduler", ptr.Deref(pts.Spec.SchedulerName, ""))
		assert.Equal(t, "rbg", ptsLabels(pts)[LabelKey])
	})

	// Upstream coscheduling resolves the PodGroup only from UpstreamLabelKey, while
	// Koordinator/ACK reads LabelKey. Dropping either one turns gang scheduling into a
	// silent no-op on that half of the ecosystem.
	t.Run("both PodGroup label conventions are injected", func(t *testing.T) {
		pts := &coreapplyv1.PodTemplateSpecApplyConfiguration{}
		New(nil, "").InjectPodSchedulingFields(rbg, role, &common.GangStrategy{}, pts)
		assert.Equal(t, "rbg", ptsLabels(pts)[LabelKey])
		assert.Equal(t, "rbg", ptsLabels(pts)[UpstreamLabelKey])
		assert.Equal(t, "scheduling.x-k8s.io/pod-group", UpstreamLabelKey)
	})

	// The profile name is chosen when scheduler-plugins is deployed, so an empty flag
	// must leave schedulerName alone rather than blanking whatever the role template set.
	t.Run("profile name empty leaves schedulerName untouched", func(t *testing.T) {
		pts := &coreapplyv1.PodTemplateSpecApplyConfiguration{}
		New(nil, "").InjectPodSchedulingFields(rbg, role, &common.GangStrategy{}, pts)
		assert.Nil(t, pts.Spec)
		assert.Equal(t, "rbg", ptsLabels(pts)[LabelKey])
	})
}

// A role outside spec.policies[].roles must not carry the PodGroup labels: coscheduling
// counts every labelled pod against minMember, which was sized for the covered roles only.
func TestInjectPodSchedulingFieldsRoleOutsideGang(t *testing.T) {
	rbg := twoRoleRBG()
	decode := &rbg.Spec.Roles[1]
	strategy := &common.GangStrategy{Roles: sets.New("prefill")}

	pts := &coreapplyv1.PodTemplateSpecApplyConfiguration{}
	New(nil, "custom-scheduler").InjectPodSchedulingFields(rbg, decode, strategy, pts)

	require.NotNil(t, pts.Spec)
	assert.Equal(t, "custom-scheduler", ptr.Deref(pts.Spec.SchedulerName, ""))
	assert.NotContains(t, ptsLabels(pts), LabelKey)
	assert.NotContains(t, ptsLabels(pts), UpstreamLabelKey)
}

func TestCreateOrUpdateMinMemberCoversScopedRolesOnly(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, schedv1alpha1.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))

	rbg := twoRoleRBG()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	require.NoError(t, New(c, "").createOrUpdate(
		context.Background(), rbg, &common.GangStrategy{Roles: sets.New("prefill")}))

	podGroup := &schedv1alpha1.PodGroup{}
	require.NoError(t, c.Get(
		context.Background(), client.ObjectKey{Name: rbg.Name, Namespace: rbg.Namespace}, podGroup))
	assert.Equal(t, int32(2), podGroup.Spec.MinMember)
}

// TestReconcilePodGroupRejectsPerRoleMinimums pins the runtime safety net: the webhook
// already refuses minReplicas under this scheduler, but a policy admitted while the
// controller ran with --scheduler-name=volcano must not silently degrade to a
// whole-group gang after a restart.
func TestReconcilePodGroupRejectsPerRoleMinimums(t *testing.T) {
	strategy := &common.GangStrategy{
		Roles:       sets.New("prefill"),
		MinReplicas: map[string]int32{"prefill": 1},
	}
	err := New(nil, "").ReconcilePodGroup(
		context.Background(), testRBG(), strategy, nil, &sync.Map{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support per-role minimum gang scheduling")
	// The controller keys its permanent-failure handling off this classification.
	assert.True(t, common.IsIncompatibleGangConfig(err))
}
