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
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestInconsistentBaselines(t *testing.T) {
	tests := []struct {
		name     string
		old      map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline
		new      map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline
		expected bool
	}{
		{
			name:     "both nil",
			old:      nil,
			new:      nil,
			expected: false,
		},
		{
			name: "old nil, new non-nil",
			old:  nil,
			new: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			expected: true,
		},
		{
			name: "old non-nil, new nil",
			old: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			new:      nil,
			expected: true,
		},
		{
			name: "same content",
			old: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}, "sidecar": {RestartCount: 2}},
			},
			new: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}, "sidecar": {RestartCount: 2}},
			},
			expected: false,
		},
		{
			name: "different pod names",
			old: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			new: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-1": {"main": {RestartCount: 0}},
			},
			expected: true,
		},
		{
			name: "different container names",
			old: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			new: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"sidecar": {RestartCount: 0}},
			},
			expected: true,
		},
		{
			name: "different restart counts",
			old: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			new: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 1}},
			},
			expected: true,
		},
		{
			name: "different ImageIDs",
			old: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0, ImageID: "img-v1"}},
			},
			new: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0, ImageID: "img-v2"}},
			},
			expected: true,
		},
		{
			name: "extra pod in new",
			old: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
			},
			new: map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"pod-0": {"main": {RestartCount: 0}},
				"pod-1": {"main": {RestartCount: 0}},
			},
			expected: true,
		},
		{
			name:     "both empty maps",
			old:      map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{},
			new:      map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := inconsistentBaselines(tt.old, tt.new)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRestartTrackingChanged(t *testing.T) {
	now := metav1.NewTime(time.Now())
	earlier := metav1.NewTime(now.Add(-1 * time.Minute))
	later := metav1.NewTime(now.Add(1 * time.Minute))

	tests := []struct {
		name                string
		newLastRestartTime  *metav1.Time
		instanceLastRestart *metav1.Time
		liveLastRestart     *metav1.Time
		expectedChanged     bool
	}{
		{
			name:                "new is nil: not changed",
			newLastRestartTime:  nil,
			instanceLastRestart: &now,
			liveLastRestart:     &now,
			expectedChanged:     false,
		},
		{
			name:                "new differs from both informer and live: changed (updateRestartTracking ran)",
			newLastRestartTime:  &later,
			instanceLastRestart: &now,
			liveLastRestart:     &now,
			expectedChanged:     true,
		},
		{
			name:                "new equals informer but differs from live: not changed (syncRestartTrackingFromAPI pass-through)",
			newLastRestartTime:  &now,
			instanceLastRestart: &now,
			liveLastRestart:     &earlier,
			expectedChanged:     false,
		},
		{
			name:                "new equals live but differs from informer: not changed (informer stale, live matches)",
			newLastRestartTime:  &now,
			instanceLastRestart: &earlier,
			liveLastRestart:     &now,
			expectedChanged:     false,
		},
		{
			name:                "new equals both: not changed",
			newLastRestartTime:  &now,
			instanceLastRestart: &now,
			liveLastRestart:     &now,
			expectedChanged:     false,
		},
		{
			name:                "informer nil, live nil, new set: changed (first restart)",
			newLastRestartTime:  &now,
			instanceLastRestart: nil,
			liveLastRestart:     nil,
			expectedChanged:     true,
		},
		{
			name:                "informer nil, new equals live: not changed",
			newLastRestartTime:  &now,
			instanceLastRestart: nil,
			liveLastRestart:     &now,
			expectedChanged:     false,
		},
		{
			name:                "live nil, new differs from informer: changed",
			newLastRestartTime:  &later,
			instanceLastRestart: &now,
			liveLastRestart:     nil,
			expectedChanged:     true,
		},
		{
			name:                "live nil, new equals informer: not changed",
			newLastRestartTime:  &now,
			instanceLastRestart: &now,
			liveLastRestart:     nil,
			expectedChanged:     false,
		},
		{
			name:                "informer nil, live set, new differs from live: changed",
			newLastRestartTime:  &later,
			instanceLastRestart: nil,
			liveLastRestart:     &now,
			expectedChanged:     true,
		},
		{
			name:                "informer nil, live set, new equals live: not changed",
			newLastRestartTime:  &now,
			instanceLastRestart: nil,
			liveLastRestart:     &now,
			expectedChanged:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := restartTrackingChanged(tt.newLastRestartTime, tt.instanceLastRestart, tt.liveLastRestart)
			assert.Equal(t, tt.expectedChanged, result)
		})
	}
}

func TestStatusPreservesPersistedRestartList(t *testing.T) {
	for _, state := range []string{"deleting", "completed", "count reset"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			cached := newReadinessTestInstance()
			oldTime := metav1.NewTime(time.Now().Add(-time.Minute))
			cached.Status.RestartCount = 5
			cached.Status.LastRestartTime = &oldTime
			cached.Status.RetiringPods = []workloadsv1alpha2.RoleInstancePodReference{{Name: "old-pod", UID: "old"}}
			cached.Status.InPlaceUpdateContainerBaselines = map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				"old-pod": {"main": {RestartCount: 0, ImageID: "old-image"}},
			}
			cached.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
				{Type: workloadsv1alpha2.RoleInstanceReady, Status: v1.ConditionTrue},
				{Type: workloadsv1alpha2.RoleInstanceRestarting, Status: v1.ConditionTrue},
				{Type: workloadsv1alpha2.RoleInstanceCustomReady, Status: v1.ConditionTrue},
			}
			live := cached.DeepCopy()
			live.Status.RestartCount = 6
			live.Status.LastRestartTime = nil
			live.Status.InPlaceUpdateContainerBaselines = nil
			live.Status.Conditions[2].Status = v1.ConditionFalse
			if state != "deleting" {
				live.Status.RetiringPods = nil
				live.Status.Conditions = append(live.Status.Conditions[:1], live.Status.Conditions[2:]...)
			}
			if state == "count reset" {
				live.Status.RestartCount = 1
			}
			c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
				WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).WithObjects(live).Build()
			updater := &realStatusUpdater{Client: c, apiReader: c}
			computed := cached.Status.DeepCopy()
			computed.ObservedGeneration = cached.Generation
			require.NoError(t, updater.updateStatus(ctx, cached, computed))
			actual := &workloadsv1alpha2.RoleInstance{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(live), actual))
			assert.Equal(t, live.Status.RetiringPods, actual.Status.RetiringPods)
			assert.Equal(t, live.Status.RestartCount, actual.Status.RestartCount)
			assert.Nil(t, actual.Status.LastRestartTime, "preserve an explicit fresh clear")
			assert.NotContains(t, actual.Status.InPlaceUpdateContainerBaselines, "old-pod")
			counts := map[workloadsv1alpha2.RoleInstanceConditionType]int{}
			for _, condition := range actual.Status.Conditions {
				counts[condition.Type]++
				if condition.Type == workloadsv1alpha2.RoleInstanceCustomReady {
					assert.Equal(t, v1.ConditionFalse, condition.Status, "preserve the external writer")
				}
				if state == "deleting" && condition.Type == workloadsv1alpha2.RoleInstanceReady {
					assert.Equal(t, v1.ConditionFalse, condition.Status)
				}
			}
			assert.Equal(t, 1, counts[workloadsv1alpha2.RoleInstanceCustomReady])
			if state == "deleting" {
				assert.Equal(t, 1, counts[workloadsv1alpha2.RoleInstanceRestarting])
			} else {
				assert.Zero(t, counts[workloadsv1alpha2.RoleInstanceRestarting])
			}
		})
	}
}

func TestStatusPreservesAppliedBaselineAcrossGenerationChange(t *testing.T) {
	for _, state := range []string{"same pod", "replacement pod", "newer update", "retiring", "newer baseline"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			cached := newReadinessTestInstance()
			cached.Status.ObservedGeneration = cached.Generation
			cached.Status.CurrentRevision = "revision-1"
			cached.Status.UpdateRevision = "revision-2"
			pod := newReadinessTestPod(cached, "router")
			pod.UID = "updated-pod"
			pod.Labels = map[string]string{apps.ControllerRevisionHashLabelKey: "revision-2"}
			livePod := pod.DeepCopy()
			live := cached.DeepCopy()
			live.Generation++
			*live.Spec.Components[0].Size = 2 // size-only changes do not change the Pod revision
			computed := cached.Status.DeepCopy()
			computed.CurrentRevision = "revision-2"
			computed.InPlaceUpdateContainerBaselines = map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
				pod.Name: {"main": {RestartCount: 0, ImageID: "old-image"}},
			}
			switch state {
			case "replacement pod":
				livePod.UID = "replacement-uid"
			case "newer update":
				livePod.Labels[apps.ControllerRevisionHashLabelKey] = "revision-3"
			case "retiring":
				live.Status.RetiringPods = []workloadsv1alpha2.RoleInstancePodReference{{Name: pod.Name, UID: pod.UID}}
			case "newer baseline":
				live.Status.InPlaceUpdateContainerBaselines = map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline{
					pod.Name: {"main": {RestartCount: 2, ImageID: "newer-image"}},
				}
			}
			c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
				WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).WithObjects(live, livePod).Build()
			updater := &realStatusUpdater{Client: c, apiReader: c}
			require.NoError(t, updater.updateStatus(ctx, cached, computed, pod))
			actual := &workloadsv1alpha2.RoleInstance{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(live), actual))
			assert.Equal(t, live.Status.ObservedGeneration, actual.Status.ObservedGeneration,
				"do not acknowledge a spec that this reconcile did not process")
			assert.Equal(t, live.Status.CurrentRevision, actual.Status.CurrentRevision,
				"do not write stale aggregate status")
			if state == "same pod" {
				assert.Equal(t, computed.InPlaceUpdateContainerBaselines, actual.Status.InPlaceUpdateContainerBaselines,
					"preserve bookkeeping for an already-applied image update")
			} else {
				assert.Equal(t, live.Status.InPlaceUpdateContainerBaselines, actual.Status.InPlaceUpdateContainerBaselines,
					"do not restore allowances for another incarnation or newer operation")
			}
		})
	}
}
