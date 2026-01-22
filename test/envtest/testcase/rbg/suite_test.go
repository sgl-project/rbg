/*
Copyright 2025.

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

package rbg

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

func TestRBGController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "RoleBasedGroup Controller Suite")
}

var _ = BeforeSuite(func() {
	testutil.SetupTestEnv()
})

var _ = AfterSuite(func() {
	testutil.TeardownTestEnv()
})
