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

package e2e

import (
	"testing"

	testcasev1alpha1 "sigs.k8s.io/rbgs/test/e2e/testcase/v1alpha1"
	testcasev1alpha2 "sigs.k8s.io/rbgs/test/e2e/testcase/v1alpha2"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/rbgs/test/e2e/framework"
)

func TestE2E(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	f := framework.NewFramework(true)
	ginkgo.BeforeSuite(
		func() {
			f.BeforeAll()
		},
	)

	ginkgo.AfterSuite(
		func() {
			f.AfterAll()
		},
	)

	ginkgo.AfterEach(
		func() {
			f.AfterEach()
		},
	)

	// v1alpha1 tests are kept for reference but skipped by default.
	// Run explicitly with: ginkgo --label-filter=v1alpha1
	ginkgo.Describe(
		"[v1alpha1] Run role based controller e2e tests",
		ginkgo.Pending,
		func() {
			testcasev1alpha1.RunRbgControllerTestCases(f)
			testcasev1alpha1.RunControllerRevisionTestCases(f)
			testcasev1alpha1.RunRbgScalingAdapterControllerTestCases(f)
			testcasev1alpha1.RunDeploymentWorkloadTestCases(f)
			testcasev1alpha1.RunStatefulSetWorkloadTestCases(f)
			testcasev1alpha1.RunLeaderWorkerSetWorkloadTestCases(f)
			testcasev1alpha1.RunRbgSetControllerTestCases(f)
			testcasev1alpha1.RunRoleTemplateTestCases(f)
		},
	)

	ginkgo.Describe(
		"[v1alpha2] Run role based controller e2e tests", func() {
			testcasev1alpha2.RunRbgControllerTestCases(f)
			testcasev1alpha2.RunControllerRevisionTestCases(f)
			testcasev1alpha2.RunRbgScalingAdapterControllerTestCases(f)
			testcasev1alpha2.RunDeploymentWorkloadTestCases(f)
			testcasev1alpha2.RunStatefulSetWorkloadTestCases(f)
			testcasev1alpha2.RunLeaderWorkerSetWorkloadTestCases(f)
			testcasev1alpha2.RunRbgSetControllerTestCases(f)
			testcasev1alpha2.RunRoleTemplateTestCases(f)
		},
	)

	ginkgo.RunSpecs(t, "run rbg e2e test")
}
