package v1alpha2

import (
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunControllerRevisionTestCases(f *framework.Framework) {
	ginkgo.Describe("revision testcase", func() {

		ginkgo.It("scenario of overriding changes that fail the semantically equal check", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
				WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("role-deployment").Obj(),
						wrappersv2.BuildStandaloneRole("role-sts").Obj(),
						wrappersv2.BuildLeaderWorkerRole("role-lws").Obj(),
					},
				).Obj()

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// update role-sts template
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[1].StandalonePattern.Template.Spec.TerminationGracePeriodSeconds = ptr.To(int64(100))
			})
			f.ExpectRbgV2Equal(rbg)

			gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Deleted(rbg)
		})

		ginkgo.It(
			"the workload spec will only be modified once", func() {
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
				oldRis := &workloadsv1alpha2.RoleInstanceSet{}
				err := f.Client.Get(
					f.Ctx, client.ObjectKey{
						Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
						Namespace: rbg.Namespace,
					}, oldRis,
				)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
					rbg.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Command = []string{"sleep", "1000001"}
				})
				f.ExpectRbgV2Equal(rbg)

				newRis := &workloadsv1alpha2.RoleInstanceSet{}
				err = f.Client.Get(
					f.Ctx, client.ObjectKey{
						Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
						Namespace: rbg.Namespace,
					}, newRis,
				)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(newRis.Generation).Should(gomega.Equal(oldRis.Generation + 2))
			},
		)

		ginkgo.It("semantic comparison can reconcile modifications on the workload", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole("role-1").Obj(),
			}).Obj()

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			oldSts := &appsv1.StatefulSet{}
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{
					Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
					Namespace: rbg.Namespace,
				}, oldSts,
			)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			oldSts.Spec.Replicas = ptr.To(int32(2))
			err = f.Client.Update(f.Ctx, oldSts)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			// wait for controller reconcile
			time.Sleep(time.Second * 5)

			f.ExpectRbgV2Equal(rbg)
			newSts := &appsv1.StatefulSet{}
			err = f.Client.Get(
				f.Ctx, client.ObjectKey{
					Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
					Namespace: rbg.Namespace,
				}, newSts,
			)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(int32(1)).Should(gomega.Equal(*newSts.Spec.Replicas))
		})

	})
}
