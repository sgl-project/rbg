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

package deprecation

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

// TestDeprecatedWorkloadGate runs against an envtest environment that first
// seeds a pre-0.9.0 RoleBasedGroupSet with a deprecated workload template, then
// installs the 0.9.0 compatibility-mode validating webhooks. This mirrors the
// production upgrade order: legacy objects exist before the gate is admitted.
func TestDeprecatedWorkloadGate(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Deprecated Workload Compatibility Gate Suite")
}

var _ = BeforeSuite(func() {
	testutil.SetupDeprecationGateTestEnv()
	testutil.CreateNamespace(gateNamespace)

	// Seed the pre-upgrade state while no webhook configuration is installed,
	// then switch the gate on. Both steps are part of the suite fixture: the
	// object is created exactly once, before admission can reject it.
	legacySet = newLegacySet()
	Expect(testutil.K8sClient.Create(testutil.Ctx, legacySet)).To(Succeed())
	testutil.InstallDeprecationGateWebhooks()
})

var _ = AfterSuite(func() {
	testutil.TeardownTestEnv()
})
