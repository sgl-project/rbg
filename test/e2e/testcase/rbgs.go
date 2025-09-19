package testcase

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/utils/ptr"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func RunRbgSetControllerTestCases(f *framework.Framework) {
	ginkgo.Describe(
		"rbgset controller", func() {
			ginkgo.It(
				"create & delete rbgset", func() {
					rbgset := wrappers.BuildBasicRoleBasedGroupSet("test", f.Namespace).Obj()
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
					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
					f.ExpectRbgAnnotation(
						rbgset, map[string]string{workloadsv1alpha1.ExclusiveKeyAnnotationKey: topologyKey},
					)
				},
			)
		},
	)

}
