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

package drills

import (
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
)

// See the package doc in harness.go for what this suite proves and what it
// deliberately does not.
func TestUpgradeDowngradeDrills(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	// Lazy on purpose: ginkgo dry runs construct the spec tree without
	// running BeforeSuite, and an eager framework would connect to a
	// cluster even then. f.BeforeAll() initializes the clients below.
	f := framework.NewLazyFramework(true)

	ginkgo.BeforeSuite(
		func() {
			if !drillsSelected() {
				ginkgo.Skip("RBGS_DRILLS is not 1; this suite installs rbgs from " + drillFromRef() +
					" itself, rolls its controller back and forward, and re-applies webhook " +
					"configurations, so it only runs when explicitly selected via " +
					"`make test-e2e-drills` (or RBGS_DRILLS=1)")
				return
			}
			// The lazy framework's clients must exist before the cluster
			// precondition reads the Deployment, and the from-release chart
			// must be checked out before any default image value is read
			// (the accessors cache their answer, so a read before the
			// checkout would poison them with its error forever).
			f.Initialize()
			chart, err := checkoutDrillFromChart()
			if err != nil {
				ginkgo.Fail(drillCleanupInstructions(err.Error()))
				return
			}
			if err := requireDrillFrom(); err != nil {
				ginkgo.Fail(drillCleanupInstructions(err.Error()))
				return
			}
			fromChartPathValue = chart

			requireCleanDrillCluster(f)

			installDrillFromRelease(f, chart)
			verifyDrillFromInstall(f, chart)

			// After the install is verified and before the first fixture
			// write: BeforeAll creates the namespace the fixtures and the
			// serving probe need.
			f.BeforeAll()
			waitDrillFromServing(f)
		},
	)
	ginkgo.AfterSuite(
		func() {
			// Registered first so it runs last. The release has to outlive the
			// namespaced cleanup below: deleting the fixtures needs a live
			// controller to remove their children, and the frozen-profile
			// gate rejects a profile delete while the fixtures still
			// reference it.
			defer teardownDrillRelease(f)

			// Nothing namespaced to clean up unless BeforeAll got as far as
			// creating the namespace — and a suite that skipped on the gate
			// or failed its precondition must not try: an empty namespace
			// would scope the cleanup calls to every namespace.
			if f.Namespace == "" {
				return
			}
			f.AfterAll()

			// The fixture RoleBasedGroups carry the last live references to
			// the profiles, and the frozen-profile gate is back on at this
			// point, so the profiles can only be deleted once the namespace
			// deletion has been processed. Bounded and logged, not
			// asserted: teardown must not add a failure that competes with
			// the finding the run exists to surface.
			waitRBGsReaped(f)
			cleanupProfiles(f, profRuntime, profPatches, profFrozen)
		},
	)

	ginkgo.Describe(
		"[drills] KEP-531 upgrade/downgrade drills: "+drillFromRef()+" to current and back",
		ginkgo.Ordered,
		func() {
			RunDrillSpecs(f)
		},
	)

	ginkgo.RunSpecs(t, "run rbg KEP-531 upgrade/downgrade drill e2e test")
}

// waitRBGsReaped waits until the fixture namespace's RoleBasedGroups are gone,
// so the profile cleanup can stop being rejected by the frozen-profile gate.
// Log-only: teardown must not add a failure that competes with the finding the
// run exists to surface, and a leak is caught with a much clearer message by
// the next run's precondition check.
func waitRBGsReaped(f *framework.Framework) {
	deadline := time.Now().Add(gateTimeout)
	for {
		list := &workloadsv1alpha2.RoleBasedGroupList{}
		if err := f.Client.List(f.Ctx, list, client.InNamespace(f.Namespace)); err != nil {
			ginkgo.GinkgoWriter.Printf("[teardown] could not list the fixture RoleBasedGroups: %v\n", err)
			return
		}
		if len(list.Items) == 0 {
			return
		}
		if time.Now().After(deadline) {
			ginkgo.GinkgoWriter.Printf(
				"[teardown] %d RoleBasedGroups are still in %s after %s; the frozen-profile gate may "+
					"keep the profiles behind\n", len(list.Items), f.Namespace, gateTimeout)
			return
		}
		time.Sleep(gateInterval)
	}
}
