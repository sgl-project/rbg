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
	"reflect"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// ClusterEngineRuntimeProfileValidator implements admission.CustomValidator
// for ClusterEngineRuntimeProfile, enforcing the freeze of KEP-531 Profile
// Mutability: the moment any live RoleBasedGroup, or any RoleBasedGroupSet
// whose group template embeds the reference, carries an engineRuntimes
// entry naming the profile, its spec is frozen — spec updates and deletes
// are rejected, while labels, annotations and other metadata stay
// editable and creation stays unrestricted. Rejection is the alpha
// behavior, registered failurePolicy: Fail, with no warning window, no
// opt-out flag and deliberately no in-use finalizer. Wired into the
// manager by SetupWebhookWithManager.
//
// +kubebuilder:object:generate=false
type ClusterEngineRuntimeProfileValidator struct {
	// Reader must be uncached (mgr.GetAPIReader()): a fresh reference
	// created against an informer cache that has not caught up must
	// still freeze the profile.
	Reader client.Reader
}

var _ admission.CustomValidator = &ClusterEngineRuntimeProfileValidator{}

// ValidateCreate lets profile creation through unrestricted.
func (v *ClusterEngineRuntimeProfileValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	profile, ok := obj.(*ClusterEngineRuntimeProfile)
	if !ok {
		return nil, fmt.Errorf("expected *ClusterEngineRuntimeProfile but got %T", obj)
	}
	klog.V(4).InfoS("validating ClusterEngineRuntimeProfile on create", "name", profile.Name)
	return nil, nil
}

// ValidateUpdate rejects a spec change while the profile is referenced;
// metadata-only changes are admitted.
func (v *ClusterEngineRuntimeProfileValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldProfile, ok := oldObj.(*ClusterEngineRuntimeProfile)
	if !ok {
		return nil, fmt.Errorf("expected *ClusterEngineRuntimeProfile but got %T", oldObj)
	}
	newProfile, ok := newObj.(*ClusterEngineRuntimeProfile)
	if !ok {
		return nil, fmt.Errorf("expected *ClusterEngineRuntimeProfile but got %T", newObj)
	}
	klog.V(4).InfoS("validating ClusterEngineRuntimeProfile on update", "name", newProfile.Name)

	if reflect.DeepEqual(oldProfile.Spec, newProfile.Spec) {
		return nil, nil
	}
	return nil, v.rejectWhileReferenced(ctx, newProfile.Name, "update")
}

// ValidateDelete rejects deletion while the profile is referenced.
func (v *ClusterEngineRuntimeProfileValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	profile, ok := obj.(*ClusterEngineRuntimeProfile)
	if !ok {
		return nil, fmt.Errorf("expected *ClusterEngineRuntimeProfile but got %T", obj)
	}
	klog.V(4).InfoS("validating ClusterEngineRuntimeProfile on delete", "name", profile.Name)

	return nil, v.rejectWhileReferenced(ctx, profile.Name, "delete")
}

func (v *ClusterEngineRuntimeProfileValidator) rejectWhileReferenced(ctx context.Context, profileName, verb string) error {
	refs, err := ListEngineRuntimeProfileReferences(ctx, v.Reader, profileName)
	if err != nil {
		// failurePolicy: Fail — a lookup error blocks the write rather
		// than passing it unenforced.
		return fmt.Errorf("check engine runtime profile %q references: %w", profileName, err)
	}
	if len(refs) == 0 {
		return nil
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })
	refStrings := make([]string, len(refs))
	for i := range refs {
		refStrings[i] = refs[i].String()
	}
	return fmt.Errorf(
		"engine runtime profile %q is frozen: %s is rejected while a live reference exists (%s) — publish the revision as a new profile and migrate references one at a time",
		profileName, verb, strings.Join(refStrings, ", "))
}
