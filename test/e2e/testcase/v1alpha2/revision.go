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
	"fmt"
	"time"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
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
						wrappersv2.BuildStandaloneRole("role-deployment").
							WithWorkload("apps/v1", "Deployment").Obj(),
						wrappersv2.BuildStandaloneRole("role-sts").
							WithWorkload("apps/v1", "StatefulSet").Obj(),
						wrappersv2.BuildLeaderWorkerRole("role-lws").
							WithWorkload("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet").Obj(),
					},
				).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

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

				f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

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
				ginkgo.By(fmt.Sprintf("Got old RoleInstanceSet, generation: %d", oldRis.Generation))

				updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
					rbg.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Command = []string{"sleep", "1000001"}
				})
				f.ExpectRbgV2Equal(rbg)

				newRis := &workloadsv1alpha2.RoleInstanceSet{}
				gomega.Eventually(func() bool {
					err = f.Client.Get(
						f.Ctx, client.ObjectKey{
							Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
							Namespace: rbg.Namespace,
						}, newRis,
					)
					if err != nil {
						return false
					}
					ginkgo.By(fmt.Sprintf("Got new RoleInstanceSet, generation: %d (expected: %d)", newRis.Generation, oldRis.Generation+2))
					return newRis.Generation == oldRis.Generation+1
				}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
			},
		)

		ginkgo.It("semantic comparison can reconcile modifications on the workload", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole("role-1").
					WithWorkload("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet").
					Obj(),
			}).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// With LeaderWorkerSet workload type, the controller creates a LWS resource.
			oldLws := &lwsv1.LeaderWorkerSet{}
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{
					Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
					Namespace: rbg.Namespace,
				}, oldLws,
			)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			// Drift the replica count on the underlying LWS
			oldLws.Spec.Replicas = ptr.To(int32(2))
			err = f.Client.Update(f.Ctx, oldLws)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			// wait for controller reconcile
			time.Sleep(time.Second * 5)

			f.ExpectRbgV2Equal(rbg)
			newLws := &lwsv1.LeaderWorkerSet{}
			err = f.Client.Get(
				f.Ctx, client.ObjectKey{
					Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
					Namespace: rbg.Namespace,
				}, newLws,
			)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(int32(1)).Should(gomega.Equal(*newLws.Spec.Replicas))
		})

	})
}
