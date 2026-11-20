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

package roleinstance

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	statelessutils "sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statelessmode/utils"
)

func TestMinReadySecondsTimingSurvivesStatusRewrites(t *testing.T) {
	// The legacy controller rebuilt the RoleInstanceReady condition with the
	// current time on every status write, restarting minReadySeconds
	// availability. The status model preserves lastTransitionTime; this is the
	// before/after regression test the KEP upgrade table requires.
	instance := newReadinessTestInstance()
	instance.Spec.ReadinessGates = nil
	pods := []*v1.Pod{newReadinessTestPod(instance, "router"), newReadinessTestPod(instance, "sidecar")}
	for _, pod := range pods {
		pod.Status.Phase = v1.PodRunning
		pod.Status.Conditions[0].Status = v1.ConditionTrue
	}
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithObjects(pods[0], pods[1]).Build()
	updater := &realStatusUpdater{Client: c}

	first := &workloadsv1alpha2.RoleInstanceStatus{CurrentRevision: "r1", UpdateRevision: "r1"}
	updater.calculateStatus(instance, first, nil, pods)
	updater.setInstanceConditions(instance, first, nil, pods)

	// The instance became Ready 90 seconds ago.
	becameReady := metav1.NewTime(time.Now().Add(-90 * time.Second))
	for i := range first.Conditions {
		if first.Conditions[i].Type == workloadsv1alpha2.RoleInstanceReady {
			first.Conditions[i].LastTransitionTime = becameReady
		}
	}
	instance.Status = *first.DeepCopy()

	// A status-model rewrite recomputes conditions without restarting
	// availability timing.
	recomputed := &workloadsv1alpha2.RoleInstanceStatus{CurrentRevision: "r1", UpdateRevision: "r1"}
	updater.calculateStatus(instance, recomputed, nil, pods)
	updater.setInstanceConditions(instance, recomputed, nil, pods)
	var preserved metav1.Time
	for i := range recomputed.Conditions {
		if recomputed.Conditions[i].Type == workloadsv1alpha2.RoleInstanceReady {
			preserved = recomputed.Conditions[i].LastTransitionTime
		}
	}
	require.Equal(t, becameReady, preserved, "lastTransitionTime must survive the rewrite")

	// With minReadySeconds=60 the instance is available; with 120 it is not,
	// proving the preserved timestamp, not a fresh one, drives availability.
	instance.Status = *recomputed.DeepCopy()
	require.True(t, statelessutils.IsRunningAndAvailable(instance, 60))
	require.False(t, statelessutils.IsRunningAndAvailable(instance, 120))
}

func TestTransientFailureConditionsClearAfterRecovery(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Spec.Components = instance.Spec.Components[:1]
	pod := newReadinessTestPod(instance, "router")
	pod.Status.Phase = v1.PodRunning
	pod.Status.Conditions[0].Status = v1.ConditionTrue
	instance.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
		{
			Type:   workloadsv1alpha2.RoleInstanceInPlaceUpdateReady,
			Status: v1.ConditionTrue,
		},
		{
			Type:   workloadsv1alpha2.RoleInstanceFailedScale,
			Status: v1.ConditionTrue,
		},
		{
			Type:   workloadsv1alpha2.RoleInstanceFailedUpdate,
			Status: v1.ConditionTrue,
		},
		{
			Type:   workloadsv1alpha2.RoleInstanceCustomReady,
			Status: v1.ConditionTrue,
		},
	}

	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}, &v1.Pod{}).
		WithObjects(instance, pod).Build()
	updater := newStatusUpdater(c)

	current := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), current))
	require.NoError(t, updater.UpdateInstanceStatus(ctx, current, &workloadsv1alpha2.RoleInstanceStatus{}, []*v1.Pod{pod}))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), current))

	assertCondition := func(conditionType workloadsv1alpha2.RoleInstanceConditionType, want bool) {
		t.Helper()
		found := false
		for _, condition := range current.Status.Conditions {
			if condition.Type == conditionType {
				found = true
				break
			}
		}
		assert.Equal(t, want, found, "unexpected presence of condition %s", conditionType)
	}

	assertCondition(workloadsv1alpha2.RoleInstanceFailedScale, false)
	assertCondition(workloadsv1alpha2.RoleInstanceFailedUpdate, false)
	assertCondition(workloadsv1alpha2.RoleInstanceCustomReady, true)
	assertCondition(workloadsv1alpha2.RoleInstanceReady, true)
}
