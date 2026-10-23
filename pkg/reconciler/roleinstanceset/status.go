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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	v1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instanceutil "sigs.k8s.io/rbgs/pkg/inplace/instance"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/specifieddelete"
)

// SetStatusConditions 在原计数与 revision 计算后补充展示，不更改它们或执行动作。
// updatePending 来自当前模式的 rollout；scaleDelta 只用于展示方向和暂停时仍可执行的伸缩。
func SetStatusConditions(set *v1alpha2.RoleInstanceSet, status *v1alpha2.RoleInstanceSetStatus, instances []*v1alpha2.RoleInstance, updatePending bool, scaleDelta int) {
	desired := ptr.Deref(set.Spec.Replicas, 1)
	updatePending = updatePending || status.CurrentRevision != status.UpdateRevision
	// revision 已收敛后仍可能在回收 surge；同一 generation 延续更新展示，
	// 新的用户伸缩 generation 则重新按本轮拓扑判断。此值不参与控制决策。
	if status.Replicas > desired && set.Status.Phase == v1alpha2.RoleInstanceSetPhaseUpdating && set.Status.ObservedGeneration == set.Generation {
		updatePending = true
	}
	fresh := len(instances) == int(desired)
	for _, instance := range instances {
		if instance == nil {
			fresh = false
			continue
		}
		state := lifecycle.GetRoleInstanceLifecycleState(instance)
		if state == constants.RoleInstanceLifecycleStatePreparingUpdate || state == constants.RoleInstanceLifecycleStateUpdating || state == constants.RoleInstanceLifecycleStateUpdated {
			updatePending = true
		}
		if instance.Status.CurrentRevision != "" && instance.Status.UpdateRevision != "" && instance.Status.CurrentRevision != instance.Status.UpdateRevision {
			updatePending = true
		}
		if instance.DeletionTimestamp != nil || instance.Status.ObservedGeneration < instance.Generation ||
			instance.Status.CurrentRevision == "" || instance.Status.CurrentRevision != instance.Status.UpdateRevision ||
			!instanceutil.IsRoleInstanceReady(instance) || specifieddelete.IsSpecifiedDelete(instance) ||
			(state != "" && state != constants.RoleInstanceLifecycleStateNormal) {
			fresh = false
		}
		for _, gate := range instance.Spec.ReadinessGates {
			condition := instanceutil.GetRoleInstanceCondition(instance, gate.ConditionType)
			if condition == nil || condition.Status != corev1.ConditionTrue {
				fresh = false
			}
		}
	}
	ready := fresh && !updatePending && scaleDelta == 0 && status.ObservedGeneration >= set.Generation &&
		status.UpdateRevision != "" && status.CurrentRevision == status.UpdateRevision &&
		status.Replicas == desired && status.ReadyReplicas == desired && status.UpdatedReplicas == desired && status.UpdatedReadyReplicas == desired

	phase, reason := v1alpha2.RoleInstanceSetPhaseRunning, v1alpha2.RoleInstanceSetReasonWaitingForPods
	progressing := corev1.ConditionTrue
	switch {
	case set.DeletionTimestamp != nil:
		phase, reason, ready, progressing = v1alpha2.RoleInstanceSetPhaseTerminating, v1alpha2.RoleInstanceSetReasonTerminating, false, corev1.ConditionFalse
	case updatePending:
		phase, reason = v1alpha2.RoleInstanceSetPhaseUpdating, v1alpha2.RoleInstanceSetReasonRollingUpdate
	case set.Status.CurrentRevision == "" && len(instances) == 0 && !ready:
		phase, reason = v1alpha2.RoleInstanceSetPhaseCreating, v1alpha2.RoleInstanceSetReasonCreating
	case scaleDelta > 0:
		phase, reason = v1alpha2.RoleInstanceSetPhaseScalingUp, v1alpha2.RoleInstanceSetReasonScalingUp
	case scaleDelta < 0:
		phase, reason = v1alpha2.RoleInstanceSetPhaseScalingDown, v1alpha2.RoleInstanceSetReasonScalingDown
	}
	if set.DeletionTimestamp == nil && !ready && set.Spec.UpdateStrategy.Paused && scaleDelta == 0 {
		reason, progressing = v1alpha2.RoleInstanceSetReasonPaused, corev1.ConditionFalse
	}
	status.Phase = phase
	status.Conditions = MergeConditions(set.Status.Conditions, status.Conditions)
	readyStatus, readyReason := corev1.ConditionFalse, reason
	if ready {
		readyStatus, readyReason = corev1.ConditionTrue, v1alpha2.RoleInstanceSetReasonAllInstancesReady
		progressing, reason = corev1.ConditionFalse, v1alpha2.RoleInstanceSetReasonConverged
	}
	setCondition(status, v1alpha2.RoleInstanceSetConditionReady, readyStatus, readyReason)
	setCondition(status, v1alpha2.RoleInstanceSetConditionProgressing, progressing, reason)
}

// SetWaitingConditions 仅供迁移、明确无效配置与删除等已有早退路径调用。
func SetWaitingConditions(set *v1alpha2.RoleInstanceSet, status *v1alpha2.RoleInstanceSetStatus, phase v1alpha2.RoleInstanceSetPhase, reason string, progressing corev1.ConditionStatus) {
	if phase == "" {
		phase = v1alpha2.RoleInstanceSetPhaseCreating
	}
	if set.DeletionTimestamp != nil {
		phase, reason, progressing = v1alpha2.RoleInstanceSetPhaseTerminating, v1alpha2.RoleInstanceSetReasonTerminating, corev1.ConditionFalse
	}
	status.Phase = phase
	status.Conditions = MergeConditions(set.Status.Conditions, status.Conditions)
	setCondition(status, v1alpha2.RoleInstanceSetConditionReady, corev1.ConditionFalse, reason)
	setCondition(status, v1alpha2.RoleInstanceSetConditionProgressing, progressing, reason)
}

// MergeConditions 保留最新外部条件，内置条件按 type 覆盖并清理旧瞬态条件。
func MergeConditions(live, computed []v1alpha2.RoleInstanceSetCondition) []v1alpha2.RoleInstanceSetCondition {
	out := make([]v1alpha2.RoleInstanceSetCondition, 0, len(live)+2)
	seen := map[v1alpha2.RoleInstanceSetConditionType]bool{}
	for _, c := range live {
		if c.Type == v1alpha2.RoleInstanceSetConditionFailedScale || c.Type == v1alpha2.RoleInstanceSetConditionFailedUpdate || c.Type == "RollingUpdateInProgress" || seen[c.Type] {
			continue
		}
		seen[c.Type] = true
		out = append(out, c)
	}
	status := &v1alpha2.RoleInstanceSetStatus{Conditions: out}
	for _, c := range computed {
		if c.Type == v1alpha2.RoleInstanceSetConditionReady || c.Type == v1alpha2.RoleInstanceSetConditionProgressing {
			putCondition(status, c)
		}
	}
	return status.Conditions
}

func setCondition(status *v1alpha2.RoleInstanceSetStatus, typ v1alpha2.RoleInstanceSetConditionType, value corev1.ConditionStatus, reason string) {
	putCondition(status, v1alpha2.RoleInstanceSetCondition{Type: typ, Status: value, Reason: reason, Message: reason})
}

func putCondition(status *v1alpha2.RoleInstanceSetStatus, c v1alpha2.RoleInstanceSetCondition) {
	if c.LastTransitionTime.IsZero() {
		c.LastTransitionTime = metav1.Now()
	}
	for i, old := range status.Conditions {
		if old.Type != c.Type {
			continue
		}
		if old.Status == c.Status && !old.LastTransitionTime.IsZero() {
			c.LastTransitionTime = old.LastTransitionTime
		}
		status.Conditions[i] = c
		return
	}
	status.Conditions = append(status.Conditions, c)
}
