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

package instance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func TestConditionTypeMergeAndTransitionTime(t *testing.T) {
	oldTime := metav1.NewTime(time.Now().Add(-time.Hour))
	c := workloadsv1alpha2.RoleInstanceCondition{Type: workloadsv1alpha2.RoleInstanceInPlaceUpdateReady, Status: corev1.ConditionFalse, Reason: "Old", LastTransitionTime: oldTime}
	instance := &workloadsv1alpha2.RoleInstance{Status: workloadsv1alpha2.RoleInstanceStatus{Conditions: []workloadsv1alpha2.RoleInstanceCondition{c, c}}}
	c.Reason, c.Message = "New", "changed"
	c.LastTransitionTime = metav1.Now()
	SetRoleInstanceCondition(instance, c)
	require.Len(t, instance.Status.Conditions, 1)
	require.Equal(t, oldTime, instance.Status.Conditions[0].LastTransitionTime)
	require.Equal(t, "New", instance.Status.Conditions[0].Reason)
	c.Status, c.LastTransitionTime = corev1.ConditionTrue, metav1.Time{}
	SetRoleInstanceCondition(instance, c)
	require.True(t, instance.Status.Conditions[0].LastTransitionTime.After(oldTime.Time))
}

func TestIsRoleInstanceReady(t *testing.T) {
	testCases := []struct {
		name     string
		instance *workloadsv1alpha2.RoleInstance
		expected bool
	}{
		{
			name: "Instance is ready",
			instance: &workloadsv1alpha2.RoleInstance{
				Status: workloadsv1alpha2.RoleInstanceStatus{
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "Instance is not ready, ready condition is null",
			instance: &workloadsv1alpha2.RoleInstance{
				Status: workloadsv1alpha2.RoleInstanceStatus{
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{},
				},
			},
			expected: false,
		},
		{
			name: "Instance is not ready, ready condition is false",
			instance: &workloadsv1alpha2.RoleInstance{
				Status: workloadsv1alpha2.RoleInstanceStatus{
					Conditions: []workloadsv1alpha2.RoleInstanceCondition{
						{
							Type:   workloadsv1alpha2.RoleInstanceReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ready := IsRoleInstanceReady(tc.instance)
			if ready != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, ready)
			}
		})
	}
}

func TestGetRoleInstanceControlReadiness(t *testing.T) {
	created := metav1.NewTime(time.Now().Add(-3 * time.Minute))
	podsReadyAt := metav1.NewTime(time.Now().Add(-2 * time.Minute))
	inplaceReadyAt := metav1.NewTime(time.Now().Add(-time.Minute))
	displayChangedAt := metav1.Now()
	for _, gateStatus := range []corev1.ConditionStatus{"", corev1.ConditionUnknown, corev1.ConditionFalse, corev1.ConditionTrue} {
		t.Run("外部gate/"+string(gateStatus), func(t *testing.T) {
			instance := &workloadsv1alpha2.RoleInstance{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: created}}
			instance.Spec.ReadinessGates = []workloadsv1alpha2.RoleInstanceReadinessGate{{ConditionType: "example.com/ready"}}
			instance.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{
				{Type: workloadsv1alpha2.RoleInstanceReady, Status: corev1.ConditionFalse, LastTransitionTime: displayChangedAt},
				{Type: workloadsv1alpha2.RoleInstanceAllPodsReady, Status: corev1.ConditionTrue, LastTransitionTime: podsReadyAt},
			}
			if gateStatus != "" {
				instance.Status.Conditions = append(instance.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{Type: "example.com/ready", Status: gateStatus})
			}
			assertReadiness := func(obj *workloadsv1alpha2.RoleInstance, want bool, since metav1.Time) {
				t.Helper()
				before := obj.DeepCopy()
				ready, gotSince := GetRoleInstanceControlReadiness(obj)
				require.Equal(t, want, ready)
				if want {
					require.Equal(t, since, gotSince)
				}
				require.Equal(t, before, obj, "操作判断不能修改展示状态")
			}
			assertReadiness(instance, true, podsReadyAt)
			require.False(t, IsRoleInstanceReady(instance), "展示读取仍以聚合 Ready 为准")

			for _, status := range []corev1.ConditionStatus{"", corev1.ConditionFalse, corev1.ConditionUnknown} {
				unready := instance.DeepCopy()
				if status == "" {
					unready.Status.Conditions = unready.Status.Conditions[:1]
				} else {
					GetRoleInstanceCondition(unready, workloadsv1alpha2.RoleInstanceAllPodsReady).Status = status
				}
				assertReadiness(unready, false, metav1.Time{})
			}
			unreported := instance.DeepCopy()
			unreported.Status.Conditions = unreported.Status.Conditions[1:]
			assertReadiness(unreported, false, metav1.Time{})

			for _, none := range []bool{false, true} {
				for _, status := range []corev1.ConditionStatus{"", corev1.ConditionFalse, corev1.ConditionUnknown, corev1.ConditionTrue} {
					withInplace := instance.DeepCopy()
					withInplace.Spec.ReadinessGates = append(withInplace.Spec.ReadinessGates, workloadsv1alpha2.RoleInstanceReadinessGate{ConditionType: workloadsv1alpha2.RoleInstanceInPlaceUpdateReady})
					if none {
						withInplace.Spec.ReadyPolicy = workloadsv1alpha2.RoleInstanceReadyPolicyTypeNone
						GetRoleInstanceCondition(withInplace, workloadsv1alpha2.RoleInstanceAllPodsReady).Status = corev1.ConditionFalse
					}
					if status != "" {
						withInplace.Status.Conditions = append(withInplace.Status.Conditions, workloadsv1alpha2.RoleInstanceCondition{Type: workloadsv1alpha2.RoleInstanceInPlaceUpdateReady, Status: status, LastTransitionTime: inplaceReadyAt})
					}
					assertReadiness(withInplace, status == corev1.ConditionTrue, inplaceReadyAt)
					// 旧稀疏对象回退 Ready 时，仍不能跳过已声明的原地更新 gate。
					withInplace.Status.Conditions = append(withInplace.Status.Conditions[:1], withInplace.Status.Conditions[2:]...)
					withInplace.Status.Conditions[0].Status = corev1.ConditionTrue
					ready, _ := GetRoleInstanceControlReadiness(withInplace)
					require.Equal(t, status == corev1.ConditionTrue, ready)
				}
			}

			none := instance.DeepCopy()
			none.Spec.ReadyPolicy = workloadsv1alpha2.RoleInstanceReadyPolicyTypeNone
			none.Status.Conditions = none.Status.Conditions[:1]
			assertReadiness(none, true, created)
			none.Status.Conditions[0].Status = corev1.ConditionTrue
			none.Status.Conditions[0].LastTransitionTime = metav1.NewTime(time.Now().Add(time.Minute))
			assertReadiness(none, true, created)
		})
	}

	for _, status := range []corev1.ConditionStatus{corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionUnknown} {
		legacy := &workloadsv1alpha2.RoleInstance{Status: workloadsv1alpha2.RoleInstanceStatus{Conditions: []workloadsv1alpha2.RoleInstanceCondition{
			{Type: workloadsv1alpha2.RoleInstanceReady, Status: status, LastTransitionTime: podsReadyAt},
		}}}
		ready, since := GetRoleInstanceControlReadiness(legacy)
		require.Equal(t, status == corev1.ConditionTrue, ready)
		require.Equal(t, podsReadyAt, since)
		legacy.Spec.ReadinessGates = []workloadsv1alpha2.RoleInstanceReadinessGate{{ConditionType: "example.com/ready"}}
		ready, since = GetRoleInstanceControlReadiness(legacy)
		require.Equal(t, status == corev1.ConditionTrue, ready)
		require.Equal(t, podsReadyAt, since)
	}
}

func TestIsMatchComponent(t *testing.T) {
	testCases := []struct {
		name          string
		podLabels     map[string]string
		componentName string
		expected      bool
	}{
		{
			name: "Pod matches component",
			podLabels: map[string]string{
				constants.ComponentNameLabelKey: "web",
			},
			componentName: "web",
			expected:      true,
		},
		{
			name: "Pod does not match component",
			podLabels: map[string]string{
				constants.ComponentNameLabelKey: "db",
			},
			componentName: "web",
			expected:      false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: tc.podLabels,
				},
			}
			match := MatchComponent(pod, tc.componentName)
			if match != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, match)
			}
		})
	}
}

func TestGetComponentSize(t *testing.T) {
	testCases := []struct {
		name      string
		component workloadsv1alpha2.RoleInstanceComponent
		expected  int32
	}{
		{
			name: "Component with replicas",
			component: workloadsv1alpha2.RoleInstanceComponent{
				Size: func() *int32 { i := int32(3); return &i }(),
			},
			expected: 3,
		},
		{
			name: "Component with default replicas",
			component: workloadsv1alpha2.RoleInstanceComponent{
				Size: nil,
			},
			expected: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			size := GetComponentSize(&tc.component)
			if size != tc.expected {
				t.Errorf("expected %d, got %d", tc.expected, size)
			}
		})
	}
}

func TestInjectIdentityLabelsIntoComponents(t *testing.T) {
	instanceWith := func(instanceLabels, templateLabels map[string]string) *workloadsv1alpha2.RoleInstance {
		return &workloadsv1alpha2.RoleInstance{
			ObjectMeta: metav1.ObjectMeta{Name: "set-0", Labels: instanceLabels},
			Spec: workloadsv1alpha2.RoleInstanceSpec{
				Components: []workloadsv1alpha2.RoleInstanceComponent{
					{Name: "worker", Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: templateLabels},
					}},
				},
			},
		}
	}

	testCases := []struct {
		name     string
		instance *workloadsv1alpha2.RoleInstance
		expected map[string]string
	}{
		{
			name: "identity is copied onto a template that has none",
			instance: instanceWith(map[string]string{
				"statefulset.kubernetes.io/pod-name": "set-0",
				constants.RoleInstanceIndexLabelKey:  "0",
				constants.RoleNameLabelKey:           "worker",
			}, nil),
			expected: map[string]string{
				"statefulset.kubernetes.io/pod-name": "set-0",
				constants.RoleInstanceIndexLabelKey:  "0",
			},
		},
		{
			name: "another ordinal's identity is overwritten, not merged",
			instance: instanceWith(map[string]string{
				"statefulset.kubernetes.io/pod-name": "set-0",
				constants.RoleInstanceIndexLabelKey:  "0",
			}, map[string]string{
				"statefulset.kubernetes.io/pod-name": "set-1",
				constants.RoleInstanceIndexLabelKey:  "1",
				constants.RoleNameLabelKey:           "worker",
			}),
			expected: map[string]string{
				"statefulset.kubernetes.io/pod-name": "set-0",
				constants.RoleInstanceIndexLabelKey:  "0",
				constants.RoleNameLabelKey:           "worker",
			},
		},
		{
			name:     "a stateless instance has no identity to copy",
			instance: instanceWith(map[string]string{constants.RoleNameLabelKey: "worker"}, nil),
			expected: nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			InjectIdentityLabelsIntoComponents(tc.instance)
			got := tc.instance.Spec.Components[0].Template.Labels
			if len(got) != len(tc.expected) {
				t.Fatalf("expected %v, got %v", tc.expected, got)
			}
			for key, want := range tc.expected {
				if got[key] != want {
					t.Errorf("label %s: expected %q, got %q", key, want, got[key])
				}
			}
		})
	}
}
