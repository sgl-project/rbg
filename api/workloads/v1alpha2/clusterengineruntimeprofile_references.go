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
	"sync/atomic"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
)

// Kind names of the group's kinds, shared by the profile-reference listing,
// the owner checks and the invalid-error constructors.
const (
	roleBasedGroupKind    = "RoleBasedGroup"
	roleBasedGroupSetKind = "RoleBasedGroupSet"
)

// engineRuntimeLabelConverged records whether the has-engine-runtimes
// label scan has completed at least once with no mismatch since this
// process started. Until then the admission webhook keeps using the
// unscoped reference lookup, so the upgrade window before the backfill
// converges cannot admit a write that pre-label referencing objects should
// have blocked.
var engineRuntimeLabelConverged atomic.Bool

// SetEngineRuntimeLabelConverged records whether the has-engine-runtimes
// label backfill has converged. The ClusterEngineRuntimeProfile controller
// sets it after a full scan finds no mismatch; it resets on process
// restart.
func SetEngineRuntimeLabelConverged(converged bool) {
	engineRuntimeLabelConverged.Store(converged)
}

// EngineRuntimeLabelConverged reports whether the has-engine-runtimes label
// backfill has converged in this process. Admission falls back to the
// unscoped reference lookup while it has not.
func EngineRuntimeLabelConverged() bool {
	return engineRuntimeLabelConverged.Load()
}

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
//
// The Lists are scoped to objects carrying the has-engine-runtimes label,
// so the lookup's cost scales with the number of referencing objects
// rather than the size of the cluster. The label is maintained on every
// RoleBasedGroup and RoleBasedGroupSet write by the admission defaulters
// (SetHasEngineRuntimesLabel) and backfilled by the profile controller for
// objects that predate it; see ListAllEngineRuntimeProfileReferences for
// the unscoped variant.
func ListEngineRuntimeProfileReferences(
	ctx context.Context,
	reader client.Reader,
	profileName string,
) ([]EngineRuntimeProfileReference, error) {
	return listEngineRuntimeProfileReferences(ctx, reader, profileName,
		client.HasLabels{constants.HasEngineRuntimesLabelKey})
}

// ListAllEngineRuntimeProfileReferences is the label-independent variant of
// ListEngineRuntimeProfileReferences. It is the migration and authority
// path: the profile controller uses it (through the manager's informer
// cache) to count references and to backfill the has-engine-runtimes label
// onto referencing objects written before the label existed.
func ListAllEngineRuntimeProfileReferences(
	ctx context.Context,
	reader client.Reader,
	profileName string,
) ([]EngineRuntimeProfileReference, error) {
	return listEngineRuntimeProfileReferences(ctx, reader, profileName)
}

func listEngineRuntimeProfileReferences(
	ctx context.Context,
	reader client.Reader,
	profileName string,
	listOpts ...client.ListOption,
) ([]EngineRuntimeProfileReference, error) {
	var refs []EngineRuntimeProfileReference

	rbgList := &RoleBasedGroupList{}
	if err := reader.List(ctx, rbgList, listOpts...); err != nil {
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
	if err := reader.List(ctx, rbgsList, listOpts...); err != nil {
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

// SetHasEngineRuntimesLabel keeps the has-engine-runtimes label on a
// RoleBasedGroup in sync with whether any of its roles references an
// engine runtime profile. The admission defaulters call it on every write
// so the webhook's label-scoped reference lookup cannot miss a reference
// created after the lookup's label maintenance shipped.
func SetHasEngineRuntimesLabel(rbg *RoleBasedGroup) {
	if rbg.Labels == nil && roleSpecReferencesEngineRuntime(&rbg.Spec) {
		rbg.Labels = make(map[string]string, 1)
	}
	setHasEngineRuntimesLabel(rbg.Labels, roleSpecReferencesEngineRuntime(&rbg.Spec))
}

// SetRoleBasedGroupSetHasEngineRuntimesLabel is the RoleBasedGroupSet
// counterpart of SetHasEngineRuntimesLabel, keyed on the set's group
// template: a set whose template references a profile counts as a live
// reference even at zero replicas.
func SetRoleBasedGroupSetHasEngineRuntimesLabel(rbgs *RoleBasedGroupSet) {
	if rbgs.Labels == nil && roleSpecReferencesEngineRuntime(&rbgs.Spec.GroupTemplate.Spec) {
		rbgs.Labels = make(map[string]string, 1)
	}
	setHasEngineRuntimesLabel(rbgs.Labels, roleSpecReferencesEngineRuntime(&rbgs.Spec.GroupTemplate.Spec))
}

// setHasEngineRuntimesLabel upserts or removes the label value in place.
// A nil map means the object carries no labels: if the label must be
// removed the map can stay nil, so callers only allocate when setting it.
func setHasEngineRuntimesLabel(labels map[string]string, references bool) {
	if references {
		labels[constants.HasEngineRuntimesLabelKey] = "true"
		return
	}
	delete(labels, constants.HasEngineRuntimesLabelKey)
}

// roleSpecReferencesEngineRuntime reports whether any role of the spec
// carries an engineRuntimes entry with any profile name.
func roleSpecReferencesEngineRuntime(spec *RoleBasedGroupSpec) bool {
	for i := range spec.Roles {
		if len(spec.Roles[i].EngineRuntimes) > 0 {
			return true
		}
	}
	return false
}
