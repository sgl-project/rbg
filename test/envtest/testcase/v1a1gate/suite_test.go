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

package v1a1gate

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

// TestV1alpha1Gate runs against envtest with production conversion webhooks
// enabled (CRDs carry webhook conversion strategy) and the production
// v1alpha2 validators installed. v1alpha1 requests traverse the real
// conversion chain: v1alpha1 -> convert -> v1alpha2 validator -> convert back.
func TestV1alpha1Gate(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "v1alpha1 Request Path Gate Suite")
}

var _ = BeforeSuite(func() {
	testutil.SetupV1alpha1GateTestEnv()
	// Create the shared namespace once, before any Describe: Ginkgo randomizes
	// top-level container order, so per-container creation is order-flaky.
	testutil.CreateNamespace("v1a1-gate-test")
})

var _ = AfterSuite(func() {
	testutil.TeardownTestEnv()
})
