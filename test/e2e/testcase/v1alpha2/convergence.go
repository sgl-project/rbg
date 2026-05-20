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

package v1alpha2

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunConvergenceTestCases(f *framework.Framework) {
	ginkgo.Describe("convergence", func() {

		ginkgo.It("rapid successive updates converge to final state with no intermediate remnants", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-1").
						WithReplicas(3).
						WithRestartPolicy(workloadsv1alpha2.RestartPolicyNone).
						WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
							MaxUnavailable: ptr.To(intstr.FromInt32(1)),
						}).Obj(),
				}).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// First update (v2)
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].StandalonePattern.Template.Labels = map[string]string{"version": "v2"}
			})

			// Immediately follow with second update (v3) without waiting for v2 to complete
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].StandalonePattern.Template.Labels = map[string]string{"version": "v3"}
			})

			// Wait for final state to converge
			f.ExpectRbgV2Equal(rbg)

			// All instances should be on the same (latest) revision
			finalRevision := getRoleInstanceRevision(f, rbg, "role-1", 0)
			gomega.Expect(finalRevision).ShouldNot(gomega.BeEmpty())
			for i := 1; i < 3; i++ {
				gomega.Expect(getRoleInstanceRevision(f, rbg, "role-1", i)).Should(gomega.Equal(finalRevision),
					"all instances should converge to the same final revision")
			}

			// Verify 3 ready instances with correct count
			gomega.Expect(countReadyRoleInstances(f, rbg, "role-1")).Should(gomega.Equal(3))
			gomega.Expect(countRoleInstances(f, rbg, "role-1")).Should(gomega.Equal(3))

			// Verify stability (no continued churn after convergence)
			finalPodUIDs := getPodUIDsForRole(f, rbg, "role-1")
			gomega.Consistently(func() map[string]types.UID {
				return getPodUIDsForRole(f, rbg, "role-1")
			}, 15, 2).Should(gomega.Equal(finalPodUIDs),
				"pods should be stable after rapid successive updates converge")
		})

		ginkgo.It("mid-rollout scaling converges to correct final state", func() {
			template := buildPodTemplateWithStartupDelay(5, 5)

			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-1").
						WithReplicas(4).
						WithTemplate(&template).
						WithRestartPolicy(workloadsv1alpha2.RestartPolicyNone).
						WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
							MaxUnavailable: ptr.To(intstr.FromInt32(1)),
						}).Obj(),
				}).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// Trigger rolling update
			updateTemplate := buildPodTemplateWithStartupDelay(3, 3)
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].StandalonePattern.Template = &updateTemplate
			})

			// Wait for update to start (at least one instance updating)
			gomega.Eventually(func() bool {
				return countRoleInstances(f, rbg, "role-1") > 0 &&
					countReadyRoleInstances(f, rbg, "role-1") < 4
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"rolling update should start")

			// Mid-rollout: scale from 4 to 6
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].Replicas = ptr.To(int32(6))
			})

			// Wait for final state: 6 instances, all ready, all on latest revision
			f.ExpectRbgV2Equal(rbg)

			gomega.Eventually(func() int {
				return countReadyRoleInstances(f, rbg, "role-1")
			}, utils.Timeout, utils.Interval).Should(gomega.Equal(6),
				"should have 6 ready instances after mid-rollout scaling")

			gomega.Expect(countRoleInstances(f, rbg, "role-1")).Should(gomega.Equal(6))

			// All should be on the same revision
			finalRevision := getRoleInstanceRevision(f, rbg, "role-1", 0)
			for i := 1; i < 6; i++ {
				gomega.Expect(getRoleInstanceRevision(f, rbg, "role-1", i)).Should(gomega.Equal(finalRevision),
					"all instances should be on the latest revision after mid-rollout scaling")
			}

			// Verify stability after convergence
			finalPodUIDs := getPodUIDsForRole(f, rbg, "role-1")
			gomega.Consistently(func() map[string]types.UID {
				return getPodUIDsForRole(f, rbg, "role-1")
			}, 15, 2).Should(gomega.Equal(finalPodUIDs),
				"pods should be stable after mid-rollout scaling converges")
		})
	})
}
