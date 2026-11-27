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

package lifecycle

import (
	"context"
	"maps"
	"slices"
	"strings"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// HookControl implements the KEP-527 runtime metadata protocol.
type HookControl struct {
	client.Client
}

// NewHookControl creates a KEP-527 lifecycle-hook controller.
func NewHookControl(c client.Client) *HookControl {
	return &HookControl{Client: c}
}

// HookState returns the current runtime hook state.
func HookState(instance *workloadsv1alpha2.RoleInstance) constants.RoleInstanceLifecycleHookStateType {
	if instance == nil {
		return ""
	}
	return constants.RoleInstanceLifecycleHookStateType(instance.Labels[constants.RoleInstanceLifecycleHookStateKey])
}

// PendingSignals returns the configured signal labels that still match.
func PendingSignals(instance *workloadsv1alpha2.RoleInstance, hook *workloadsv1alpha2.RoleInstanceLifecycleHook) []string {
	if instance == nil || hook == nil {
		return nil
	}
	var pending []string
	for key, value := range hook.SignalLabels {
		// A missing label and a label set to "" both read as "". Require the
		// key to exist so an empty-valued signal is released by label removal
		// instead of blocking the operation forever.
		if got, exists := instance.Labels[key]; exists && got == value {
			pending = append(pending, key)
		}
	}
	slices.Sort(pending)
	return pending
}

// HookReleased reports whether every configured signal label no longer matches.
func HookReleased(instance *workloadsv1alpha2.RoleInstance, hook *workloadsv1alpha2.RoleInstanceLifecycleHook) bool {
	return len(PendingSignals(instance, hook)) == 0
}

// EnterHook writes the hook state, signal labels, and the durable key record
// in one RoleInstance metadata write, then propagates the labels to Pods.
func (c *HookControl) EnterHook(
	ctx context.Context,
	instance *workloadsv1alpha2.RoleInstance,
	state constants.RoleInstanceLifecycleHookStateType,
	hook *workloadsv1alpha2.RoleInstanceLifecycleHook,
	pods []*v1.Pod,
) error {
	if instance == nil {
		return nil
	}
	if hook == nil {
		return c.SetHookState(ctx, instance, state, pods)
	}

	base := instance.DeepCopy()
	if instance.Labels == nil {
		instance.Labels = make(map[string]string)
	}
	if instance.Annotations == nil {
		instance.Annotations = make(map[string]string)
	}
	instance.Labels[constants.RoleInstanceLifecycleHookStateKey] = string(state)
	for key, value := range hook.SignalLabels {
		instance.Labels[key] = value
	}
	instance.Annotations[constants.RoleInstanceLifecycleHookSignalLabelKeys] =
		formatSignalKeys(unionSignalKeys(recordedSignalKeys(instance), hook.SignalLabels))

	// The optimistic lock makes hook-state transitions compare-and-swap: two
	// controllers (the RoleInstance reconciler and the set reconciler) write
	// the same label, and a stale read must not silently overwrite the other
	// writer's state.
	if err := c.Patch(ctx, instance,
		client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	return c.mirrorSignalLabels(ctx, instance, pods)
}

// SupersedeHook arms the given hook over a RoleInstance-owned state. The
// superseded hooks' signal labels that the new hook does not share are removed
// in the same optimistic-lock write, so an executor watching them stops
// instead of acting on an operation that was interrupted. The removed keys
// stay in the record until mirrorSignalLabels confirms their absence from
// every member Pod, mirroring the ordinary release path.
func (c *HookControl) SupersedeHook(
	ctx context.Context,
	instance *workloadsv1alpha2.RoleInstance,
	state constants.RoleInstanceLifecycleHookStateType,
	hook *workloadsv1alpha2.RoleInstanceLifecycleHook,
	superseded []*workloadsv1alpha2.RoleInstanceLifecycleHook,
	pods []*v1.Pod,
) error {
	if instance == nil || hook == nil {
		return nil
	}
	base := instance.DeepCopy()
	if instance.Labels == nil {
		instance.Labels = make(map[string]string)
	}
	if instance.Annotations == nil {
		instance.Annotations = make(map[string]string)
	}
	instance.Labels[constants.RoleInstanceLifecycleHookStateKey] = string(state)
	owned := unionSignalKeys(recordedSignalKeys(instance), hook.SignalLabels)
	for key, value := range hook.SignalLabels {
		instance.Labels[key] = value
	}
	for _, candidate := range superseded {
		if candidate == nil {
			continue
		}
		for key := range candidate.SignalLabels {
			if _, shared := hook.SignalLabels[key]; shared {
				continue
			}
			delete(instance.Labels, key)
		}
	}
	instance.Annotations[constants.RoleInstanceLifecycleHookSignalLabelKeys] = formatSignalKeys(owned)
	if err := c.Patch(ctx, instance,
		client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	return c.mirrorSignalLabels(ctx, instance, pods)
}

// SetHookState writes only the runtime hook state and then syncs Pod mirrors.
func (c *HookControl) SetHookState(
	ctx context.Context,
	instance *workloadsv1alpha2.RoleInstance,
	state constants.RoleInstanceLifecycleHookStateType,
	pods []*v1.Pod,
) error {
	if instance == nil {
		return nil
	}
	base := instance.DeepCopy()
	if state == "" {
		if instance.Labels == nil || instance.Labels[constants.RoleInstanceLifecycleHookStateKey] == "" {
			return nil
		}
		delete(instance.Labels, constants.RoleInstanceLifecycleHookStateKey)
	} else {
		if instance.Labels == nil {
			instance.Labels = make(map[string]string)
		}
		instance.Labels[constants.RoleInstanceLifecycleHookStateKey] = string(state)
	}
	if err := c.Patch(ctx, instance,
		client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	return c.mirrorSignalLabels(ctx, instance, pods)
}

// SetHookStateLabel returns an in-place update option function that stamps
// the KEP-527 hook-state label. The option runs inside the same metadata
// write as the mutation that consumes a released preUpdate grant, so Updating
// always means the mutation was applied.
func SetHookStateLabel(state constants.RoleInstanceLifecycleHookStateType) func(instance *workloadsv1alpha2.RoleInstance) {
	return func(instance *workloadsv1alpha2.RoleInstance) {
		if instance == nil {
			return
		}
		if instance.Labels == nil {
			instance.Labels = make(map[string]string)
		}
		instance.Labels[constants.RoleInstanceLifecycleHookStateKey] = string(state)
	}
}

// CancelHook ends an armed pre-operation hook: it removes the hook's signal
// labels and transitions to the post-hook state in one metadata write. A
// cancelled operation leaves neither a stale grant a later update could
// consume nor a signal the executor keeps honoring.
func (c *HookControl) CancelHook(
	ctx context.Context,
	instance *workloadsv1alpha2.RoleInstance,
	state constants.RoleInstanceLifecycleHookStateType,
	hook *workloadsv1alpha2.RoleInstanceLifecycleHook,
	pods []*v1.Pod,
) error {
	if instance == nil {
		return nil
	}
	base := instance.DeepCopy()
	if instance.Labels == nil {
		instance.Labels = make(map[string]string)
	}
	if state == "" {
		delete(instance.Labels, constants.RoleInstanceLifecycleHookStateKey)
	} else {
		instance.Labels[constants.RoleInstanceLifecycleHookStateKey] = string(state)
	}
	if hook != nil {
		for key := range hook.SignalLabels {
			delete(instance.Labels, key)
		}
	}
	if err := c.Patch(ctx, instance,
		client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return err
	}
	return c.mirrorSignalLabels(ctx, instance, pods)
}

// EnsurePreDeleteFinalizer adds the reserved finalizer before deletion can begin.
func (c *HookControl) EnsurePreDeleteFinalizer(
	ctx context.Context,
	instance *workloadsv1alpha2.RoleInstance,
) (bool, error) {
	if instance == nil || controllerutil.ContainsFinalizer(instance, constants.RoleInstancePreDeleteFinalizer) {
		return false, nil
	}
	base := instance.DeepCopy()
	controllerutil.AddFinalizer(instance, constants.RoleInstancePreDeleteFinalizer)
	if instance.Labels == nil {
		instance.Labels = make(map[string]string)
	}
	// The guard label routes the RI DELETE webhook to exactly the instances
	// the finalizer protects. It must land in the same write as the finalizer
	// so webhook routing never lags the protection.
	instance.Labels[constants.RoleInstancePreDeleteGuardLabelKey] = "true"
	// The optimistic lock keeps a plain merge patch from replacing the whole
	// finalizer array when another writer adds a finalizer concurrently.
	if err := c.Patch(ctx, instance,
		client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return false, err
	}
	return true, nil
}

// RemovePreDeleteFinalizer removes the reserved finalizer when preDelete is disabled
// or its Pods have been cleaned up.
func (c *HookControl) RemovePreDeleteFinalizer(
	ctx context.Context,
	instance *workloadsv1alpha2.RoleInstance,
) (bool, error) {
	if instance == nil || !controllerutil.ContainsFinalizer(instance, constants.RoleInstancePreDeleteFinalizer) {
		return false, nil
	}
	base := instance.DeepCopy()
	controllerutil.RemoveFinalizer(instance, constants.RoleInstancePreDeleteFinalizer)
	if instance.Labels != nil {
		delete(instance.Labels, constants.RoleInstancePreDeleteGuardLabelKey)
	}
	// The optimistic lock keeps a plain merge patch from replacing the whole
	// finalizer array when another writer adds a finalizer concurrently.
	if err := c.Patch(ctx, instance,
		client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return false, err
	}
	return true, nil
}

// SyncRuntimeLabels removes stale protocol labels, mirrors current signal
// labels onto member Pods, and shrinks the key record only after both the
// RoleInstance label and all Pod mirrors are absent. A nil lifecycle means no
// hooks are configured, so every recorded protocol label is stale.
func (c *HookControl) SyncRuntimeLabels(
	ctx context.Context,
	instance *workloadsv1alpha2.RoleInstance,
	lifecycle *workloadsv1alpha2.RoleInstanceLifecycle,
	pods []*v1.Pod,
) error {
	if instance == nil {
		return nil
	}
	recorded := recordedSignalKeys(instance)
	if len(recorded) == 0 {
		return nil
	}
	configured := configuredSignalKeys(lifecycle)

	base := instance.DeepCopy()
	changed := false
	for key := range recorded {
		if _, active := configured[key]; active {
			continue
		}
		if _, exists := instance.Labels[key]; exists {
			delete(instance.Labels, key)
			changed = true
		}
	}
	if changed {
		if err := c.Patch(ctx, instance,
			client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
	}
	return c.mirrorSignalLabels(ctx, instance, pods)
}

func (c *HookControl) mirrorSignalLabels(
	ctx context.Context,
	instance *workloadsv1alpha2.RoleInstance,
	pods []*v1.Pod,
) error {
	recorded := recordedSignalKeys(instance)
	if len(recorded) == 0 {
		return nil
	}

	for _, pod := range pods {
		if pod == nil {
			continue
		}
		podBase := pod.DeepCopy()
		podChanged := false
		for key := range recorded {
			value, exists := instance.Labels[key]
			if exists {
				if pod.Labels == nil {
					pod.Labels = make(map[string]string)
				}
				// A missing Pod label also reads as "", so mirror presence as
				// well as value: an empty-valued signal must appear as a
				// present label on the Pod.
				_, podHas := pod.Labels[key]
				if !podHas || pod.Labels[key] != value {
					pod.Labels[key] = value
					podChanged = true
				}
				continue
			}
			if pod.Labels != nil {
				if _, had := pod.Labels[key]; had {
					delete(pod.Labels, key)
					podChanged = true
				}
			}
		}
		if podChanged {
			// The optimistic lock protects against a stale cached Pod hitting
			// a same-name replacement (the new object has a different
			// resourceVersion). It is more conservative than the convention's
			// UID-only suggestion, but controller-runtime cannot express a UID
			// precondition on patch calls.
			if err := c.Patch(ctx, pod,
				client.MergeFromWithOptions(podBase, client.MergeFromWithOptimisticLock{})); err != nil {
				return err
			}
		}
	}

	remaining := make(map[string]struct{}, len(recorded))
	for key := range recorded {
		if _, exists := instance.Labels[key]; exists {
			remaining[key] = struct{}{}
			continue
		}
		for _, pod := range pods {
			if pod != nil && pod.Labels != nil {
				if _, exists := pod.Labels[key]; exists {
					remaining[key] = struct{}{}
					break
				}
			}
		}
	}
	if len(remaining) == len(recorded) {
		return nil
	}

	base := instance.DeepCopy()
	if instance.Annotations == nil {
		instance.Annotations = make(map[string]string)
	}
	if len(remaining) == 0 {
		delete(instance.Annotations, constants.RoleInstanceLifecycleHookSignalLabelKeys)
	} else {
		instance.Annotations[constants.RoleInstanceLifecycleHookSignalLabelKeys] = formatSignalKeys(remaining)
	}
	return c.Patch(ctx, instance,
		client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

// CleanupPostReady clears the asynchronous postReady state after its executor
// removed the signal label.
func (c *HookControl) CleanupPostReady(
	ctx context.Context,
	instance *workloadsv1alpha2.RoleInstance,
	lifecycle *workloadsv1alpha2.RoleInstanceLifecycle,
	pods []*v1.Pod,
) error {
	if instance == nil || lifecycle == nil || lifecycle.PostReady == nil {
		return nil
	}
	if HookState(instance) != constants.RoleInstanceHookStatePostReady {
		return nil
	}
	if !HookReleased(instance, lifecycle.PostReady) {
		return nil
	}
	if err := c.SyncRuntimeLabels(ctx, instance, lifecycle, pods); err != nil {
		return err
	}
	return c.SetHookState(ctx, instance, "", pods)
}

func configuredSignalKeys(lifecycle *workloadsv1alpha2.RoleInstanceLifecycle) map[string]struct{} {
	out := make(map[string]struct{})
	if lifecycle == nil {
		return out
	}
	for _, hookName := range workloadsv1alpha2.LifecycleHookNames() {
		hook := workloadsv1alpha2.RoleInstanceLifecycleHookFor(lifecycle, hookName)
		if hook == nil {
			continue
		}
		for key := range hook.SignalLabels {
			out[key] = struct{}{}
		}
	}
	return out
}

func recordedSignalKeys(instance *workloadsv1alpha2.RoleInstance) map[string]struct{} {
	out := make(map[string]struct{})
	if instance == nil || instance.Annotations == nil {
		return out
	}
	value := instance.Annotations[constants.RoleInstanceLifecycleHookSignalLabelKeys]
	if value == "" {
		return out
	}
	for key := range strings.SplitSeq(value, ",") {
		if key != "" {
			out[key] = struct{}{}
		}
	}
	return out
}

func unionSignalKeys(left map[string]struct{}, labels map[string]string) map[string]struct{} {
	out := make(map[string]struct{}, len(left)+len(labels))
	maps.Copy(out, left)
	for key := range labels {
		out[key] = struct{}{}
	}
	return out
}

func formatSignalKeys(keys map[string]struct{}) string {
	if len(keys) == 0 {
		return ""
	}
	sorted := make([]string, 0, len(keys))
	for key := range keys {
		sorted = append(sorted, key)
	}
	slices.Sort(sorted)
	return strings.Join(sorted, ",")
}
