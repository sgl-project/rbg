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

package v1alpha1

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	testutils "sigs.k8s.io/rbgs/test/utils"
	wrappers "sigs.k8s.io/rbgs/test/wrappers/v1alpha1"
)

// RunLeaderWorkerSetWorkloadTestCases exercises the leader-worker RoleSpec shape
// through the v1alpha1 API. Since 0.9.0 the shared wrappers default these roles to
// RoleInstanceSet, so the deprecated Deployment/StatefulSet/LeaderWorkerSet
// reconciliation coverage lives in the upgrade suite (test/e2e/upgrade), which
// creates legacy objects before upgrading the controller.
func RunLeaderWorkerSetWorkloadTestCases(f *framework.Framework) {
	ginkgo.It("create a leader-worker RoleInstanceSet role with engine runtime and without leaderTemplate", func() {
		rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles([]workloadsv1alpha1.RoleSpec{
			wrappers.BuildLwsRole("role-1").
				WithEngineRuntime([]workloadsv1alpha1.EngineRuntime{{ProfileName: testutils.DefaultEngineRuntimeProfileName}}).
				Obj(),
		}).Obj()

		ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(testutils.CreatePatioRuntime(f.Ctx, f.Client)).Should(gomega.Succeed())
		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgEqual(rbg)
	})

	ginkgo.It("update a leader-worker RoleInstanceSet role.replicas & role.Template", func() {
		rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles([]workloadsv1alpha1.RoleSpec{
			wrappers.BuildLwsRole("role-1").Obj(),
		}).Obj()

		ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgEqual(rbg)

		// update
		updateLabel := map[string]string{"update-label": "new"}
		testutils.UpdateRbg(f.Ctx, f.Client, rbg, func(rbg *workloadsv1alpha1.RoleBasedGroup) {
			rbg.Spec.Roles[0].Replicas = ptr.To(*rbg.Spec.Roles[0].Replicas + 1)
			rbg.Spec.Roles[0].Template.Labels = updateLabel
		})
		f.ExpectRbgEqual(rbg)

		f.ExpectWorkloadPodTemplateLabelContains(rbg, rbg.Spec.Roles[0], updateLabel)
	})

	ginkgo.It("update a leader-worker RoleInstanceSet role.leaderTemplate & workerTemplate", func() {
		rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles([]workloadsv1alpha1.RoleSpec{
			wrappers.BuildLwsRole("role-1").Obj(),
		}).Obj()

		ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgEqual(rbg)

		// update
		updateLabel := map[string]string{"update-label": "new"}
		testutils.UpdateRbg(f.Ctx, f.Client, rbg, func(rbg *workloadsv1alpha1.RoleBasedGroup) {
			rbg.Spec.Roles[0].LeaderWorkerSet.PatchLeaderTemplate = ptr.To(wrappers.BuildLWSTemplatePatch(updateLabel))
			rbg.Spec.Roles[0].LeaderWorkerSet.PatchWorkerTemplate = ptr.To(wrappers.BuildLWSTemplatePatch(updateLabel))
		})
		f.ExpectRbgEqual(rbg)

		f.ExpectWorkloadPodTemplateLabelContains(rbg, rbg.Spec.Roles[0], updateLabel, updateLabel)
	})

	ginkgo.It("leader-worker RoleInstanceSet role with rollingUpdate", func() {
		rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
			[]workloadsv1alpha1.RoleSpec{
				wrappers.BuildLwsRole("role-1").
					WithReplicas(2).
					WithRollingUpdate(workloadsv1alpha1.RollingUpdate{
						MaxUnavailable: ptr.To(intstr.FromInt32(1)),
						MaxSurge:       ptr.To(intstr.FromInt32(1)),
					}).Obj(),
			}).Obj()

		ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgEqual(rbg)

		// update, start rolling update
		updateLabel := map[string]string{"update-label": "new"}
		testutils.UpdateRbg(f.Ctx, f.Client, rbg, func(rbg *workloadsv1alpha1.RoleBasedGroup) {
			rbg.Spec.Roles[0].Template.Labels = updateLabel
		})
		f.ExpectRbgEqual(rbg)
	})

}
