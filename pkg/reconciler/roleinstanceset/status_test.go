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

package roleinstanceset

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	v1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestSetStatusConditions(t *testing.T) {
	tests := []struct {
		name                                                 string
		desired, actual                                      int32
		initial, updating, paused, deleting, stale, notReady bool
		scaleDelta                                           int
		phase                                                v1alpha2.RoleInstanceSetPhase
		reason                                               string
		ready                                                bool
	}{
		{name: "首次创建", desired: 2, initial: true, scaleDelta: 2, phase: v1alpha2.RoleInstanceSetPhaseCreating, reason: v1alpha2.RoleInstanceSetReasonCreating},
		{name: "运行且就绪", desired: 2, actual: 2, phase: v1alpha2.RoleInstanceSetPhaseRunning, reason: v1alpha2.RoleInstanceSetReasonConverged, ready: true},
		{name: "运行等待 Pod", desired: 2, actual: 2, notReady: true, phase: v1alpha2.RoleInstanceSetPhaseRunning, reason: v1alpha2.RoleInstanceSetReasonWaitingForPods},
		{name: "扩容", desired: 3, actual: 2, scaleDelta: 1, phase: v1alpha2.RoleInstanceSetPhaseScalingUp, reason: v1alpha2.RoleInstanceSetReasonScalingUp},
		{name: "缩容", desired: 1, actual: 2, scaleDelta: -1, phase: v1alpha2.RoleInstanceSetPhaseScalingDown, reason: v1alpha2.RoleInstanceSetReasonScalingDown},
		{name: "更新 surge", desired: 2, actual: 3, updating: true, scaleDelta: -1, phase: v1alpha2.RoleInstanceSetPhaseUpdating, reason: v1alpha2.RoleInstanceSetReasonRollingUpdate},
		{name: "相同 revision 回滚替换", desired: 2, actual: 2, updating: true, phase: v1alpha2.RoleInstanceSetPhaseUpdating, reason: v1alpha2.RoleInstanceSetReasonRollingUpdate},
		{name: "暂停更新", desired: 2, actual: 2, updating: true, paused: true, phase: v1alpha2.RoleInstanceSetPhaseUpdating, reason: v1alpha2.RoleInstanceSetReasonPaused},
		{name: "暂停仍可扩容", desired: 3, actual: 2, updating: true, paused: true, scaleDelta: 1, phase: v1alpha2.RoleInstanceSetPhaseUpdating, reason: v1alpha2.RoleInstanceSetReasonRollingUpdate},
		{name: "暂停已收敛", desired: 2, actual: 2, paused: true, phase: v1alpha2.RoleInstanceSetPhaseRunning, reason: v1alpha2.RoleInstanceSetReasonConverged, ready: true},
		{name: "子 generation 过期", desired: 2, actual: 2, stale: true, phase: v1alpha2.RoleInstanceSetPhaseRunning, reason: v1alpha2.RoleInstanceSetReasonWaitingForPods},
		{name: "零副本", phase: v1alpha2.RoleInstanceSetPhaseRunning, reason: v1alpha2.RoleInstanceSetReasonConverged, ready: true},
		{name: "删除优先", desired: 2, actual: 3, updating: true, deleting: true, scaleDelta: -1, phase: v1alpha2.RoleInstanceSetPhaseTerminating, reason: v1alpha2.RoleInstanceSetReasonTerminating},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := &v1alpha2.RoleInstanceSet{ObjectMeta: metav1.ObjectMeta{Generation: 5}, Spec: v1alpha2.RoleInstanceSetSpec{Replicas: ptr.To(tt.desired)}}
			set.Spec.UpdateStrategy.Paused = tt.paused
			set.Status.CurrentRevision = "revision"
			if tt.initial {
				set.Status.CurrentRevision = ""
			}
			if tt.deleting {
				now := metav1.Now()
				set.DeletionTimestamp = &now
			}
			status := &v1alpha2.RoleInstanceSetStatus{ObservedGeneration: 5, CurrentRevision: "revision", UpdateRevision: "revision", Replicas: tt.actual, ReadyReplicas: tt.actual, UpdatedReplicas: tt.actual, UpdatedReadyReplicas: tt.actual}
			instances := make([]*v1alpha2.RoleInstance, 0, tt.actual)
			for i := int32(0); i < tt.actual; i++ {
				instance := &v1alpha2.RoleInstance{ObjectMeta: metav1.ObjectMeta{Generation: 3}, Status: v1alpha2.RoleInstanceStatus{ObservedGeneration: 3, CurrentRevision: "ri-revision", UpdateRevision: "ri-revision", Conditions: []v1alpha2.RoleInstanceCondition{{Type: v1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue}}}}
				if tt.stale {
					instance.Status.ObservedGeneration = 2
				}
				if tt.notReady {
					instance.Status.Conditions[0].Status = corev1.ConditionFalse
				}
				instances = append(instances, instance)
			}
			before := status.DeepCopy()
			SetStatusConditions(set, status, instances, tt.updating, tt.scaleDelta)
			require.Equal(t, tt.phase, status.Phase)
			require.Len(t, status.Conditions, 2)
			require.Equal(t, tt.ready, status.Conditions[0].Status == corev1.ConditionTrue)
			require.Equal(t, tt.reason, status.Conditions[1].Reason)
			for _, c := range status.Conditions {
				require.NotEmpty(t, c.Reason)
				require.False(t, c.LastTransitionTime.IsZero())
			}
			require.Equal(t, before.CurrentRevision, status.CurrentRevision)
			require.Equal(t, before.Replicas, status.Replicas)
			stable := status.DeepCopy()
			SetStatusConditions(set, status, instances, tt.updating, tt.scaleDelta)
			require.Equal(t, stable, status)
		})
	}
}

func TestSurgePhaseAcrossReconciles(t *testing.T) {
	for _, tt := range []struct {
		name       string
		generation int64
		replicas   int32
		phase      v1alpha2.RoleInstanceSetPhase
	}{
		{name: "同 generation 回收残余 surge", generation: 5, replicas: 3, phase: v1alpha2.RoleInstanceSetPhaseUpdating},
		{name: "新 generation 用户缩容", generation: 6, replicas: 3, phase: v1alpha2.RoleInstanceSetPhaseScalingDown},
		{name: "surge 已回收", generation: 5, replicas: 2, phase: v1alpha2.RoleInstanceSetPhaseRunning},
	} {
		t.Run(tt.name, func(t *testing.T) {
			set := &v1alpha2.RoleInstanceSet{
				ObjectMeta: metav1.ObjectMeta{Generation: tt.generation},
				Spec:       v1alpha2.RoleInstanceSetSpec{Replicas: ptr.To[int32](2)},
				Status:     v1alpha2.RoleInstanceSetStatus{Phase: v1alpha2.RoleInstanceSetPhaseUpdating, ObservedGeneration: 5, CurrentRevision: "revision", UpdateRevision: "revision"},
			}
			status := &v1alpha2.RoleInstanceSetStatus{ObservedGeneration: tt.generation, CurrentRevision: "revision", UpdateRevision: "revision", Replicas: tt.replicas}
			SetStatusConditions(set, status, nil, false, int(2-tt.replicas))
			require.Equal(t, tt.phase, status.Phase)
			require.Equal(t, tt.replicas, status.Replicas)
			require.Equal(t, "revision", status.CurrentRevision)
		})
	}
}

func TestWaitingAndMergeConditions(t *testing.T) {
	oldTime := metav1.NewTime(time.Now().Add(-time.Hour))
	set := &v1alpha2.RoleInstanceSet{}
	set.Status.Phase = v1alpha2.RoleInstanceSetPhaseRunning
	set.Status.Conditions = []v1alpha2.RoleInstanceSetCondition{
		{Type: v1alpha2.RoleInstanceSetConditionReady, Status: corev1.ConditionFalse, Reason: "Old", LastTransitionTime: oldTime},
		{Type: v1alpha2.RoleInstanceSetConditionReady, Status: corev1.ConditionFalse},
		{Type: v1alpha2.RoleInstanceSetConditionFailedScale, Status: corev1.ConditionTrue},
		{Type: "External", Status: corev1.ConditionUnknown, Reason: "ExternalReason", LastTransitionTime: oldTime},
	}
	status := set.Status.DeepCopy()
	SetWaitingConditions(set, status, status.Phase, v1alpha2.RoleInstanceSetReasonInvalidSpec, corev1.ConditionFalse)
	require.Equal(t, v1alpha2.RoleInstanceSetPhaseRunning, status.Phase)
	require.Len(t, status.Conditions, 3)
	require.Equal(t, oldTime, status.Conditions[0].LastTransitionTime)
	require.Equal(t, "ExternalReason", status.Conditions[1].Reason)
	SetWaitingConditions(set, status, v1alpha2.RoleInstanceSetPhaseUpdating, v1alpha2.RoleInstanceSetReasonPatternMigration, corev1.ConditionTrue)
	require.Equal(t, v1alpha2.RoleInstanceSetPhaseUpdating, status.Phase)
	require.Equal(t, v1alpha2.RoleInstanceSetReasonPatternMigration, status.Conditions[2].Reason)
	live := append([]v1alpha2.RoleInstanceSetCondition(nil), status.Conditions...)
	live[1].Reason = "ConcurrentExternalReason"
	merged := MergeConditions(live, status.Conditions)
	require.Equal(t, "ConcurrentExternalReason", merged[1].Reason)
	require.Equal(t, oldTime, merged[0].LastTransitionTime)
}
