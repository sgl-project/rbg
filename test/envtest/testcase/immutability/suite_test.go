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

package immutability

import (
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

// TestEngineRuntimeProfileImmutability runs against a webhook-enabled envtest
// environment: the ClusterEngineRuntimeProfile freeze webhook is served and
// installed, the reference-tracking controller runs, and the mutating defaulters
// ride along because the suite creates RBGs and RBGSets.
func TestEngineRuntimeProfileImmutability(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Engine Runtime Profile Immutability Suite")
}

var _ = BeforeSuite(func() {
	testutil.SetupImmutabilityTestEnv()
	testutil.StartImmutabilityManager()

	// The apiserver's admission layer ingests the freshly-installed webhook
	// configuration asynchronously, so the first admission-reliant request
	// can race a not-yet-synced configuration and sail through the freeze.
	// Block until the freeze is demonstrably enforcing before any spec runs.
	// (testNs is normally assigned per-spec in BeforeEach; the warm-up owns
	// its own namespace.)
	testNs = fmt.Sprintf("test-immutability-warmup-%d", time.Now().UnixNano())
	testutil.CreateNamespace(testNs)
	requireCERPFreezeEnforcing()
	testutil.DeleteNamespace(testNs)
})

var _ = AfterSuite(func() {
	testutil.TeardownTestEnv()
})
