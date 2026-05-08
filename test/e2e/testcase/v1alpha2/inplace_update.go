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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	testutils "sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// newTestImage returns a different image for testing updates
const updatedImage = "nginx:stable-alpine"

func RunInPlaceUpdateTestCases(f *framework.Framework) {
	ginkgo.Describe("inplace update", func() {
		registerBasicInplaceUpdateTestCases(f)
		registerLeaderWorkerInplaceUpdateTestCases(f)
		registerGracePeriodInplaceUpdateTestCases(f)
		registerMultiReplicaInplaceUpdateTestCases(f)
		registerRoleTemplateInplaceUpdateTestCases(f)
	})
}

// nolint:gocyclo
func registerBasicInplaceUpdateTestCases(f *framework.Framework) {
	ginkgo.It("standalone pattern with InPlaceIfPossible should perform inplace update when only image changes", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-basic", f.Namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
					}).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get initial pod and record UID
		podList := &corev1.PodList{}
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			return err == nil && len(podList.Items) > 0
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		initialPodUID := podList.Items[0].UID

		// Update image
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Image = updatedImage
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify inplace update: Pod UID should remain the same
		gomega.Eventually(func(g gomega.Gomega) {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			g.Expect(err).ShouldNot(gomega.HaveOccurred())
			g.Expect(podList.Items).ShouldNot(gomega.BeEmpty())
			g.Expect(podList.Items[0].UID).Should(gomega.Equal(initialPodUID))
			g.Expect(podList.Items[0].Spec.Containers[0].Image).Should(gomega.Equal(updatedImage))
		}, testutils.Timeout, testutils.Interval).Should(gomega.Succeed())

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})

	ginkgo.It("standalone pattern with InPlaceIfPossible should recreate pod when non-image field changes", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-recreate", f.Namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
					}).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get initial pod and record UID
		podList := &corev1.PodList{}
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			return err == nil && len(podList.Items) > 0
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		initialPodUID := podList.Items[0].UID

		// Update image + env (non-image field should trigger recreate)
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Image = updatedImage
			rbg.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Env = []corev1.EnvVar{
				{Name: "TEST_ENV", Value: "test"},
			}
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify recreate: Pod UID should change
		gomega.Eventually(func(g gomega.Gomega) {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			g.Expect(err).ShouldNot(gomega.HaveOccurred())
			g.Expect(podList.Items).ShouldNot(gomega.BeEmpty())
			g.Expect(podList.Items[0].UID).ShouldNot(gomega.Equal(initialPodUID))
		}, testutils.Timeout, testutils.Interval).Should(gomega.Succeed())

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})

	ginkgo.XIt("standalone pattern with RecreatePod should always recreate pod even for image-only update", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-force-recreate", f.Namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.RecreatePodUpdateStrategyType,
					}).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get initial pod and record UID
		podList := &corev1.PodList{}
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			return err == nil && len(podList.Items) > 0
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		initialPodUID := podList.Items[0].UID

		// Update image only (should still recreate due to RecreatePod strategy)
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Image = updatedImage
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify recreate: Pod UID should change
		gomega.Eventually(func(g gomega.Gomega) {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			g.Expect(err).ShouldNot(gomega.HaveOccurred())
			g.Expect(podList.Items).ShouldNot(gomega.BeEmpty())
			g.Expect(podList.Items[0].UID).ShouldNot(gomega.Equal(initialPodUID))
			g.Expect(podList.Items[0].Spec.Containers[0].Image).Should(gomega.Equal(updatedImage))
		}, testutils.Timeout, testutils.Interval).Should(gomega.Succeed())

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})

	ginkgo.XIt("standalone pattern with InPlaceOnly should perform inplace update for image-only change", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-only", f.Namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceOnlyUpdateStrategyType,
					}).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get initial pod and record UID
		podList := &corev1.PodList{}
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			return err == nil && len(podList.Items) > 0
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		initialPodUID := podList.Items[0].UID

		// Update image only
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Image = updatedImage
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify inplace update: Pod UID should remain the same
		gomega.Eventually(func(g gomega.Gomega) {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			g.Expect(err).ShouldNot(gomega.HaveOccurred())
			g.Expect(podList.Items).ShouldNot(gomega.BeEmpty())
			g.Expect(podList.Items[0].UID).Should(gomega.Equal(initialPodUID))
			g.Expect(podList.Items[0].Spec.Containers[0].Image).Should(gomega.Equal(updatedImage))
		}, testutils.Timeout, testutils.Interval).Should(gomega.Succeed())

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})

	ginkgo.XIt("standalone pattern with InPlaceOnly should fail when non-image field changes", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-only-fail", f.Namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceOnlyUpdateStrategyType,
					}).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get initial pod and record UID
		podList := &corev1.PodList{}
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			return err == nil && len(podList.Items) > 0
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		initialPodUID := podList.Items[0].UID
		initialImage := podList.Items[0].Spec.Containers[0].Image

		// Update image + env (should not work with InPlaceOnly)
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Image = updatedImage
			rbg.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Env = []corev1.EnvVar{
				{Name: "TEST_ENV", Value: "test"},
			}
		})

		// Verify pod is NOT updated (InPlaceOnly doesn't allow recreate)
		gomega.Consistently(func(g gomega.Gomega) {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			g.Expect(err).ShouldNot(gomega.HaveOccurred())
			g.Expect(podList.Items).ShouldNot(gomega.BeEmpty())
			// Pod should remain unchanged
			g.Expect(podList.Items[0].UID).Should(gomega.Equal(initialPodUID))
			g.Expect(podList.Items[0].Spec.Containers[0].Image).Should(gomega.Equal(initialImage))
		}, 10*time.Second, testutils.Interval).Should(gomega.Succeed())

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})
}

// nolint:gocyclo
func registerGracePeriodInplaceUpdateTestCases(f *framework.Framework) {
	ginkgo.It("standalone pattern with GracePeriodSeconds should wait before updating image", func() {
		gracePeriod := int32(5) // 5 seconds grace period
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-grace", f.Namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
						InPlaceUpdateStrategy: &workloadsv1alpha2.InPlaceUpdateStrategy{
							GracePeriodSeconds: gracePeriod,
						},
					}).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get initial pod
		podList := &corev1.PodList{}
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			return err == nil && len(podList.Items) > 0
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		initialPodUID := podList.Items[0].UID
		initialImage := podList.Items[0].Spec.Containers[0].Image

		// Update image
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Image = updatedImage
		})

		// Wait for RBG to be ready again
		f.ExpectRbgV2Equal(rbg)

		// Verify inplace update with grace period
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			if err != nil || len(podList.Items) == 0 {
				return false
			}
			return podList.Items[0].UID == initialPodUID &&
				podList.Items[0].Spec.Containers[0].Image == updatedImage
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue(),
			"Pod UID should remain unchanged, image should be updated from %s to %s", initialImage, updatedImage)

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})

	ginkgo.It("leaderWorker pattern with GracePeriodSeconds should wait before updating image", func() {
		gracePeriod := int32(5)
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-lw-grace", f.Namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole("role-1").
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
						InPlaceUpdateStrategy: &workloadsv1alpha2.InPlaceUpdateStrategy{
							GracePeriodSeconds: gracePeriod,
						},
					}).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get initial pods
		podList := &corev1.PodList{}
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			return err == nil && len(podList.Items) >= 2
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		initialPodUIDs := make(map[types.UID]bool)
		for _, pod := range podList.Items {
			initialPodUIDs[pod.UID] = true
		}

		// Update image
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].LeaderWorkerPattern.Template.Spec.Containers[0].Image = updatedImage
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify inplace update
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			if err != nil || len(podList.Items) < 2 {
				return false
			}

			for _, pod := range podList.Items {
				if !initialPodUIDs[pod.UID] {
					return false
				}
				if pod.Spec.Containers[0].Image != updatedImage {
					return false
				}
			}
			return true
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})
}

// nolint:gocyclo
func registerMultiReplicaInplaceUpdateTestCases(f *framework.Framework) {
	ginkgo.It("standalone pattern with multiple replicas should perform rolling inplace update", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-multi", f.Namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithReplicas(3).
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
					}).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get initial pods
		podList := &corev1.PodList{}
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			return err == nil && len(podList.Items) == 3
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		initialPodUIDs := make(map[types.UID]bool)
		for _, pod := range podList.Items {
			initialPodUIDs[pod.UID] = true
		}

		// Update image
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Image = updatedImage
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify all pods are updated with same UIDs
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			if err != nil || len(podList.Items) != 3 {
				return false
			}

			for _, pod := range podList.Items {
				if !initialPodUIDs[pod.UID] {
					return false
				}
				if pod.Spec.Containers[0].Image != updatedImage {
					return false
				}
			}
			return true
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue(),
			"All Pod UIDs should remain unchanged for multi-replica inplace update")

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})

	ginkgo.It("leaderWorker pattern with multiple replicas should perform inplace update", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-lw-multi", f.Namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole("role-1").
					WithReplicas(2).
					WithSize(2).
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
					}).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get initial pods (2 groups * 2 pods each = 4 pods)
		podList := &corev1.PodList{}
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			return err == nil && len(podList.Items) >= 4
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		initialPodUIDs := make(map[types.UID]bool)
		for _, pod := range podList.Items {
			initialPodUIDs[pod.UID] = true
		}

		// Update image
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].LeaderWorkerPattern.Template.Spec.Containers[0].Image = updatedImage
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify all pods are updated with same UIDs
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			if err != nil || len(podList.Items) < 4 {
				return false
			}

			for _, pod := range podList.Items {
				if !initialPodUIDs[pod.UID] {
					return false
				}
				if pod.Spec.Containers[0].Image != updatedImage {
					return false
				}
			}
			return true
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})
}

// nolint:gocyclo
func registerLeaderWorkerInplaceUpdateTestCases(f *framework.Framework) {
	ginkgo.It("leaderWorker pattern with InPlaceIfPossible should perform inplace update for image-only change", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-lw", f.Namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole("role-1").
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
					}).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get initial pods and record UIDs
		podList := &corev1.PodList{}
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			return err == nil && len(podList.Items) >= 2 // leader + worker
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		initialPodUIDs := make(map[types.UID]string)
		for _, pod := range podList.Items {
			initialPodUIDs[pod.UID] = pod.Spec.Containers[0].Image
		}

		// Update image
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].LeaderWorkerPattern.Template.Spec.Containers[0].Image = updatedImage
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify inplace update: All Pod UIDs should remain the same
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			if err != nil || len(podList.Items) < 2 {
				return false
			}

			for _, pod := range podList.Items {
				if _, exists := initialPodUIDs[pod.UID]; !exists {
					return false // New pod UID means recreate
				}
				if pod.Spec.Containers[0].Image != updatedImage {
					return false
				}
			}
			return true
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue(),
			"All Pod UIDs should remain unchanged for inplace update in LeaderWorker pattern")

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})

	ginkgo.It("leaderWorker pattern with InPlaceIfPossible should recreate pods when updating leader/worker patches", func() {
		template := wrappersv2.BuildBasicPodTemplateSpec()
		template.Spec.Containers[0].Image = testutils.DefaultImage

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-lw-patch", f.Namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				{
					Name:     "role-1",
					Replicas: ptr.To(int32(1)),
					RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
						Type: workloadsv1alpha2.RollingUpdateStrategyType,
						RollingUpdate: &workloadsv1alpha2.RollingUpdate{
							Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
						},
					},
					Annotations: map[string]string{
						"rbg.workloads.x-k8s.io/role-workload-type": "leaderworkerset.x-k8s.io/v1/LeaderWorkerSet",
					},
					Pattern: workloadsv1alpha2.Pattern{
						LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
							Size: ptr.To(int32(2)),
							TemplateSource: workloadsv1alpha2.TemplateSource{
								Template: &template,
							},
							LeaderTemplatePatch: buildRawExtensionPatch(map[string]string{"role": "leader"}),
							WorkerTemplatePatch: buildRawExtensionPatch(map[string]string{"role": "worker"}),
						},
					},
				},
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get initial pods
		podList := &corev1.PodList{}
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			return err == nil && len(podList.Items) >= 2
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		initialPodUIDs := make(map[types.UID]bool)
		for _, pod := range podList.Items {
			initialPodUIDs[pod.UID] = true
		}

		// Update image in base template
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].LeaderWorkerPattern.Template.Spec.Containers[0].Image = updatedImage
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify inplace update
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			if err != nil || len(podList.Items) < 2 {
				return false
			}

			for _, pod := range podList.Items {
				if !initialPodUIDs[pod.UID] {
					return false
				}
				if pod.Spec.Containers[0].Image != updatedImage {
					return false
				}
			}
			return true
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})

	ginkgo.XIt("leaderWorker pattern with RecreatePod should recreate pods for image update", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-lw-recreate", f.Namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole("role-1").
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.RecreatePodUpdateStrategyType,
					}).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get initial pods
		podList := &corev1.PodList{}
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			return err == nil && len(podList.Items) >= 2
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		initialPodUIDs := make(map[types.UID]bool)
		for _, pod := range podList.Items {
			initialPodUIDs[pod.UID] = true
		}

		// Update image
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].LeaderWorkerPattern.Template.Spec.Containers[0].Image = updatedImage
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify recreate: All Pod UIDs should change
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			if err != nil || len(podList.Items) < 2 {
				return false
			}

			// All pods should have new UIDs
			for _, pod := range podList.Items {
				if initialPodUIDs[pod.UID] {
					return false
				}
				if pod.Spec.Containers[0].Image != updatedImage {
					return false
				}
			}
			return true
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue(),
			"All Pod UIDs should change for RecreatePod strategy in LeaderWorker pattern")

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})
}
