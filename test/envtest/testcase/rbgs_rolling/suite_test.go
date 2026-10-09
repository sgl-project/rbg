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

package rbgs_rolling

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

// TestRoleBasedGroupSetRolling runs the RoleBasedGroupSet controller against envtest
// together with the RoleBasedGroup chain it depends on, so the rolling update loop is
// driven by real child readiness rather than by a fake client.
func TestRoleBasedGroupSetRolling(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "RoleBasedGroupSet Rolling Update Suite")
}

var _ = BeforeSuite(func() {
	testutil.SetupTestEnv()
	startForegroundDeletionReaper()
})

var _ = AfterSuite(func() {
	testutil.TeardownTestEnv()
})
