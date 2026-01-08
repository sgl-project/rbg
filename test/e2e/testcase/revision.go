package testcase

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	pkgutils "sigs.k8s.io/rbgs/pkg/utils"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func RunControllerRevisionTestCases(f *framework.Framework) {
	ginkgo.Describe("revision testcase", func() {

		ginkgo.It("scenario of overriding changes that fail the semantically equal check", func() {
			rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
				WithRoles(
					[]v1alpha1.RoleSpec{
						wrappers.BuildBasicRole("role-deployment").WithWorkload(v1alpha1.DeploymentWorkloadType).Obj(),
						wrappers.BuildBasicRole("role-sts").WithWorkload(v1alpha1.StatefulSetWorkloadType).Obj(),
						wrappers.BuildLwsRole("role-lws").Obj(),
					},
				).Obj()

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			old, err := pkgutils.NewRevision(f.Ctx, f.Client, rbg, nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			oldRoleRevisionHash, err := pkgutils.GetRolesRevisionHash(old)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			f.ExpectRbgEqual(rbg)

			utils.UpdateRbg(f.Ctx, f.Client, rbg, func(rbg *v1alpha1.RoleBasedGroup) {
				// update
				rbg.Spec.Roles[1].TemplateSource.Template.Spec.TerminationGracePeriodSeconds = ptr.To(int64(100))
			})
			f.ExpectRbgEqual(rbg)

			// not update
			roleHashKey0 := fmt.Sprintf(v1alpha1.RoleRevisionLabelKeyFmt, rbg.Spec.Roles[0].Name)
			f.ExpectWorkloadLabelContains(rbg, rbg.Spec.Roles[0], map[string]string{
				roleHashKey0: oldRoleRevisionHash[rbg.Spec.Roles[0].Name],
			})
			roleHashKey2 := fmt.Sprintf(v1alpha1.RoleRevisionLabelKeyFmt, rbg.Spec.Roles[2].Name)
			f.ExpectWorkloadLabelContains(rbg, rbg.Spec.Roles[2], map[string]string{
				roleHashKey2: oldRoleRevisionHash[rbg.Spec.Roles[2].Name],
			})
			// update
			new, err := pkgutils.NewRevision(f.Ctx, f.Client, rbg, nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			newRoleRevisionHash, err := pkgutils.GetRolesRevisionHash(new)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			roleHashKey1 := fmt.Sprintf(v1alpha1.RoleRevisionLabelKeyFmt, rbg.Spec.Roles[1].Name)
			f.ExpectWorkloadLabelContains(rbg, rbg.Spec.Roles[1], map[string]string{
				roleHashKey1: newRoleRevisionHash[rbg.Spec.Roles[1].Name],
			})
			gomega.Expect(newRoleRevisionHash[rbg.Spec.Roles[1].Name]).
				ShouldNot(gomega.Equal(oldRoleRevisionHash[rbg.Spec.Roles[1].Name]))

			gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgDeleted(rbg)
			f.ExpectRevisionDeleted(rbg)
		})

		ginkgo.It(
			"the workload spec will only be modified once", func() {
				rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
					WithRoles(
						[]v1alpha1.RoleSpec{
							wrappers.BuildBasicRole("role-1").
								WithWorkload(v1alpha1.StatefulSetWorkloadType).
								WithEngineRuntime(
									[]v1alpha1.EngineRuntime{{ProfileName: utils.DefaultEngineRuntimeProfileName}},
								).
								Obj(),
						},
					).Obj()

				gomega.Expect(utils.CreatePatioRuntime(f.Ctx, f.Client)).Should(gomega.Succeed())

				gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

				f.ExpectRbgEqual(rbg)
				oldSts := &appsv1.StatefulSet{}
				err := f.Client.Get(
					f.Ctx, client.ObjectKey{
						Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
						Namespace: rbg.Namespace,
					}, oldSts,
				)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

			utils.UpdateRbg(f.Ctx, f.Client, rbg, func(rbg *v1alpha1.RoleBasedGroup) {
				// update
				rbg.Spec.Roles[0].TemplateSource.Template.Spec.Containers[0].Command = []string{"sleep", "1000001"}
			})
				f.ExpectRbgEqual(rbg)

				newSts := &appsv1.StatefulSet{}
				err = f.Client.Get(
					f.Ctx, client.ObjectKey{
						Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
						Namespace: rbg.Namespace,
					}, newSts,
				)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(newSts.Generation).Should(gomega.Equal(oldSts.Generation + 2))
			},
		)

		ginkgo.It("semantic comparison can reconcile modifications on the workload", func() {
			rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles([]v1alpha1.RoleSpec{
				wrappers.BuildLwsRole("role-1").Obj(),
			}).Obj()
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgEqual(rbg)

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

			f.ExpectRbgEqual(rbg)
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
