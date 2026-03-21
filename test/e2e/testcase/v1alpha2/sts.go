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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunStatefulSetWorkloadTestCases(f *framework.Framework) {
	ginkgo.It("update standalone role replicas & template (stateful)", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildStandaloneRole("role-1").WithWorkload("apps/v1", "StatefulSet").Obj(),
		}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// update
		updateLabel := map[string]string{"update-label": "new"}
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].Replicas = ptr.To(*rbg.Spec.Roles[0].Replicas + 1)
			rbg.Spec.Roles[0].StandalonePattern.Template.Labels = updateLabel
		})
		f.ExpectRbgV2Equal(rbg)

		f.ExpectWorkloadV2PodTemplateLabelContains(rbg, rbg.Spec.Roles[0], updateLabel)
	})

	//nolint:dupl
	ginkgo.It("standalone role with rollingUpdate (stateful)", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithWorkload("apps/v1", "StatefulSet").
					WithReplicas(2).
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						MaxUnavailable: ptr.To(intstr.FromInt32(1)),
						MaxSurge:       ptr.To(intstr.FromInt32(1)),
					}).Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// update, start rolling update
		updateLabel := map[string]string{"update-label": "new"}
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].StandalonePattern.Template.Labels = updateLabel
		})
		f.ExpectRbgV2Equal(rbg)

		f.ExpectWorkloadV2PodTemplateLabelContains(rbg, rbg.Spec.Roles[0], updateLabel)
	})

	ginkgo.It("standalone role with restartPolicy (stateful)", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithWorkload("apps/v1", "StatefulSet").
					WithReplicas(2).
					WithRestartPolicy(workloadsv1alpha2.RecreateRBGOnPodRestart).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		gomega.Expect(utils.DeletePodV2(f.Ctx, f.Client, f.Namespace, rbg.Name)).Should(gomega.Succeed())

		// wait rbg recreate
		f.ExpectRbgV2Equal(rbg)
		f.ExpectRbgV2Condition(rbg, workloadsv1alpha2.RoleBasedGroupRestartInProgress, metav1.ConditionFalse)
	})
}
