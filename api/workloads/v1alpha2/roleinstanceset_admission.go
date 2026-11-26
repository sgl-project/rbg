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

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/rbgs/api/workloads/constants"
)

// RoleInstanceSetValidator implements admission.CustomValidator for
// RoleInstanceSet. It enforces the static KEP-527 rules that cannot be
// expressed in the CRD schema. Live RoleInstance coordination state is
// deliberately not cross-checked here: the executor's conditional
// signalLabel removal (re-reading hook-state before deleting) fences
// superseded executions, so reconfiguring hooks while operations are armed
// is an operator decision.
// +kubebuilder:object:generate=false
type RoleInstanceSetValidator struct{}

var _ admission.CustomValidator = &RoleInstanceSetValidator{}

// ValidateCreate validates a new RoleInstanceSet.
func (v *RoleInstanceSetValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	set, ok := obj.(*RoleInstanceSet)
	if !ok {
		return nil, fmt.Errorf("expected *RoleInstanceSet but got %T", obj)
	}
	return nil, v.validate(nil, set)
}

// ValidateUpdate validates a RoleInstanceSet update.
func (v *RoleInstanceSetValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldSet, ok := oldObj.(*RoleInstanceSet)
	if !ok {
		return nil, fmt.Errorf("expected *RoleInstanceSet but got %T", oldObj)
	}
	set, ok := newObj.(*RoleInstanceSet)
	if !ok {
		return nil, fmt.Errorf("expected *RoleInstanceSet but got %T", newObj)
	}
	return nil, v.validate(oldSet, set)
}

// ValidateDelete just implements admission.CustomValidator. Deletion is not
// rejected here; foreground-deletion options are not visible in the object.
func (v *RoleInstanceSetValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *RoleInstanceSetValidator) validate(oldSet, set *RoleInstanceSet) error {
	newLifecycle := set.Spec.RoleInstanceTemplate.Lifecycle
	oldLifecycle := (*RoleInstanceLifecycle)(nil)
	if oldSet != nil {
		oldLifecycle = oldSet.Spec.RoleInstanceTemplate.Lifecycle
	}
	newHasHooks := HasRoleInstanceLifecycleHooks(newLifecycle)
	if !newHasHooks && !HasRoleInstanceLifecycleHooks(oldLifecycle) {
		return nil
	}
	if pattern := set.Annotations[constants.RoleInstancePatternKey]; pattern != "" &&
		pattern != string(constants.StatefulPattern) {
		return fmt.Errorf(
			"spec.roleInstanceTemplate.lifecycle requires the native Stateful RoleInstanceSet pattern")
	}
	if newHasHooks {
		return ValidateRoleInstanceSetLifecycle(set)
	}
	return nil
}
