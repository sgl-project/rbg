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
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/features"
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
	features.SetRoleInstanceLifecycleHooks(true)
	t.Cleanup(func() { features.SetRoleInstanceLifecycleHooks(false) })
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

// Removing every hook (or disabling the gate) must not leave the reserved
// preDelete finalizer behind: deletion would hang forever on state the
// controller no longer owns.
func TestReconcileLifecycleDeletionRemovesFinalizerWithoutActiveHook(t *testing.T) {
	ctx := context.Background()
	removedHooks := newReadinessTestInstance()
	removedHooks.Status.ObservedGeneration = 1
	removedHooks.Finalizers = []string{constants.RoleInstancePreDeleteFinalizer, "test.rbg.io/hold"}
	removedHooks.DeletionTimestamp = ptr.To(metav1.Now())

	gateDisabled := newReadinessTestInstance()
	gateDisabled.Status.ObservedGeneration = 1
	gateDisabled.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/drain": "true"},
		},
	}
	gateDisabled.Finalizers = []string{constants.RoleInstancePreDeleteFinalizer, "test.rbg.io/hold"}
	gateDisabled.DeletionTimestamp = ptr.To(metav1.Now())

	for name, test := range map[string]struct {
		instance *workloadsv1alpha2.RoleInstance
		gateOn   bool
	}{
		"hooks removed": {instance: removedHooks, gateOn: true},
		"gate disabled": {instance: gateDisabled, gateOn: false},
	} {
		t.Run(name, func(t *testing.T) {
			c := newLifecycleTestClient(t, test.instance)
			r := newLifecycleTestReconciler(t, c)
			features.SetRoleInstanceLifecycleHooks(test.gateOn)

			blocked, err := r.reconcileLifecycleDeletion(ctx, test.instance)
			require.NoError(t, err)
			require.False(t, blocked)

			live := requireLifecycleInstance(t, c, test.instance)
			require.NotContains(t, live.Finalizers, constants.RoleInstancePreDeleteFinalizer)
		})
	}
}

// A genuinely new instance must not enter preRecover before its first Pod
// exists: the phase machine immediately classifies it as Creating and the
// next reconcile would bypass the pending hook, leaving a spurious signal.
func TestPrepareLifecycleOperationSkipsPreRecoverForNewInstance(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
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

// With the gate disabled, the finalizer must still be removed while the
// configured signal labels are left for a controller restart that re-enables
// the feature.
func TestPrepareLifecycleOperationRemovesFinalizerWhenGateDisabled(t *testing.T) {
	ctx := context.Background()
	instance := newReadinessTestInstance()
	instance.Status.ObservedGeneration = 1
	instance.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/drain": "true"},
		},
	}
	instance.Labels = map[string]string{
		constants.RoleInstanceLifecycleHookStateKey: string(constants.RoleInstanceHookStatePreDelete),
		"consumer.example/drain":                    "true",
	}
	instance.Finalizers = []string{constants.RoleInstancePreDeleteFinalizer}
	c := newLifecycleTestClient(t, instance)
	r := newLifecycleTestReconciler(t, c)
	features.SetRoleInstanceLifecycleHooks(false)

	newStatus := &workloadsv1alpha2.RoleInstanceStatus{}
	_, err := r.prepareLifecycleOperation(ctx, instance, newStatus, roleInstancePhaseFacts{
		podsPresent: false,
	}, nil)
	require.NoError(t, err)

	live := requireLifecycleInstance(t, c, instance)
	require.NotContains(t, live.Finalizers, constants.RoleInstancePreDeleteFinalizer)
	require.Equal(t, "true", live.Labels["consumer.example/drain"],
		"a disabled gate must not rewrite signal labels")
}
