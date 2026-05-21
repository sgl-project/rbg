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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunUpdateStrategyTestCases(f *framework.Framework) {
	ginkgo.Describe("update strategy", func() {

		ginkgo.It("[RoleInstanceSet] InPlaceIfPossible does not recreate pods when only image changes", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-1").
						WithReplicas(2).
						WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
							Type:           workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
							MaxUnavailable: ptr.To(intstr.FromInt32(1)),
						}).Obj(),
				}).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// Record pod UIDs before update
			initialPodUIDs := getPodUIDsForRole(f, rbg, "role-1")
			gomega.Expect(initialPodUIDs).Should(gomega.HaveLen(2))

			// Update only the container image (in-place updatable field)
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Image = "nginx:1.25"
			})

			// Wait for update to complete
			f.ExpectRbgV2Equal(rbg)

			// Verify pods were NOT recreated (UIDs unchanged = in-place update)
			updatedPodUIDs := getPodUIDsForRole(f, rbg, "role-1")
			gomega.Expect(updatedPodUIDs).Should(gomega.Equal(initialPodUIDs),
				"pod UIDs should not change during in-place update (image-only change)")

			// Verify pods are stable after in-place update
			gomega.Consistently(func() map[string]types.UID {
				return getPodUIDsForRole(f, rbg, "role-1")
			}, 15, 2).Should(gomega.Equal(initialPodUIDs),
				"pods should remain stable after in-place update completes")
		})

		ginkgo.It("[RoleInstanceSet] InPlaceIfPossible falls back to recreate when non-image fields change", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-1").
						WithReplicas(2).
						WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
							Type:           workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
							MaxUnavailable: ptr.To(intstr.FromInt32(1)),
						}).Obj(),
				}).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// Record pod UIDs before update
			initialPodUIDs := getPodUIDsForRole(f, rbg, "role-1")
			gomega.Expect(initialPodUIDs).Should(gomega.HaveLen(2))

			// Update command field (cannot be in-place updated, forces recreate)
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Command = []string{"nginx", "-g", "daemon off;"}
			})

			// Wait for update to complete
			f.ExpectRbgV2Equal(rbg)

			// Verify pods were recreated (UIDs changed = fallback to recreate)
			updatedPodUIDs := getPodUIDsForRole(f, rbg, "role-1")
			gomega.Expect(updatedPodUIDs).ShouldNot(gomega.Equal(initialPodUIDs),
				"pod UIDs should change when in-place update is not possible (command change)")

			// Verify pods are stable after recreate (no extra restarts)
			gomega.Consistently(func() map[string]types.UID {
				return getPodUIDsForRole(f, rbg, "role-1")
			}, 15, 2).Should(gomega.Equal(updatedPodUIDs),
				"pods should remain stable after recreate completes (no extra restarts)")
		})

		ginkgo.It("[RoleInstanceSet] rolling update paused blocks update progress and resumes correctly", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-1").
						WithReplicas(3).
						WithRestartPolicy(workloadsv1alpha2.RestartPolicyNone).
						WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
							MaxUnavailable: ptr.To(intstr.FromInt32(1)),
							Paused:         true,
						}).Obj(),
				}).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// Record pod UIDs before update
			initialPodUIDs := getPodUIDsForRole(f, rbg, "role-1")
			gomega.Expect(initialPodUIDs).Should(gomega.HaveLen(3))

			// Record initial revision
			initialRevision := getRoleInstanceRevision(f, rbg, "role-1", 0)

			// Trigger template update (but paused=true should block it)
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].StandalonePattern.Template.Labels = map[string]string{"version": "v2"}
			})

			// Verify update does NOT progress while paused
			time.Sleep(3 * time.Second)
			gomega.Consistently(func() bool {
				// All instances should still be on the original revision
				for i := 0; i < 3; i++ {
					rev := getRoleInstanceRevision(f, rbg, "role-1", i)
					if rev != initialRevision {
						return false
					}
				}
				return true
			}, 15, 2).Should(gomega.BeTrue(),
				"no instances should be updated while paused=true")

			// Resume the rolling update
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].RolloutStrategy.RollingUpdate.Paused = false
			})

			// Wait for all instances to be updated
			f.ExpectRbgV2Equal(rbg)

			// Verify all instances now have the new revision
			newRevision := getRoleInstanceRevision(f, rbg, "role-1", 0)
			gomega.Expect(newRevision).ShouldNot(gomega.Equal(initialRevision),
				"instances should be updated to new revision after resume")
			for i := 1; i < 3; i++ {
				gomega.Expect(getRoleInstanceRevision(f, rbg, "role-1", i)).Should(gomega.Equal(newRevision))
			}

			// Verify stability after resume (no extra restarts)
			finalPodUIDs := getPodUIDsForRole(f, rbg, "role-1")
			gomega.Consistently(func() map[string]types.UID {
				return getPodUIDsForRole(f, rbg, "role-1")
			}, 15, 2).Should(gomega.Equal(finalPodUIDs),
				"pods should be stable after rolling update completes")
		})
	})
}

