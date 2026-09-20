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

package upgrade

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
)

// releaseProfile is everything the suite needs to know about the release it is
// upgrading FROM. The hop is the same experiment for every supported release --
// install the old release, build a running world on it, upgrade, assert nothing
// moved -- but the releases differ in how they are installed, in what proves the
// upgrade landed, and in what the hop itself is expected to change.
type releaseProfile struct {
	// installValues are the helm --set arguments for the release's OWN values
	// layout. The chart moved every top-level value under controller.* between
	// v0.7.0 and v0.8.0 and has no values schema, so a path from the wrong
	// layout is accepted as an inert key and the chart's defaults silently
	// install instead of the pinned images.
	installValues []string

	// newBundleMarker is the evidence that the upgrade's CRD bundle really
	// landed: something the new bundle has that the release being upgraded from
	// cannot have. It must be absent right after installFromRelease -- its
	// absence there is also the proof that the fixtures were created against
	// the old schema -- and present once the upgrade ran. An image tag cannot
	// fake it, which is what makes it the marker.
	newBundleMarker bundleMarker

	// prunesRestartPolicyConfig reports whether the release's CRDs drop
	// restartPolicyConfig from a written RoleBasedGroup. It gates the phase-1
	// prune probe: v0.7.0's CRDs predate the field, v0.8.0's already carry it,
	// and no schema change between v0.8.0 and the version under test adds a
	// RoleBasedGroup field a prune probe could watch instead.
	prunesRestartPolicyConfig bool

	// rewrites records what the hop from this release is expected to change.
	// See recordedRewrites.
	rewrites recordedRewrites
}

// bundleMarker identifies one observable difference between the CRD bundle of the
// release being upgraded from and the bundle the upgrade applies.
type bundleMarker struct {
	// description names the marker in failure messages.
	description string
	// present reports whether the marker currently exists in the cluster. A
	// missing CRD means absent, never an error.
	present func(f *framework.Framework) (bool, error)
}

// fromProfile resolves the profile of the release being upgraded from. An unknown
// tag fails rather than guessing a profile: the values layout and the rewrites
// are release-specific, and a guessed one would either install the wrong images
// or tolerate and require the wrong changes, both vacuously.
func fromProfile() *releaseProfile {
	switch fromGitTag() {
	case "v0.7.0":
		return v070Profile()
	case "v0.8.0":
		return v080Profile()
	}
	ginkgo.Fail(fmt.Sprintf(
		"RBGS_FROM_GIT_TAG=%q is not a release this suite can upgrade from; known: v0.7.0, v0.8.0",
		fromGitTag()))
	return nil
}

// v070Profile is the v0.7.0 -> current hop.
func v070Profile() *releaseProfile {
	rewrites := strategyHealRewrites()

	// KEP 260 flips the default of sharedServiceSelection: v0.7.0 treated an
	// unset field as All, and the current release resolves it to LeaderOnly for
	// RoleInstanceSet leader-worker roles. The controller then patches the
	// selector of the shared Service in place, which drops every worker pod IP
	// from its EndpointSlice without touching a pod.
	// keps/260-leaderonly-service/README.md:251 records this as the breaking
	// case for endpoints and tells affected workloads to set
	// sharedServiceSelection: All.
	//
	// This suite cannot say whether that is the right default, only that it is
	// what the upgrade does. The entry is the fixture's own Service, so a role
	// whose selector is narrowed anywhere else still fails. v0.8.0 already
	// resolves the unset field to LeaderOnly, so its profile records nothing
	// here.
	rewrites.leaderOnlyServices = map[string]bool{sharedServiceName(fxLwp, lwpRole): true}

	return &releaseProfile{
		installValues: []string{
			// The v0.7.0 value paths, which are NOT the current ones: every
			// top-level key moved under controller.* afterwards.
			"--set", "image.repository=" + fromRepo(),
			"--set", "image.tag=" + fromTag(),
			"--set", "image.pullPolicy=IfNotPresent",
			"--set", "crdUpgrade.repository=" + fromCRDUpgradeRepo(),
			"--set", "crdUpgrade.tag=" + fromTag(),
			"--set", "crdUpgrade.imagePullPolicy=IfNotPresent",
			"--set", "portAllocator.enabled=true",
		},
		newBundleMarker: bundleMarker{
			description: "CRD " + warmupCRDName,
			present: func(f *framework.Framework) (bool, error) {
				return crdExists(f, warmupCRDName)
			},
		},
		prunesRestartPolicyConfig: true,
		rewrites:                  rewrites,
	}
}

// v080Profile is the v0.8.0 -> current hop.
func v080Profile() *releaseProfile {
	return &releaseProfile{
		installValues: []string{
			// v0.8.0 already uses the current controller.* layout. The values
			// are still passed explicitly, because the chart defaults to the
			// images of its own release while the suite pins exact ones.
			"--set", "controller.image.repository=" + fromRepo(),
			"--set", "controller.image.tag=" + fromTag(),
			"--set", "controller.image.pullPolicy=IfNotPresent",
			"--set", "crdUpgrade.image.repository=" + fromCRDUpgradeRepo(),
			"--set", "crdUpgrade.image.tag=" + fromTag(),
			"--set", "crdUpgrade.image.pullPolicy=IfNotPresent",
			"--set", "controller.features.portAllocator.enabled=true",
		},
		// The warmup CRD already exists in v0.8.0, so it cannot mark this hop.
		// The strategy type enum on the RoleInstanceSet CRD is what the new
		// bundle adds: v0.8.0's CRDs accept any string there.
		newBundleMarker: bundleMarker{
			description: "the update strategy type enum on CRD " + risCRDName,
			present:     risStrategyEnumPresent,
		},
		prunesRestartPolicyConfig: false,
		rewrites:                  strategyHealRewrites(),
	}
}

// strategyHealRewrites is what every supported hop has in common: neither v0.7.0
// nor v0.8.0 normalized the legacy update strategy spellings, so both store them
// verbatim and the upgraded controller heals them the same way.
//
// RoleInstanceSet and RoleInstance generations were both 1 while the reconciler
// rewrote the stored restartPolicy string into restartPolicyConfig. That rewrite
// moved the RoleInstanceSet revision hash and rolled every role on upgrade, and
// the RoleInstance bump was its consequence: only the resulting in-place update
// reached the code that adds the RoleInstanceInPlaceUpdateReady gate. With the
// reconciler no longer touching the template, both kinds are left alone
// entirely, so neither belongs here.
func strategyHealRewrites() recordedRewrites {
	return recordedRewrites{
		specRewrites: map[string]func(map[string]any) error{
			// The legacy-strategy fixture stores the v1alpha1 spelling "Recreate"
			// of the update strategy type, which the old release copied verbatim
			// into the RoleInstanceSet. The new RoleInstanceSet CRD enum rejects
			// that value, so the mutating webhook heals it to "RecreatePod" on
			// the first write the upgraded controller sends. The heal is a
			// one-off -- the next apply is a no-op.
			"RoleInstanceSet/" + legacyStrategyRISName(): healStoredStrategyType,
			// The legacy-set fixture's child owns its own RoleInstanceSet, which
			// the old release also wrote with the legacy spelling copied verbatim
			// from the template, so it is healed on the upgraded controller's
			// first reconcile too.
			"RoleInstanceSet/" + legacySetChildRISName(): healStoredStrategyType,
			// The same heal reaches the child RoleBasedGroup itself: the RBGS
			// controller re-applies the child from its groupTemplate with the
			// strategy type normalized, so the stored child spec changes on its
			// first reconcile. A top-level RoleBasedGroup has no writer above it,
			// so up-legacy keeps the legacy spelling -- only the child is
			// re-applied.
			"RoleBasedGroup/" + legacySetChildName(): healRoleStrategyTypes,
		},

		// Healing the child's stored spec moved the RBG-layer revision hash, so
		// the controller stamps one new revision for it. The hash moves but no
		// pod does: the RoleInstanceSet layer is repaired in place and its
		// revision is stable, which the RoleInstanceSet entries above assert
		// through content. No revision may be removed, and any count other than
		// one is still reported with its content diff.
		revisionAdds: map[string]int{
			legacySetChildName(): 1,
		},
	}
}

// risStrategyEnumPresent reports whether the RoleInstanceSet CRD constrains
// spec.updateStrategy.type to the v1alpha2 enum. The enum is what the version
// under test adds to that CRD, so its presence marks the new CRD bundle.
func risStrategyEnumPresent(f *framework.Framework) (bool, error) {
	crd := newCRDObject()
	exists, err := crdExists(f, risCRDName)
	if err != nil || !exists {
		return false, err
	}
	if err := f.Client.Get(f.Ctx, clientObjectKey(risCRDName), crd); err != nil {
		return false, err
	}

	versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if err != nil || !found {
		return false, err
	}
	for _, item := range versions {
		version, _ := item.(map[string]interface{})
		enum, found, err := unstructured.NestedSlice(
			version, "schema", "openAPIV3Schema", "properties", "spec", "properties",
			"updateStrategy", "properties", "type", "enum")
		if err != nil {
			return false, err
		}
		if !found {
			continue
		}
		for _, value := range enum {
			if value == string(workloadsv1alpha2.RecreatePodUpdateStrategyType) {
				return true, nil
			}
		}
	}
	return false, nil
}
