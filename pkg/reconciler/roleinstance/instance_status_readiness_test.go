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

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instanceutils "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

func TestInstanceReadyRequiresActiveOwnedPods(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*v1.Pod)
		missing   bool
		wantReady v1.ConditionStatus
	}{
		{
			name:      "all expected pods are ready and controlled by the instance",
			wantReady: v1.ConditionTrue,
		},
		{
			name: "same-name pod belongs to the deleted instance",
			mutate: func(pod *v1.Pod) {
				pod.OwnerReferences[0].UID = "old-instance"
			},
			wantReady: v1.ConditionFalse,
		},
		{
			name: "pod has no owner",
			mutate: func(pod *v1.Pod) {
				pod.OwnerReferences = nil
			},
			wantReady: v1.ConditionFalse,
		},
		{
			name: "instance is not the controller owner",
			mutate: func(pod *v1.Pod) {
				pod.OwnerReferences[0].Controller = ptr.To(false)
			},
			wantReady: v1.ConditionFalse,
		},
		{
			name: "owned pod is terminating but still reports ready",
			mutate: func(pod *v1.Pod) {
				now := metav1.Now()
				pod.DeletionTimestamp = &now
				pod.Finalizers = []string{"test-hold"}
			},
			wantReady: v1.ConditionFalse,
		},
		{
			name: "owned pod is not ready",
			mutate: func(pod *v1.Pod) {
				pod.Status.Conditions[0].Status = v1.ConditionFalse
			},
			wantReady: v1.ConditionFalse,
		},
		{
			name:      "expected pod is missing",
			missing:   true,
			wantReady: v1.ConditionFalse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := newReadinessTestInstance()
			// Keep the first component healthy: one invalid dependent in another
			// component must prevent the whole instance from becoming Ready.
			firstPod := newReadinessTestPod(instance, "router")
			secondPod := newReadinessTestPod(instance, "sidecar")
			if tt.mutate != nil {
				tt.mutate(secondPod)
			}
			objects := []client.Object{firstPod}
			if !tt.missing {
				objects = append(objects, secondPod)
			}
			c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).WithObjects(objects...).Build()
			updater := &realStatusUpdater{Client: c}
			status := &workloadsv1alpha2.RoleInstanceStatus{}
			updater.setInstanceConditions(instance, status, nil)

			assertReadinessTestConditions(t, status, tt.wantReady)
		})
	}
}

func TestRecreatedInstanceReadyAfterOldPodIsReplaced(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Spec.Components = instance.Spec.Components[:1]
	oldPod := newReadinessTestPod(instance, "router")
	oldPod.OwnerReferences[0].UID = "old-instance"
	c := fake.NewClientBuilder().WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}, &v1.Pod{}).
		WithObjects(instance, oldPod).Build()
	updater := newStatusUpdater(c)

	checkReady := func(want v1.ConditionStatus) {
		t.Helper()
		current := &workloadsv1alpha2.RoleInstance{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), current))
		status := &workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: current.Generation}
		// Readiness must not find an old owner's Pod through the name-based
		// lookup when the owner-UID-filtered reconciliation input is empty.
		require.NoError(t, updater.UpdateInstanceStatus(ctx, current, status, nil))
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(instance), current))
		assertReadinessTestConditions(t, &current.Status, want)
	}

	checkReady(v1.ConditionFalse)
	// Simulate GC completing before the new same-name Pod is created.
	require.NoError(t, c.Delete(ctx, oldPod))
	checkReady(v1.ConditionFalse)
	newPod := newReadinessTestPod(instance, "router")
	newPod.Status.Phase = v1.PodPending
	newPod.Status.Conditions[0].Status = v1.ConditionFalse
	require.NoError(t, c.Create(ctx, newPod))
	checkReady(v1.ConditionFalse)
	newPod.Status.Phase = v1.PodRunning
	newPod.Status.Conditions[0].Status = v1.ConditionTrue
	require.NoError(t, c.Status().Update(ctx, newPod))
	checkReady(v1.ConditionTrue)
	// Repeated status updates must preserve readiness once the replacement
	// really has an active, ready Pod controlled by its own UID.
	checkReady(v1.ConditionTrue)
}

func readinessTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, v1.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	return scheme
}

func newReadinessTestInstance() *workloadsv1alpha2.RoleInstance {
	return &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name: "router-0", Namespace: "default", UID: "new-instance", Generation: 1,
		},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			ReadyPolicy: workloadsv1alpha2.RoleInstanceReadyOnAllPodReady,
			ReadinessGates: []workloadsv1alpha2.RoleInstanceReadinessGate{{
				ConditionType: workloadsv1alpha2.RoleInstanceInPlaceUpdateReady,
			}},
			Components: []workloadsv1alpha2.RoleInstanceComponent{
				{Name: "router", Size: ptr.To(int32(1))},
				{Name: "sidecar", Size: ptr.To(int32(1))},
			},
		},
		Status: workloadsv1alpha2.RoleInstanceStatus{
			Conditions: []workloadsv1alpha2.RoleInstanceCondition{{
				Type: workloadsv1alpha2.RoleInstanceInPlaceUpdateReady, Status: v1.ConditionTrue,
			}},
		},
	}
}

func newReadinessTestPod(instance *workloadsv1alpha2.RoleInstance, component string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceutils.FormatComponentPodName(instance.Name, component, 0, instance.GetRoleTemplateType()),
			Namespace: instance.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: workloadsv1alpha2.SchemeGroupVersion.String(), Kind: "RoleInstance",
				Name: instance.Name, UID: instance.UID, Controller: ptr.To(true),
			}},
		},
		Status: v1.PodStatus{
			Phase:      v1.PodRunning,
			Conditions: []v1.PodCondition{{Type: v1.PodReady, Status: v1.ConditionTrue}},
		},
	}
}

func assertReadinessTestConditions(t *testing.T, status *workloadsv1alpha2.RoleInstanceStatus, want v1.ConditionStatus) {
	t.Helper()
	for _, conditionType := range []workloadsv1alpha2.RoleInstanceConditionType{
		workloadsv1alpha2.RoleInstanceAllPodsReady, workloadsv1alpha2.RoleInstanceReady,
	} {
		found := false
		for _, condition := range status.Conditions {
			if condition.Type == conditionType {
				require.Equal(t, want, condition.Status, "condition %s", conditionType)
				found = true
			}
		}
		require.True(t, found, "missing condition %s", conditionType)
	}
}
