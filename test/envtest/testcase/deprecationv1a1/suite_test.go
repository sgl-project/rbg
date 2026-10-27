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

package deprecationv1a1

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

// TestV1alpha1DeprecationContract runs against envtest with the production
// manager webhook registrations and the shipped config/webhook/manifests.yaml
// installed (rewritten to the local test server). It pins the v1alpha1 half of
// the 0.9.0 deprecation gate end-to-end: on-the-wire Admission Warnings, the
// Exact match policy, the new-use rejection, and unregistered DELETE behavior.
func TestV1alpha1DeprecationContract(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "v1alpha1 Deprecation Contract Suite")
}

var _ = BeforeSuite(func() {
	// The setup also creates the contract namespace: the admission-readiness
	// barrier submits dry-run probes into it before any spec runs.
	testutil.SetupV1alpha1ContractTestEnv()
})

var _ = AfterSuite(func() {
	testutil.TeardownTestEnv()
})

const contractNamespace = "deprecation-v1a1-contract"
