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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	revisioncontrol "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/revision"
)

func budgetFixture(t *testing.T, limit *int32) (*realControl, *workloadsv1alpha2.RoleInstance, []*v1.Pod) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, v1.AddToScheme(scheme))
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	instance := &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "budget-0", Namespace: "default", UID: "instance-uid", Generation: 1},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart, ActivateAfter: workloadsv1alpha2.RestartPolicyActivateAfterCreated, MaxGroupRestarts: limit},
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
	resetWave := func() {
		restartingCache.Delete(instanceKey(instance))
		waveDeletesIssued.Delete(instanceKey(instance))
	}
	resetWave()
	t.Cleanup(resetWave)
	return control, instance, pods
}

func budgetTemplateRevision(t *testing.T, instance *workloadsv1alpha2.RoleInstance) string {
	t.Helper()
	revision, err := revisioncontrol.NewRevisionControl().NewRevision(instance, 1, ptr.To[int32](0))
	require.NoError(t, err)
	return revision.Name
}

func liveBudgetInstance(t *testing.T, c *realControl, instance *workloadsv1alpha2.RoleInstance) *workloadsv1alpha2.RoleInstance {
	t.Helper()
	live := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.apiReader.Get(context.Background(), client.ObjectKeyFromObject(instance), live))
	return live
}

func TestRestartBudgetUnsetAndHealthyZero(t *testing.T) {
	ctx := context.Background()
	t.Run("unset needs no reader and does not block scale", func(t *testing.T) {
		c, instance, _ := budgetFixture(t, nil)
		c.apiReader = nil
		blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
		require.NoError(t, err)
		require.False(t, blocked)
	})
	t.Run("healthy zero does not exhaust initial creation", func(t *testing.T) {
		c, instance, pods := budgetFixture(t, ptr.To[int32](0))
		pods[0].Status.Phase = v1.PodRunning
		require.NoError(t, c.Status().Update(ctx, pods[0]))
		blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
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
	require.False(t, prepareGroupRestartBudget(instance, budgetTemplateRevision(t, instance)))
	require.Equal(t, before.Status, instance.Status, "denial must not mutate the status patch baseline")
	_, accepted, err := c.acceptBudgetRestart(ctx, instance, budgetTemplateRevision(t, instance))
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

func TestBudgetDrainPreservesRestartingWithStaleInformer(t *testing.T) {
	for _, cachedCompleted := range []bool{false, true} {
		for _, drain := range []string{"waiting", "retry", "delete-error"} {
			t.Run(fmt.Sprintf("cachedCompleted=%t/%s", cachedCompleted, drain), func(t *testing.T) {
				ctx := context.Background()
				c, instance, pods := budgetFixture(t, ptr.To[int32](2))
				stale := instance.DeepCopy()
				if cachedCompleted {
					setBudgetRestarting(stale)
					completeRestartingCondition(stale)
				}
				for _, pod := range pods {
					pod.Finalizers = []string{"test.rbg.io/hold-drain"}
					require.NoError(t, c.Client.Update(ctx, pod))
				}
				members, accepted, err := c.acceptBudgetRestart(ctx, instance, budgetTemplateRevision(t, instance))
				require.NoError(t, err)
				require.True(t, accepted)
				baseClient := c.Client
				switch drain {
				case "waiting":
					_, err = c.deletePods(ctx, instance, members)
					require.NoError(t, err)
				case "delete-error":
					c.Client = &budgetFailingDeleteClient{Client: c.Client, fail: true}
				}
				// The API has accepted the wave, but the informer still has no
				// Restarting condition (or the previous completed condition).
				handled, _, err := c.Scale(ctx, stale, nil, &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: budgetTemplateRevision(t, stale)}}, nil, nil, nil)
				if drain == "delete-error" {
					require.ErrorContains(t, err, "injected budget termination failure")
				} else {
					require.NoError(t, err)
				}
				require.True(t, handled)
				require.True(t, isInstanceRestarting(stale), "the computed status must retain the live drain guard")
				require.EqualValues(t, 1, liveBudgetInstance(t, c, instance).Status.GroupRestartCount)
				require.True(t, isInstanceRestarting(liveBudgetInstance(t, c, instance)))

				// Completion must still replace True with the committed False guard.
				c.Client = baseClient
				for _, pod := range pods {
					live := &v1.Pod{}
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), live))
					if live.DeletionTimestamp == nil {
						require.NoError(t, c.Delete(ctx, live))
						require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), live))
					}
					live.Finalizers = nil
					require.NoError(t, c.Client.Update(ctx, live))
				}
				handled, _, err = c.Scale(ctx, stale, nil, &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: budgetTemplateRevision(t, stale)}}, nil, nil, nil)
				require.NoError(t, err)
				require.True(t, handled)
				require.False(t, isInstanceRestarting(stale))
				require.False(t, isInstanceRestarting(liveBudgetInstance(t, c, instance)))
			})
		}
	}
}

func TestRestartBudgetOwnerOnlyNeededForExhaustion(t *testing.T) {
	for _, limit := range []int32{0, 2} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			ctx := context.Background()
			c, instance, pods := budgetFixture(t, ptr.To(limit))
			pods[0].Status.Phase = v1.PodRunning
			require.NoError(t, c.Status().Update(ctx, pods[0]))
			blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
			require.NoError(t, err)
			require.False(t, blocked)
			require.NotContains(t, liveBudgetInstance(t, c, instance).Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
			instance = liveBudgetInstance(t, c, instance)
			instance.Spec.RestartPolicyConfig.MaxGroupRestarts = nil
			require.NoError(t, c.Client.Update(ctx, instance))
			require.False(t, HasRestartBudgetLifecycle(instance), "unsetting an unused budget leaves no cleanup obligation")
			require.NoError(t, c.Delete(ctx, instance))
			require.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(instance), &workloadsv1alpha2.RoleInstance{})))
		})
	}
}

func TestRestartBudgetExhaustedReacquiresCleanupOwner(t *testing.T) {
	for _, failOwner := range []bool{false, true} {
		t.Run(fmt.Sprintf("failOwner=%t", failOwner), func(t *testing.T) {
			ctx := context.Background()
			c, instance, pods := budgetFixture(t, ptr.To[int32](0))
			selected := budgetTemplateRevision(t, instance)
			require.NoError(t, c.exhaustRestartBudget(ctx, instance, selected))
			instance = liveBudgetInstance(t, c, instance)
			instance.Finalizers = nil // simulate a manually removed cleanup owner
			require.NoError(t, c.Client.Update(ctx, instance))
			podWrites := 0
			baseClient := c.Client.(client.WithWatch)
			c.Client = interceptor.NewClient(baseClient, interceptor.Funcs{
				Patch: func(ctx context.Context, cli client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if _, isRI := obj.(*workloadsv1alpha2.RoleInstance); isRI {
						if failOwner {
							return fmt.Errorf("injected cleanup owner patch failure")
						}
					} else {
						podWrites++
						owner := liveBudgetInstance(t, c, instance)
						require.Contains(t, owner.Finalizers, constants.GroupRestartBudgetCleanupFinalizer,
							"owner must exist before Pod finalizers are added")
					}
					return cli.Patch(ctx, obj, patch, opts...)
				},
			})
			blocked, err := c.reconcileRestartBudget(ctx, instance, selected)
			require.True(t, blocked)
			live := liveBudgetInstance(t, c, instance)
			require.True(t, live.Status.RestartBudgetExhausted)
			if failOwner {
				require.ErrorContains(t, err, "cleanup owner patch failure")
				require.Zero(t, podWrites)
			} else {
				require.NoError(t, err)
				require.Equal(t, len(pods), podWrites)
				require.Contains(t, live.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
			}
		})
	}
}

func TestRestartBudgetRecoveryWaitMessageConverges(t *testing.T) {
	for _, action := range []string{"annotation", "revision"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			c, instance, pods := budgetFixture(t, ptr.To[int32](0))
			for _, pod := range pods {
				pod.Finalizers = []string{"test/external-hold"}
				require.NoError(t, c.Client.Update(ctx, pod))
			}
			selected := budgetTemplateRevision(t, instance)
			require.NoError(t, c.exhaustRestartBudget(ctx, instance, selected))
			instance = liveBudgetInstance(t, c, instance)
			instance.Finalizers = nil
			if action == "annotation" {
				instance.Annotations = map[string]string{constants.RoleInstanceRecoverAnnotationKey: "true"}
			} else {
				instance.Spec.Components[0].Size = ptr.To[int32](3)
			}
			require.NoError(t, c.Client.Update(ctx, instance))
			statusWrites := 0
			c.Client = interceptor.NewClient(c.Client.(client.WithWatch), interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, cli client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					statusWrites++
					return cli.SubResource(sub).Patch(ctx, obj, patch, opts...)
				},
			})
			checkWaiting := func(count int) {
				t.Helper()
				live := liveBudgetInstance(t, c, instance)
				require.True(t, live.Status.RestartBudgetExhausted)
				require.Contains(t, live.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
				EnforceRestartBudgetConditions(live) // generic enforcement must not erase progress
				for _, cond := range live.Status.Conditions {
					if cond.Type == workloadsv1alpha2.RoleInstanceDegraded {
						require.Contains(t, cond.Message, fmt.Sprintf("waiting for %d old owned Pod", count))
					}
				}
				if action == "annotation" {
					require.Equal(t, "true", live.Annotations[constants.RoleInstanceRecoverAnnotationKey])
				}
			}
			blocked, err := c.reconcileRestartBudget(ctx, instance, selected)
			require.NoError(t, err)
			require.True(t, blocked)
			checkWaiting(2)
			require.Equal(t, 1, statusWrites)
			for pass := 0; pass < 3; pass++ {
				instance = liveBudgetInstance(t, c, instance)
				_, err = c.reconcileRestartBudget(ctx, instance, selected)
				require.NoError(t, err)
			}
			require.Equal(t, 1, statusWrites, "unchanged counts must not create status churn")
			pod := &v1.Pod{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pods[0]), pod))
			pod.Finalizers = nil
			require.NoError(t, c.Client.Update(ctx, pod))
			instance = liveBudgetInstance(t, c, instance)
			_, err = c.reconcileRestartBudget(ctx, instance, selected)
			require.NoError(t, err)
			checkWaiting(1)
			require.Equal(t, 2, statusWrites)
			pod = &v1.Pod{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pods[1]), pod))
			pod.Finalizers = nil
			require.NoError(t, c.Client.Update(ctx, pod))
			instance = liveBudgetInstance(t, c, instance)
			_, err = c.reconcileRestartBudget(ctx, instance, selected)
			require.NoError(t, err)
			live := liveBudgetInstance(t, c, instance)
			require.False(t, live.Status.RestartBudgetExhausted)
			require.NotContains(t, live.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
			require.Equal(t, 3, statusWrites)
		})
	}
}

func TestRestartBudgetOwnerReleasedAfterReset(t *testing.T) {
	for _, action := range []string{"recovery", "revision", "reset-interrupted", "legacy-healthy"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			c, instance, pods := budgetFixture(t, ptr.To[int32](0))
			instance.Finalizers = []string{"test/external-ri"}
			require.NoError(t, c.Client.Update(ctx, instance))
			for _, pod := range pods {
				pod.Finalizers = []string{"test/hold-pod"}
				require.NoError(t, c.Client.Update(ctx, pod))
			}
			if action == "legacy-healthy" {
				// Earlier versions acquired an RI owner before the first fault.
				require.NoError(t, c.ensureBudgetOwner(ctx, instance, false))
				instance.Spec.RestartPolicyConfig.MaxGroupRestarts = nil
				require.NoError(t, c.Client.Update(ctx, instance))
			} else {
				blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
				require.NoError(t, err)
				require.True(t, blocked)
				instance = liveBudgetInstance(t, c, instance)
				require.Contains(t, instance.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
				if action == "revision" {
					instance.Spec.Components[0].Template.Spec.Containers[0].Image = "fixed"
				} else {
					instance.Annotations = map[string]string{constants.RoleInstanceRecoverAnnotationKey: "true"}
				}
				require.NoError(t, c.Client.Update(ctx, instance))
				blocked, err = c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
				require.NoError(t, err)
				require.True(t, blocked)
				require.Contains(t, liveBudgetInstance(t, c, instance).Finalizers, constants.GroupRestartBudgetCleanupFinalizer,
					"keep the RI owner while old Pod objects still block recovery")
				for _, pod := range pods {
					live := &v1.Pod{}
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), live))
					live.Finalizers = nil
					require.NoError(t, c.Client.Update(ctx, live))
				}
				if action == "reset-interrupted" {
					instance = liveBudgetInstance(t, c, instance)
					instance.Status.RestartBudgetExhausted = false
					instance.Status.GroupRestartCount = 0
					require.NoError(t, c.Status().Update(ctx, instance))
					// Also cover a crash after annotation consumption but before
					// releasing the now-unneeded RI cleanup owner.
					delete(instance.Annotations, constants.RoleInstanceRecoverAnnotationKey)
					require.NoError(t, c.Client.Update(ctx, instance))
				}
			}
			instance = liveBudgetInstance(t, c, instance)
			blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
			require.NoError(t, err)
			require.True(t, blocked)
			instance = liveBudgetInstance(t, c, instance)
			require.Equal(t, []string{"test/external-ri"}, instance.Finalizers)
			require.NotContains(t, instance.Annotations, constants.RoleInstanceRecoverAnnotationKey)
			require.False(t, instance.Status.RestartBudgetExhausted)
			require.Zero(t, instance.Status.GroupRestartCount)
		})
	}
}

func TestRestartBudgetOwnerReleaseOrderingAndConflicts(t *testing.T) {
	for _, change := range []string{"unrelated-patch", "new-exhaustion", "generation", "pod-release-error"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			c, instance, pods := budgetFixture(t, ptr.To[int32](2))
			instance.Finalizers = []string{constants.GroupRestartBudgetCleanupFinalizer, "test/external-ri"}
			require.NoError(t, c.Client.Update(ctx, instance))
			pods[0].Finalizers = []string{constants.GroupRestartBudgetCleanupFinalizer, "test/external-pod"}
			require.NoError(t, c.Client.Update(ctx, pods[0]))
			baseClient := c.Client.(client.WithWatch)
			riPatches := 0
			c.Client = interceptor.NewClient(baseClient, interceptor.Funcs{
				Patch: func(ctx context.Context, cli client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					if _, isPod := obj.(*v1.Pod); isPod {
						if change == "pod-release-error" {
							return fmt.Errorf("injected Pod finalizer release failure")
						}
						return cli.Patch(ctx, obj, patch, opts...)
					}
					riPatches++
					pod := &v1.Pod{}
					require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(pods[0]), pod))
					require.Equal(t, []string{"test/external-pod"}, pod.Finalizers, "release the Pod before removing its RI owner")
					if riPatches == 1 {
						live := liveBudgetInstance(t, c, instance)
						switch change {
						case "unrelated-patch":
							live.Labels = map[string]string{"test": "preserved"}
						case "new-exhaustion":
							live.Status.RestartBudgetExhausted = true
							require.NoError(t, cli.Status().Update(ctx, live))
						case "generation":
							live.Generation++
						}
						if change != "new-exhaustion" {
							require.NoError(t, cli.Update(ctx, live))
						}
					}
					return cli.Patch(ctx, obj, patch, opts...)
				},
			})
			err := c.releaseBudgetOwner(ctx, instance)
			live := liveBudgetInstance(t, c, instance)
			if change == "pod-release-error" {
				require.Error(t, err)
				require.Zero(t, riPatches)
			} else {
				require.NoError(t, err)
			}
			if change == "unrelated-patch" {
				require.Equal(t, 2, riPatches)
				require.Equal(t, []string{"test/external-ri"}, live.Finalizers)
				require.Equal(t, "preserved", live.Labels["test"])
			} else {
				require.Contains(t, live.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
			}
		})
	}
}

func TestRestartBudgetRevisionChangePreservesActiveWave(t *testing.T) {
	for _, change := range []string{"size", "template"} {
		for _, cached := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cached=%t", change, cached), func(t *testing.T) {
				ctx := context.Background()
				c, instance, pods := budgetFixture(t, ptr.To[int32](1))
				for _, pod := range pods {
					pod.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
					pod.Finalizers = []string{"test/hold-wave"}
					require.NoError(t, c.Client.Update(ctx, pod))
				}
				members, accepted, err := c.acceptBudgetRestart(ctx, instance, budgetTemplateRevision(t, instance))
				require.NoError(t, err)
				require.True(t, accepted)
				_, err = c.deletePods(ctx, instance, members)
				require.NoError(t, err)
				instance = liveBudgetInstance(t, c, instance)
				tracking := instance.Status.DeepCopy()
				if change == "size" {
					instance.Spec.Components[0].Size = ptr.To[int32](3)
				} else {
					instance.Spec.Components[0].Template.Spec.Containers[0].Image = "v2"
				}
				require.NoError(t, c.Client.Update(ctx, instance))
				if !cached {
					c.ClearRestarting(instance)
				}
				for pass := 0; pass < 3; pass++ {
					instance = liveBudgetInstance(t, c, instance)
					blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
					require.NoError(t, err)
					require.True(t, blocked)
					instance = liveBudgetInstance(t, c, instance)
					require.Zero(t, instance.Status.GroupRestartCount, "the controller's old deletes cannot spend the new budget")
					require.Equal(t, groupRestartRevision(instance, budgetTemplateRevision(t, instance)), instance.Status.GroupRestartRevision)
					require.True(t, isInstanceRestarting(instance))
					require.Equal(t, tracking.RestartCount, instance.Status.RestartCount)
					require.Equal(t, tracking.LastRestartTime, instance.Status.LastRestartTime)
					require.False(t, instance.Status.RestartBudgetExhausted)
				}
				for _, pod := range pods {
					live := &v1.Pod{}
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), live))
					live.Finalizers = nil
					require.NoError(t, c.Client.Update(ctx, live))
				}
				instance = liveBudgetInstance(t, c, instance)
				blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
				require.NoError(t, err)
				require.True(t, blocked)
				instance = liveBudgetInstance(t, c, instance)
				require.False(t, isInstanceRestarting(instance))
				require.Zero(t, instance.Status.GroupRestartCount)
				require.Equal(t, tracking.RestartCount, instance.Status.RestartCount)
				require.Equal(t, tracking.LastRestartTime, instance.Status.LastRestartTime)
			})
		}
	}
}

func TestRestartBudgetLifecycleEvents(t *testing.T) {
	for _, failure := range []string{"none", "status-conflict", "delete-error"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			c, instance, _ := budgetFixture(t, ptr.To[int32](0))
			recorder := c.recorder.(*record.FakeRecorder)
			base := c.Client
			switch failure {
			case "status-conflict":
				c.Client = conflictingBudgetClient{base}
			case "delete-error":
				c.Client = &budgetFailingDeleteClient{Client: base, fail: true}
			}
			_, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
			if failure != "none" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			if failure == "status-conflict" {
				require.Empty(t, recorder.Events, "uncommitted exhaustion must not emit an event")
				return
			}
			require.Len(t, recorder.Events, 1)
			event := <-recorder.Events
			require.Contains(t, event, "Warning GroupRestartBudgetExceeded")
			require.Contains(t, event, "pod=budget-0-worker-0")
			require.Contains(t, event, "reason=PodFailed")
			require.Contains(t, event, "count=0, limit=0")
			c.Client = base
			for pass := 0; pass < 2; pass++ {
				instance = liveBudgetInstance(t, c, instance)
				_, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
				require.NoError(t, err)
				require.Empty(t, recorder.Events, "exhaustion retries must not emit another transition event")
			}
			instance = liveBudgetInstance(t, c, instance)
			instance.Annotations = map[string]string{constants.RoleInstanceRecoverAnnotationKey: "true"}
			require.NoError(t, c.Client.Update(ctx, instance))
			_, err = c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
			require.NoError(t, err)
			require.Len(t, recorder.Events, 1)
			require.Contains(t, <-recorder.Events, "Normal GroupRestartBudgetRecovered")
			instance = liveBudgetInstance(t, c, instance)
			_, err = c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
			require.NoError(t, err)
			require.Empty(t, recorder.Events, "completed recovery must not emit on every reconcile")
		})
	}
}

func TestRestartBudgetExactCountAndNoQuietReset(t *testing.T) {
	ctx := context.Background()
	c, instance, _ := budgetFixture(t, ptr.To[int32](2))
	for round := int32(1); round <= 2; round++ {
		instance = liveBudgetInstance(t, c, instance)
		instance.Status.Conditions = nil // prior group completed its restart
		old := metav1.NewTime(time.Now().Add(-time.Hour))
		instance.Status.LastRestartTime = &old
		instance.Status.RestartCount = 9
		require.NoError(t, c.Status().Update(ctx, instance))
		blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
		require.NoError(t, err)
		require.False(t, blocked)
		pods, accepted, err := c.acceptBudgetRestart(ctx, instance, budgetTemplateRevision(t, instance))
		require.NoError(t, err)
		require.True(t, accepted)
		require.Len(t, pods, 2)
		live := liveBudgetInstance(t, c, instance)
		require.Equal(t, round, live.Status.GroupRestartCount)
		require.EqualValues(t, 1, live.Status.RestartCount, "quiet-period backoff reset must not reset durable count")
		_, accepted, err = c.acceptBudgetRestart(ctx, instance, budgetTemplateRevision(t, instance))
		require.NoError(t, err)
		require.False(t, accepted, "repeated events in accepted restart consume nothing")
		require.Equal(t, round, liveBudgetInstance(t, c, instance).Status.GroupRestartCount)
	}
	instance = liveBudgetInstance(t, c, instance)
	instance.Status.Conditions = nil
	now := metav1.Now()
	instance.Status.LastRestartTime = &now // would normally require backoff
	require.NoError(t, c.Status().Update(ctx, instance))
	blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
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
		blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
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
		blocked, err := c.reconcileRestartBudget(ctx, live, budgetTemplateRevision(t, live))
		require.NoError(t, err)
		require.True(t, blocked, change)
		require.True(t, liveBudgetInstance(t, c, instance).Status.RestartBudgetExhausted)
	}
	// A new controller process has no cache and must still honor the persistent marker.
	c.ClearRestarting(instance)
	live = liveBudgetInstance(t, c, instance)
	live.Annotations = map[string]string{constants.RoleInstanceRecoverAnnotationKey: "true"}
	require.NoError(t, c.Client.Update(ctx, live))
	blocked, err := c.reconcileRestartBudget(ctx, live, budgetTemplateRevision(t, live))
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
	blocked, err = c.reconcileRestartBudget(ctx, live, budgetTemplateRevision(t, live))
	require.NoError(t, err)
	require.False(t, blocked)
}

func TestRestartBudgetRecoveryWaitsForOldPodUIDs(t *testing.T) {
	for _, limit := range []int32{0, 1} {
		for _, action := range []string{"annotation", "revision"} {
			t.Run(fmt.Sprintf("limit=%d/%s", limit, action), func(t *testing.T) {
				ctx := context.Background()
				c, instance, pods := budgetFixture(t, ptr.To(limit))
				instance.Status.GroupRestartCount = limit
				require.NoError(t, c.Status().Update(ctx, instance))
				for _, pod := range pods {
					pod.Finalizers = []string{"test.rbg.io/hold-old-member"}
					require.NoError(t, c.Client.Update(ctx, pod))
				}
				blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
				require.NoError(t, err)
				require.True(t, blocked)
				live := liveBudgetInstance(t, c, instance)
				if action == "annotation" {
					live.Annotations = map[string]string{constants.RoleInstanceRecoverAnnotationKey: "true"}
				} else {
					live.Spec.Components[0].Template.Spec.Containers[0].Image = "fixed-image"
				}
				require.NoError(t, c.Client.Update(ctx, live))

				// Each pass can come from a new controller process. Readiness is not
				// the barrier: even a Ready status cannot release old incarnations.
				for round := 0; round < 3; round++ {
					live = liveBudgetInstance(t, c, instance)
					live.Status.Conditions = []workloadsv1alpha2.RoleInstanceCondition{{Type: workloadsv1alpha2.RoleInstanceReady, Status: v1.ConditionTrue}}
					require.NoError(t, c.Status().Update(ctx, live))
					control := &realControl{Client: c.Client, apiReader: c.apiReader, recorder: record.NewFakeRecorder(20)}
					blocked, err = control.reconcileRestartBudget(ctx, live, budgetTemplateRevision(t, live))
					require.NoError(t, err)
					require.True(t, blocked)
					live = liveBudgetInstance(t, c, instance)
					require.True(t, live.Status.RestartBudgetExhausted)
					require.Equal(t, limit, live.Status.GroupRestartCount, "waiting must neither reset nor charge")
					if action == "annotation" {
						require.Equal(t, "true", live.Annotations[constants.RoleInstanceRecoverAnnotationKey], "intent survives until termination completes")
					}
					for _, pod := range pods {
						got := &v1.Pod{}
						require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), got))
						require.Equal(t, pod.UID, got.UID)
						require.NotNil(t, got.DeletionTimestamp)
						require.Equal(t, []string{"test.rbg.io/hold-old-member"}, got.Finalizers, "only the budget finalizer may be released")
					}
				}
				for _, pod := range pods {
					got := &v1.Pod{}
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), got))
					got.Finalizers = nil
					require.NoError(t, c.Client.Update(ctx, got))
				}
				live = liveBudgetInstance(t, c, instance)
				blocked, err = c.reconcileRestartBudget(ctx, live, budgetTemplateRevision(t, live))
				require.NoError(t, err)
				require.True(t, blocked, "a fresh pass creates the new group after reset")
				live = liveBudgetInstance(t, c, instance)
				require.False(t, live.Status.RestartBudgetExhausted)
				require.Zero(t, live.Status.GroupRestartCount)
				require.Empty(t, live.Annotations[constants.RoleInstanceRecoverAnnotationKey])
				blocked, err = c.reconcileRestartBudget(ctx, live, budgetTemplateRevision(t, live))
				require.NoError(t, err)
				require.False(t, blocked, "old Terminating members cannot re-exhaust the new budget")
			})
		}
	}
}

type budgetFailingDeleteClient struct {
	client.Client
	fail bool
}

func (c *budgetFailingDeleteClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if c.fail {
		return fmt.Errorf("injected budget termination failure")
	}
	return c.Client.Delete(ctx, obj, opts...)
}

func TestRestartBudgetRecoveryRetriesFailedTermination(t *testing.T) {
	ctx := context.Background()
	c, instance, pods := budgetFixture(t, ptr.To[int32](1))
	instance.Status.GroupRestartCount = 1
	require.NoError(t, c.Status().Update(ctx, instance))
	// Model a crash/error after the exhausted marker commits, before Delete.
	require.NoError(t, c.exhaustRestartBudget(ctx, instance, budgetTemplateRevision(t, instance)))
	live := liveBudgetInstance(t, c, instance)
	live.Annotations = map[string]string{constants.RoleInstanceRecoverAnnotationKey: "true"}
	require.NoError(t, c.Client.Update(ctx, live))
	deleting := &budgetFailingDeleteClient{Client: c.Client, fail: true}
	c.Client = deleting
	blocked, err := c.reconcileRestartBudget(ctx, live, budgetTemplateRevision(t, live))
	require.ErrorContains(t, err, "injected budget termination failure")
	require.True(t, blocked)
	live = liveBudgetInstance(t, c, instance)
	require.True(t, live.Status.RestartBudgetExhausted)
	require.EqualValues(t, 1, live.Status.GroupRestartCount)
	require.Equal(t, "true", live.Annotations[constants.RoleInstanceRecoverAnnotationKey])
	for _, pod := range pods {
		got := &v1.Pod{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), got))
		require.Nil(t, got.DeletionTimestamp)
	}
	deleting.fail = false
	blocked, err = c.reconcileRestartBudget(ctx, live, budgetTemplateRevision(t, live))
	require.NoError(t, err)
	require.True(t, blocked)
	live = liveBudgetInstance(t, c, instance)
	require.False(t, live.Status.RestartBudgetExhausted)
	require.Zero(t, live.Status.GroupRestartCount)
	for _, pod := range pods {
		require.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(pod), &v1.Pod{})))
	}
}

func TestRestartBudgetRecoveryAnnotationCleanupAfterReset(t *testing.T) {
	ctx := context.Background()
	c, instance, pods := budgetFixture(t, ptr.To[int32](0))
	instance.Status.GroupRestartCount = 1
	require.NoError(t, c.Status().Update(ctx, instance))
	require.NoError(t, c.exhaustRestartBudget(ctx, instance, budgetTemplateRevision(t, instance)))
	live := liveBudgetInstance(t, c, instance)
	live.Annotations = map[string]string{constants.RoleInstanceRecoverAnnotationKey: "true"}
	require.NoError(t, c.Client.Update(ctx, live))

	failAnnotation := true
	baseClient := c.Client
	c.Client = &budgetFailingRecoveryPatchClient{Client: baseClient, fail: &failAnnotation}
	blocked, err := c.reconcileRestartBudget(ctx, live, budgetTemplateRevision(t, live))
	require.ErrorContains(t, err, "injected recovery annotation cleanup failure")
	require.True(t, blocked)
	live = liveBudgetInstance(t, c, instance)
	require.False(t, live.Status.RestartBudgetExhausted)
	require.Zero(t, live.Status.GroupRestartCount)
	require.Equal(t, "true", live.Annotations[constants.RoleInstanceRecoverAnnotationKey])

	failAnnotation = false
	blocked, err = c.reconcileRestartBudget(ctx, live, budgetTemplateRevision(t, live))
	require.NoError(t, err)
	require.True(t, blocked)
	live = liveBudgetInstance(t, c, instance)
	require.Empty(t, live.Annotations[constants.RoleInstanceRecoverAnnotationKey])
	require.False(t, live.Status.RestartBudgetExhausted)
	require.Zero(t, live.Status.GroupRestartCount)
	for _, pod := range pods {
		require.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(pod), &v1.Pod{})))
	}
}

type budgetFailingRecoveryPatchClient struct {
	client.Client
	fail *bool
}

func (c *budgetFailingRecoveryPatchClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if *c.fail {
		return fmt.Errorf("injected recovery annotation cleanup failure")
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func TestRestartBudgetRecoveryAnnotationCleanupRetries(t *testing.T) {
	for _, change := range []string{"metadata", "exhaustion", "generation", "annotation-removed"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			c, instance, _ := budgetFixture(t, ptr.To[int32](2))
			// Model restart after status reset committed, before annotation cleanup.
			instance.Status.GroupRestartRevision = groupRestartRevision(instance, budgetTemplateRevision(t, instance))
			instance.Status.GroupRestartCount = 1
			require.NoError(t, c.Status().Update(ctx, instance))
			instance.Annotations = map[string]string{constants.RoleInstanceRecoverAnnotationKey: "true"}
			require.NoError(t, c.Client.Update(ctx, instance))
			baseClient := c.Client
			patches := 0
			c.Client = interceptor.NewClient(baseClient.(client.WithWatch), interceptor.Funcs{
				Patch: func(ctx context.Context, cli client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					patches++
					if patches == 1 {
						live := liveBudgetInstance(t, c, instance)
						switch change {
						case "metadata":
							live.Annotations["test/external"] = "preserved"
						case "exhaustion":
							live.Status.RestartBudgetExhausted = true
							require.NoError(t, cli.Status().Update(ctx, live))
						case "generation":
							live.Generation++
						case "annotation-removed":
							delete(live.Annotations, constants.RoleInstanceRecoverAnnotationKey)
						}
						if change != "exhaustion" {
							require.NoError(t, cli.Update(ctx, live))
						}
					}
					return cli.Patch(ctx, obj, patch, opts...)
				},
			})
			blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
			require.NoError(t, err)
			require.True(t, blocked)
			live := liveBudgetInstance(t, c, instance)
			require.EqualValues(t, 1, live.Status.GroupRestartCount, "cleanup must not reset accounting")
			switch change {
			case "metadata":
				require.Equal(t, 2, patches, "retry on the live resourceVersion after a metadata conflict")
				require.NotContains(t, live.Annotations, constants.RoleInstanceRecoverAnnotationKey)
				require.Equal(t, "preserved", live.Annotations["test/external"])
			case "annotation-removed":
				require.Equal(t, 1, patches, "concurrent removal makes the retry a no-op")
				require.NotContains(t, live.Annotations, constants.RoleInstanceRecoverAnnotationKey)
			default:
				require.Equal(t, 1, patches, "a new exhaustion or spec generation must stop stale cleanup")
				require.Equal(t, "true", live.Annotations[constants.RoleInstanceRecoverAnnotationKey])
			}
		})
	}
}

func TestRestartBudgetRecoveryAnnotationCleanupScope(t *testing.T) {
	for _, change := range []string{"uid", "false", "absent", "deleting", "missing"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			c, instance, _ := budgetFixture(t, ptr.To[int32](2))
			instance.Annotations = map[string]string{constants.RoleInstanceRecoverAnnotationKey: "true"}
			require.NoError(t, c.Client.Update(ctx, instance))
			snapshot := instance.DeepCopy()
			switch change {
			case "uid":
				snapshot.UID = "old-owner"
			case "false":
				instance.Annotations[constants.RoleInstanceRecoverAnnotationKey] = "false"
				require.NoError(t, c.Client.Update(ctx, instance))
			case "absent":
				delete(instance.Annotations, constants.RoleInstanceRecoverAnnotationKey)
				require.NoError(t, c.Client.Update(ctx, instance))
			case "deleting":
				instance.Finalizers = []string{"test/retain"}
				require.NoError(t, c.Client.Update(ctx, instance))
				require.NoError(t, c.Delete(ctx, instance))
			case "missing":
				require.NoError(t, c.Delete(ctx, instance))
			}
			counter := &budgetIOCounter{Client: c.Client}
			c.Client = counter
			require.NoError(t, c.clearRecoveryAnnotation(ctx, snapshot))
			require.Zero(t, counter.patches)
		})
	}
}

func TestRestartBudgetRevisionAndDeletionCleanup(t *testing.T) {
	for _, action := range []string{"revision", "delete"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			c, instance, pods := budgetFixture(t, ptr.To[int32](0))
			blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
			require.NoError(t, err)
			require.True(t, blocked)
			live := liveBudgetInstance(t, c, instance)
			live.Status.GroupRestartCount = 3
			require.NoError(t, c.Status().Update(ctx, live))
			if action == "revision" {
				live.Spec.Components[0].Template.Spec.Containers[0].Image = "v2"
				require.NoError(t, c.Client.Update(ctx, live))
				blocked, err = c.reconcileRestartBudget(ctx, live, budgetTemplateRevision(t, live))
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
	blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
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

func TestRestartBudgetSizeChangeStartsNewRevision(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(fmt.Sprintf("exhausted=%t", exhausted), func(t *testing.T) {
			ctx := context.Background()
			c, instance, pods := budgetFixture(t, ptr.To[int32](2))
			instance.Status.GroupRestartRevision = groupRestartRevision(instance, budgetTemplateRevision(t, instance))
			instance.Status.GroupRestartCount = 1
			oldRevision := instance.Status.GroupRestartRevision
			require.NoError(t, c.Status().Update(ctx, instance))
			if exhausted {
				require.NoError(t, c.exhaustRestartBudget(ctx, instance, budgetTemplateRevision(t, instance)))
				require.NoError(t, c.terminateBudgetPods(ctx, instance))
			}
			live := liveBudgetInstance(t, c, instance)
			live.Spec.Components[0].Size = ptr.To[int32](3)
			require.NoError(t, c.Client.Update(ctx, live))
			blocked, err := c.reconcileRestartBudget(ctx, live, budgetTemplateRevision(t, live))
			require.NoError(t, err)
			require.True(t, blocked, "new topology starts a fresh budget on a separate pass")
			live = liveBudgetInstance(t, c, instance)
			require.Zero(t, live.Status.GroupRestartCount)
			require.NotEqual(t, oldRevision, live.Status.GroupRestartRevision)
			require.Equal(t, groupRestartRevision(live, budgetTemplateRevision(t, live)), live.Status.GroupRestartRevision)
			require.False(t, live.Status.RestartBudgetExhausted)
			if exhausted {
				for _, pod := range pods {
					require.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(pod), &v1.Pod{})), "old exhausted members must leave before the reset")
				}
			}
		})
	}
}

func TestRestartBudgetSelectedRevisionAndCanonicalSizes(t *testing.T) {
	_, instance, _ := budgetFixture(t, ptr.To[int32](2))
	selected := "instance-legacy-template"
	instance.Spec.Components = append(instance.Spec.Components,
		workloadsv1alpha2.RoleInstanceComponent{Name: "auxiliary", Size: ptr.To[int32](1)})
	before := instance.DeepCopy()
	key := groupRestartRevision(instance, selected)
	require.Equal(t, before, instance)
	instance.Spec.Components[0], instance.Spec.Components[1] = instance.Spec.Components[1], instance.Spec.Components[0]
	require.Equal(t, key, groupRestartRevision(instance, selected), "component order does not change the size identity")
	instance.Spec.Components[0].Template.CreationTimestamp = metav1.Now()
	instance.Spec.Components[0].Template.Labels = map[string]string{"serializer": "different"}
	instance.Status.UpdateRevision = "old-status-target"
	require.Equal(t, key, groupRestartRevision(instance, selected), "templates and lagging status are not serialized into the budget key")
	require.NotEqual(t, key, groupRestartRevision(instance, "new-template"), "a real selected-template revision starts a new budget")
	instance.Spec.Components[0].Size = ptr.To[int32](3)
	require.NotEqual(t, key, groupRestartRevision(instance, selected), "size must supplement the RI revision")
}

func TestRestartBudgetWithoutSelectedRevisionFailsClosed(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(fmt.Sprintf("exhausted=%t", exhausted), func(t *testing.T) {
			ctx := context.Background()
			c, instance, pods := budgetFixture(t, ptr.To[int32](0))
			if exhausted {
				require.NoError(t, c.exhaustRestartBudget(ctx, instance, budgetTemplateRevision(t, instance)))
				instance = liveBudgetInstance(t, c, instance)
			}
			before := instance.Status.DeepCopy()
			handled, delay, err := c.Scale(ctx, instance, nil, nil, nil, pods[1:], pods[:1])
			require.ErrorContains(t, err, "requires a selected ControllerRevision")
			require.True(t, handled)
			require.Zero(t, delay)
			_, err = c.Update(ctx, instance, &instance.Status, nil, nil, nil, pods, nil)
			require.ErrorContains(t, err, "requires a selected ControllerRevision")
			live := liveBudgetInstance(t, c, instance)
			require.Equal(t, before, &live.Status)
			for _, pod := range pods {
				got := &v1.Pod{}
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), got))
				require.Nil(t, got.DeletionTimestamp)
			}
		})
	}
}

func TestRestartBudgetUsesSelectedRevisionBeforeStatusUpdate(t *testing.T) {
	for _, staleStatus := range []string{"", "previous-template"} {
		t.Run("status="+staleStatus, func(t *testing.T) {
			ctx := context.Background()
			c, instance, pods := budgetFixture(t, ptr.To[int32](0))
			instance.Status.UpdateRevision = staleStatus
			instance.Status.CurrentRevision = staleStatus
			require.NoError(t, c.Status().Update(ctx, instance))
			target := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "selected-current-template"}}
			handled, _, err := c.Scale(ctx, instance, nil, target, nil, pods[1:], pods[:1])
			require.NoError(t, err)
			require.True(t, handled)
			live := liveBudgetInstance(t, c, instance)
			require.True(t, live.Status.RestartBudgetExhausted)
			require.Equal(t, groupRestartRevision(live, target.Name), live.Status.GroupRestartRevision)
			require.Equal(t, staleStatus, live.Status.UpdateRevision, "budget evaluation must not write the general revision fields")
		})
	}
}

func TestGroupRestartRevisionIncludesSizeAndPreservesSource(t *testing.T) {
	_, instance, _ := budgetFixture(t, ptr.To[int32](2))
	before := instance.DeepCopy()
	revision := groupRestartRevision(instance, budgetTemplateRevision(t, instance))
	require.Equal(t, before, instance, "hashing must not modify the source")
	instance.Spec.Components[0].Size = ptr.To[int32](3)
	require.NotEqual(t, revision, groupRestartRevision(instance, budgetTemplateRevision(t, instance)), "group size is part of the topology revision, as in LWS")
	instance.Spec.Components[0].Size = ptr.To[int32](2)
	instance.Spec.Components[0].Template.Labels = map[string]string{constants.ComponentSizeLabelKey: "3"}
	require.NotEqual(t, revision, groupRestartRevision(instance, budgetTemplateRevision(t, instance)), "derived topology labels select a new ControllerRevision")
}

func TestRestartBudgetCountSurvivesLimitAndPolicyEdits(t *testing.T) {
	c, instance, _ := budgetFixture(t, ptr.To[int32](2))
	instance.Status.GroupRestartRevision = groupRestartRevision(instance, budgetTemplateRevision(t, instance))
	instance.Status.GroupRestartCount = 1
	revision := instance.Status.GroupRestartRevision
	instance.Spec.RestartPolicyConfig.MaxGroupRestarts = nil
	instance.Spec.RestartPolicyConfig.Type = workloadsv1alpha2.RestartPolicyNone
	require.Equal(t, revision, groupRestartRevision(instance, budgetTemplateRevision(t, instance)))
	require.EqualValues(t, 1, instance.Status.GroupRestartCount)
	instance.Spec.RestartPolicyConfig.Type = workloadsv1alpha2.RecreateRoleInstanceOnPodRestart
	instance.Spec.RestartPolicyConfig.ActivateAfter = workloadsv1alpha2.RestartPolicyActivateAfterCreated
	require.True(t, prepareGroupRestartBudget(instance, budgetTemplateRevision(t, instance)))
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

func TestRestartBudgetSharesReconcileReadsAndSkipsHealthyAtLimitList(t *testing.T) {
	for _, atLimit := range []bool{false, true} {
		t.Run(fmt.Sprintf("atLimit=%t", atLimit), func(t *testing.T) {
			ctx := WithBudgetObservation(context.Background())
			limit := int32(2)
			if atLimit {
				limit = 1
			}
			c, instance, pods := budgetFixture(t, ptr.To(limit))
			for _, pod := range pods {
				pod.Status.Phase = v1.PodRunning
				require.NoError(t, c.Status().Update(context.Background(), pod))
			}
			if atLimit {
				instance.Status.GroupRestartCount = limit
				require.NoError(t, c.Status().Update(context.Background(), instance))
			}
			reader := &budgetIOCounter{Client: c.apiReader.(client.Client)}
			c.apiReader = reader
			target := &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: budgetTemplateRevision(t, instance)}}
			handled, _, err := c.Scale(ctx, instance, nil, target, nil, pods, nil)
			require.NoError(t, err)
			require.False(t, handled)
			c.inplaceControl = &fakeInplaceControl{}
			for _, pod := range pods {
				pod.Labels[apps.ControllerRevisionHashLabelKey] = target.Name
			}
			_, err = c.Update(ctx, instance, &instance.Status, nil, target, nil, pods, nil)
			require.NoError(t, err)
			require.Equal(t, 1, reader.gets, "Scale, restart tracking and Update share the fresh RI observation")
			require.Zero(t, reader.lists, "healthy at-limit instances do not need an uncached Pod list")
			_, err = c.Update(WithBudgetObservation(context.Background()), instance, &instance.Status, nil, target, nil, pods, nil)
			require.NoError(t, err)
			require.Equal(t, 2, reader.gets, "a new reconcile must perform a new read")
			if atLimit {
				require.Equal(t, 1, reader.lists, "direct Update has no complete Pod snapshot and must still check live evidence")
			}
		})
	}
}

func TestRestartBudgetAtLimitRevalidatesSuspectedFault(t *testing.T) {
	for _, liveFault := range []bool{false, true} {
		t.Run(fmt.Sprintf("liveFault=%t", liveFault), func(t *testing.T) {
			ctx := WithBudgetObservation(context.Background())
			c, instance, pods := budgetFixture(t, ptr.To[int32](0))
			if !liveFault {
				healthy := pods[0].DeepCopy()
				healthy.Status.Phase = v1.PodRunning
				require.NoError(t, c.Status().Update(ctx, healthy))
			}
			reader := &budgetIOCounter{Client: c.Client}
			c.apiReader = reader
			blocked, err := c.reconcileRestartBudgetOnce(ctx, instance, budgetTemplateRevision(t, instance), pods)
			require.NoError(t, err)
			require.Equal(t, liveFault, blocked)
			require.Positive(t, reader.lists, "cached fault evidence must be confirmed through a live LIST")
			live := liveBudgetInstance(t, c, instance)
			require.Equal(t, liveFault, live.Status.RestartBudgetExhausted)
			require.Zero(t, live.Status.GroupRestartCount)
			if !liveFault {
				require.Empty(t, live.Finalizers)
				for _, pod := range pods {
					got := &v1.Pod{}
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), got))
					require.Nil(t, got.DeletionTimestamp)
				}
			}
		})
	}
}

func TestRestartBudgetExhaustedSteadyStateHasNoPerPodReads(t *testing.T) {
	ctx := context.Background()
	c, instance, pods := budgetFixture(t, ptr.To[int32](0))
	selected := budgetTemplateRevision(t, instance)
	blocked, err := c.reconcileRestartBudget(ctx, instance, selected)
	require.NoError(t, err)
	require.True(t, blocked)
	instance = liveBudgetInstance(t, c, instance)
	counter := &budgetIOCounter{Client: c.Client}
	c.Client, c.apiReader = counter, counter
	for pass := 0; pass < 3; pass++ {
		counter.gets, counter.lists, counter.patches = 0, 0, 0
		blocked, err = c.reconcileRestartBudgetOnce(WithBudgetObservation(ctx), instance, selected, pods)
		require.NoError(t, err)
		require.True(t, blocked)
		require.Equal(t, 1, counter.gets, "only the RI must be read; retained deleting Pods need no GET")
		require.Equal(t, 1, counter.lists)
		require.Zero(t, counter.patches)
	}
}

func TestRestartBudgetSkippedPodReadsStillRetryMissingWork(t *testing.T) {
	ctx := context.Background()
	c, instance, pods := budgetFixture(t, ptr.To[int32](0))
	selected := budgetTemplateRevision(t, instance)
	require.NoError(t, c.exhaustRestartBudget(ctx, instance, selected))
	reader := &budgetIOCounter{Client: c.Client}
	c.apiReader = reader
	deletes := 0
	failDelete := true
	c.Client = interceptor.NewClient(c.Client.(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			deletes++
			if failDelete {
				return fmt.Errorf("injected retryable DELETE failure")
			}
			return cli.Delete(ctx, obj, opts...)
		},
	})
	_, err := c.reconcileRestartBudgetOnce(WithBudgetObservation(ctx), instance, selected, pods)
	require.ErrorContains(t, err, "retryable DELETE failure")
	require.Equal(t, 1, deletes)
	for _, pod := range pods {
		actual := &v1.Pod{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), actual))
		require.Contains(t, actual.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
		require.Nil(t, actual.DeletionTimestamp)
	}
	failDelete = false
	reader.gets, reader.lists, reader.patches = 0, 0, 0
	_, err = c.reconcileRestartBudgetOnce(WithBudgetObservation(ctx), instance, selected, pods)
	require.NoError(t, err)
	require.Equal(t, 3, reader.gets, "one RI read and two live deletion checks; no reads for existing finalizers")
	require.Equal(t, 3, deletes)
	reader.gets = 0
	_, err = c.reconcileRestartBudgetOnce(WithBudgetObservation(ctx), instance, selected, pods)
	require.NoError(t, err)
	require.Equal(t, 1, reader.gets, "once both DELETEs are accepted only the RI read remains")
	require.Equal(t, 3, deletes, "already deleting Pods do not need another DELETE")
}

func TestRestartBudgetObservationSeparatesIdentityAndRevision(t *testing.T) {
	ctx := WithBudgetObservation(context.Background())
	c, instance, pods := budgetFixture(t, ptr.To[int32](2))
	reader := &budgetIOCounter{Client: c.Client}
	c.apiReader = reader
	selected := budgetTemplateRevision(t, instance)
	_, err := c.reconcileRestartBudgetOnce(ctx, instance, selected, pods)
	require.NoError(t, err)
	require.Equal(t, 1, reader.gets)
	other := instance.DeepCopy()
	other.Generation++
	blocked, err := c.reconcileRestartBudgetOnce(ctx, other, selected, pods)
	require.NoError(t, err)
	require.True(t, blocked)
	require.Equal(t, 2, reader.gets, "a different generation cannot reuse the old observation")
	_, err = c.reconcileRestartBudgetOnce(ctx, instance, selected+"-different", pods)
	require.NoError(t, err)
	require.Equal(t, 3, reader.gets, "a different selected revision cannot reuse the old observation")
	other = instance.DeepCopy()
	other.UID = "other-uid"
	_, err = c.reconcileRestartBudgetOnce(ctx, other, selected, pods)
	require.Error(t, err)
	require.Equal(t, 4, reader.gets)
}

func TestRestartBudgetHotPathAndNoopMetadata(t *testing.T) {
	ctx := context.Background()
	c, instance, _ := budgetFixture(t, nil)
	counter := &budgetIOCounter{Client: c.Client}
	c.Client = counter
	c.apiReader = counter
	blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
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
	blocked, err = c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
	require.NoError(t, err)
	require.False(t, blocked)
	require.Zero(t, counter.patches)
	blocked, err = c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
	require.NoError(t, err)
	require.False(t, blocked)
	require.Zero(t, counter.patches, "a healthy budget does not acquire a cleanup owner")
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
	blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
	require.NoError(t, err)
	require.False(t, blocked)
	c.Client = conflictingBudgetClient{c.Client}
	_, accepted, err := c.acceptBudgetRestart(ctx, instance, budgetTemplateRevision(t, instance))
	require.True(t, apierrors.IsConflict(err))
	require.False(t, accepted)
	require.Zero(t, liveBudgetInstance(t, c, instance).Status.GroupRestartCount)
	for _, pod := range pods {
		live := &v1.Pod{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), live))
		require.Nil(t, live.DeletionTimestamp)
	}
	_, cached := restartingCache.Load(instanceKey(instance))
	require.False(t, cached, "failed transactions must not poison the restart cache")
}

func TestRestartBudgetAddedBeforeInformerRefresh(t *testing.T) {
	for _, limit := range []int32{0, 1} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			ctx := context.Background()
			c, live, pods := budgetFixture(t, ptr.To(limit))
			stale := live.DeepCopy()
			stale.Spec.RestartPolicyConfig.MaxGroupRestarts = nil
			live.Generation++
			live.Status.ObservedGeneration = live.Generation
			require.NoError(t, c.Client.Update(ctx, live))
			require.NoError(t, c.Status().Update(ctx, live))
			cache := fake.NewClientBuilder().WithScheme(c.Scheme()).WithObjects(stale, pods[0], pods[1]).Build()
			deletes := 0
			c.Client = interceptor.NewClient(cache, interceptor.Funcs{
				Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
					deletes++
					return nil
				},
			})
			handled, delay, err := c.Scale(ctx, stale, nil, &apps.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: budgetTemplateRevision(t, stale)}}, nil, pods[1:], pods[:1])
			require.NoError(t, err)
			require.Positive(t, delay, "wait for the budget generation instead of executing stale intent")
			require.False(t, handled)
			require.Zero(t, deletes, "stale unbudgeted dispatch must not rebuild the group for free")
			actual := liveBudgetInstance(t, c, live)
			require.Zero(t, actual.Status.GroupRestartCount)
			require.False(t, actual.Status.RestartBudgetExhausted)
		})
	}
}

func TestRestartBudgetGenerationLagAndErrorRequeue(t *testing.T) {
	for _, change := range []string{"generation", "uid", "read-error"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			c, instance, _ := budgetFixture(t, ptr.To[int32](1))
			switch change {
			case "generation":
				instance.Generation--
			case "uid":
				instance.UID = "old-instance"
			case "read-error":
				c.apiReader = interceptor.NewClient(c.Client.(client.WithWatch), interceptor.Funcs{
					Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
						return fmt.Errorf("injected API read failure")
					},
				})
			}
			handled, delay, err := c.Scale(ctx, instance, nil, nil, nil, nil, nil)
			require.True(t, handled)
			if change == "generation" {
				require.NoError(t, err)
				require.Positive(t, delay)
			} else {
				require.Error(t, err)
				require.Zero(t, delay, "errors use controller-runtime rate limiting")
			}
			updateDelay, updateErr := c.Update(ctx, instance, &instance.Status, nil, nil, nil, nil, nil)
			if change == "generation" {
				require.NoError(t, updateErr)
				require.Positive(t, updateDelay)
			} else {
				require.Error(t, updateErr)
				require.Zero(t, updateDelay)
			}
		})
	}
}

func TestRestartBudgetCounterSaturates(t *testing.T) {
	_, instance, _ := budgetFixture(t, nil)
	instance.Status.GroupRestartRevision = groupRestartRevision(instance, budgetTemplateRevision(t, instance))
	instance.Status.GroupRestartCount = 1<<31 - 2
	require.True(t, prepareGroupRestartBudget(instance, budgetTemplateRevision(t, instance)))
	require.EqualValues(t, 1<<31-1, instance.Status.GroupRestartCount)
	require.True(t, prepareGroupRestartBudget(instance, budgetTemplateRevision(t, instance)))
	require.EqualValues(t, 1<<31-1, instance.Status.GroupRestartCount)
}

func TestRestartBudgetNonTrueAnnotationDoesNotRecover(t *testing.T) {
	for _, value := range []string{"", "false", "True", "1"} {
		t.Run(fmt.Sprintf("value=%q", value), func(t *testing.T) {
			ctx := context.Background()
			c, instance, _ := budgetFixture(t, ptr.To[int32](0))
			require.NoError(t, c.exhaustRestartBudget(ctx, instance, budgetTemplateRevision(t, instance)))
			instance = liveBudgetInstance(t, c, instance)
			instance.Annotations = map[string]string{constants.RoleInstanceRecoverAnnotationKey: value}
			require.NoError(t, c.Client.Update(ctx, instance))
			blocked, err := c.reconcileRestartBudget(ctx, instance, budgetTemplateRevision(t, instance))
			require.NoError(t, err)
			require.True(t, blocked)
			actual := liveBudgetInstance(t, c, instance)
			require.True(t, actual.Status.RestartBudgetExhausted)
			require.Equal(t, value, actual.Annotations[constants.RoleInstanceRecoverAnnotationKey])
		})
	}
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
			_, accepted, err := c.acceptBudgetRestart(context.Background(), instance, budgetTemplateRevision(t, instance))
			require.False(t, accepted)
			if change == "uid" || change == "generation" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBudgetRecreationEventUsesLiveAcceptedTrigger(t *testing.T) {
	for _, reason := range []string{"PodFailed", "ContainerRestarted", "MemberTerminating"} {
		t.Run(reason, func(t *testing.T) {
			ctx := context.Background()
			c, instance, pods := budgetFixture(t, ptr.To[int32](1))
			pod := pods[0]
			switch reason {
			case "ContainerRestarted":
				pod.Status.Phase = v1.PodRunning
				pod.Status.ContainerStatuses = []v1.ContainerStatus{{Name: "worker", RestartCount: 1}}
				require.NoError(t, c.Status().Update(ctx, pod))
			case "MemberTerminating":
				pod.Finalizers = []string{"test.rbg.io/hold"}
				require.NoError(t, c.Client.Update(ctx, pod))
				require.NoError(t, c.Delete(ctx, pod))
			}
			recorder := c.recorder.(*record.FakeRecorder)
			_, accepted, err := c.acceptBudgetRestart(ctx, instance, budgetTemplateRevision(t, instance))
			require.NoError(t, err)
			require.True(t, accepted)
			select {
			case event := <-recorder.Events:
				require.Contains(t, event, "Normal ReCreateInstance RestartPolicy is RecreateRoleInstanceOnPodRestart,")
				require.Contains(t, event, "reason="+reason+", pod="+pod.Name+", restartCount=1")
				require.EqualValues(t, 1, liveBudgetInstance(t, c, instance).Status.GroupRestartCount)
			default:
				t.Fatal("accepted budget restart must emit its live trigger")
			}
			_, accepted, err = c.acceptBudgetRestart(ctx, instance, budgetTemplateRevision(t, instance))
			require.NoError(t, err)
			require.False(t, accepted)
			require.Empty(t, recorder.Events, "same accepted round must not emit again")
		})
	}
}

func TestBudgetDeniedRestartDoesNotEmitRecreation(t *testing.T) {
	for _, denial := range []string{"zero", "conflict", "stale-evidence", "backoff"} {
		t.Run(denial, func(t *testing.T) {
			ctx := context.Background()
			c, instance, pods := budgetFixture(t, ptr.To[int32](1))
			switch denial {
			case "zero":
				instance.Spec.RestartPolicyConfig.MaxGroupRestarts = ptr.To[int32](0)
				require.NoError(t, c.Client.Update(ctx, instance))
			case "conflict":
				c.Client = conflictingBudgetClient{c.Client}
			case "stale-evidence":
				pods[0].Status.Phase = v1.PodRunning
				require.NoError(t, c.Status().Update(ctx, pods[0]))
			case "backoff":
				instance.Status.RestartCount = 1
				instance.Status.LastRestartTime = ptr.To(metav1.Now())
				require.NoError(t, c.Status().Update(ctx, instance))
			}
			recorder := c.recorder.(*record.FakeRecorder)
			if denial == "backoff" {
				diff, err := c.calculateDiffsWithExpectation(ctx, instance, nil, nil, nil, pods[1:], pods[:1])
				require.NoError(t, err)
				require.Positive(t, diff.requeueAfter)
			} else {
				_, accepted, err := c.acceptBudgetRestart(ctx, instance, budgetTemplateRevision(t, instance))
				if denial == "conflict" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				require.False(t, accepted)
			}
			for len(recorder.Events) > 0 {
				require.False(t, strings.Contains(<-recorder.Events, "ReCreateInstance"), "denied/deferred rebuild is not accepted")
			}
			if denial == "zero" {
				require.Contains(t, liveBudgetInstance(t, c, instance).Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
			}
		})
	}
}

// legacyGroupRestartRevision reproduces the budget revision persisted by builds
// up to v0.9.0-alpha.5: a bare SHA-256 of the name-sorted component JSON.
func legacyGroupRestartRevision(t *testing.T, instance *workloadsv1alpha2.RoleInstance) string {
	t.Helper()
	components := instance.DeepCopy().Spec.Components
	slices.SortFunc(components, func(a, b workloadsv1alpha2.RoleInstanceComponent) int { return strings.Compare(a.Name, b.Name) })
	data, err := json.Marshal(components)
	require.NoError(t, err)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func TestRestartBudgetUpgradeFromLegacyRevisionFormat(t *testing.T) {
	for _, state := range []string{"exhausted", "spent", "legacy-owner"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			limit := int32(2)
			if state == "exhausted" {
				limit = 1
			}
			c, instance, pods := budgetFixture(t, ptr.To(limit))
			selected := budgetTemplateRevision(t, instance)
			instance.Status.GroupRestartCount = 1
			require.NoError(t, c.Status().Update(ctx, instance))
			if state == "exhausted" {
				// pods[0] is Failed: the spent budget is exhausted and the group retained.
				blocked, err := c.reconcileRestartBudget(ctx, instance, selected)
				require.NoError(t, err)
				require.True(t, blocked)
			} else {
				pods[0].Status.Phase = v1.PodRunning
				require.NoError(t, c.Status().Update(ctx, pods[0]))
			}
			// Persist the state an alpha.5 controller leaves behind.
			live := liveBudgetInstance(t, c, instance)
			if state == "legacy-owner" {
				live.Finalizers = []string{constants.GroupRestartBudgetCleanupFinalizer}
				require.NoError(t, c.Client.Update(ctx, live))
			}
			legacy := legacyGroupRestartRevision(t, live)
			require.True(t, isLegacyGroupRestartRevision(legacy))
			require.False(t, isLegacyGroupRestartRevision(groupRestartRevision(live, selected)))
			live.Status.GroupRestartRevision = legacy
			require.NoError(t, c.Status().Update(ctx, live))

			recorder := record.NewFakeRecorder(20)
			c.recorder = recorder
			for pass := 0; pass < 3; pass++ {
				live = liveBudgetInstance(t, c, instance)
				_, err := c.reconcileRestartBudgetOnce(WithBudgetObservation(ctx), live, selected, pods)
				require.NoError(t, err)
			}
			live = liveBudgetInstance(t, c, instance)
			require.Equal(t, groupRestartRevision(live, selected), live.Status.GroupRestartRevision, "the legacy revision is re-stamped in place")
			require.EqualValues(t, 1, live.Status.GroupRestartCount, "the format change must not grant a fresh budget")
			require.Equal(t, state == "exhausted", live.Status.RestartBudgetExhausted, "the format change must not release exhaustion")
			require.Empty(t, live.Annotations[constants.RoleInstanceRecoverAnnotationKey])
			for len(recorder.Events) > 0 {
				require.NotContains(t, <-recorder.Events, GroupRestartBudgetRecovered)
			}
			if state == "exhausted" {
				require.Contains(t, live.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
				for _, pod := range pods {
					got := &v1.Pod{}
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), got), "retained Pods must survive the upgrade")
					require.Contains(t, got.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
				}
				return
			}
			require.NotContains(t, live.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)

			// A real topology change after migration still starts a fresh budget.
			live.Spec.Components[0].Template.Spec.Containers[0].Image = "v2"
			require.NoError(t, c.Client.Update(ctx, live))
			live = liveBudgetInstance(t, c, instance)
			next := budgetTemplateRevision(t, live)
			require.NotEqual(t, selected, next)
			_, err := c.reconcileRestartBudget(ctx, live, next)
			require.NoError(t, err)
			live = liveBudgetInstance(t, c, instance)
			require.Zero(t, live.Status.GroupRestartCount)
			require.Equal(t, groupRestartRevision(live, next), live.Status.GroupRestartRevision)
		})
	}
}

func TestRestartBudgetDrainSkipsRedundantPodReads(t *testing.T) {
	ctx := context.Background()
	c, instance, pods := budgetFixture(t, ptr.To[int32](0))
	for _, pod := range pods {
		pod.Finalizers = []string{"test.rbg.io/hold-old-member"}
		require.NoError(t, c.Client.Update(ctx, pod))
	}
	selected := budgetTemplateRevision(t, instance)
	blocked, err := c.reconcileRestartBudget(ctx, instance, selected)
	require.NoError(t, err)
	require.True(t, blocked)
	live := liveBudgetInstance(t, c, instance)
	live.Annotations = map[string]string{constants.RoleInstanceRecoverAnnotationKey: "true"}
	require.NoError(t, c.Client.Update(ctx, live))
	counter := &budgetIOCounter{Client: c.Client}
	c.Client, c.apiReader = counter, counter

	// First recovery pass: both Pods are Terminating and still retained, so
	// only the finalizer release reads them (once each, inside its retry).
	live = liveBudgetInstance(t, c, instance)
	counter.gets, counter.lists, counter.patches = 0, 0, 0
	blocked, err = c.reconcileRestartBudget(ctx, live, selected)
	require.NoError(t, err)
	require.True(t, blocked)
	require.Equal(t, 1+len(pods), counter.gets, "one RI read plus one release read per retained Pod")
	for _, pod := range pods {
		got := &v1.Pod{}
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), got))
		require.NotContains(t, got.Finalizers, constants.GroupRestartBudgetCleanupFinalizer)
	}

	// Waiting on a foreign finalizer: no per-Pod reads remain.
	for pass := 0; pass < 2; pass++ {
		live = liveBudgetInstance(t, c, instance)
		counter.gets, counter.lists, counter.patches = 0, 0, 0
		blocked, err = c.reconcileRestartBudget(ctx, live, selected)
		require.NoError(t, err)
		require.True(t, blocked)
		require.Equal(t, 1, counter.gets, "already terminating, released Pods need no GET")
		require.Equal(t, 2, counter.lists, "drain list plus the authoritative remaining list")
		require.True(t, liveBudgetInstance(t, c, instance).Status.RestartBudgetExhausted)
	}
}
