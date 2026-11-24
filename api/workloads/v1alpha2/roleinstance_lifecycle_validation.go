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
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevalidation "k8s.io/apimachinery/pkg/util/validation"

	"sigs.k8s.io/rbgs/api/workloads/constants"
)

// LifecycleHookNames returns hook names in a stable order.
func LifecycleHookNames() [4]string {
	return [4]string{"preUpdate", "preRecover", "postReady", "preDelete"}
}

// RoleInstanceLifecycleHookFor returns the configured hook for a stable hook name.
func RoleInstanceLifecycleHookFor(lifecycle *RoleInstanceLifecycle, name string) *RoleInstanceLifecycleHook {
	if lifecycle == nil {
		return nil
	}
	switch name {
	case "preUpdate":
		return lifecycle.PreUpdate
	case "preRecover":
		return lifecycle.PreRecover
	case "postReady":
		return lifecycle.PostReady
	case "preDelete":
		return lifecycle.PreDelete
	default:
		return nil
	}
}

// HasRoleInstanceLifecycleHooks reports whether at least one KEP-527 hook is configured.
func HasRoleInstanceLifecycleHooks(lifecycle *RoleInstanceLifecycle) bool {
	if lifecycle == nil {
		return false
	}
	return lifecycle.PreUpdate != nil || lifecycle.PreRecover != nil ||
		lifecycle.PostReady != nil || lifecycle.PreDelete != nil
}

// ValidateRoleInstanceLifecycle validates the static KEP-527 configuration.
// selectorKeys and declarativeLabels may be nil. selectorKeys rejects a signal
// key that can cause membership to flap. declarativeLabels rejects a key that
// is also copied by ordinary metadata synchronization.
func ValidateRoleInstanceLifecycle(
	path string,
	lifecycle *RoleInstanceLifecycle,
	selector *metav1.LabelSelector,
	declarativeLabels ...map[string]string,
) error {
	if !HasRoleInstanceLifecycleHooks(lifecycle) {
		return nil
	}

	var errs []string
	for _, hookName := range LifecycleHookNames() {
		hook := RoleInstanceLifecycleHookFor(lifecycle, hookName)
		if hook == nil {
			continue
		}
		if len(hook.SignalLabels) == 0 {
			errs = append(errs, fmt.Sprintf("%s.%s.signalLabels must not be empty", path, hookName))
			continue
		}
		for key, value := range hook.SignalLabels {
			if key == "" {
				errs = append(errs, fmt.Sprintf("%s.%s.signalLabels key must not be empty", path, hookName))
				continue
			}
			if strings.HasPrefix(key, constants.RBGPrefix) {
				errs = append(errs, fmt.Sprintf(
					"%s.%s.signalLabels[%s] uses reserved prefix %q", path, hookName, key, constants.RBGPrefix))
				continue
			}
			if key == constants.RoleInstanceLifecycleHookStateKey {
				errs = append(errs, fmt.Sprintf(
					"%s.%s.signalLabels[%s] collides with the controller-reserved hook-state key",
					path, hookName, key))
				continue
			}
			if reasons := kubevalidation.IsQualifiedName(key); len(reasons) > 0 {
				errs = append(errs, fmt.Sprintf(
					"%s.%s.signalLabels[%s] is not a valid Kubernetes label key: %s",
					path, hookName, key, strings.Join(reasons, ", ")))
				continue
			}
			if reasons := kubevalidation.IsValidLabelValue(value); len(reasons) > 0 {
				errs = append(errs, fmt.Sprintf(
					"%s.%s.signalLabels[%s] has invalid value %q: %s",
					path, hookName, key, value, strings.Join(reasons, ", ")))
				continue
			}
			if selector != nil {
				if _, found := selector.MatchLabels[key]; found {
					errs = append(errs, fmt.Sprintf(
						"%s.%s.signalLabels[%s] collides with the owning selector",
						path, hookName, key))
				}
				for _, expression := range selector.MatchExpressions {
					if expression.Key == key {
						errs = append(errs, fmt.Sprintf(
							"%s.%s.signalLabels[%s] collides with owning selector expression %q",
							path, hookName, key, expression.Key))
						break
					}
				}
			}
			for _, labels := range declarativeLabels {
				if _, found := labels[key]; found {
					errs = append(errs, fmt.Sprintf(
						"%s.%s.signalLabels[%s] collides with a declarative label",
						path, hookName, key))
					break
				}
			}
		}
	}

	if selector != nil {
		selectorKeys := make(map[string]struct{}, len(selector.MatchLabels)+len(selector.MatchExpressions))
		for key := range selector.MatchLabels {
			selectorKeys[key] = struct{}{}
		}
		for _, expression := range selector.MatchExpressions {
			selectorKeys[expression.Key] = struct{}{}
		}
		for key := range selectorKeys {
			if key == constants.RoleInstanceLifecycleHookStateKey {
				errs = append(errs, fmt.Sprintf(
					"selector key %s is reserved for the RoleInstance lifecycle hook-state", key))
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return fmt.Errorf("%s", errs[0])
	}
	return fmt.Errorf("invalid RoleInstance lifecycle: %s", strings.Join(errs, "; "))
}

// ValidateRoleInstanceSetLifecycle validates a standalone or RBG-owned
// RoleInstanceSet's new lifecycle snapshot and its mutual exclusion with the
// legacy RoleInstanceSet protocol.
func ValidateRoleInstanceSetLifecycle(set *RoleInstanceSet) error {
	lifecycle := set.Spec.RoleInstanceTemplate.Lifecycle
	if !HasRoleInstanceLifecycleHooks(lifecycle) {
		return nil
	}
	if set.Spec.Lifecycle != nil {
		return fmt.Errorf(
			"RoleInstanceSet.spec.roleInstanceTemplate.lifecycle and legacy spec.lifecycle cannot both be non-empty")
	}

	declarativeLabels := make(map[string]string)
	for i := range set.Spec.RoleInstanceTemplate.Components {
		for key, value := range set.Spec.RoleInstanceTemplate.Components[i].Labels {
			declarativeLabels[key] = value
		}
		for key, value := range set.Spec.RoleInstanceTemplate.Components[i].Template.Labels {
			declarativeLabels[key] = value
		}
	}
	return ValidateRoleInstanceLifecycle(
		"spec.roleInstanceTemplate.lifecycle", lifecycle, set.Spec.Selector, declarativeLabels)
}
