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
	"encoding/json"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	testutils "sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// nolint:gocyclo
func registerRoleTemplateInplaceUpdateTestCases(f *framework.Framework) {
	ginkgo.It("templateRef role with InPlaceIfPossible should perform inplace update for image-only change", func() {
		baseTemplate := buildBasicPodTemplateSpecV2([]corev1.Container{
			{
				Name:  "nginx",
				Image: testutils.DefaultImage,
			},
		})

		// Patch that updates image
		imagePatch := buildTemplatePatchV2(map[string]interface{}{
			"spec": map[string]interface{}{
				"containers": []map[string]interface{}{
					{
						"name":  "nginx",
						"image": testutils.DefaultImage,
					},
				},
			},
		})

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-templateref", f.Namespace).
			WithRoleTemplates([]workloadsv1alpha2.RoleTemplate{
				{Name: "base-template", Template: baseTemplate},
			}).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithPatchRef("base-template", &imagePatch).
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
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

		// Update image via patch
		updatedPatch := buildTemplatePatchV2(map[string]interface{}{
			"spec": map[string]interface{}{
				"containers": []map[string]interface{}{
					{
						"name":  "nginx",
						"image": updatedImage,
					},
				},
			},
		})

		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].StandalonePattern.TemplateSource.TemplateRef.Patch = &updatedPatch
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify inplace update
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
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})

	ginkgo.It("templateRef role with InPlaceIfPossible should recreate pod for non-image field change", func() {
		baseTemplate := buildBasicPodTemplateSpecV2([]corev1.Container{
			{
				Name:  "nginx",
				Image: testutils.DefaultImage,
			},
		})

		initialPatch := buildTemplatePatchV2(map[string]interface{}{})

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-templateref-recreate", f.Namespace).
			WithRoleTemplates([]workloadsv1alpha2.RoleTemplate{
				{Name: "base-template", Template: baseTemplate},
			}).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithPatchRef("base-template", &initialPatch).
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
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

		// Update with non-image field change
		updatedPatch := buildTemplatePatchV2(map[string]interface{}{
			"spec": map[string]interface{}{
				"containers": []map[string]interface{}{
					{
						"name": "nginx",
						"env": []map[string]string{
							{"name": "TEST_ENV", "value": "test"},
						},
					},
				},
			},
		})

		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].StandalonePattern.TemplateSource.TemplateRef.Patch = &updatedPatch
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify recreate: Pod UID should change
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			if err != nil || len(podList.Items) == 0 {
				return false
			}
			return podList.Items[0].UID != initialPodUID
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})

	ginkgo.It("updating roleTemplate image should trigger inplace update for all referencing roles", func() {
		baseTemplate := buildBasicPodTemplateSpecV2([]corev1.Container{
			{
				Name:  "nginx",
				Image: testutils.DefaultImage,
			},
		})

		emptyPatch := buildTemplatePatchV2(map[string]interface{}{})

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-template-update", f.Namespace).
			WithRoleTemplates([]workloadsv1alpha2.RoleTemplate{
				{Name: "shared-template", Template: baseTemplate},
			}).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithPatchRef("shared-template", &emptyPatch).
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
					}).
					Obj(),
				wrappersv2.BuildStandaloneRole("role-2").
					WithPatchRef("shared-template", &emptyPatch).
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
					}).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Get initial pods for both roles
		podList1 := &corev1.PodList{}
		podList2 := &corev1.PodList{}
		gomega.Eventually(func() bool {
			err1 := f.Client.List(f.Ctx, podList1, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			err2 := f.Client.List(f.Ctx, podList2, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-2",
				})
			return err1 == nil && err2 == nil && len(podList1.Items) > 0 && len(podList2.Items) > 0
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		initialPodUID1 := podList1.Items[0].UID
		initialPodUID2 := podList2.Items[0].UID

		// Update roleTemplate image
		updatedTemplate := buildBasicPodTemplateSpecV2([]corev1.Container{
			{
				Name:  "nginx",
				Image: updatedImage,
			},
		})

		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.RoleTemplates[0].Template = updatedTemplate
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify both pods are updated with inplace update
		gomega.Eventually(func() bool {
			err1 := f.Client.List(f.Ctx, podList1, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			err2 := f.Client.List(f.Ctx, podList2, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-2",
				})
			if err1 != nil || err2 != nil || len(podList1.Items) == 0 || len(podList2.Items) == 0 {
				return false
			}
			return podList1.Items[0].UID == initialPodUID1 &&
				podList1.Items[0].Spec.Containers[0].Image == updatedImage &&
				podList2.Items[0].UID == initialPodUID2 &&
				podList2.Items[0].Spec.Containers[0].Image == updatedImage
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})

	ginkgo.It("updating roleTemplate non-image field should trigger recreate for referencing roles", func() {
		baseTemplate := buildBasicPodTemplateSpecV2([]corev1.Container{
			{
				Name:  "nginx",
				Image: testutils.DefaultImage,
			},
		})

		emptyPatch := buildTemplatePatchV2(map[string]interface{}{})

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-template-recreate", f.Namespace).
			WithRoleTemplates([]workloadsv1alpha2.RoleTemplate{
				{Name: "shared-template", Template: baseTemplate},
			}).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithPatchRef("shared-template", &emptyPatch).
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
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

		// Update roleTemplate with non-image field (env)
		updatedTemplate := buildBasicPodTemplateSpecV2([]corev1.Container{
			{
				Name:  "nginx",
				Image: testutils.DefaultImage,
				Env: []corev1.EnvVar{
					{Name: "TEST_ENV", Value: "test"},
				},
			},
		})

		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.RoleTemplates[0].Template = updatedTemplate
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify recreate: Pod UID should change
		gomega.Eventually(func() bool {
			err := f.Client.List(f.Ctx, podList, client.InNamespace(f.Namespace),
				client.MatchingLabels{
					"rbg.workloads.x-k8s.io/group-name": rbg.Name,
					"rbg.workloads.x-k8s.io/role-name":  "role-1",
				})
			if err != nil || len(podList.Items) == 0 {
				return false
			}
			return podList.Items[0].UID != initialPodUID
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})

	ginkgo.It("leaderWorker pattern with templateRef should perform inplace update", func() {
		baseTemplate := buildBasicPodTemplateSpecV2([]corev1.Container{
			{
				Name:  "nginx",
				Image: testutils.DefaultImage,
			},
		})

		template := wrappersv2.BuildBasicPodTemplateSpec()
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-lw-templateref", f.Namespace).
			WithRoleTemplates([]workloadsv1alpha2.RoleTemplate{
				{Name: "base-template", Template: baseTemplate},
			}).
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
		ginkgo.Skip("LeaderWorker with templateRef needs RoleInstanceSet workload type support")

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

	ginkgo.It("leaderWorker pattern updating shared template image should trigger inplace update", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-lw-template", f.Namespace).
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

	ginkgo.It("templateRef with empty patch should perform inplace update when template image changes", func() {
		baseTemplate := buildBasicPodTemplateSpecV2([]corev1.Container{
			{
				Name:  "nginx",
				Image: testutils.DefaultImage,
			},
		})

		emptyPatch := buildTemplatePatchV2(map[string]interface{}{})

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-empty-patch", f.Namespace).
			WithRoleTemplates([]workloadsv1alpha2.RoleTemplate{
				{Name: "base-template", Template: baseTemplate},
			}).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithPatchRef("base-template", &emptyPatch).
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
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

		// Update roleTemplate image
		updatedTemplate := buildBasicPodTemplateSpecV2([]corev1.Container{
			{
				Name:  "nginx",
				Image: updatedImage,
			},
		})

		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.RoleTemplates[0].Template = updatedTemplate
		})
		f.ExpectRbgV2Equal(rbg)

		// Verify inplace update
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
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})

	ginkgo.It("templateRef role with GracePeriodSeconds should wait before updating image", func() {
		gracePeriod := int32(5)
		baseTemplate := buildBasicPodTemplateSpecV2([]corev1.Container{
			{
				Name:  "nginx",
				Image: testutils.DefaultImage,
			},
		})

		emptyPatch := buildTemplatePatchV2(map[string]interface{}{})

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-inplace-templateref-grace", f.Namespace).
			WithRoleTemplates([]workloadsv1alpha2.RoleTemplate{
				{Name: "base-template", Template: baseTemplate},
			}).
			WithRoles([]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithPatchRef("base-template", &emptyPatch).
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

		// Update roleTemplate image
		updatedTemplate := buildBasicPodTemplateSpecV2([]corev1.Container{
			{
				Name:  "nginx",
				Image: updatedImage,
			},
		})

		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.RoleTemplates[0].Template = updatedTemplate
		})
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
		}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

		gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Deleted(rbg)
	})
}

// buildRawExtensionPatch creates a runtime.RawExtension for pod metadata labels
func buildRawExtensionPatch(labels map[string]string) *runtime.RawExtension {
	type metadata struct {
		Labels map[string]string `json:"labels"`
	}
	type patch struct {
		Metadata metadata `json:"metadata"`
	}
	b, err := json.Marshal(patch{Metadata: metadata{Labels: labels}})
	if err != nil {
		return &runtime.RawExtension{}
	}
	return &runtime.RawExtension{Raw: b}
}
