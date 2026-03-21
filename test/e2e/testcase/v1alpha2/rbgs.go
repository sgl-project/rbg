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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunRbgSetControllerTestCases(f *framework.Framework) {
	ginkgo.Describe(
		"rbgset controller", func() {
			ginkgo.It(
				"create & delete rbgset", func() {
					rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("test", f.Namespace).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfoForRBGSet(f, rbgset) })

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
					f.ExpectRbgSetV2Equal(rbgset)

					// delete rbgset
					gomega.Expect(f.Client.Delete(f.Ctx, rbgset)).Should(gomega.Succeed())
					f.ExpectRbgSetV2Deleted(rbgset)
				},
			)

			ginkgo.It(
				"scaling rbgset", func() {
					rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("test", f.Namespace).WithReplicas(1).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfoForRBGSet(f, rbgset) })

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
					f.ExpectRbgSetV2Equal(rbgset)

					// replicas 1 to 2
					updateRbgSetV2(
						f, rbgset, func(rs *workloadsv1alpha2.RoleBasedGroupSet) {
							rs.Spec.Replicas = ptr.To(int32(2))
						},
					)
					f.ExpectRbgSetV2Equal(rbgset)

					// replicas 2 to 1
					updateRbgSetV2(
						f, rbgset, func(rs *workloadsv1alpha2.RoleBasedGroupSet) {
							rs.Spec.Replicas = ptr.To(int32(1))
						},
					)
					f.ExpectRbgSetV2Equal(rbgset)
				},
			)

			ginkgo.It(
				"exclusive-topology", func() {
					rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("test", f.Namespace).
						WithReplicas(1).
						WithAnnotations(
							map[string]string{constants.GroupExclusiveTopologyKey: "topology.kubernetes.io/zone"},
						).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfoForRBGSet(f, rbgset) })

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
					f.ExpectRbgV2AnnotationFromSet(
						rbgset, map[string]string{constants.GroupExclusiveTopologyKey: "topology.kubernetes.io/zone"},
					)
				},
			)
		},
	)
}
