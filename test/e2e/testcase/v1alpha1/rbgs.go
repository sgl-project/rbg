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
	"k8s.io/utils/ptr"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappers "sigs.k8s.io/rbgs/test/wrappers/v1alpha1"
)

func RunRbgSetControllerTestCases(f *framework.Framework) {
	ginkgo.Describe(
		"rbgset controller", func() {
			ginkgo.It(
				"create & delete rbgset", func() {
					rbgset := wrappers.BuildBasicRoleBasedGroupSet("test", f.Namespace).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfoForRBGSet(f, rbgset) })

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
					f.ExpectRbgSetEqual(rbgset)

					// delete rbg
					gomega.Expect(f.Client.Delete(f.Ctx, rbgset)).Should(gomega.Succeed())
					f.ExpectRbgSetDeleted(rbgset)
				},
			)

			ginkgo.It(
				"scaling rbgset", func() {
					rbgset := wrappers.BuildBasicRoleBasedGroupSet("test", f.Namespace).WithReplicas(1).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfoForRBGSet(f, rbgset) })

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
					f.ExpectRbgSetEqual(rbgset)

					//  replicas 1 to 2
					utils.UpdateRbgSet(
						f.Ctx, f.Client, rbgset, func(rs *workloadsv1alpha1.RoleBasedGroupSet) {
							rs.Spec.Replicas = ptr.To(int32(2))
						},
					)
					f.ExpectRbgSetEqual(rbgset)

					// replicas 2 to 1
					utils.UpdateRbgSet(
						f.Ctx, f.Client, rbgset, func(rs *workloadsv1alpha1.RoleBasedGroupSet) {
							rs.Spec.Replicas = ptr.To(int32(1))
						},
					)
					f.ExpectRbgSetEqual(rbgset)
				},
			)

			ginkgo.It(
				"exclusive-topology", func() {
					topologyKey := "topology.kubernetes.io/zone"
					rbgset := wrappers.BuildBasicRoleBasedGroupSet("test", f.Namespace).
						WithReplicas(1).
						WithAnnotations(
							map[string]string{workloadsv1alpha1.ExclusiveKeyAnnotationKey: topologyKey},
						).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfoForRBGSet(f, rbgset) })

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
					f.ExpectRbgAnnotation(
						rbgset, map[string]string{workloadsv1alpha1.ExclusiveKeyAnnotationKey: topologyKey},
					)
				},
			)
		},
	)

}
