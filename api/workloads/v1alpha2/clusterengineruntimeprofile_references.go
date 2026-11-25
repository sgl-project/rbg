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
	"sync"
	"time"

	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
)

// Kind names of the group's kinds, shared by the profile-reference listing,
// the owner checks and the invalid-error constructors.
const (
	roleBasedGroupKind    = "RoleBasedGroup"
	roleBasedGroupSetKind = "RoleBasedGroupSet"
)

// engineRuntimeLabelConvergence tracks whether the has-engine-runtimes
// label scan has completed at least once with no mismatch since this
// process started. Until then the admission webhook keeps using the
// unscoped reference lookup, so the upgrade window before the backfill
// converges cannot admit a write that pre-label referencing objects should
// have blocked.
var engineRuntimeLabelConvergence struct {
	mu sync.RWMutex

	converged  bool
	driftEpoch uint64
}

// engineRuntimeLabelStartupScanner converges the process-local gate on every
// webhook replica, including replicas that never become leader and therefore
// never run the profile controller. It reads through the manager's shared
// cache and never writes.
type engineRuntimeLabelStartupScanner struct {
	reader     client.Reader
	cache      cache.Cache
	retryAfter time.Duration
	drift      chan struct{}
}

// SetEngineRuntimeLabelConverged records whether the has-engine-runtimes
// label backfill has converged. The ClusterEngineRuntimeProfile controller
// sets it after a full scan finds no mismatch; it resets on process
// restart.
func SetEngineRuntimeLabelConverged(converged bool) {
	engineRuntimeLabelConvergence.mu.Lock()
	defer engineRuntimeLabelConvergence.mu.Unlock()
	engineRuntimeLabelConvergence.converged = converged
	if !converged {
		engineRuntimeLabelConvergence.driftEpoch++
	}
}

// EngineRuntimeLabelConverged reports whether the has-engine-runtimes label
// backfill has converged in this process. Admission falls back to the
// unscoped reference lookup while it has not.
func EngineRuntimeLabelConverged() bool {
	engineRuntimeLabelConvergence.mu.RLock()
	defer engineRuntimeLabelConvergence.mu.RUnlock()
	return engineRuntimeLabelConvergence.converged
}

// BeginEngineRuntimeLabelScan returns the current drift epoch for a label
// backfill scan. The scan must complete only if that epoch is unchanged.
func BeginEngineRuntimeLabelScan() uint64 {
	engineRuntimeLabelConvergence.mu.RLock()
	defer engineRuntimeLabelConvergence.mu.RUnlock()
	return engineRuntimeLabelConvergence.driftEpoch
}

// CompleteEngineRuntimeLabelScan marks the label scan converged only when
// no drift event arrived while it was running.
func CompleteEngineRuntimeLabelScan(epoch uint64) {
	engineRuntimeLabelConvergence.mu.Lock()
	defer engineRuntimeLabelConvergence.mu.Unlock()
	if engineRuntimeLabelConvergence.driftEpoch != epoch {
		engineRuntimeLabelConvergence.converged = false
		return
	}
	engineRuntimeLabelConvergence.converged = true
}

// MarkEngineRuntimeLabelDrift records that a referencing object arrived
// without the has-engine-runtimes label and invalidates any in-flight scan.
func MarkEngineRuntimeLabelDrift() {
	engineRuntimeLabelConvergence.mu.Lock()
	defer engineRuntimeLabelConvergence.mu.Unlock()
	engineRuntimeLabelConvergence.driftEpoch++
	engineRuntimeLabelConvergence.converged = false
}

// Start scans this replica's cache and then waits for RBG/RBGSet events.
// A scan error retries after a delay; a mismatch does not poll. The leader's
// backfill (or any later write) emits a label event that wakes this scanner.
func (s *engineRuntimeLabelStartupScanner) Start(ctx context.Context) error {
	if s.drift == nil {
		s.drift = make(chan struct{}, 1)
	}
	if s.cache != nil {
		if err := s.watch(ctx); err != nil {
			return err
		}
	}
	for {
		if s.cache != nil && !s.cache.WaitForCacheSync(ctx) {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(s.retryAfter):
			}
			continue
		}
		epoch := BeginEngineRuntimeLabelScan()
		converged, err := scanEngineRuntimeLabelState(ctx, s.reader)
		if err == nil {
			if converged {
				CompleteEngineRuntimeLabelScan(epoch)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-s.drift:
				continue
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(s.retryAfter):
		}
	}
}

func (s *engineRuntimeLabelStartupScanner) watch(ctx context.Context) error {
	rbgInformer, err := s.cache.GetInformer(ctx, &RoleBasedGroup{})
	if err != nil {
		return fmt.Errorf("get rolebasedgroup informer for engine runtime label convergence: %w", err)
	}
	if _, err := rbgInformer.AddEventHandler(s.eventHandler()); err != nil {
		return fmt.Errorf("watch rolebasedgroups for engine runtime label convergence: %w", err)
	}

	rbgsInformer, err := s.cache.GetInformer(ctx, &RoleBasedGroupSet{})
	if err != nil {
		return fmt.Errorf("get rolebasedgroupset informer for engine runtime label convergence: %w", err)
	}
	if _, err := rbgsInformer.AddEventHandler(s.eventHandler()); err != nil {
		return fmt.Errorf("watch rolebasedgroupsets for engine runtime label convergence: %w", err)
	}
	return nil
}

func (s *engineRuntimeLabelStartupScanner) eventHandler() toolscache.ResourceEventHandler {
	return toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if !s.objectMatches(obj) {
				s.markDrift(obj)
				s.wake()
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldMatch, oldValid := s.objectMatchState(oldObj)
			newMatch, newValid := s.objectMatchState(newObj)
			if !oldValid || !newValid || oldMatch == newMatch {
				return
			}
			if newMatch {
				s.wake()
				return
			}
			s.markDrift(newObj)
			s.wake()
		},
		DeleteFunc: func(obj interface{}) {
			if !s.objectMatches(obj) {
				s.wake()
			}
		},
	}
}

func (s *engineRuntimeLabelStartupScanner) markDrift(obj interface{}) {
	switch typed := obj.(type) {
	case *RoleBasedGroup:
		if !engineRuntimeLabelMatchesSpec(typed.Labels, &typed.Spec) {
			MarkEngineRuntimeLabelDrift()
		}
	case *RoleBasedGroupSet:
		if !engineRuntimeLabelMatchesSpec(typed.Labels, &typed.Spec.GroupTemplate.Spec) {
			MarkEngineRuntimeLabelDrift()
		}
	}
}

func (s *engineRuntimeLabelStartupScanner) objectMatches(obj interface{}) bool {
	matches, valid := s.objectMatchState(obj)
	return valid && matches
}

func (s *engineRuntimeLabelStartupScanner) objectMatchState(obj interface{}) (bool, bool) {
	switch typed := obj.(type) {
	case *RoleBasedGroup:
		return engineRuntimeLabelMatchesSpec(typed.Labels, &typed.Spec), true
	case *RoleBasedGroupSet:
		return engineRuntimeLabelMatchesSpec(typed.Labels, &typed.Spec.GroupTemplate.Spec), true
	default:
		return false, false
	}
}

func (s *engineRuntimeLabelStartupScanner) wake() {
	select {
	case s.drift <- struct{}{}:
	default:
	}
}

// NeedLeaderElection reports that the scanner must run on every webhook
// replica, not only on the leader.
func (s *engineRuntimeLabelStartupScanner) NeedLeaderElection() bool {
	return false
}

// scanEngineRuntimeLabelState reports whether every live RoleBasedGroup and
// RoleBasedGroupSet carries the label state implied by its spec.
func scanEngineRuntimeLabelState(ctx context.Context, reader client.Reader) (bool, error) {
	rbgList := &RoleBasedGroupList{}
	if err := reader.List(ctx, rbgList); err != nil {
		return false, fmt.Errorf("list rolebasedgroups for engine runtime label convergence: %w", err)
	}
	for i := range rbgList.Items {
		if !engineRuntimeLabelMatchesSpec(rbgList.Items[i].Labels, &rbgList.Items[i].Spec) {
			return false, nil
		}
	}

	rbgsList := &RoleBasedGroupSetList{}
	if err := reader.List(ctx, rbgsList); err != nil {
		return false, fmt.Errorf("list rolebasedgroupsets for engine runtime label convergence: %w", err)
	}
	for i := range rbgsList.Items {
		if !engineRuntimeLabelMatchesSpec(rbgsList.Items[i].Labels, &rbgsList.Items[i].Spec.GroupTemplate.Spec) {
			return false, nil
		}
	}
	return true, nil
}

// engineRuntimeLabelMatchesSpec reports whether an object's top-level label
// state agrees with whether its roles reference any engine runtime profile.
func engineRuntimeLabelMatchesSpec(labels map[string]string, spec *RoleBasedGroupSpec) bool {
	if roleSpecReferencesEngineRuntime(spec) {
		return labels[constants.HasEngineRuntimesLabelKey] == constants.HasEngineRuntimesLabelValue
	}
	_, present := labels[constants.HasEngineRuntimesLabelKey]
	return !present
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
		labels[constants.HasEngineRuntimesLabelKey] = constants.HasEngineRuntimesLabelValue
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
