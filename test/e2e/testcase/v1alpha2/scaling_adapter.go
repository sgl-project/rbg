package v1alpha2

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scale"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunRbgScalingAdapterControllerTestCases(f *framework.Framework) {
	ginkgo.Describe(
		"rbg scaling adapter controller", func() {

			ginkgo.It(
				"test role with scalingAdapter", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("role-1").
									WithScalingAdapter(true).Obj(),
								wrappersv2.BuildStandaloneRole("role-2").
									WithScalingAdapter(false).Obj(),
								wrappersv2.BuildStandaloneRole("role-3").Obj(),
							},
						).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)

					f.ExpectRbgV2ScalingAdapterEqual(rbg)
					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					for _, role := range rbg.Spec.Roles {
						f.ExpectScalingAdapterV2NotExist(rbg, role)
					}
				},
			)

			ginkgo.It(
				"test role with scalingAdapter and update rbg to delete the role", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("role-1").
									WithScalingAdapter(true).Obj(),
								wrappersv2.BuildStandaloneRole("role-2").
									WithScalingAdapter(true).Obj(),
								wrappersv2.BuildStandaloneRole("role-3").Obj(),
							},
						).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)
					f.ExpectRbgV2ScalingAdapterEqual(rbg)

					newRbg := &workloadsv1alpha2.RoleBasedGroup{}
					gomega.Expect(
						f.Client.Get(
							f.Ctx, client.ObjectKey{
								Name:      rbg.Name,
								Namespace: rbg.Namespace,
							}, newRbg,
						),
					).Should(gomega.Succeed())

					newRbg.Spec.Roles = rbg.Spec.Roles[1:]
					gomega.Expect(f.Client.Update(f.Ctx, newRbg)).Should(gomega.Succeed())
					f.ExpectScalingAdapterV2NotExist(rbg, rbg.Spec.Roles[0])
					f.ExpectRoleScalingAdapterV2Equal(rbg, rbg.Spec.Roles[1], nil)

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					for _, role := range rbg.Spec.Roles {
						f.ExpectScalingAdapterV2NotExist(rbg, role)
					}
				},
			)

			ginkgo.It(
				"test update rbg to add a new role with scalingAdapter enabling", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("role-1").Obj(),
							},
						).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)
					f.ExpectRbgV2ScalingAdapterEqual(rbg)

					newRbg := &workloadsv1alpha2.RoleBasedGroup{}
					gomega.Expect(
						f.Client.Get(
							f.Ctx, client.ObjectKey{
								Name:      rbg.Name,
								Namespace: rbg.Namespace,
							}, newRbg,
						),
					).Should(gomega.Succeed())

					newRbg.Spec.Roles = append(
						newRbg.Spec.Roles, wrappersv2.BuildStandaloneRole("role-2").
							WithScalingAdapter(true).Obj(),
					)
					gomega.Expect(f.Client.Update(f.Ctx, newRbg)).Should(gomega.Succeed())
					f.ExpectScalingAdapterV2NotExist(rbg, newRbg.Spec.Roles[0])
					f.ExpectRoleScalingAdapterV2Equal(rbg, newRbg.Spec.Roles[1], nil)

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					for _, role := range rbg.Spec.Roles {
						f.ExpectScalingAdapterV2NotExist(rbg, role)
					}
				},
			)

			ginkgo.It(
				"test update role.scalingAdapter.enable from true to false and nil", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("role-1").
									WithScalingAdapter(true).Obj(),
								wrappersv2.BuildStandaloneRole("role-2").
									WithScalingAdapter(true).Obj(),
								wrappersv2.BuildStandaloneRole("role-3").Obj(),
							},
						).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)
					f.ExpectRbgV2ScalingAdapterEqual(rbg)

					newRbg := &workloadsv1alpha2.RoleBasedGroup{}
					gomega.Expect(
						f.Client.Get(
							f.Ctx, client.ObjectKey{
								Name:      rbg.Name,
								Namespace: rbg.Namespace,
							}, newRbg,
						),
					).Should(gomega.Succeed())

					newRbg.Spec.Roles[0].ScalingAdapter.Enable = false
					newRbg.Spec.Roles[1].ScalingAdapter = nil
					gomega.Expect(f.Client.Update(f.Ctx, newRbg)).Should(gomega.Succeed())
					f.ExpectScalingAdapterV2NotExist(rbg, rbg.Spec.Roles[0])
					f.ExpectScalingAdapterV2NotExist(rbg, rbg.Spec.Roles[1])

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					for _, role := range rbg.Spec.Roles {
						f.ExpectScalingAdapterV2NotExist(rbg, role)
					}
				},
			)

			ginkgo.It(
				"test scale role in rbg via ScalingAdapter", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("role-1").
									WithReplicas(int32(1)).
									WithScalingAdapter(true).Obj(),
								wrappersv2.BuildLeaderWorkerRole("role-2").
									WithReplicas(int32(1)).
									WithScalingAdapter(true).Obj(),
							},
						).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)
					f.ExpectRbgV2ScalingAdapterEqual(rbg)

					for _, targetRole := range rbg.Spec.Roles {
						rbgSa := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
						gomega.Expect(
							f.Client.Get(
								f.Ctx, client.ObjectKey{
									Name:      scale.GenerateScalingAdapterName(rbg.Name, targetRole.Name),
									Namespace: rbg.Namespace,
								}, rbgSa,
							),
						).Should(gomega.Succeed())

						scaleObj := &autoscalingv1.Scale{}
						gomega.Expect(f.Client.SubResource("scale").Get(f.Ctx, rbgSa, scaleObj)).Should(gomega.Succeed())
						newReplicas := int32(2)
						scaleObj.Spec.Replicas = newReplicas
						gomega.Expect(
							f.Client.SubResource("scale").Update(
								f.Ctx, rbgSa, client.WithSubResourceBody(scaleObj),
							),
						).Should(gomega.Succeed())

						f.ExpectRoleScalingAdapterV2Equal(rbg, targetRole, &newReplicas)
					}

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					for _, role := range rbg.Spec.Roles {
						f.ExpectScalingAdapterV2NotExist(rbg, role)
					}
				},
			)
		},
	)
}
