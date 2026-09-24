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
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunWebhookValidationTestCases(f *framework.Framework) {
	ginkgo.Describe(
		"rbg validating webhook", func() {

			// The test case checks whether ValidateCreate() works fine
			ginkgo.It(
				"should reject RoleBasedGroup creation with invalid name containing dot", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test.invalid-name", f.Namespace).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("role-1").Obj(),
							},
						).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

					err := f.Client.Create(f.Ctx, rbg)
					gomega.Expect(err).Should(gomega.HaveOccurred())
					gomega.Expect(err.Error()).Should(gomega.ContainSubstring("DNS"))
				},
			)

			// The test case checks whether ValidateUpdate() works fine
			ginkgo.It(
				"should reject role replicas change but allow env update when scalingAdapter is enabled", func() {
					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).
						WithRoles(
							[]workloadsv1alpha2.RoleSpec{
								wrappersv2.BuildStandaloneRole("role-1").
									WithReplicas(int32(1)).
									WithScalingAdapter(true).Obj(),
							},
						).Obj()

					f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)
					f.ExpectRbgV2ScalingAdapterEqual(rbg)

					// Step 1: Try to change role replicas, should be rejected
					newRbg := &workloadsv1alpha2.RoleBasedGroup{}
					gomega.Expect(
						f.Client.Get(
							f.Ctx, client.ObjectKey{
								Name:      rbg.Name,
								Namespace: rbg.Namespace,
							}, newRbg,
						),
					).Should(gomega.Succeed())

					newRbg.Spec.Roles[0].Replicas = ptr.To(int32(3))
					err := f.Client.Update(f.Ctx, newRbg)
					gomega.Expect(err).Should(gomega.HaveOccurred())
					gomega.Expect(strings.ToLower(err.Error())).Should(gomega.Or(
						gomega.ContainSubstring("scalingadapter"),
						gomega.ContainSubstring("replicas"),
					))

					// Step 2: Try to add an env var (non-replicas change), should succeed
					newRbg2 := &workloadsv1alpha2.RoleBasedGroup{}
					gomega.Expect(
						f.Client.Get(
							f.Ctx, client.ObjectKey{
								Name:      rbg.Name,
								Namespace: rbg.Namespace,
							}, newRbg2,
						),
					).Should(gomega.Succeed())

					if newRbg2.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Env == nil {
						newRbg2.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Env = []corev1.EnvVar{}
					}
					newRbg2.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Env = append(
						newRbg2.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Env,
						corev1.EnvVar{Name: "E2E_TEST", Value: "webhook-validation"},
					)
					gomega.Expect(f.Client.Update(f.Ctx, newRbg2)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(newRbg2)

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					for _, role := range rbg.Spec.Roles {
						f.ExpectScalingAdapterV2NotExist(rbg, role)
					}
				},
			)
		},
	)

	ginkgo.Describe(
		"rbgset validating webhook", func() {

			// The test case checks whether ValidateCreate() rejects a ScalingAdapter that is
			// enabled from a group template.
			ginkgo.It(
				"should reject RoleBasedGroupSet creation when a group template role enables scalingAdapter",
				func() {
					rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("e2e-test", f.Namespace).
						WithReplicas(1).Obj()
					rbgset.Spec.GroupTemplate.Spec.Roles = []workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("role-1").WithScalingAdapter(true).Obj(),
					}

					err := f.Client.Create(f.Ctx, rbgset)
					gomega.Expect(err).Should(gomega.HaveOccurred())
					gomega.Expect(strings.ToLower(err.Error())).Should(gomega.ContainSubstring("scalingadapter"))
				},
			)

			// The scalingAdapter rule is create-only: a set created before the rule existed
			// (or one that picked up an adapter later) must stay updatable, because the
			// webhook's failurePolicy=fail would otherwise lock it out of every update.
			ginkgo.It(
				"should allow updates on a RoleBasedGroupSet carrying scalingAdapter", func() {
					rbgset := wrappersv2.BuildBasicRoleBasedGroupSet("e2e-test", f.Namespace).
						WithReplicas(1).Obj()

					gomega.Expect(f.Client.Create(f.Ctx, rbgset)).Should(gomega.Succeed())
					f.ExpectRbgSetV2Equal(rbgset)

					// Enabling the adapter on update succeeds: the rule rejects only creation.
					// The controller writes status concurrently, so retry past resourceVersion
					// conflicts.
					gomega.Eventually(
						func() error {
							updated := &workloadsv1alpha2.RoleBasedGroupSet{}
							if err := f.Client.Get(
								f.Ctx, client.ObjectKeyFromObject(rbgset), updated,
							); err != nil {
								return err
							}
							updated.Spec.GroupTemplate.Spec.Roles[0].ScalingAdapter = &workloadsv1alpha2.ScalingAdapter{
								Enable: true,
							}
							return f.Client.Update(f.Ctx, updated)
						}, utils.Timeout, utils.Interval,
					).Should(gomega.Succeed())

					// And the set carrying the adapter stays updatable for unrelated changes,
					// which is the upgrade-compatibility scenario the create-only boundary exists for.
					gomega.Eventually(
						func() error {
							allowed := &workloadsv1alpha2.RoleBasedGroupSet{}
							if err := f.Client.Get(
								f.Ctx, client.ObjectKeyFromObject(rbgset), allowed,
							); err != nil {
								return err
							}
							allowed.Spec.Replicas = ptr.To(int32(2))
							return f.Client.Update(f.Ctx, allowed)
						}, utils.Timeout, utils.Interval,
					).Should(gomega.Succeed())
				},
			)
		},
	)
}
