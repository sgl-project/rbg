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

package webhook

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

// TestWebhookDefaulting runs against a webhook-enabled envtest environment: the
// three mutating defaulters from cmd/rbgs/main.go are served and installed, and no
// controllers run, so the stored object is exactly what admission let through.
func TestWebhookDefaulting(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Admission Webhook Defaulting Suite")
}

var _ = BeforeSuite(func() {
	testutil.SetupWebhookTestEnv()
})

var _ = AfterSuite(func() {
	testutil.TeardownTestEnv()
})
