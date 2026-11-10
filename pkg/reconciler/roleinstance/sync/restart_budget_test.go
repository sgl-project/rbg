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

package sync

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

func budgetFixture(t *testing.T, limit *int32) (*realControl, *workloadsv1alpha2.RoleInstance, []*v1.Pod) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, v1.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "budget-0", Namespace: "default", UID: "instance-uid", Generation: 1},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation, MaxGroupRestarts: limit},
			Components:          []workloadsv1alpha2.RoleInstanceComponent{{Name: "worker", Size: ptr.To[int32](2), Template: v1.PodTemplateSpec{Spec: v1.PodSpec{Containers: []v1.Container{{Name: "worker", Image: "v1"}}}}}},
		},
		Status: workloadsv1alpha2.RoleInstanceStatus{ObservedGeneration: 1},
	}
	pods := make([]*v1.Pod, 2)
	objects := make([]client.Object, 0, 1+len(pods))
	objects = append(objects, instance)
	for i := range pods {
		pods[i] = &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("budget-0-worker-%d", i), Namespace: instance.Namespace, UID: types.UID(fmt.Sprintf("pod-%d", i)),
				Labels:          map[string]string{constants.RoleInstanceNameLabelKey: instance.Name, constants.ComponentNameLabelKey: "worker", constants.ComponentIDLabelKey: fmt.Sprint(i)},
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(instance, workloadsv1alpha2.SchemeGroupVersion.WithKind("RoleInstance"))}},
			Status: v1.PodStatus{Phase: v1.PodRunning},
		}
		objects = append(objects, pods[i])
	}
	pods[0].Status.Phase = v1.PodFailed
	cli := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(instance, &v1.Pod{}).WithObjects(objects...).Build()
	control := &realControl{Client: cli, apiReader: cli, recorder: record.NewFakeRecorder(100)}
	return control, instance, pods
}

func liveBudgetInstance(t *testing.T, c *realControl, instance *workloadsv1alpha2.RoleInstance) *workloadsv1alpha2.RoleInstance {
	t.Helper()
	live := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.apiReader.Get(context.Background(), client.ObjectKeyFromObject(instance), live))
	return live
}

// acceptBudgetRestart exercises the shared budget+retirement transaction without
// driving Pod deletion; real Scale follows this with the durable UID drain.
func (c *realControl) acceptBudgetRestart(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) ([]*v1.Pod, bool, error) {
	fresh, err := c.readBudgetInstance(ctx, instance)
	if err != nil {
		return nil, false, err
	}
	if fresh.DeletionTimestamp != nil || fresh.Status.RestartBudgetExhausted || len(fresh.Status.RetiringPods) > 0 {
		return nil, false, nil
	}
	pods, err := c.listBudgetPods(ctx, fresh)
	if err != nil {
		return nil, false, err
	}
	accepted, err := c.shouldRecreateInstanceGuarded(ctx, fresh, pods, fresh.Status.InPlaceUpdateContainerBaselines, fresh.Status.CurrentRevision, fresh.Status.UpdateRevision)
	instance.Status = *fresh.Status.DeepCopy()
	return pods, accepted, err
}

func TestRestartBudgetUnsetAndHealthyZero(t *testing.T) {
	ctx := context.Background()
	t.Run("unset needs no reader and does not block scale", func(t *testing.T) {
		c, instance, _ := budgetFixture(t, nil)
		c.apiReader = nil
		blocked, err := c.reconcileRestartBudget(ctx, instance)
		require.NoError(t, err)
		require.False(t, blocked)
	})
	t.Run("healthy zero does not exhaust initial creation", func(t *testing.T) {
		c, instance, pods := budgetFixture(t, ptr.To[int32](0))
		pods[0].Status.Phase = v1.PodRunning
		require.NoError(t, c.Status().Update(ctx, pods[0]))
		blocked, err := c.reconcileRestartBudget(ctx, instance)
		require.NoError(t, err)
		require.False(t, blocked)
		live := liveBudgetInstance(t, c, instance)
		require.False(t, live.Status.RestartBudgetExhausted)
		require.Zero(t, live.Status.GroupRestartCount)
	})
}

func TestRestartBudgetGuardPersistsExhaustionBeforeTerminating(t *testing.T) {
	ctx := context.Background()
	c, instance, pods := budgetFixture(t, ptr.To[int32](0))
	// With an existing cleanup owner and no recovery annotation, exhaustion
	// must still patch the marker; metadata writes cannot mask a bad baseline.
	require.NoError(t, c.ensureBudgetOwner(ctx, instance, false))
	before := instance.DeepCopy()
	require.False(t, prepareGroupRestartBudget(instance))
	require.Equal(t, before.Status, instance.Status, "denial must not mutate the status patch baseline")
	_, accepted, err := c.acceptBudgetRestart(ctx, instance)
	require.NoError(t, err)
	require.False(t, accepted)
	live := liveBudgetInstance(t, c, instance)
	require.True(t, live.Status.RestartBudgetExhausted, "suppression must be durable before Pod teardown")
	require.Zero(t, live.Status.GroupRestartCount)
	for _, pod := range pods {
		actual := &v1.Pod{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), actual))
		require.NotNil(t, actual.DeletionTimestamp)
		require.Contains(t, actual.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
	}
}

func TestRestartBudgetExactCountAndNoQuietReset(t *testing.T) {
	ctx := context.Background()
	c, instance, _ := budgetFixture(t, ptr.To[int32](2))
	for round := int32(1); round <= 2; round++ {
		instance = liveBudgetInstance(t, c, instance)
		instance.Status.Conditions = nil // prior group completed its restart
		instance.Status.RetiringPods = nil
		old := metav1.NewTime(time.Now().Add(-time.Hour))
		instance.Status.LastRestartTime = &old
		instance.Status.RestartCount = 9
		require.NoError(t, c.Status().Update(ctx, instance))
		blocked, err := c.reconcileRestartBudget(ctx, instance)
		require.NoError(t, err)
		require.False(t, blocked)
		pods, accepted, err := c.acceptBudgetRestart(ctx, instance)
		require.NoError(t, err)
		require.True(t, accepted)
		require.Len(t, pods, 2)
		live := liveBudgetInstance(t, c, instance)
		require.Equal(t, round, live.Status.GroupRestartCount)
		require.EqualValues(t, 1, live.Status.RestartCount, "quiet-period backoff reset must not reset durable count")
		_, accepted, err = c.acceptBudgetRestart(ctx, instance)
		require.NoError(t, err)
		require.False(t, accepted, "repeated events in accepted restart consume nothing")
		require.Equal(t, round, liveBudgetInstance(t, c, instance).Status.GroupRestartCount)
	}
	instance = liveBudgetInstance(t, c, instance)
	instance.Status.Conditions = nil
	instance.Status.RetiringPods = nil
	now := metav1.Now()
	instance.Status.LastRestartTime = &now // would normally require backoff
	require.NoError(t, c.Status().Update(ctx, instance))
	blocked, err := c.reconcileRestartBudget(ctx, instance)
	require.NoError(t, err)
	require.True(t, blocked)
	live := liveBudgetInstance(t, c, instance)
	require.True(t, live.Status.RestartBudgetExhausted, "exhaustion must precede backoff")
	require.EqualValues(t, 2, live.Status.GroupRestartCount)
}

func TestRestartBudgetExhaustionRetainsCompleteGroupAndRecovery(t *testing.T) {
	ctx := context.Background()
	c, instance, pods := budgetFixture(t, ptr.To[int32](0))
	instance = liveBudgetInstance(t, c, instance)
	instance.Annotations = map[string]string{constants.RoleInstanceRecoverAnnotationKey: "true"}
	require.NoError(t, c.Client.Update(ctx, instance))
	// Auxiliary/ignored and removed components still belong to the COMPLETE group.
	pods[1].Annotations = map[string]string{constants.RestartTriggerPolicyAnnotationKey: constants.RestartTriggerPolicyIgnore}
	pods[1].Labels[constants.ComponentNameLabelKey] = "removed-component"
	require.NoError(t, c.Client.Update(ctx, pods[1]))
	// A same-name-pattern foreign Pod must remain untouched.
	foreign := pods[1].DeepCopy()
	foreign.Name = "foreign"
	foreign.UID = "foreign"
	foreign.ResourceVersion = ""
	foreign.OwnerReferences[0].UID = "old-owner"
	require.NoError(t, c.Create(ctx, foreign))
	for i := 0; i < 3; i++ {
		instance = liveBudgetInstance(t, c, instance)
		blocked, err := c.reconcileRestartBudget(ctx, instance)
		require.NoError(t, err)
		require.True(t, blocked)
	}
	live := liveBudgetInstance(t, c, instance)
	require.True(t, live.Status.RestartBudgetExhausted)
	require.Empty(t, live.Annotations[constants.RoleInstanceRecoverAnnotationKey], "pre-exhaustion annotation must be invalidated")
	require.Zero(t, live.Status.GroupRestartCount)
	require.Contains(t, live.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
	for _, p := range pods {
		got := &v1.Pod{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(p), got))
		require.NotNil(t, got.DeletionTimestamp)
		require.Contains(t, got.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
		require.Equal(t, p.Status.Phase, got.Status.Phase, "controller must never synthesize a terminal phase")
	}
	gotForeign := &v1.Pod{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(foreign), gotForeign))
	require.Nil(t, gotForeign.DeletionTimestamp)
	for _, change := range []string{"increase", "unset", "policy", "ready"} {
		live = liveBudgetInstance(t, c, instance)
		switch change {
		case "increase":
			live.Spec.RestartPolicyConfig.MaxGroupRestarts = ptr.To[int32](100)
		case "unset":
			live.Spec.RestartPolicyConfig.MaxGroupRestarts = nil
		case "policy":
			live.Spec.RestartPolicyConfig.Type = workloadsv1alpha2.RestartPolicyNone
		case "ready":
			live.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{{Type: workloadsv1alpha2.RoleInstanceReady, Status: v1.ConditionTrue}}
		}
		if change == "ready" {
			require.NoError(t, c.Status().Update(ctx, live))
		} else {
			require.NoError(t, c.Client.Update(ctx, live))
		}
		blocked, err := c.reconcileRestartBudget(ctx, live)
		require.NoError(t, err)
		require.True(t, blocked, change)
		require.True(t, liveBudgetInstance(t, c, instance).Status.RestartBudgetExhausted)
	}
	// A new controller process has no cache and must still honor the persistent marker.
	live = liveBudgetInstance(t, c, instance)
	live.Annotations = map[string]string{constants.RoleInstanceRecoverAnnotationKey: "true"}
	require.NoError(t, c.Client.Update(ctx, live))
	blocked, err := c.reconcileRestartBudget(ctx, live)
	require.NoError(t, err)
	require.True(t, blocked, "cleanup takes a separate pass before creation")
	live = liveBudgetInstance(t, c, instance)
	require.False(t, live.Status.RestartBudgetExhausted)
	require.Zero(t, live.Status.GroupRestartCount)
	require.False(t, slices.ContainsFunc(live.Status.Conditions, func(cond workloadsv1alpha2.RoleInstanceCondition) bool {
		return cond.Type == workloadsv1alpha2.RoleInstanceDegraded
	}))
	for _, p := range pods {
		require.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(p), &v1.Pod{})))
	}
	blocked, err = c.reconcileRestartBudget(ctx, live)
	require.NoError(t, err)
	require.False(t, blocked)
}

func TestRestartBudgetRevisionAndDeletionCleanup(t *testing.T) {
	for _, action := range []string{"revision", "delete"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			c, instance, pods := budgetFixture(t, ptr.To[int32](0))
			blocked, err := c.reconcileRestartBudget(ctx, instance)
			require.NoError(t, err)
			require.True(t, blocked)
			live := liveBudgetInstance(t, c, instance)
			live.Status.GroupRestartCount = 3
			require.NoError(t, c.Status().Update(ctx, live))
			if action == "revision" {
				live.Spec.Components[0].Template.Spec.Containers[0].Image = "v2"
				require.NoError(t, c.Client.Update(ctx, live))
				blocked, err = c.reconcileRestartBudget(ctx, live)
				require.NoError(t, err)
				require.True(t, blocked)
				live = liveBudgetInstance(t, c, live)
				require.Zero(t, live.Status.GroupRestartCount)
				require.False(t, live.Status.RestartBudgetExhausted)
			} else {
				// The owner's cleanup finalizer guarantees a reconcile even for background deletion.
				live.Finalizers = append(live.Finalizers, "test/retain-ri")
				require.NoError(t, c.Client.Update(ctx, live))
				require.NoError(t, c.Delete(ctx, live))
				live = liveBudgetInstance(t, c, live)
				require.NoError(t, c.CleanupRestartBudget(ctx, live))
				live = liveBudgetInstance(t, c, live)
				require.EqualValues(t, 3, live.Status.GroupRestartCount, "teardown must not recover or charge")
				require.True(t, live.Status.RestartBudgetExhausted)
				require.NotContains(t, live.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
			}
			for _, p := range pods {
				require.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(p), &v1.Pod{})))
			}
		})
	}
}

func TestRestartBudgetAlreadyTerminatingPodAndOwnerSafety(t *testing.T) {
	ctx := context.Background()
	c, instance, pods := budgetFixture(t, ptr.To[int32](0))
	pods[1].Finalizers = []string{"test/existing"}
	require.NoError(t, c.Client.Update(ctx, pods[1]))
	require.NoError(t, c.Delete(ctx, pods[1]))
	blocked, err := c.reconcileRestartBudget(ctx, instance)
	require.NoError(t, err)
	require.True(t, blocked)
	got := &v1.Pod{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pods[1]), got))
	require.Equal(t, []string{"test/existing"}, got.Finalizers, "no new finalizer after deletion starts")
	// Cleanup cannot patch a replacement Pod with another UID or another RI owner.
	replacement := got.DeepCopy()
	replacement.UID = "new-uid"
	require.NoError(t, c.mutateBudgetPodFinalizer(ctx, instance, replacement, false))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(got), got))
	require.Equal(t, []string{"test/existing"}, got.Finalizers)
}

func TestRestartBudgetCountSurvivesLimitAndPolicyEdits(t *testing.T) {
	c, instance, _ := budgetFixture(t, ptr.To[int32](2))
	instance.Status.GroupRestartRevision = groupRestartRevision(instance)
	instance.Status.GroupRestartCount = 1
	revision := instance.Status.GroupRestartRevision
	instance.Spec.RestartPolicyConfig.MaxGroupRestarts = nil
	instance.Spec.RestartPolicyConfig.Type = workloadsv1alpha2.RestartPolicyNone
	instance.Spec.Components[0].Size = ptr.To[int32](10)
	require.Equal(t, revision, groupRestartRevision(instance))
	require.EqualValues(t, 1, instance.Status.GroupRestartCount)
	instance.Spec.RestartPolicyConfig.Type = workloadsv1alpha2.RecreateRoleInstanceOnPodRestartFromCreation
	require.True(t, prepareGroupRestartBudget(instance))
	require.EqualValues(t, 2, instance.Status.GroupRestartCount, "unsetting a limit does not reset accumulated accepted attempts")
	require.NoError(t, c.Status().Update(context.Background(), instance))
}

type budgetIOCounter struct {
	client.Client
	gets, lists, patches int
}

func (c *budgetIOCounter) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	c.gets++
	return c.Client.Get(ctx, key, obj, opts...)
}
func (c *budgetIOCounter) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.lists++
	return c.Client.List(ctx, list, opts...)
}
func (c *budgetIOCounter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.patches++
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func TestRestartBudgetHotPathAndNoopMetadata(t *testing.T) {
	ctx := context.Background()
	c, instance, _ := budgetFixture(t, nil)
	counter := &budgetIOCounter{Client: c.Client}
	c.Client = counter
	c.apiReader = counter
	blocked, err := c.reconcileRestartBudget(ctx, instance)
	require.NoError(t, err)
	require.False(t, blocked)
	require.NoError(t, c.CleanupRestartBudget(ctx, instance))
	require.Zero(t, counter.gets)
	require.Zero(t, counter.lists)
	require.Zero(t, counter.patches)

	c, instance, _ = budgetFixture(t, ptr.To[int32](2))
	counter = &budgetIOCounter{Client: c.Client}
	c.Client = counter
	c.apiReader = counter
	blocked, err = c.reconcileRestartBudget(ctx, instance)
	require.NoError(t, err)
	require.False(t, blocked)
	require.Equal(t, 1, counter.patches)
	blocked, err = c.reconcileRestartBudget(ctx, instance)
	require.NoError(t, err)
	require.False(t, blocked)
	require.Equal(t, 1, counter.patches, "must not patch a pre-existing owner finalizer on every loop")
	require.Zero(t, counter.lists, "remaining budget needs no uncached Pod list before a fault is accepted")
}

type conflictingBudgetClient struct{ client.Client }
type conflictingBudgetStatus struct{ client.SubResourceWriter }

func (c conflictingBudgetClient) Status() client.SubResourceWriter {
	return conflictingBudgetStatus{c.Client.Status()}
}
func (c conflictingBudgetStatus) Patch(context.Context, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
	return apierrors.NewConflict(schema.GroupResource{Group: workloadsv1alpha2.SchemeGroupVersion.Group, Resource: "roleinstances"}, "budget-0", fmt.Errorf("concurrent spec/status update"))
}

func TestRestartBudgetOptimisticStatusBeforeDelete(t *testing.T) {
	ctx := context.Background()
	c, instance, pods := budgetFixture(t, ptr.To[int32](1))
	blocked, err := c.reconcileRestartBudget(ctx, instance)
	require.NoError(t, err)
	require.False(t, blocked)
	c.Client = conflictingBudgetClient{c.Client}
	_, accepted, err := c.acceptBudgetRestart(ctx, instance)
	require.True(t, apierrors.IsConflict(err))
	require.False(t, accepted)
	require.Zero(t, liveBudgetInstance(t, c, instance).Status.GroupRestartCount)
	for _, pod := range pods {
		live := &v1.Pod{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), live))
		require.Nil(t, live.DeletionTimestamp)
	}
	require.Empty(t, liveBudgetInstance(t, c, instance).Status.RetiringPods, "failed transactions must not authorize deletion")
}

func TestRestartBudgetRejectsStaleIdentityGenerationAndEvidence(t *testing.T) {
	for _, change := range []string{"uid", "generation", "foreign-evidence", "healthy-evidence"} {
		t.Run(change, func(t *testing.T) {
			c, instance, pods := budgetFixture(t, ptr.To[int32](1))
			switch change {
			case "uid":
				instance.UID = "stale"
			case "generation":
				instance.Generation++
			case "foreign-evidence":
				pods[0].OwnerReferences[0].UID = "other"
				require.NoError(t, c.Client.Update(context.Background(), pods[0]))
			case "healthy-evidence":
				pods[0].Status.Phase = v1.PodRunning
				require.NoError(t, c.Status().Update(context.Background(), pods[0]))
			}
			_, accepted, err := c.acceptBudgetRestart(context.Background(), instance)
			require.False(t, accepted)
			if change == "uid" || change == "generation" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Zero(t, liveBudgetInstance(t, c, instance).Status.GroupRestartCount)
		})
	}
}
