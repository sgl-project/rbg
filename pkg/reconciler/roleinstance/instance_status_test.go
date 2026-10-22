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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	inplaceutil "sigs.k8s.io/rbgs/pkg/inplace/instance"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestInstanceReadyGates(t *testing.T) {
	for _, gateType := range []workloadsv1alpha2.RoleInstanceConditionType{workloadsv1alpha2.RoleInstanceInPlaceUpdateReady, workloadsv1alpha2.RoleInstanceCustomReady, "example.com/ready"} {
		for _, gateStatus := range []corev1.ConditionStatus{"", corev1.ConditionUnknown, corev1.ConditionFalse, corev1.ConditionTrue} {
			for _, policy := range []workloadsv1alpha2.RoleInstanceReadyPolicyType{"", workloadsv1alpha2.RoleInstanceReadyOnAllPodReady, workloadsv1alpha2.RoleInstanceReadyPolicyTypeNone} {
				t.Run(string(gateType)+"/"+string(gateStatus)+"/"+string(policy), func(t *testing.T) {
					instance := &workloadsv1alpha2.RoleInstance{}
					instance.Spec.ReadyPolicy = policy
					instance.Spec.ReadinessGates = []workloadsv1alpha2.RoleInstanceReadinessGate{{ConditionType: gateType}}
					var gate *workloadsv1alpha2.RoleInstanceCondition
					if gateStatus != "" {
						gate = &workloadsv1alpha2.RoleInstanceCondition{Type: gateType, Status: gateStatus, Reason: "ExternalReason"}
						instance.Status.Conditions = append(instance.Status.Conditions, *gate)
					}
					for _, podStatus := range []corev1.ConditionStatus{corev1.ConditionUnknown, corev1.ConditionFalse, corev1.ConditionTrue} {
						pods := &workloadsv1alpha2.RoleInstanceCondition{Status: podStatus}
						ready := getInstanceReadyCondition(instance, pods, gate)
						want := gateStatus == corev1.ConditionTrue && (policy == workloadsv1alpha2.RoleInstanceReadyPolicyTypeNone || podStatus == corev1.ConditionTrue)
						require.Equal(t, want, ready.Status == corev1.ConditionTrue)
						require.NotEmpty(t, ready.Reason)
						if gate != nil {
							require.Equal(t, "ExternalReason", gate.Reason)
						}
					}
				})
			}
		}
	}
}

func TestInstanceConditionStabilityAndRestartClear(t *testing.T) {
	instance := &workloadsv1alpha2.RoleInstance{}
	instance.Spec.ReadyPolicy = workloadsv1alpha2.RoleInstanceReadyPolicyTypeNone
	oldTime := metav1.NewTime(time.Now().Add(-time.Hour))
	instance.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
		{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionTrue, Reason: "OldReady", LastTransitionTime: oldTime},
		{Type: workloadsv1alpha2.RoleInstanceRestarting, Status: corev1.ConditionTrue, Reason: workloadsv1alpha2.RoleInstanceReasonRestartPolicyTriggered, LastTransitionTime: oldTime},
		{Type: workloadsv1alpha2.RoleInstanceFailedScale, Status: corev1.ConditionTrue},
		{Type: workloadsv1alpha2.RoleInstanceFailedUpdate, Status: corev1.ConditionTrue},
		{Type: "RollingUpdateInProgress", Status: corev1.ConditionTrue},
		{Type: "External", Status: corev1.ConditionUnknown, Reason: "ExternalReason", LastTransitionTime: oldTime},
	}
	updater := &realStatusUpdater{}
	status := &workloadsv1alpha2.RoleInstanceStatus{}
	active := instance.Status.Conditions[1]
	updater.setInstanceConditions(instance, status, []workloadsv1alpha2.RoleInstanceCondition{active})
	got := instance.DeepCopy()
	got.Status = *status
	require.Equal(t, corev1.ConditionTrue, inplaceutil.GetRoleInstanceCondition(got, workloadsv1alpha2.RoleInstanceRestarting).Status)
	updater.setInstanceConditions(instance, status, nil)
	got.Status = *status
	require.Len(t, status.Conditions, 3)
	require.Equal(t, oldTime, inplaceutil.GetRoleInstanceCondition(got, workloadsv1alpha2.RoleInstanceReady).LastTransitionTime)
	restarting := inplaceutil.GetRoleInstanceCondition(got, workloadsv1alpha2.RoleInstanceRestarting)
	require.Equal(t, corev1.ConditionFalse, restarting.Status)
	require.Equal(t, workloadsv1alpha2.RoleInstanceReasonNoRestartInProgress, restarting.Reason)
	before := status.DeepCopy()
	updater.setInstanceConditions(got, status, nil)
	require.Equal(t, before, status)
}

func TestRestartCompletionWithCustomGateNotReady(t *testing.T) {
	for _, gateStatus := range []corev1.ConditionStatus{"", corev1.ConditionUnknown, corev1.ConditionFalse} {
		t.Run(string(gateStatus), func(t *testing.T) {
			instance := &workloadsv1alpha2.RoleInstance{ObjectMeta: metav1.ObjectMeta{Name: "restart", Namespace: "default"}}
			instance.Spec.ReadinessGates = []workloadsv1alpha2.RoleInstanceReadinessGate{{ConditionType: "example.com/ready"}}
			instance.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{{Type: workloadsv1alpha2.RoleInstanceRestarting, Status: corev1.ConditionTrue, Reason: workloadsv1alpha2.RoleInstanceReasonRestartPolicyTriggered}}
			if gateStatus != "" {
				instance.Status.Conditions = append(instance.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{Type: "example.com/ready", Status: gateStatus, Reason: "ExternalReason"})
			}
			scheme := runtime.NewScheme()
			require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(instance).WithObjects(instance).Build()
			updater := &realStatusUpdater{Client: c}
			status := &workloadsv1alpha2.RoleInstanceStatus{}
			updater.setInstanceConditions(instance, status, instance.Status.Conditions[:1])
			got := instance.DeepCopy()
			got.Status = *status
			require.Equal(t, corev1.ConditionTrue, inplaceutil.GetRoleInstanceCondition(got, workloadsv1alpha2.RoleInstanceRestarting).Status, "活动重启继续保留")
			updater.setInstanceConditions(instance, status, nil)
			require.NoError(t, updater.updateStatus(context.Background(), instance, status))
			require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), got))
			require.Equal(t, corev1.ConditionFalse, inplaceutil.GetRoleInstanceCondition(got, workloadsv1alpha2.RoleInstanceReady).Status)
			require.Equal(t, corev1.ConditionFalse, inplaceutil.GetRoleInstanceCondition(got, workloadsv1alpha2.RoleInstanceRestarting).Status)
			if gateStatus != "" {
				require.Equal(t, "ExternalReason", inplaceutil.GetRoleInstanceCondition(got, "example.com/ready").Reason)
			}
		})
	}
}

func TestInstanceStatusConflictPreservesLatestGates(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	instance := &workloadsv1alpha2.RoleInstance{ObjectMeta: metav1.ObjectMeta{Name: "instance", Namespace: "default"}}
	instance.Spec.ReadyPolicy = workloadsv1alpha2.RoleInstanceReadyPolicyTypeNone
	instance.Spec.ReadinessGates = []workloadsv1alpha2.RoleInstanceReadinessGate{{ConditionType: workloadsv1alpha2.RoleInstanceCustomReady}}
	inplaceutil.SetRoleInstanceCondition(instance, workloadsv1alpha2.RoleInstanceCondition{Type: workloadsv1alpha2.RoleInstanceCustomReady, Status: corev1.ConditionTrue, Reason: "Old"})
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(instance).WithObjects(instance).Build()
	writes := 0
	c := interceptor.NewClient(base, interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, cli client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
		writes++
		if writes == 1 {
			live := &workloadsv1alpha2.RoleInstance{}
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(instance), live))
			inplaceutil.SetRoleInstanceCondition(live, workloadsv1alpha2.RoleInstanceCondition{Type: workloadsv1alpha2.RoleInstanceCustomReady, Status: corev1.ConditionUnknown, Reason: "ConcurrentExternalReason"})
			inplaceutil.SetRoleInstanceCondition(live, workloadsv1alpha2.RoleInstanceCondition{Type: "External", Status: corev1.ConditionTrue, Reason: "Preserve"})
			require.NoError(t, cli.Status().Update(ctx, live))
			return apierrors.NewConflict(schema.GroupResource{Resource: "roleinstances"}, instance.Name, nil)
		}
		return cli.SubResource(sub).Update(ctx, obj, opts...)
	}})
	updater := &realStatusUpdater{Client: c}
	status := &workloadsv1alpha2.RoleInstanceStatus{}
	updater.setInstanceConditions(instance, status, nil)
	require.NoError(t, updater.updateStatus(context.Background(), instance, status))
	require.Equal(t, 2, writes)
	got := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, base.Get(context.Background(), client.ObjectKeyFromObject(instance), got))
	require.Len(t, got.Status.Conditions, 4)
	require.Equal(t, "ConcurrentExternalReason", inplaceutil.GetRoleInstanceCondition(got, workloadsv1alpha2.RoleInstanceCustomReady).Reason)
	require.Equal(t, corev1.ConditionFalse, inplaceutil.GetRoleInstanceCondition(got, workloadsv1alpha2.RoleInstanceReady).Status)
	require.Equal(t, got.Status.Conditions, status.Conditions)
}

func TestRestartCompletionConflictPreservesInPlaceGate(t *testing.T) {
	for _, gateStatus := range []corev1.ConditionStatus{"", corev1.ConditionUnknown, corev1.ConditionFalse} {
		t.Run(string(gateStatus), func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
			instance := &workloadsv1alpha2.RoleInstance{ObjectMeta: metav1.ObjectMeta{Name: "restart-conflict", Namespace: "default"}}
			instance.Spec.ReadinessGates = []workloadsv1alpha2.RoleInstanceReadinessGate{
				{ConditionType: "example.com/ready"}, {ConditionType: workloadsv1alpha2.RoleInstanceInPlaceUpdateReady},
			}
			inplaceutil.SetRoleInstanceCondition(instance, workloadsv1alpha2.RoleInstanceCondition{Type: workloadsv1alpha2.RoleInstanceRestarting, Status: corev1.ConditionTrue, Reason: workloadsv1alpha2.RoleInstanceReasonRestartPolicyTriggered})
			inplaceutil.SetRoleInstanceCondition(instance, workloadsv1alpha2.RoleInstanceCondition{Type: workloadsv1alpha2.RoleInstanceInPlaceUpdateReady, Status: corev1.ConditionTrue})
			base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(instance).WithObjects(instance).Build()
			writes := 0
			c := interceptor.NewClient(base, interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, cli client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				writes++
				if writes == 1 {
					live := &workloadsv1alpha2.RoleInstance{}
					require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(instance), live))
					live.Status.Conditions = live.Status.Conditions[:1]
					if gateStatus != "" {
						inplaceutil.SetRoleInstanceCondition(live, workloadsv1alpha2.RoleInstanceCondition{Type: workloadsv1alpha2.RoleInstanceInPlaceUpdateReady, Status: gateStatus, Reason: "ConcurrentInPlaceUpdate"})
					}
					require.NoError(t, cli.Status().Update(ctx, live))
					return apierrors.NewConflict(schema.GroupResource{Resource: "roleinstances"}, instance.Name, nil)
				}
				return cli.SubResource(sub).Update(ctx, obj, opts...)
			}})
			updater := &realStatusUpdater{Client: c}
			status := &workloadsv1alpha2.RoleInstanceStatus{}
			updater.setInstanceConditions(instance, status, nil)
			computed := instance.DeepCopy()
			computed.Status = *status
			require.Equal(t, corev1.ConditionFalse, inplaceutil.GetRoleInstanceCondition(computed, workloadsv1alpha2.RoleInstanceRestarting).Status)
			require.NoError(t, updater.updateStatus(context.Background(), instance, status))
			require.Equal(t, 2, writes)
			got := &workloadsv1alpha2.RoleInstance{}
			require.NoError(t, base.Get(context.Background(), client.ObjectKeyFromObject(instance), got))
			require.Equal(t, corev1.ConditionFalse, inplaceutil.GetRoleInstanceCondition(got, workloadsv1alpha2.RoleInstanceReady).Status)
			require.Equal(t, corev1.ConditionTrue, inplaceutil.GetRoleInstanceCondition(got, workloadsv1alpha2.RoleInstanceRestarting).Status)
			ready, _ := inplaceutil.GetRoleInstanceControlReadiness(got)
			require.False(t, ready, "冲突后不能绕过最新的原地更新 gate")
			if gateStatus != "" {
				require.Equal(t, "ConcurrentInPlaceUpdate", inplaceutil.GetRoleInstanceCondition(got, workloadsv1alpha2.RoleInstanceInPlaceUpdateReady).Reason)
			}
			require.Equal(t, got.Status.Conditions, status.Conditions)
		})
	}
}

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
