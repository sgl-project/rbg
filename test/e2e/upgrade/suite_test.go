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
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
)

// See the package doc in preflight.go for what this suite proves and what it does not.
func TestUpgradeV070ToCurrent(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	f := framework.NewFramework(true)

	ginkgo.BeforeSuite(
		func() {
			requireCleanCluster(f)
			installFromRelease(f)
			verifyOnFromRelease(f)
			f.BeforeAll()
			// After BeforeAll, which the dry-run probe needs for a real namespace, and
			// before the first actual write: CreatePatioRuntime goes through the same
			// webhooks and would race them just as the fixtures did.
			waitFromReleaseServing(f)
			gomega.Expect(utils.CreatePatioRuntime(f.Ctx, f.Client)).Should(gomega.Succeed())
		},
	)
	ginkgo.AfterSuite(
		func() {
			// Registered first so it runs last. The release has to outlive the
			// namespaced cleanup below: deleting the fixtures needs a live controller
			// to run their finalizers, and deleting the CRDs first would remove the
			// types out from under it.
			defer teardownFromRelease(f)

			// Nothing namespaced to clean up unless BeforeAll got as far as creating the
			// namespace, and cleaning up anyway would be actively destructive:
			// framework.AfterEach scopes its DeleteAllOf calls with
			// client.InNamespace(f.Namespace), and an empty namespace means every
			// namespace. A failed precondition leaves f.Namespace empty, so without this
			// guard it would delete every RoleBasedGroup in whatever cluster the
			// KUBECONFIG points at.
			if f.Namespace == "" {
				return
			}

			// Deferred so the namespace is torn down even when the object cleanup
			// below fails its assertions and aborts this function.
			defer f.AfterAll()

			// f.AfterEach() runs here and only here, deliberately not wired as a
			// ginkgo.AfterEach. It would be wrong per spec: the specs are ordered
			// phases sharing one set of fixtures, so deleting them in between would
			// destroy what is measured.
			//
			// It also DeleteAllOf's RoleBasedGroupWarmup, whose CRD only exists once
			// the upgrade has landed, so calling it after an earlier failure raises a
			// NoKindMatchError that shows up as a second failure and hides the first.
			// Skipping it then costs nothing: the deferred namespace delete removes
			// every namespaced fixture anyway.
			if exists, err := crdExists(f, warmupCRDName); err == nil && exists {
				f.AfterEach()
			}
		},
	)

	ginkgo.Describe(
		"[upgrade] v0.7.0 to current upgrade leaves running pods untouched",
		ginkgo.Ordered,
		func() {
			RunUpgradeSpecs(f)
		},
	)

	ginkgo.RunSpecs(t, "run rbg v0.7.0 -> current upgrade compatibility e2e test")
}
