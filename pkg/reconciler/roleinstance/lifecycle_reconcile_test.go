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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	roleinstancelifecycle "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/lifecycle"
	"sigs.k8s.io/rbgs/pkg/utils/fieldindex"
)

func newLifecycleTestClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	builder := fake.NewClientBuilder().
		WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithIndex(&v1.Pod{}, fieldindex.IndexNameForOwnerRefUID, func(obj client.Object) []string {
			keys := make([]string, 0, len(obj.GetOwnerReferences()))
			for _, ref := range obj.GetOwnerReferences() {
				keys = append(keys, string(ref.UID))
			}
			return keys
		})
	for _, obj := range objects {
		builder.WithObjects(obj)
	}
	return builder.Build()
}

func newLifecycleTestReconciler(t *testing.T, c client.Client) *reconciler {
	t.Helper()
	return &reconciler{
		Client:      c,
		hookControl: roleinstancelifecycle.NewHookControl(c),
		recorder:    record.NewFakeRecorder(20),
	}
}

func lifecycleTestPod(instance *workloadsv1alpha2.RoleInstance) *v1.Pod {
	pod := newReadinessTestPod(instance, "router")
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[constants.RoleInstanceNameLabelKey] = instance.Name
	return pod
}

func requireLifecycleInstance(t *testing.T, c client.Client, instance *workloadsv1alpha2.RoleInstance) *workloadsv1alpha2.RoleInstance {
	t.Helper()
	live := &workloadsv1alpha2.RoleInstance{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), live))
	return live
}

// A deletion retry in Terminating must resume cleanup: replaying preDelete
// would reinstall the released signal labels and block deletion until the
// executor acknowledges a second time.
func TestReconcileLifecycleDeletionResumesTerminatingWithoutReplay(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = 1
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/drain": "true"},
		},
	}
	instance.Labels = map[string]string{
		constants.RoleInstanceLifecycleHookStateKey: string(constants.RoleInstanceHookStateTerminating),
	}
	instance.Annotations = map[string]string{
		constants.RoleInstanceLifecycleHookSignalLabelKeys: "consumer.example/drain",
	}
	instance.Finalizers = []string{constants.RoleInstancePreDeleteFinalizer, "test.rbg.io/hold"}
	instance.DeletionTimestamp = ptr.To(metav1.Now())
	pod := lifecycleTestPod(instance)
	c := newLifecycleTestClient(t, instance, pod)
	r := newLifecycleTestReconciler(t, c)

	blocked, err := r.reconcileLifecycleDeletion(ctx, instance)
	require.NoError(t, err)
	require.False(t, blocked, "Terminating must resume cleanup without waiting for another acknowledgement")

	live := requireLifecycleInstance(t, c, instance)
	require.NotContains(t, live.Finalizers, constants.RoleInstancePreDeleteFinalizer)
	require.NotContains(t, live.Labels, "consumer.example/drain")
	require.Equal(t, constants.RoleInstanceHookStateTerminating, roleinstancelifecycle.HookState(live))
	require.Equal(t, workloadsv1alpha2.RoleInstancePhaseTerminating, live.Status.Phase)
	err = c.Get(ctx, client.ObjectKeyFromObject(pod), &v1.Pod{})
	require.Error(t, err, "member Pod must be deleted")
}

// Member Pods are deleted with a UID precondition so a stale cached object
// cannot remove a same-name replacement.
func TestReconcileLifecycleDeletionDeletesPodsWithUIDPrecondition(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = 1
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/drain": "true"},
		},
	}
	instance.Labels = map[string]string{
		constants.RoleInstanceLifecycleHookStateKey: string(constants.RoleInstanceHookStateTerminating),
	}
	instance.Finalizers = []string{constants.RoleInstancePreDeleteFinalizer, "test.rbg.io/hold"}
	instance.DeletionTimestamp = ptr.To(metav1.Now())
	pod := lifecycleTestPod(instance)
	pod.UID = "pod-uid-42"

	var deletedPreconditions []*metav1.Preconditions
	c := fake.NewClientBuilder().
		WithScheme(readinessTestScheme(t)).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(instance, pod).
		WithIndex(&v1.Pod{}, fieldindex.IndexNameForOwnerRefUID, func(obj client.Object) []string {
			keys := make([]string, 0, len(obj.GetOwnerReferences()))
			for _, ref := range obj.GetOwnerReferences() {
				keys = append(keys, string(ref.UID))
			}
			return keys
		}).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				deleteOptions := &client.DeleteOptions{}
				deleteOptions.ApplyOptions(opts)
				deletedPreconditions = append(deletedPreconditions, deleteOptions.Preconditions)
				return c.Delete(ctx, obj, opts...)
			},
		}).
		Build()
	r := &reconciler{
		Client:      c,
		hookControl: roleinstancelifecycle.NewHookControl(c),
		recorder:    record.NewFakeRecorder(20),
	}

	blocked, err := r.reconcileLifecycleDeletion(ctx, instance)
	require.NoError(t, err)
	require.False(t, blocked)
	require.Len(t, deletedPreconditions, 1)
	require.NotNil(t, deletedPreconditions[0], "Pod deletion must carry a UID precondition")
	require.NotNil(t, deletedPreconditions[0].UID)
	require.Equal(t, types.UID("pod-uid-42"), *deletedPreconditions[0].UID)
}

// Removing every hook must not leave the reserved
// preDelete finalizer behind: deletion would hang forever on state the
// controller no longer owns.
func TestReconcileLifecycleDeletionRemovesFinalizerWithoutActiveHook(t *testing.T) {
	ctx := context.Background()
	removedHooks := newReadinessTestInstance()
	removedHooks.Status.ObservedGeneration = 1
	removedHooks.Finalizers = []string{constants.RoleInstancePreDeleteFinalizer, "test.rbg.io/hold"}
	removedHooks.DeletionTimestamp = ptr.To(metav1.Now())

	c := newLifecycleTestClient(t, removedHooks)
	r := newLifecycleTestReconciler(t, c)

	blocked, err := r.reconcileLifecycleDeletion(ctx, removedHooks)
	require.NoError(t, err)
	require.False(t, blocked)

	live := requireLifecycleInstance(t, c, removedHooks)
	require.NotContains(t, live.Finalizers, constants.RoleInstancePreDeleteFinalizer)
}

// A genuinely new instance must not enter preRecover before its first Pod
// exists: the phase machine immediately classifies it as Creating and the
// next reconcile would bypass the pending hook, leaving a spurious signal.
func TestPrepareLifecycleOperationSkipsPreRecoverForNewInstance(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreUpdate: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/update": "blocked"},
		},
		PreRecover: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/recover": "true"},
		},
	}
	c := newLifecycleTestClient(t, instance)
	r := newLifecycleTestReconciler(t, c)

	newStatus := &workloadsv1alpha2.RoleInstanceStatus{}
	result, err := r.prepareLifecycleOperation(ctx, instance, newStatus, roleInstancePhaseFacts{
		podsPresent: false,
		newInstance: true,
	}, nil)
	require.NoError(t, err)
	require.False(t, result.blocked)
	require.False(t, result.operation)

	live := requireLifecycleInstance(t, c, instance)
	require.NotContains(t, live.Labels, "consumer.example/recover")
	require.Empty(t, roleinstancelifecycle.HookState(live))
}

// A recovery retry in Recovering must resume Pod creation: replaying
// preRecover would reinstall released signal labels and ask the executor to
// acknowledge the same operation again.
func TestPrepareLifecycleOperationResumesRecoveringWithoutReplay(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = 1
	instance.Status.Phase = workloadsv1alpha2.RoleInstancePhaseRecovering
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreRecover: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/recover": "true"},
		},
	}
	instance.Labels = map[string]string{
		constants.RoleInstanceLifecycleHookStateKey: string(constants.RoleInstanceHookStateRecovering),
	}
	instance.Annotations = map[string]string{
		constants.RoleInstanceLifecycleHookSignalLabelKeys: "consumer.example/recover",
	}
	c := newLifecycleTestClient(t, instance)
	r := newLifecycleTestReconciler(t, c)

	newStatus := &workloadsv1alpha2.RoleInstanceStatus{}
	result, err := r.prepareLifecycleOperation(ctx, instance, newStatus, roleInstancePhaseFacts{
		podsPresent: false,
	}, nil)
	require.NoError(t, err)
	require.False(t, result.blocked)
	require.True(t, result.operation)

	live := requireLifecycleInstance(t, c, instance)
	require.NotContains(t, live.Labels, "consumer.example/recover")
	require.Equal(t, constants.RoleInstanceHookStateRecovering, roleinstancelifecycle.HookState(live))
}

// Mirror of the set-side arbitration: while the owning RoleInstanceSet
// controller holds an armed preUpdate, the RoleInstance controller must not
// enter preRecover on the same hook-state label.
func TestPrepareLifecycleOperationDefersPreRecoverForArmedPreUpdate(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = 1
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		// A preUpdate armed by the set controller is always paired with a
		// configured preUpdate hook; otherwise the state is stale and cleanup
		// correctly clears it before preRecover may enter.
		PreUpdate: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/update": "blocked"},
		},
		PreRecover: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/recover": "true"},
		},
	}
	instance.Labels = map[string]string{
		constants.RoleInstanceLifecycleHookStateKey: string(constants.RoleInstanceHookStatePreUpdate),
		"consumer.example/update":                   "blocked",
	}
	c := newLifecycleTestClient(t, instance)
	r := newLifecycleTestReconciler(t, c)

	newStatus := &workloadsv1alpha2.RoleInstanceStatus{}
	result, err := r.prepareLifecycleOperation(ctx, instance, newStatus, roleInstancePhaseFacts{
		podsPresent: false,
	}, nil)
	require.NoError(t, err)
	require.True(t, result.blocked, "recovery must defer while the set controller gates an update")
	require.True(t, result.operation)

	live := requireLifecycleInstance(t, c, instance)
	require.Equal(t, constants.RoleInstanceHookStatePreUpdate, roleinstancelifecycle.HookState(live),
		"an armed preUpdate must not be overwritten by a preRecover entry")
	require.NotContains(t, live.Labels, "consumer.example/recover")
}

// A generation created by a lifecycle snapshot sync is not a spec-driven
// workload update: the phase machine must classify the instance as recovering
// and enter preRecover, not the update branch.
func TestPrepareLifecycleOperationIgnoresLifecycleOnlyGenerationBump(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = 1
	instance.Status.LastPhaseCompleteGeneration = 1
	instance.Generation = 2
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreRecover: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/recover": "true"},
		},
	}
	instance.Annotations = map[string]string{
		constants.RoleInstanceLifecycleSnapshotGenerationAnnotationKey: "2",
	}
	c := newLifecycleTestClient(t, instance)
	r := newLifecycleTestReconciler(t, c)

	newStatus := &workloadsv1alpha2.RoleInstanceStatus{}
	result, err := r.prepareLifecycleOperation(ctx, instance, newStatus, roleInstancePhaseFacts{
		podsPresent: false,
	}, nil)
	require.NoError(t, err)
	require.True(t, result.blocked, "the lifecycle-only bump must classify as recovery, not update")
	require.True(t, result.operation)

	live := requireLifecycleInstance(t, c, instance)
	require.Equal(t, constants.RoleInstanceHookStatePreRecover, roleinstancelifecycle.HookState(live))
	require.Contains(t, live.Labels, "consumer.example/recover")
}

// When no hooks remain configured, reconciliation must remove stale protocol
// labels, the hook-state label, and the preDelete finalizer instead of
// skipping cleanup entirely.
func TestPrepareLifecycleOperationCleansStaleLifecycleState(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = 1
	instance.Labels = map[string]string{
		constants.RoleInstanceLifecycleHookStateKey: string(constants.RoleInstanceHookStatePreUpdate),
		"consumer.example/update":                   "blocked",
	}
	instance.Annotations = map[string]string{
		constants.RoleInstanceLifecycleHookSignalLabelKeys: "consumer.example/update",
	}
	instance.Finalizers = []string{constants.RoleInstancePreDeleteFinalizer}
	pod := lifecycleTestPod(instance)
	pod.Labels["consumer.example/update"] = "blocked"
	c := newLifecycleTestClient(t, instance, pod)
	r := newLifecycleTestReconciler(t, c)

	newStatus := &workloadsv1alpha2.RoleInstanceStatus{}
	_, err := r.prepareLifecycleOperation(ctx, instance, newStatus, roleInstancePhaseFacts{
		podsPresent: false,
	}, []*v1.Pod{pod})
	require.NoError(t, err)

	live := requireLifecycleInstance(t, c, instance)
	require.NotContains(t, live.Labels, "consumer.example/update")
	require.Empty(t, roleinstancelifecycle.HookState(live))
	require.NotContains(t, live.Annotations, constants.RoleInstanceLifecycleHookSignalLabelKeys)
	require.NotContains(t, live.Finalizers, constants.RoleInstancePreDeleteFinalizer)
	storedPod := &v1.Pod{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), storedPod))
	require.NotContains(t, storedPod.Labels, "consumer.example/update")
}

// A controlled operation that reaches readiness with postReady configured
// enters the asynchronous hook: the state label is armed and the signal labels
// are mirrored to the workload Pod.
func TestFinishLifecycleOperationEntersPostReady(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = 1
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PostReady: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/ready": "true"},
		},
	}
	pod := lifecycleTestPod(instance)
	c := newLifecycleTestClient(t, instance, pod)
	r := newLifecycleTestReconciler(t, c)

	newStatus := &workloadsv1alpha2.RoleInstanceStatus{
		UpdateRevision:  "rev-1",
		CurrentRevision: "rev-1",
	}
	err := r.finishLifecycleOperation(ctx, instance, newStatus, roleInstancePhaseFacts{
		podsPresent:    true,
		readySatisfied: true,
	}, true, []*v1.Pod{pod})
	require.NoError(t, err)

	live := requireLifecycleInstance(t, c, instance)
	require.Equal(t, constants.RoleInstanceHookStatePostReady, roleinstancelifecycle.HookState(live))
	require.Equal(t, "true", live.Labels["consumer.example/ready"])
	storedPod := &v1.Pod{}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pod), storedPod))
	require.Equal(t, "true", storedPod.Labels["consumer.example/ready"])
}

// postReady must not overwrite a pre-operation barrier armed by the other
// controller writer on the shared hook-state label.
func TestFinishLifecycleOperationDefersPostReadyBehindArmedPreUpdate(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = 1
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreUpdate: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/update": "blocked"},
		},
		PostReady: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/ready": "true"},
		},
	}
	instance.Labels = map[string]string{
		constants.RoleInstanceLifecycleHookStateKey: string(constants.RoleInstanceHookStatePreUpdate),
		"consumer.example/update":                   "blocked",
	}
	c := newLifecycleTestClient(t, instance)
	r := newLifecycleTestReconciler(t, c)

	newStatus := &workloadsv1alpha2.RoleInstanceStatus{
		UpdateRevision:  "rev-1",
		CurrentRevision: "rev-1",
	}
	err := r.finishLifecycleOperation(ctx, instance, newStatus, roleInstancePhaseFacts{
		podsPresent:    true,
		readySatisfied: true,
	}, true, nil)
	require.NoError(t, err)

	live := requireLifecycleInstance(t, c, instance)
	require.Equal(t, constants.RoleInstanceHookStatePreUpdate, roleinstancelifecycle.HookState(live))
	require.NotContains(t, live.Labels, "consumer.example/ready")
}

// Without postReady, resolving a controlled operation clears the grant state
// so a cancelled or already-applied update cannot linger forever.
func TestFinishLifecycleOperationClearsStateWithoutPostReadyHook(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = 1
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreRecover: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/recover": "true"},
		},
	}
	instance.Labels = map[string]string{
		constants.RoleInstanceLifecycleHookStateKey: string(constants.RoleInstanceHookStateUpdating),
	}
	c := newLifecycleTestClient(t, instance)
	r := newLifecycleTestReconciler(t, c)

	newStatus := &workloadsv1alpha2.RoleInstanceStatus{
		UpdateRevision:  "rev-1",
		CurrentRevision: "rev-1",
	}
	err := r.finishLifecycleOperation(ctx, instance, newStatus, roleInstancePhaseFacts{
		podsPresent:    true,
		readySatisfied: true,
	}, false, nil)
	require.NoError(t, err)

	live := requireLifecycleInstance(t, c, instance)
	require.Empty(t, roleinstancelifecycle.HookState(live))
}

// A steady, already-ready instance with no observed operation must not enter
// postReady on every reconcile; the hook fires on transitions only.
func TestFinishLifecycleOperationSkipsSteadyReadyInstance(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = 1
	instance.Status.Phase = workloadsv1alpha2.RoleInstancePhaseRunning
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PostReady: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/ready": "true"},
		},
	}
	c := newLifecycleTestClient(t, instance)
	r := newLifecycleTestReconciler(t, c)

	newStatus := &workloadsv1alpha2.RoleInstanceStatus{
		UpdateRevision:  "rev-1",
		CurrentRevision: "rev-1",
	}
	err := r.finishLifecycleOperation(ctx, instance, newStatus, roleInstancePhaseFacts{
		podsPresent:    true,
		readySatisfied: true,
	}, false, nil)
	require.NoError(t, err)

	live := requireLifecycleInstance(t, c, instance)
	require.Empty(t, roleinstancelifecycle.HookState(live))
}

// After the postReady executor removes the signal label, reconciliation
// clears the asynchronous state instead of re-arming it on every pass.
func TestPrepareLifecycleOperationCleansReleasedPostReady(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = 1
	instance.Status.Phase = workloadsv1alpha2.RoleInstancePhaseRunning
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PostReady: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/ready": "true"},
		},
	}
	instance.Labels = map[string]string{
		constants.RoleInstanceLifecycleHookStateKey: string(constants.RoleInstanceHookStatePostReady),
	}
	instance.Annotations = map[string]string{
		constants.RoleInstanceLifecycleHookSignalLabelKeys: "consumer.example/ready",
	}
	c := newLifecycleTestClient(t, instance)
	r := newLifecycleTestReconciler(t, c)

	newStatus := &workloadsv1alpha2.RoleInstanceStatus{}
	_, err := r.prepareLifecycleOperation(ctx, instance, newStatus, roleInstancePhaseFacts{
		podsPresent: true,
	}, nil)
	require.NoError(t, err)

	live := requireLifecycleInstance(t, c, instance)
	require.Empty(t, roleinstancelifecycle.HookState(live))
	require.NotContains(t, live.Annotations, constants.RoleInstanceLifecycleHookSignalLabelKeys)
}

// A state whose owning hook was removed from the configuration is stale even
// when other hooks remain: it must be cleared before the remaining protocol
// can proceed, instead of blocking the instance forever.
func TestPrepareLifecycleOperationClearsStaleStateWhenOwningHookRemoved(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = 1
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreRecover: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/recover": "true"},
		},
	}
	instance.Labels = map[string]string{
		constants.RoleInstanceLifecycleHookStateKey: string(constants.RoleInstanceHookStatePreUpdate),
		"consumer.example/update":                   "blocked",
	}
	instance.Annotations = map[string]string{
		constants.RoleInstanceLifecycleHookSignalLabelKeys: "consumer.example/update",
	}
	c := newLifecycleTestClient(t, instance)
	r := newLifecycleTestReconciler(t, c)

	newStatus := &workloadsv1alpha2.RoleInstanceStatus{}
	_, err := r.prepareLifecycleOperation(ctx, instance, newStatus, roleInstancePhaseFacts{
		podsPresent: false,
	}, nil)
	require.NoError(t, err)

	live := requireLifecycleInstance(t, c, instance)
	require.NotContains(t, live.Labels, "consumer.example/update")
	require.Equal(t, constants.RoleInstanceHookStatePreRecover, roleinstancelifecycle.HookState(live))
	require.Contains(t, live.Labels, "consumer.example/recover")
}

// The controller must fail safely when admission is bypassed: a reserved-prefix
// signal key cannot execute the protocol or forge protocol state, and any
// existing state is cleaned up instead.
func TestLifecycleEnabledRejectsReservedSignalKeys(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = 1
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{
				constants.RBGPrefix + "fake-state": "Terminating",
			},
		},
	}
	instance.Labels = map[string]string{
		constants.RoleInstanceLifecycleHookStateKey: string(constants.RoleInstanceHookStateTerminating),
		"consumer.example/drain":                    "true",
	}
	instance.Annotations = map[string]string{
		constants.RoleInstanceLifecycleHookSignalLabelKeys: "consumer.example/drain",
	}
	instance.Finalizers = []string{constants.RoleInstancePreDeleteFinalizer}
	c := newLifecycleTestClient(t, instance)
	r := newLifecycleTestReconciler(t, c)
	require.False(t, r.lifecycleEnabled(instance))

	newStatus := &workloadsv1alpha2.RoleInstanceStatus{}
	_, err := r.prepareLifecycleOperation(ctx, instance, newStatus, roleInstancePhaseFacts{
		podsPresent: false,
	}, nil)
	require.NoError(t, err)

	live := requireLifecycleInstance(t, c, instance)
	require.Empty(t, roleinstancelifecycle.HookState(live))
	require.NotContains(t, live.Labels, "consumer.example/drain")
	require.NotContains(t, live.Finalizers, constants.RoleInstancePreDeleteFinalizer)
}

// A lifecycle-only generation bump is only masked when the pre-snapshot
// generation was phase-confirmed; an in-flight workload update must not be
// misclassified as recovery.
func TestLifecycleOnlyGenerationBumpRequiresConfirmedGeneration(t *testing.T) {
	confirmed := newReadinessTestInstance()
	confirmed.Generation = 2
	confirmed.Status.ObservedGeneration = 1
	confirmed.Status.LastPhaseCompleteGeneration = 1
	confirmed.Annotations = map[string]string{
		constants.RoleInstanceLifecycleSnapshotGenerationAnnotationKey: "2",
	}
	require.True(t, lifecycleOnlyGenerationBump(confirmed))

	inFlight := confirmed.DeepCopy()
	inFlight.Status.LastPhaseCompleteGeneration = 0
	require.False(t, lifecycleOnlyGenerationBump(inFlight))
}
