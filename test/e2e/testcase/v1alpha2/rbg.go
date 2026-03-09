package v1alpha2

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/constants"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunRbgControllerTestCases(f *framework.Framework) {
	ginkgo.Describe(
		"rbg controller", func() {

			ginkgo.It(
				"create & delete rbg with standalone roles", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("role-1").Obj(),
								wrappersv2.BuildStandaloneRole("role-2").Obj(),
							},
						).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)

					// delete rbg
					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Deleted(rbg)
				},
			)

			ginkgo.It(
				"rbg with dependency", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("role-1").Obj(),
								wrappersv2.BuildStandaloneRole("role-2").
									WithDependencies([]string{"role-1"}).Obj(),
							},
						).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

					f.ExpectRbgV2Equal(rbg)
				},
			)

			ginkgo.It(
				"rbg with engine runtime existed", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("role-1").
									WithEngineRuntime(
										[]workloadsv1alpha2.EngineRuntime{{ProfileName: utils.DefaultEngineRuntimeProfileName}},
									).
									Obj(),
							},
						).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(utils.CreatePatioRuntime(f.Ctx, f.Client)).Should(gomega.Succeed())

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

					f.ExpectRbgV2Equal(rbg)
				},
			)

			ginkgo.It(
				"rbg with orphan roles", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
						[]workloadsv1alpha2.RoleSpec{
							wrappersv2.BuildStandaloneRole("role-1").Obj(),
							wrappersv2.BuildStandaloneRole("role-2").Obj(),
						},
					).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)

					// update role name
					updateRbgV2(
						f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
							rbg.Spec.Roles = []workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("sts-1").Obj(),
								wrappersv2.BuildStandaloneRole("sts-2").Obj(),
							}
						},
					)
					f.ExpectRbgV2Equal(rbg)

					f.ExpectWorkloadV2NotExist(rbg, wrappersv2.BuildStandaloneRole("role-1").Obj())
					f.ExpectWorkloadV2NotExist(rbg, wrappersv2.BuildStandaloneRole("role-2").Obj())
				},
			)

			ginkgo.It(
				"rbg with exclusive-topology", func() {
					topologyKey := "kubernetes.io/hostname"
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithAnnotations(
							map[string]string{constants.GroupExclusiveTopologyKey: topologyKey},
						).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("router").
									WithReplicas(int32(1)).Obj(),
								wrappersv2.BuildStandaloneRole("prefill").
									WithReplicas(int32(1)).Obj(),
								wrappersv2.BuildLeaderWorkerRole("decode").
									WithReplicas(int32(1)).Obj(),
							},
						).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectWorkloadV2ExclusiveTopology(rbg, rbg.Spec.Roles[0], topologyKey)
					f.ExpectWorkloadV2ExclusiveTopology(rbg, rbg.Spec.Roles[1], topologyKey)
					f.ExpectWorkloadV2ExclusiveTopology(rbg, rbg.Spec.Roles[2], topologyKey)
				},
			)
		},
	)
}

// updateRbgV2 updates a v1alpha2 RoleBasedGroup with the given update function.
func updateRbgV2(
	f *framework.Framework,
	rbg *workloadsv1alpha2.RoleBasedGroup,
	updateFunc func(rbg *workloadsv1alpha2.RoleBasedGroup),
) {
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx,
				client.ObjectKeyFromObject(rbg),
				rbg,
			)
			if err != nil {
				return false
			}
			updateFunc(rbg)
			return f.Client.Update(f.Ctx, rbg) == nil
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

// updateRbgSetV2 updates a v1alpha2 RoleBasedGroupSet with the given update function.
func updateRbgSetV2(
	f *framework.Framework,
	rbgset *workloadsv1alpha2.RoleBasedGroupSet,
	updateFunc func(rs *workloadsv1alpha2.RoleBasedGroupSet),
) {
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx,
				client.ObjectKeyFromObject(rbgset),
				rbgset,
			)
			if err != nil {
				return false
			}
			updateFunc(rbgset)
			return f.Client.Update(f.Ctx, rbgset) == nil
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}
