package testcase

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/scale"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func RunRbgScalingAdapterControllerTestCases(f *framework.Framework) {
	ginkgo.Describe(
		"rbg scaling adapter controller", func() {

			ginkgo.It(
				"test role with scalingAdapter", func() {
					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha1.RoleSpec{
								wrappers.BuildBasicRole("role-1").
									WithScalingAdapter(true).Obj(),
								wrappers.BuildBasicRole("role-2").
									WithScalingAdapter(false).Obj(),
								wrappers.BuildBasicRole("role-3").Obj(),
							},
						).Obj()
					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgEqual(rbg)

					f.ExpectRbgScalingAdapterEqual(rbg)
					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					for _, role := range rbg.Spec.Roles {
						f.ExpectScalingAdapterNotExist(rbg, role)
					}
				},
			)

			ginkgo.It(
				"test role with scalingAdapter and update rbg to delete the role", func() {
					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha1.RoleSpec{
								wrappers.BuildBasicRole("role-1").
									WithScalingAdapter(true).Obj(),
								wrappers.BuildBasicRole("role-2").
									WithScalingAdapter(true).Obj(),
								wrappers.BuildBasicRole("role-3").Obj(),
							},
						).Obj()
					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

					f.ExpectRbgEqual(rbg)

					f.ExpectRbgScalingAdapterEqual(rbg)
					newRbg := &workloadsv1alpha1.RoleBasedGroup{}
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
					f.ExpectScalingAdapterNotExist(rbg, rbg.Spec.Roles[0])
					f.ExpectRoleScalingAdapterEqual(rbg, rbg.Spec.Roles[1], nil)

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					for _, role := range rbg.Spec.Roles {
						f.ExpectScalingAdapterNotExist(rbg, role)
					}
				},
			)

			ginkgo.It(
				"test update rbg to add a new role with scalingAdapter enabling", func() {
					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha1.RoleSpec{
								wrappers.BuildBasicRole("role-1").Obj(),
							},
						).Obj()
					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

					f.ExpectRbgEqual(rbg)

					f.ExpectRbgScalingAdapterEqual(rbg)

					newRbg := &workloadsv1alpha1.RoleBasedGroup{}
					gomega.Expect(
						f.Client.Get(
							f.Ctx, client.ObjectKey{
								Name:      rbg.Name,
								Namespace: rbg.Namespace,
							}, newRbg,
						),
					).Should(gomega.Succeed())

					newRbg.Spec.Roles = append(
						newRbg.Spec.Roles, wrappers.BuildBasicRole("role-2").
							WithScalingAdapter(true).Obj(),
					)
					gomega.Expect(f.Client.Update(f.Ctx, newRbg)).Should(gomega.Succeed())
					f.ExpectScalingAdapterNotExist(rbg, newRbg.Spec.Roles[0])
					f.ExpectRoleScalingAdapterEqual(rbg, newRbg.Spec.Roles[1], nil)

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					for _, role := range rbg.Spec.Roles {
						f.ExpectScalingAdapterNotExist(rbg, role)
					}
				},
			)

			ginkgo.It(
				"test update role.scalingAdapter.enable from true to false and nil", func() {
					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha1.RoleSpec{
								wrappers.BuildBasicRole("role-1").
									WithScalingAdapter(true).Obj(),
								wrappers.BuildBasicRole("role-2").
									WithScalingAdapter(true).Obj(),
								wrappers.BuildBasicRole("role-3").Obj(),
							},
						).Obj()
					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgEqual(rbg)

					f.ExpectRbgScalingAdapterEqual(rbg)

					newRbg := &workloadsv1alpha1.RoleBasedGroup{}
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
					f.ExpectScalingAdapterNotExist(rbg, rbg.Spec.Roles[0])
					f.ExpectScalingAdapterNotExist(rbg, rbg.Spec.Roles[1])

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					for _, role := range rbg.Spec.Roles {
						f.ExpectScalingAdapterNotExist(rbg, role)
					}
				},
			)

			ginkgo.It(
				"test scale role in rbg", func() {
					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha1.RoleSpec{
								wrappers.BuildBasicRole("role-1").WithWorkload(workloadsv1alpha1.StatefulSetWorkloadType).
									WithScalingAdapter(true).Obj(),
							},
						).Obj()
					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

					f.ExpectRbgEqual(rbg)

					f.ExpectRbgScalingAdapterEqual(rbg)

					targetRole := rbg.Spec.Roles[0]
					rbgSa := &workloadsv1alpha1.RoleBasedGroupScalingAdapter{}
					gomega.Expect(
						f.Client.Get(
							f.Ctx, client.ObjectKey{
								Name:      scale.GenerateScalingAdapterName(rbg.Name, targetRole.Name),
								Namespace: rbg.Namespace,
							}, rbgSa,
						),
					).Should(gomega.Succeed())

					scale := &autoscalingv1.Scale{}
					gomega.Expect(f.Client.SubResource("scale").Get(f.Ctx, rbgSa, scale)).Should(gomega.Succeed())
					newReplicas := int32(2)
					scale.Spec.Replicas = newReplicas
					gomega.Expect(
						f.Client.SubResource("scale").Update(
							f.Ctx, rbgSa, client.WithSubResourceBody(scale),
						),
					).Should(gomega.Succeed())

					for _, role := range rbg.Spec.Roles {
						f.ExpectRoleScalingAdapterEqual(rbg, role, &newReplicas)
					}

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					for _, role := range rbg.Spec.Roles {
						f.ExpectScalingAdapterNotExist(rbg, role)
					}
				},
			)
		},
	)

}
