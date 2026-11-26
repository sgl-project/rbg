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

package statefulmode

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	instancelifecycle "sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/readiness"
	roleinstancelifecycle "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/lifecycle"
)

const preDeleteHoldLabel = "consumer.example/held"

func holdPreDelete(t *testing.T, h *rollbackTestHarness, markNotReady bool, instances ...*workloadsv1alpha2.RoleInstance) client.WithWatch {
	t.Helper()
	h.set.Spec.Lifecycle = &workloadsv1alpha2.RoleInstanceSetLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceSetLifecycleHook{
			LabelsHandler: map[string]string{preDeleteHoldLabel: "true"},
			MarkNotReady:  markNotReady,
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	builder := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&workloadsv1alpha2.RoleInstance{})
	for _, instance := range instances {
		instance.Labels[preDeleteHoldLabel] = "true"
		if markNotReady {
			instance.Spec.ReadinessGates = []workloadsv1alpha2.RoleInstanceReadinessGate{{ConditionType: workloadsv1alpha2.RoleInstanceCustomReady}}
			instance.Status.Conditions = append(instance.Status.Conditions,
				workloadsv1alpha2.RoleInstanceCondition{Type: workloadsv1alpha2.RoleInstanceCustomReady, Status: corev1.ConditionTrue})
		}
		builder.WithObjects(instance)
	}
	c := builder.Build()
	h.control.lifecycleControl = instancelifecycle.New(c)
	return c
}

func readLifecycle(t *testing.T, c client.Client, instance *workloadsv1alpha2.RoleInstance) constants.RoleInstanceLifecycleStateType {
	t.Helper()
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), instance))
	return instancelifecycle.GetRoleInstanceLifecycleState(instance)
}

func TestStatefulRollingUpdateAndScaleDownWaitForPreDeleteHook(t *testing.T) {
	for _, scaleDown := range []bool{false, true} {
		name := "rolling-update"
		if scaleDown {
			name = "scale-down"
		}
		t.Run(name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "hook-"+name, 1, 0, 0)
			h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
			if scaleDown {
				h.set.Spec.Replicas = ptr.To(int32(0))
			}
			instance := h.instance(0, h.intermediate.Name, true)
			instance.ResourceVersion = "1"
			c := holdPreDelete(t, h, false, instance)
			for pass := 0; pass < 2; pass++ {
				h.reconcile(t, instance)
				h.assertDeleted(t)
				require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, instance))
			}
			delete(instance.Labels, preDeleteHoldLabel)
			require.NoError(t, c.Update(context.Background(), instance))
			h.reconcile(t, instance)
			h.assertDeleted(t, instance.Name)
		})
	}
}

func TestStatefulPreUpdateBlocksWorkloadUpdateAndClearsPodMirror(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-pre-update", 1, 0, 0)
	h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreUpdate: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"patio.workloads.x-k8s.io/pre-update": "blocked"},
		},
	}
	instance := h.instance(0, h.intermediate.Name, true)
	instance.ResourceVersion = "1"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				constants.RoleInstanceNameLabelKey: instance.Name,
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				instance, workloadsv1alpha2.GroupVersion.WithKind("RoleInstance"))},
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(instance, pod).
		Build()
	h.control.hookControl = roleinstancelifecycle.NewHookControl(c)
	h.control.client = c

	h.reconcile(t, instance)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceHookStatePreUpdate, roleinstancelifecycle.HookState(instance))
	require.Equal(t, "blocked", instance.Labels["patio.workloads.x-k8s.io/pre-update"])
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pod), pod))
	require.Equal(t, "blocked", pod.Labels["patio.workloads.x-k8s.io/pre-update"])

	delete(instance.Labels, "patio.workloads.x-k8s.io/pre-update")
	require.NoError(t, c.Update(context.Background(), instance))
	h.reconcile(t, instance)
	h.assertDeleted(t, instance.Name)
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pod), pod))
	require.Empty(t, pod.Labels["patio.workloads.x-k8s.io/pre-update"])
}

// KEP-527 preDelete is not a set-level barrier: the set controller deletes
// the RoleInstance, and the drain runs inside the finalizer-held deletion.
// The set controller must not write hook state or signal labels itself.
func TestStatefulPreDeleteRunsDuringRoleInstanceDeletion(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-delete-then-hook", 1, 0, 0)
	h.set.Spec.Replicas = ptr.To(int32(0))
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/drain": "true"},
		},
	}
	instance := h.instance(0, h.current.Name, true)
	instance.ResourceVersion = "1"
	// The RI controller installs the reserved finalizer before deletion
	// begins; the gate in processCondemned relies on it being present.
	instance.Finalizers = []string{constants.RoleInstancePreDeleteFinalizer}
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(instance).
		Build()
	h.control.hookControl = roleinstancelifecycle.NewHookControl(c)
	h.control.client = c

	h.reconcile(t, instance)
	h.assertDeleted(t, instance.Name)
	require.Empty(t, roleinstancelifecycle.HookState(instance),
		"the set controller must not arm preDelete itself")
	require.NotContains(t, instance.Labels, "consumer.example/drain")
}

// While a finalizer-held deletion is still draining its preDelete hook, the
// next condemned ordinal must wait in OrderedReady mode, and the waiting
// instance must not have a set-level hook armed on it.
func TestStatefulOrderedScaleDownSerializesPreDeleteDrains(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-ordered-drain", 2, 0, 0)
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	h.set.Spec.Replicas = ptr.To(int32(0))
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/drain": "true"},
		},
	}
	low := h.instance(0, h.current.Name, true)
	high := h.instance(1, h.current.Name, true)
	high.DeletionTimestamp = ptr.To(metav1.Now())
	high.Finalizers = []string{constants.RoleInstancePreDeleteFinalizer}
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(low, high).
		Build()
	h.control.hookControl = roleinstancelifecycle.NewHookControl(c)
	h.control.client = c

	h.reconcile(t, low, high)
	h.assertDeleted(t)
	require.Empty(t, roleinstancelifecycle.HookState(low),
		"a waiting ordinal must not carry a set-level preDelete hook")
	require.NotContains(t, low.Labels, "consumer.example/drain")
}

// Updating is durable authorization: after preUpdate was released, a failed
// mutation retries without re-entering the hook or reinstalling the signals.
func TestStatefulPreUpdateResumesUpdatingWithoutReplay(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-updating-resume", 1, 0, 0)
	h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreUpdate: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/update": "blocked"},
		},
	}
	instance := h.instance(0, h.intermediate.Name, true)
	instance.ResourceVersion = "1"
	instance.Labels[constants.RoleInstanceLifecycleHookStateKey] = string(constants.RoleInstanceHookStateUpdating)
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(instance).
		Build()
	h.control.hookControl = roleinstancelifecycle.NewHookControl(c)
	h.control.client = c

	h.reconcile(t, instance)
	h.assertDeleted(t, instance.Name)
	require.NotContains(t, instance.Labels, "consumer.example/update",
		"a retry in Updating must not reinstall preUpdate signal labels")
	require.Equal(t, constants.RoleInstanceHookStateUpdating, roleinstancelifecycle.HookState(instance))
}

// Entering preUpdate emits a normal event on the set so a blocked rollout is
// observable instead of appearing stuck.
func TestStatefulPreUpdateEmitsLifecycleHookEnteredEvent(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-pre-update-event", 1, 0, 0)
	h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreUpdate: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/update": "blocked"},
		},
	}
	instance := h.instance(0, h.intermediate.Name, true)
	instance.ResourceVersion = "1"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels:    map[string]string{constants.RoleInstanceNameLabelKey: instance.Name},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				instance, workloadsv1alpha2.GroupVersion.WithKind("RoleInstance"))},
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(instance, pod).
		Build()
	h.control.hookControl = roleinstancelifecycle.NewHookControl(c)
	h.control.client = c

	h.reconcile(t, instance)
	fakeRecorder, ok := h.control.recorder.(*record.FakeRecorder)
	require.True(t, ok, "the harness must use a fake recorder")
	select {
	case event := <-fakeRecorder.Events:
		require.Contains(t, event, "LifecycleHookEntered")
	default:
		t.Fatal("entering preUpdate must emit a LifecycleHookEntered event")
	}
}

// A condemned instance whose set configures preDelete waits until the reserved
// finalizer is installed: deleting first would skip the hook entirely.
func TestStatefulScaleDownWaitsForPreDeleteFinalizer(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-finalizer-gate", 1, 0, 0)
	h.set.Spec.Replicas = ptr.To(int32(0))
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/drain": "true"},
		},
	}
	instance := h.instance(0, h.current.Name, true)
	instance.ResourceVersion = "1"
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(instance).
		Build()
	h.control.hookControl = roleinstancelifecycle.NewHookControl(c)
	h.control.client = c

	h.reconcile(t, instance)
	h.assertDeleted(t)
	require.Empty(t, roleinstancelifecycle.HookState(instance),
		"the set controller must not arm preDelete before the finalizer exists")

	// Once the reserved finalizer is present, deletion starts and the drain
	// runs inside the finalizer-held deletion.
	instance.Finalizers = []string{constants.RoleInstancePreDeleteFinalizer}
	h.reconcile(t, instance)
	h.assertDeleted(t, instance.Name)
}

// Enabling preDelete together with a Recreate image update must not delete
// the target before the reserved finalizer exists.
func TestStatefulRecreateUpdateWaitsForPreDeleteFinalizer(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-recreate-gate", 1, 0, 0)
	h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	h.set.Spec.UpdateStrategy.Type = workloadsv1alpha2.RecreatePodUpdateStrategyType
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/drain": "true"},
		},
	}
	target := h.instance(0, h.intermediate.Name, true)
	target.ResourceVersion = "1"

	h.reconcile(t, target)
	h.assertDeleted(t)

	target.Finalizers = []string{constants.RoleInstancePreDeleteFinalizer}
	h.reconcile(t, target)
	h.assertDeleted(t, target.Name)
}

// Finalizer-held RIs are deleted with Background propagation and a UID
// precondition, so a Foreground cascade above this instance completes only
// after the preDelete drain.
func TestStatefulDeleteUsesBackgroundAndUIDForFinalizerHeldInstances(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-background-delete", 1, 0, 0)
	h.set.Spec.Replicas = ptr.To(int32(0))
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreDelete: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/drain": "true"},
		},
	}
	instance := h.instance(0, h.current.Name, true)
	instance.ResourceVersion = "1"
	instance.UID = types.UID("background-delete-uid")
	instance.Finalizers = []string{constants.RoleInstancePreDeleteFinalizer}
	instance.Labels[constants.RoleInstanceLifecycleHookStateKey] = string(constants.RoleInstanceHookStateTerminating)
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(instance).
		Build()
	recordingManager := &recordingDeleteObjectManager{inner: h.objects}
	h.control.instanceControl = NewStatefulInstanceControlFromManager(
		recordingManager, record.NewFakeRecorder(10))
	h.control.hookControl = roleinstancelifecycle.NewHookControl(c)
	h.control.client = c

	h.reconcile(t, instance)
	h.assertDeleted(t, instance.Name)
	require.NotEmpty(t, recordingManager.options, "the manager must observe the instance deletion")
	last := recordingManager.options[len(recordingManager.options)-1]
	require.NotNil(t, last.PropagationPolicy, "finalizer-held RI deletion must carry a propagation policy")
	require.Equal(t, metav1.DeletePropagationBackground, *last.PropagationPolicy,
		"finalizer-held RI deletion must use Background propagation")
	require.NotNil(t, last.Preconditions, "finalizer-held RI deletion must carry a UID precondition")
	require.Equal(t, instance.UID, *last.Preconditions.UID)
}

type recordingDeleteObjectManager struct {
	inner   *fakeInstanceObjectManager
	options []client.DeleteOptions
}

func (r *recordingDeleteObjectManager) CreateInstance(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) error {
	return r.inner.CreateInstance(ctx, instance)
}
func (r *recordingDeleteObjectManager) GetInstance(namespace, name string) (*workloadsv1alpha2.RoleInstance, error) {
	return r.inner.GetInstance(namespace, name)
}
func (r *recordingDeleteObjectManager) UpdateInstance(instance *workloadsv1alpha2.RoleInstance) error {
	return r.inner.UpdateInstance(instance)
}
func (r *recordingDeleteObjectManager) DeleteInstance(instance *workloadsv1alpha2.RoleInstance, opts ...client.DeleteOption) error {
	options := client.DeleteOptions{}
	options.ApplyOptions(opts)
	r.options = append(r.options, options)
	return r.inner.DeleteInstance(instance)
}

// Without lifecycle hooks, OrderedReady scale-down keeps main's one-pass
// pacing: every condemned instance is deleted in a single reconcile.
func TestStatefulOrderedScaleDownKeepsMainPacingWithoutHooks(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-main-pacing", 2, 0, 0)
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	h.set.Spec.Replicas = ptr.To(int32(0))
	low := h.instance(0, h.current.Name, true)
	high := h.instance(1, h.current.Name, true)

	h.reconcile(t, low, high)
	h.assertDeleted(t, low.Name, high.Name)
}

// A released preUpdate grant is consumed atomically: the in-place update that
// follows writes the Updating hook-state in the same metadata write.
func TestStatefulPreUpdateGrantConsumedByInPlaceUpdate(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-grant-inplace", 1, 0, 0)
	h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	h.set.Spec.UpdateStrategy.Type = workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreUpdate: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/update": "blocked"},
		},
	}
	instance := h.instance(0, h.intermediate.Name, true)
	instance.ResourceVersion = "1"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels:    map[string]string{constants.RoleInstanceNameLabelKey: instance.Name},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				instance, workloadsv1alpha2.GroupVersion.WithKind("RoleInstance"))},
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(instance, pod).
		Build()
	h.control.hookControl = roleinstancelifecycle.NewHookControl(c)
	h.control.client = c
	h.control.inplaceControl = &fakeInplaceControl{canUpdateInPlace: true}

	// First pass arms the hook.
	h.reconcile(t, instance)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceHookStatePreUpdate, roleinstancelifecycle.HookState(instance))

	// The executor releases the signal; the in-place update that consumes the
	// grant writes Updating atomically.
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(instance), instance))
	delete(instance.Labels, "consumer.example/update")
	require.NoError(t, c.Update(context.Background(), instance))
	h.reconcile(t, instance)
	require.Equal(t, constants.RoleInstanceHookStateUpdating, roleinstancelifecycle.HookState(instance),
		"the in-place mutation must stamp Updating in the same metadata write")
}

// A reverted update cancels its armed preUpdate: the signals are removed and
// the state is cleared, leaving no authorization a later unrelated update
// could consume without a fresh acknowledgement.
func TestStatefulRevertedUpdateCancelsPreUpdate(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-cancel", 2, 0, 0)
	h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreUpdate: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/update": "blocked"},
		},
	}
	// inst-0 matches the update revision (no longer a target) but is armed;
	// inst-1 is still a target so the rollout stays active.
	armed := h.instance(0, h.current.Name, true)
	stillTarget := h.instance(1, h.intermediate.Name, true)
	stillTarget.ResourceVersion = "1"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      armed.Name,
			Namespace: armed.Namespace,
			Labels: map[string]string{
				constants.RoleInstanceNameLabelKey: armed.Name,
				"consumer.example/update":          "blocked",
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				armed, workloadsv1alpha2.GroupVersion.WithKind("RoleInstance"))},
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(armed, stillTarget, pod).
		Build()
	h.control.hookControl = roleinstancelifecycle.NewHookControl(c)
	h.control.client = c

	// Seed the armed state: preUpdate entered earlier for inst-0.
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(armed), armed))
	armed.Labels[constants.RoleInstanceLifecycleHookStateKey] = string(constants.RoleInstanceHookStatePreUpdate)
	armed.Labels["consumer.example/update"] = "blocked"
	armed.Annotations = map[string]string{
		constants.RoleInstanceLifecycleHookSignalLabelKeys: "consumer.example/update",
	}
	require.NoError(t, c.Update(context.Background(), armed))

	h.reconcile(t, armed, stillTarget)
	require.Empty(t, roleinstancelifecycle.HookState(armed),
		"an untargeted armed instance is cancelled with no state left behind")
	require.NotContains(t, armed.Labels, "consumer.example/update")
	storedPod := &corev1.Pod{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pod), storedPod))
	require.NotContains(t, storedPod.Labels, "consumer.example/update",
		"cancellation must remove the Pod mirrors")
	// The still-target instance is armed instead, not deleted.
	require.Equal(t, constants.RoleInstanceHookStatePreUpdate, roleinstancelifecycle.HookState(stillTarget))
	h.assertDeleted(t)
}

// The hook-state label has two writers. When the RoleInstance controller owns
// an in-flight preRecover, an armed preUpdate supersedes it: the update is the
// operation that fixes the workload, so waiting would stall the rollout on an
// executor that may never acknowledge.
func TestStatefulPreUpdateSupersedesArmedPreRecover(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-arbitration", 1, 0, 0)
	h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreUpdate: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/update": "blocked"},
		},
		PreRecover: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/recover": "true"},
		},
	}
	instance := h.instance(0, h.intermediate.Name, true)
	instance.ResourceVersion = "1"
	instance.Spec.Lifecycle = h.set.Spec.RoleInstanceTemplate.Lifecycle.DeepCopy()
	instance.Labels[constants.RoleInstanceLifecycleHookStateKey] = string(constants.RoleInstanceHookStatePreRecover)
	instance.Labels["consumer.example/recover"] = "true"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels:    map[string]string{constants.RoleInstanceNameLabelKey: instance.Name},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				instance, workloadsv1alpha2.GroupVersion.WithKind("RoleInstance"))},
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(instance, pod).
		Build()
	h.control.hookControl = roleinstancelifecycle.NewHookControl(c)
	h.control.client = c

	h.reconcile(t, instance)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceHookStatePreUpdate, roleinstancelifecycle.HookState(instance),
		"an armed preUpdate supersedes the preRecover state")
	require.Contains(t, instance.Labels, "consumer.example/update",
		"the set controller installs the preUpdate signal")
	require.NotContains(t, instance.Labels, "consumer.example/recover",
		"the superseded preRecover signal is cleared in the same write")
	storedPod := &corev1.Pod{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pod), storedPod))
	require.Contains(t, storedPod.Labels, "consumer.example/update")
	require.NotContains(t, storedPod.Labels, "consumer.example/recover",
		"the superseded Pod mirror is removed")
}

func TestStatefulPreDeleteWaitsForFinalizer(t *testing.T) {
	for _, test := range []struct {
		name           string
		configureLabel bool
	}{
		{name: "finalizer only"},
		{name: "configured label absent", configureLabel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "hook-finalizer", 0, 0, 0)
			instance := h.instance(0, h.current.Name, true)
			c := holdPreDelete(t, h, false, instance)
			hook := h.set.Spec.Lifecycle.PreDelete
			if !test.configureLabel {
				hook.LabelsHandler = nil
			}
			delete(instance.Labels, preDeleteHoldLabel)
			const holdFinalizer = "consumer.example/drain"
			hook.FinalizersHandler = []string{holdFinalizer}
			instance.Finalizers = []string{holdFinalizer}
			require.NoError(t, c.Update(context.Background(), instance))

			h.reconcile(t, instance)
			h.assertDeleted(t)
			require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, instance))

			// The first pass announces the drain. The next pass must honor the finalizer.
			h.reconcile(t, instance)
			h.assertDeleted(t)

			instance.Finalizers = nil
			require.NoError(t, c.Update(context.Background(), instance))
			h.reconcile(t, instance)
			h.assertDeleted(t, instance.Name)
		})
	}
}

func TestStatefulScaleDownCancellationRestoresPreDeleteReadiness(t *testing.T) {
	h := newRollbackTestHarness(t, "cancel-scale-down", 1, 0, 0)
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	retained := h.instance(0, h.current.Name, true)
	held := h.instance(1, h.current.Name, true)
	c := holdPreDelete(t, h, true, held)
	check := func(state constants.RoleInstanceLifecycleStateType, gate corev1.ConditionStatus) {
		t.Helper()
		h.assertDeleted(t)
		require.Equal(t, state, readLifecycle(t, c, held))
		if got := readiness.GetReadinessCondition(held); got == nil || got.Status != gate {
			t.Fatalf("readiness gate = %+v, want %q", got, gate)
		}
	}

	h.reconcile(t, retained, held)
	check(constants.RoleInstanceLifecycleStatePreparingDelete, corev1.ConditionFalse)
	h.set.Spec.Replicas = ptr.To(int32(2))
	h.reconcile(t, retained, held)
	check(constants.RoleInstanceLifecycleStateNormal, corev1.ConditionTrue)

	h.set.Spec.Replicas = ptr.To(int32(1))
	h.reconcile(t, retained, held)
	check(constants.RoleInstanceLifecycleStatePreparingDelete, corev1.ConditionFalse)
	delete(held.Labels, preDeleteHoldLabel)
	require.NoError(t, c.Update(context.Background(), held))
	h.reconcile(t, retained, held)
	h.assertDeleted(t, held.Name)
}

func TestCancelScaleDownRestoresTargetRevisionAfterHookRemoval(t *testing.T) {
	h := newRollbackTestHarness(t, "cancel-scale-down-without-hook", 1, 0, 0)
	h.set.Spec.UpdateStrategy.Paused = true
	stale := h.instance(0, h.intermediate.Name, true)
	retained := h.instance(1, h.current.Name, true)
	c := holdPreDelete(t, h, true, retained)

	h.reconcile(t, stale, retained)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, retained))
	require.False(t, isHealthy(retained))

	// The stale member keeps the rollout pending while the retained member is at its target revision.
	h.set.Spec.Lifecycle = nil
	h.set.Spec.Replicas = ptr.To(int32(2))
	h.reconcile(t, stale, retained)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStateNormal, readLifecycle(t, c, retained))
	gate := readiness.GetReadinessCondition(retained)
	require.NotNil(t, gate)
	require.Equal(t, corev1.ConditionTrue, gate.Status)
}

func TestPreDeleteLifecyclePatchFailureStopsReconcile(t *testing.T) {
	for _, test := range []struct {
		name    string
		restore bool
	}{
		{name: "enter PreparingDelete"},
		{name: "restore Normal", restore: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "pre-delete-patch", 1, 0, 0)
			base := h.instance(0, h.current.Name, true)
			held := h.instance(1, h.current.Name, true)
			c := holdPreDelete(t, h, true, held)
			instances := []*workloadsv1alpha2.RoleInstance{base, held}
			if test.restore {
				h.reconcile(t, instances...)
				h.assertDeleted(t)
				require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, held))

				h.set.Spec.Replicas = ptr.To(int32(2))
				// An excess member whose hook has released can be deleted if reconcile continues.
				excess := h.instance(2, h.current.Name, true)
				instancelifecycle.SetRoleInstanceLifecycle(constants.RoleInstanceLifecycleStatePreparingDelete)(excess)
				instances = append(instances, excess)
			}
			stateBefore := instancelifecycle.GetRoleInstanceLifecycleState(held)
			patchErr := errors.New("lifecycle patch failed")
			h.control.lifecycleControl = instancelifecycle.New(interceptor.NewClient(c, interceptor.Funcs{
				Patch: func(_ context.Context, _ client.WithWatch, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
					require.Equal(t, held.Name, obj.GetName())
					return patchErr
				},
			}))

			_, err := h.control.updateStatefulInstanceSet(context.Background(), h.set, h.current, h.current,
				0, instances, []*appsv1.ControllerRevision{h.current, h.intermediate})
			require.ErrorIs(t, err, patchErr)
			h.assertDeleted(t)
			require.Equal(t, stateBefore, readLifecycle(t, c, held))
		})
	}
}

func TestOrderedScaleDownWaitsForHighestPreDeleteHook(t *testing.T) {
	h := newRollbackTestHarness(t, "ordered-hooks", 1, 0, 0)
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	base := h.instance(0, h.current.Name, true)
	lower := h.instance(1, h.current.Name, true)
	higher := h.instance(2, h.current.Name, true)
	c := holdPreDelete(t, h, false, lower, higher)
	h.reconcile(t, base, lower, higher)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, higher))
	require.NotEqual(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, lower))
	delete(higher.Labels, preDeleteHoldLabel)
	require.NoError(t, c.Update(context.Background(), higher))
	h.reconcile(t, base, lower, higher)
	h.assertDeleted(t, higher.Name)
}

func TestOrderedRolloutProceedsOnceScaleDownDeletionStarts(t *testing.T) {
	for _, test := range []struct {
		name            string
		alreadyDeleting bool
	}{
		{name: "delete issued"},
		{name: "already terminating", alreadyDeleting: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "ordered-rollout", 1, 0, 0)
			h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
			h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
			target := h.instance(0, h.intermediate.Name, true)
			surplus := h.instance(1, h.current.Name, true)
			c := holdPreDelete(t, h, false, target, surplus)

			h.reconcile(t, target, surplus)
			h.assertDeleted(t)
			require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, surplus))
			require.NotEqual(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, target))

			delete(surplus.Labels, preDeleteHoldLabel)
			require.NoError(t, c.Update(context.Background(), surplus))
			if test.alreadyDeleting {
				withTerminating(surplus)
			}
			h.reconcile(t, target, surplus)
			if test.alreadyDeleting {
				h.assertDeleted(t)
			} else {
				h.assertDeleted(t, surplus.Name)
			}
			require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, target))
		})
	}
}

func TestPausedRolloutPreservesPreDeleteForSparseUpdateTarget(t *testing.T) {
	h := newRollbackTestHarness(t, "paused-rollout", 1, 2, 0)
	h.set.Annotations = map[string]string{constants.CommittedSkipOrdinalsAnnotationKey: "1,3"}
	h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	base := h.instance(0, h.current.Name, true)
	protected := h.instance(2, h.intermediate.Name, true)
	target := h.instance(4, h.intermediate.Name, true)
	c := holdPreDelete(t, h, false, protected, target)
	h.reconcile(t, base, protected, target)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, protected))
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, target))

	h.set.Spec.Replicas = ptr.To(int32(3))
	h.set.Spec.UpdateStrategy.Paused = true
	h.reconcile(t, base, protected, target)
	h.assertDeleted(t)
	// Ordinal 2 is below partition 2 by member position; ordinal 4 is at the partition.
	require.Equal(t, constants.RoleInstanceLifecycleStateNormal, readLifecycle(t, c, protected))
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, target))

	h.set.Spec.UpdateStrategy.Paused = false
	delete(target.Labels, preDeleteHoldLabel)
	require.NoError(t, c.Update(context.Background(), target))
	h.reconcile(t, base, protected, target)
	h.assertDeleted(t, target.Name)
}

func TestRemovingPreDeleteHookUnblocksOrderedScaleDown(t *testing.T) {
	h := newRollbackTestHarness(t, "remove-pre-delete", 1, 0, 0)
	h.set.Spec.PodManagementPolicy = constants.OrderedReadyPodManagement
	base := h.instance(0, h.current.Name, true)
	lower := h.instance(1, h.current.Name, false)
	higher := h.instance(2, h.current.Name, true)
	c := holdPreDelete(t, h, true, higher)
	h.reconcile(t, base, lower, higher)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, higher))
	require.False(t, isHealthy(higher))

	h.set.Spec.Lifecycle = nil
	h.reconcile(t, base, lower, higher)
	require.Equal(t, []string{higher.Name, lower.Name}, h.objects.deleted)
}

func TestRollingUpdateCountsUnavailableDrainOnce(t *testing.T) {
	for _, test := range []struct {
		name   string
		stable bool
	}{
		{name: "recently NotReady"},
		{name: "stably NotReady", stable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newRollbackTestHarness(t, "pre-delete-budget", 2, 0, 0)
			h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
			lower := h.instance(0, h.intermediate.Name, true)
			higher := h.instance(1, h.intermediate.Name, true)
			c := holdPreDelete(t, h, true, lower, higher)
			h.reconcile(t, lower, higher)
			h.assertDeleted(t)
			require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, higher))
			require.False(t, isHealthy(higher))
			require.NotEqual(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, lower))

			h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(2))
			if test.stable {
				setUnhealthySince(h.set, higher, time.Now().Add(-2*stableUnhealthyDuration))
			}
			h.reconcile(t, lower, higher)
			h.assertDeleted(t)
			require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, lower))
		})
	}
}

func TestStaleSurgeDrainPreservesBaseRolloutBudget(t *testing.T) {
	h := newRollbackTestHarness(t, "surge-pre-delete", 2, 0, 2)
	base0 := h.instance(0, h.intermediate.Name, true)
	base1 := h.instance(1, h.intermediate.Name, true)
	readySurge := h.instance(2, h.current.Name, true)
	staleSurge := h.instance(3, h.intermediate.Name, true)
	c := holdPreDelete(t, h, false, base0, base1, staleSurge)

	// With maxUnavailable=0, the ready surge supplies the one available base rollout slot.
	h.reconcile(t, base0, base1, readySurge, staleSurge)
	h.assertDeleted(t)
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, staleSurge))
	require.Equal(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, base1))
	require.NotEqual(t, constants.RoleInstanceLifecycleStatePreparingDelete, readLifecycle(t, c, base0))

	delete(staleSurge.Labels, preDeleteHoldLabel)
	require.NoError(t, c.Update(context.Background(), staleSurge))
	h.reconcile(t, base0, base1, readySurge, staleSurge)
	h.assertDeleted(t, staleSurge.Name)
}

// A full revert leaves no update targets, so the rollout is not active. The
// armed preUpdate must still be cancelled: without the hoisted sweep the
// instance keeps its state and signals indefinitely, and a later unrelated
// update would consume the stale grant without a fresh drain.
func TestStatefulFullRevertCancelsArmedPreUpdate(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-full-revert", 1, 0, 0)
	h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreUpdate: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/update": "blocked"},
		},
	}
	// The instance is back at the update revision: no update targets remain.
	armed := h.instance(0, h.current.Name, true)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      armed.Name,
			Namespace: armed.Namespace,
			Labels: map[string]string{
				constants.RoleInstanceNameLabelKey: armed.Name,
				"consumer.example/update":          "blocked",
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				armed, workloadsv1alpha2.GroupVersion.WithKind("RoleInstance"))},
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, workloadsv1alpha2.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&workloadsv1alpha2.RoleInstance{}).
		WithObjects(armed, pod).
		Build()
	h.control.hookControl = roleinstancelifecycle.NewHookControl(c)
	h.control.client = c

	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(armed), armed))
	armed.Labels[constants.RoleInstanceLifecycleHookStateKey] = string(constants.RoleInstanceHookStatePreUpdate)
	armed.Labels["consumer.example/update"] = "blocked"
	armed.Annotations = map[string]string{
		constants.RoleInstanceLifecycleHookSignalLabelKeys: "consumer.example/update",
	}
	require.NoError(t, c.Update(context.Background(), armed))

	h.reconcile(t, armed)
	require.Empty(t, roleinstancelifecycle.HookState(armed),
		"an armed preUpdate with no remaining update targets must be cancelled")
	require.NotContains(t, armed.Labels, "consumer.example/update")
	storedPod := &corev1.Pod{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(pod), storedPod))
	require.NotContains(t, storedPod.Labels, "consumer.example/update",
		"cancellation must remove the Pod mirrors")
	h.assertDeleted(t)
}

// Without a preUpdate hook the set controller writes no hook-state, so an
// RI-owned postReady must not block the rollout: the update proceeds.
func TestStatefulUpdateSkipsArbitrationWithoutPreUpdateHook(t *testing.T) {
	h := newRollbackTestHarness(t, "kep527-no-preupdate", 1, 0, 0)
	h.set.Spec.UpdateStrategy.MaxUnavailable = ptr.To(intstr.FromInt32(1))
	h.set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PostReady: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/ready": "true"},
		},
	}
	instance := h.instance(0, h.intermediate.Name, true)
	instance.Labels[constants.RoleInstanceLifecycleHookStateKey] = string(constants.RoleInstanceHookStatePostReady)

	h.reconcile(t, instance)
	h.assertDeleted(t, instance.Name)
}

// The set-side update arbitration is minimal: Updating is the durable grant,
// preDelete/Terminating own the drain, and without a preUpdate hook there is
// nothing to arbitrate at all.
func TestPrepareLifecycleUpdateArbitration(t *testing.T) {
	preUpdate := &workloadsv1alpha2.RoleInstanceLifecycleHook{
		SignalLabels: map[string]string{"consumer.example/update": "blocked"},
	}
	tests := []struct {
		name      string
		hookState constants.RoleInstanceLifecycleHookStateType
		preUpdate *workloadsv1alpha2.RoleInstanceLifecycleHook
		wantBlock bool
	}{
		{name: "updating grant proceeds", hookState: constants.RoleInstanceHookStateUpdating, preUpdate: preUpdate},
		{name: "preDelete waits", hookState: constants.RoleInstanceHookStatePreDelete, preUpdate: preUpdate, wantBlock: true},
		{name: "terminating waits", hookState: constants.RoleInstanceHookStateTerminating, preUpdate: preUpdate, wantBlock: true},
		{
			name:      "postReady without preUpdate proceeds",
			hookState: constants.RoleInstanceHookStatePostReady,
		},
		{
			name:      "preRecover without preUpdate proceeds",
			hookState: constants.RoleInstanceHookStatePreRecover,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			set := newRevisionTestSet("nginx:1.0")
			set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
				PreUpdate: test.preUpdate,
			}
			instance := &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "instance-0",
					Namespace: "default",
					Labels: map[string]string{
						constants.RoleInstanceLifecycleHookStateKey: string(test.hookState),
					},
				},
			}
			ssc := &defaultStatefulInstanceSetControl{}
			blocked, err := ssc.prepareLifecycleUpdate(context.Background(), set, instance)
			require.NoError(t, err)
			require.Equal(t, test.wantBlock, blocked)
		})
	}
}

// A lifecycle-only template change is controller-propagated metadata, not a
// rollout input: it must reuse the existing ControllerRevision (getPatch
// deletes lifecycle) and ApplyRevision must restore the live snapshot, or
// every hook-only change would roll all instances.
func TestLifecycleOnlyTemplateChangeReusesRevision(t *testing.T) {
	set := newRevisionTestSet("nginx:1.0")
	without, err := newRevision(set, 1, ptr.To[int32](0))
	require.NoError(t, err)

	set.Spec.RoleInstanceTemplate.Lifecycle = &workloadsv1alpha2.RoleInstanceLifecycle{
		PreRecover: &workloadsv1alpha2.RoleInstanceLifecycleHook{
			SignalLabels: map[string]string{"consumer.example/recover": "true"},
		},
	}
	with, err := newRevision(set, 1, ptr.To[int32](0))
	require.NoError(t, err)
	require.Equal(t, without.Name, with.Name,
		"a lifecycle-only template change must not cut a new revision")
	require.Equal(t, without.Data.Raw, with.Data.Raw)

	restored, err := ApplyRevision(set, without)
	require.NoError(t, err)
	require.Equal(t, set.Spec.RoleInstanceTemplate.Lifecycle, restored.Spec.RoleInstanceTemplate.Lifecycle,
		"ApplyRevision must preserve the live lifecycle snapshot")
}
