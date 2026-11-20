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

package v1alpha2

import (
	"context"
	"fmt"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	"sigs.k8s.io/rbgs/pkg/features"
)

// RoleInstanceSetValidator implements admission.CustomValidator for
// RoleInstanceSet. It enforces the static KEP-527 rules and the fail-closed
// signal-key ownership checks that cannot be expressed in the CRD schema.
// +kubebuilder:object:generate=false
type RoleInstanceSetValidator struct {
	Client    client.Client
	APIReader client.Reader
}

var _ admission.CustomValidator = &RoleInstanceSetValidator{}

// ValidateCreate validates a new RoleInstanceSet.
func (v *RoleInstanceSetValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	set, ok := obj.(*RoleInstanceSet)
	if !ok {
		return nil, fmt.Errorf("expected *RoleInstanceSet but got %T", obj)
	}
	return nil, v.validate(ctx, nil, set)
}

// ValidateUpdate validates a RoleInstanceSet update.
func (v *RoleInstanceSetValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldSet, ok := oldObj.(*RoleInstanceSet)
	if !ok {
		return nil, fmt.Errorf("expected *RoleInstanceSet but got %T", oldObj)
	}
	set, ok := newObj.(*RoleInstanceSet)
	if !ok {
		return nil, fmt.Errorf("expected *RoleInstanceSet but got %T", newObj)
	}
	return nil, v.validate(ctx, oldSet, set)
}

// ValidateDelete just implements admission.CustomValidator. Deletion is not
// rejected here; foreground-deletion options are not visible in the object.
func (v *RoleInstanceSetValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *RoleInstanceSetValidator) validate(ctx context.Context, oldSet, set *RoleInstanceSet) error {
	newLifecycle := set.Spec.RoleInstanceTemplate.Lifecycle
	oldLifecycle := (*RoleInstanceLifecycle)(nil)
	if oldSet != nil {
		oldLifecycle = oldSet.Spec.RoleInstanceTemplate.Lifecycle
	}
	newHasHooks := HasRoleInstanceLifecycleHooks(newLifecycle)
	if !newHasHooks && !HasRoleInstanceLifecycleHooks(oldLifecycle) {
		return nil
	}
	if newHasHooks && !features.RoleInstanceLifecycleHooksEnabled() {
		return fmt.Errorf("spec.roleInstanceTemplate.lifecycle requires the RoleInstanceLifecycleHooks feature gate")
	}
	if newHasHooks {
		if err := ValidateRoleInstanceSetLifecycle(set); err != nil {
			return err
		}
	}
	return v.validateSignalKeyOwnership(ctx, oldSet, set)
}

// validateSignalKeyOwnership performs the Alpha fail-closed cross-resource
// checks. It lists governed RoleInstances through the uncached API reader and
// rejects on any read error. This is deliberately conservative: a stale or
// unavailable observation must not approve a key reassignment.
func (v *RoleInstanceSetValidator) validateSignalKeyOwnership(ctx context.Context, oldSet, set *RoleInstanceSet) error {
	if oldSet == nil {
		return nil
	}
	if equalRoleInstanceLifecycle(oldSet.Spec.RoleInstanceTemplate.Lifecycle, set.Spec.RoleInstanceTemplate.Lifecycle) &&
		apiequality.Semantic.DeepEqual(oldSet.Spec.Selector, set.Spec.Selector) {
		return nil
	}

	selector, err := metav1.LabelSelectorAsSelector(set.Spec.Selector)
	if err != nil {
		return err
	}
	reader := v.APIReader
	if reader == nil {
		reader = v.Client
	}
	if reader == nil {
		return fmt.Errorf("cannot validate RoleInstance lifecycle signal keys: no API reader")
	}

	instanceList := &RoleInstanceList{}
	if err := reader.List(ctx, instanceList, client.InNamespace(set.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return fmt.Errorf("failed to list governed RoleInstances for lifecycle validation: %w", err)
	}

	oldKeys := lifecycleHookOwnership(oldSet.Spec.RoleInstanceTemplate.Lifecycle)
	newKeys := lifecycleHookOwnership(set.Spec.RoleInstanceTemplate.Lifecycle)
	selectorKeys := labelSelectorKeys(set.Spec.Selector)
	for i := range instanceList.Items {
		instance := &instanceList.Items[i]
		if !metav1.IsControlledBy(instance, set) {
			continue
		}
		recorded := recordedSignalKeys(instance)
		for key, oldHook := range oldKeys {
			newHook, stillUsed := newKeys[key]
			if stillUsed && newHook == oldHook {
				continue
			}
			if _, recordedKey := recorded[key]; recordedKey {
				return fmt.Errorf(
					"signal label key %q is still recorded by RoleInstance %s; it cannot be reassigned or removed",
					key, instance.Name)
			}
		}
		for key := range selectorKeys {
			if _, recordedKey := recorded[key]; recordedKey {
				return fmt.Errorf(
					"selector key %q is still recorded as a runtime signal label on RoleInstance %s",
					key, instance.Name)
			}
		}
	}
	return nil
}

func lifecycleHookOwnership(lifecycle *RoleInstanceLifecycle) map[string]string {
	out := make(map[string]string)
	if lifecycle == nil {
		return out
	}
	for _, hookName := range LifecycleHookNames() {
		hook := RoleInstanceLifecycleHookFor(lifecycle, hookName)
		if hook == nil {
			continue
		}
		for key := range hook.SignalLabels {
			out[key] = hookName
		}
	}
	return out
}

func labelSelectorKeys(selector *metav1.LabelSelector) map[string]struct{} {
	out := make(map[string]struct{})
	if selector == nil {
		return out
	}
	for key := range selector.MatchLabels {
		out[key] = struct{}{}
	}
	for _, expression := range selector.MatchExpressions {
		out[expression.Key] = struct{}{}
	}
	return out
}

func recordedSignalKeys(instance *RoleInstance) map[string]struct{} {
	out := make(map[string]struct{})
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

func equalRoleInstanceLifecycle(old, new *RoleInstanceLifecycle) bool {
	if old == nil || new == nil {
		return old == nil && new == nil
	}
	equalHook := func(a, b *RoleInstanceLifecycleHook) bool {
		if a == nil || b == nil {
			return a == nil && b == nil
		}
		return labels.Equals(a.SignalLabels, b.SignalLabels)
	}
	return equalHook(old.PreUpdate, new.PreUpdate) &&
		equalHook(old.PreRecover, new.PreRecover) &&
		equalHook(old.PostReady, new.PostReady) &&
		equalHook(old.PreDelete, new.PreDelete)
}
