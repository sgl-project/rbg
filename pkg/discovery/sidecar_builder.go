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

package discovery

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

// InjectEngineRuntimeFragments merges the referenced profiles' pod-level
// fragments into one position's rendered pod spec, in reference order:
// initContainers, containers (sidecars) and volumes append-if-missing
// (name-keyed, therefore idempotent), then the role-side Containers override
// appends env/args to profile-provided containers, once per reference exactly
// as on main — the legacy merge keeps main's behavior verbatim, including its
// silent no-ops: a plain append with no conflict check (an override entry
// whose name the target container already declares appends a second entry
// and wins positionally, exactly as main's merge always did), an override
// naming a container the reference's own profile does not declare is
// skipped with a V(1) log line, and init containers are unreachable by it.
// roleName attributes the cross-profile skip events below to the role whose
// engineRuntimes authored the references. recorder and rbg carry the
// EngineRuntimeNameCollision warning; a nil recorder is tolerated
// (log-only).
//
// The one deliberate main divergence is the cross-profile skip event
// (KEP-531 Duplicate Delivery): when two different profiles declare the
// same container or volume name, the earlier reference wins and the later
// profile's same-named entry is skipped — a skipped container can be a
// control the operator believes present, so the skip is deterministic but
// not silent. A name already present from the role's own template (or from
// the same profile's earlier reference) is the sanctioned precedence — the
// role's same-named declaration always wins, silently.
func InjectEngineRuntimeFragments(
	ctx context.Context, podSpec *v1.PodTemplateSpec, fragments []EngineRuntimeFragment,
	roleName string, recorder record.EventRecorder, rbg *workloadsv1alpha2.RoleBasedGroup,
) error {
	logger := log.FromContext(ctx)
	// injectedBy records which profile introduced each appended fragment
	// name — the cross-profile skip detector's state.
	injectedBy := map[string]string{}
	warnCollision := func(kind, name, profileName, ownerProfile string) {
		emitEngineRuntimeWarning(ctx, recorder, rbg, ReasonEngineRuntimeNameCollision, fmt.Sprintf(
			"role %q: profile %q declares %s %q, which an earlier reference to profile %q already delivered — "+
				"the earlier reference wins and this profile's entry is skipped; reordering engineRuntimes "+
				"changes the winner, and profile publishers should prefix container and volume names to "+
				"avoid cross-publisher collisions",
			roleName, profileName, kind, name, ownerProfile))
	}
	skipOrAppend := func(kind, name, ownerProfile string, present bool) bool {
		if !present {
			injectedBy[name] = ownerProfile
			return true
		}
		if owner, ok := injectedBy[name]; ok && owner != ownerProfile {
			warnCollision(kind, name, ownerProfile, owner)
		}
		// A name present from the role's own template — or from the same
		// profile's earlier reference — is the sanctioned precedence, not a
		// cross-profile collision: silent skip.
		return false
	}
	for _, fragment := range fragments {
		profileName := fragment.Runtime.ProfileName
		profileContainerNames := make(
			map[string]struct{},
			len(fragment.Profile.Spec.InitContainers)+len(fragment.Profile.Spec.Containers),
		)
		for _, initContainer := range fragment.Profile.Spec.InitContainers {
			profileContainerNames[initContainer.Name] = struct{}{}
			if skipOrAppend("initContainer", initContainer.Name, profileName,
				hasNamedContainer(podSpec.Spec.InitContainers, initContainer.Name)) {
				podSpec.Spec.InitContainers = append(podSpec.Spec.InitContainers, initContainer)
			}
		}
		for _, container := range fragment.Profile.Spec.Containers {
			profileContainerNames[container.Name] = struct{}{}
			if skipOrAppend("container", container.Name, profileName,
				hasNamedContainer(podSpec.Spec.Containers, container.Name)) {
				podSpec.Spec.Containers = append(podSpec.Spec.Containers, container)
			}
		}
		for _, vol := range fragment.Profile.Spec.Volumes {
			if skipOrAppend("volume", vol.Name, profileName,
				hasNamedVolume(podSpec.Spec.Volumes, vol.Name)) {
				podSpec.Spec.Volumes = append(podSpec.Spec.Volumes, vol)
			}
		}
		// override containers based on the role-side runtime entry
		for _, container := range fragment.Runtime.Containers {
			if _, owned := profileContainerNames[container.Name]; !owned {
				logger.V(1).Info(fmt.Sprintf(
					"rbg runtime has container %s but not in clusterEngineRuntime, skip override",
					container.Name,
				))
				continue
			}
			for j, c := range podSpec.Spec.Containers {
				if c.Name != container.Name {
					continue
				}
				target := &podSpec.Spec.Containers[j]
				// Plain append, exactly as on main: the legacy containers
				// merge has no conflict check — a same-named entry appends a
				// second entry and wins positionally (the override gesture),
				// and legacy-only duplicates never error (KEP-531
				// Compatibility Guarantees #3).
				target.Env = append(target.Env, container.Env...)
				if len(container.Args) > 0 {
					target.Args = append(target.Args, container.Args...)
				}
				break
			}
		}
	}
	return nil
}

func hasNamedContainer(containers []v1.Container, name string) bool {
	for _, c := range containers {
		if c.Name == name {
			return true
		}
	}
	return false
}

func hasNamedVolume(volumes []v1.Volume, name string) bool {
	for _, vol := range volumes {
		if vol.Name == name {
			return true
		}
	}
	return false
}
