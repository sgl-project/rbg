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

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Kind names of the group's kinds, shared by the profile-reference listing,
// the owner checks and the invalid-error constructors.
const (
	roleBasedGroupKind    = "RoleBasedGroup"
	roleBasedGroupSetKind = "RoleBasedGroupSet"
)

// EngineRuntimeProfileReference names one live referencing object of a
// ClusterEngineRuntimeProfile.
type EngineRuntimeProfileReference struct {
	// Kind is the referencing object's kind: RoleBasedGroup or
	// RoleBasedGroupSet.
	Kind string
	// Namespace is the referencing object's namespace (empty for
	// cluster-scoped referencing kinds; both kinds are namespaced today,
	// so it is always set).
	Namespace string
	// Name is the referencing object's name.
	Name string
}

// String renders the reference as <kind> <namespace>/<name>.
func (r EngineRuntimeProfileReference) String() string {
	return fmt.Sprintf("%s %s/%s", r.Kind, r.Namespace, r.Name)
}

// ListEngineRuntimeProfileReferences returns every live object carrying an
// engineRuntimes entry naming the profile: RoleBasedGroups across all
// namespaces plus the group templates of RoleBasedGroupSets — a set's
// group template counts as a reference even at zero replicas, no child
// RoleBasedGroup required. reader must be uncached (mgr.GetAPIReader()):
// the admission webhook cannot trust informer cache freshness when
// deciding whether a profile is frozen (KEP-531 Profile Mutability).
func ListEngineRuntimeProfileReferences(
	ctx context.Context,
	reader client.Reader,
	profileName string,
) ([]EngineRuntimeProfileReference, error) {
	var refs []EngineRuntimeProfileReference

	rbgList := &RoleBasedGroupList{}
	if err := reader.List(ctx, rbgList); err != nil {
		return nil, fmt.Errorf("list rolebasedgroups: %w", err)
	}
	for i := range rbgList.Items {
		rbg := &rbgList.Items[i]
		if roleSpecReferencesProfile(&rbg.Spec, profileName) {
			refs = append(refs, EngineRuntimeProfileReference{
				Kind:      roleBasedGroupKind,
				Namespace: rbg.Namespace,
				Name:      rbg.Name,
			})
		}
	}

	rbgsList := &RoleBasedGroupSetList{}
	if err := reader.List(ctx, rbgsList); err != nil {
		return nil, fmt.Errorf("list rolebasedgroupsets: %w", err)
	}
	for i := range rbgsList.Items {
		rbgs := &rbgsList.Items[i]
		if roleSpecReferencesProfile(&rbgs.Spec.GroupTemplate.Spec, profileName) {
			refs = append(refs, EngineRuntimeProfileReference{
				Kind:      roleBasedGroupSetKind,
				Namespace: rbgs.Namespace,
				Name:      rbgs.Name,
			})
		}
	}
	return refs, nil
}

// roleSpecReferencesProfile reports whether any role of the spec carries
// an engineRuntimes entry with the given profileName.
func roleSpecReferencesProfile(spec *RoleBasedGroupSpec, profileName string) bool {
	for i := range spec.Roles {
		for _, er := range spec.Roles[i].EngineRuntimes {
			if er.ProfileName == profileName {
				return true
			}
		}
	}
	return false
}
