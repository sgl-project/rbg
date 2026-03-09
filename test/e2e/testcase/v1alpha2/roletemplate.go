package v1alpha2

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	pkgutils "sigs.k8s.io/rbgs/pkg/utils"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	testutils "sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunRoleTemplateTestCases(f *framework.Framework) {
	ginkgo.Describe(
		"roletemplate controller", func() {

			// Test 1: Basic functionality - create RBG with templateRef and verify workloads
			ginkgo.It(
				"create rbg with roleTemplates and verify workloads", func() {
					baseTemplate := buildBasicPodTemplateSpecV2([]corev1.Container{
						{
							Name:  "nginx",
							Image: testutils.DefaultImage,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
						},
					})

					// Role1: patch CPU to 200m
					role1Patch := buildTemplatePatchV2(map[string]interface{}{
						"spec": map[string]interface{}{
							"containers": []map[string]interface{}{
								{
									"name": "nginx",
									"resources": map[string]interface{}{
										"requests": map[string]interface{}{
											"cpu": "200m",
										},
									},
								},
							},
						},
					})

					// Role2: empty patch
					emptyPatch := buildTemplatePatchV2(map[string]interface{}{})

					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-roletemplate", f.Namespace).
						WithRoleTemplates([]workloadsv1alpha2.RoleTemplate{
							{Name: "base-template", Template: baseTemplate},
						}).
						WithRoles([]workloadsv1alpha2.RoleSpec{
							wrappersv2.BuildStandaloneRole("role1").
								WithPatchRef("base-template", &role1Patch).
								Obj(),
							wrappersv2.BuildStandaloneRole("role2").
								WithPatchRef("base-template", &emptyPatch).
								Obj(),
						}).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)

					// Verify role1: CPU should be patched to 200m
					sts1 := &appsv1.StatefulSet{}
					gomega.Eventually(func() error {
						return f.Client.Get(f.Ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-%s", rbg.Name, "role1"),
							Namespace: f.Namespace,
						}, sts1)
					}, 30*time.Second, 1*time.Second).Should(gomega.Succeed())

					gomega.Expect(sts1.Spec.Template.Spec.Containers).To(gomega.HaveLen(1))
					container1 := sts1.Spec.Template.Spec.Containers[0]
					gomega.Expect(container1.Image).To(gomega.Equal(testutils.DefaultImage))
					gomega.Expect(container1.Resources.Requests[corev1.ResourceCPU]).To(gomega.Equal(resource.MustParse("200m")))

					// Verify role2: empty patch
					sts2 := &appsv1.StatefulSet{}
					gomega.Eventually(func() error {
						return f.Client.Get(f.Ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-%s", rbg.Name, "role2"),
							Namespace: f.Namespace,
						}, sts2)
					}, 30*time.Second, 1*time.Second).Should(gomega.Succeed())

					container2 := sts2.Spec.Template.Spec.Containers[0]
					gomega.Expect(container2.Image).To(gomega.Equal(testutils.DefaultImage))
					gomega.Expect(container2.Resources.Requests[corev1.ResourceCPU]).To(gomega.Equal(resource.MustParse("100m")))

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Deleted(rbg)
				},
			)

			// Test 2: Update roleTemplate triggers rollout
			ginkgo.It(
				"update roleTemplate triggers rollout and increments revision", func() {
					baseTemplate := buildBasicPodTemplateSpecV2([]corev1.Container{
						{
							Name:  "app",
							Image: testutils.DefaultImage,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("100m"),
								},
							},
						},
					})

					emptyPatch := buildTemplatePatchV2(map[string]interface{}{})

					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-update-template", f.Namespace).
						WithRoleTemplates([]workloadsv1alpha2.RoleTemplate{
							{Name: "base", Template: baseTemplate},
						}).
						WithRoles([]workloadsv1alpha2.RoleSpec{
							wrappersv2.BuildStandaloneRole("role1").
								WithPatchRef("base", &emptyPatch).
								Obj(),
						}).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)

					// Get initial state
					sts := &appsv1.StatefulSet{}
					gomega.Eventually(func() error {
						return f.Client.Get(f.Ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-%s", rbg.Name, "role1"),
							Namespace: f.Namespace,
						}, sts)
					}, 30*time.Second, 1*time.Second).Should(gomega.Succeed())

					initialRevision := sts.Status.CurrentRevision

					// Update roleTemplate
					updatedTemplate := buildBasicPodTemplateSpecV2([]corev1.Container{
						{
							Name:  "app",
							Image: testutils.DefaultImage,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("200m"),
								},
							},
						},
					})

					updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
						rbg.Spec.RoleTemplates[0].Template = updatedTemplate
					})
					f.ExpectRbgV2Equal(rbg)

					// Verify rollout happens
					gomega.Eventually(func() bool {
						err := f.Client.Get(f.Ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-%s", rbg.Name, "role1"),
							Namespace: f.Namespace,
						}, sts)
						if err != nil {
							return false
						}
						return sts.Status.CurrentRevision != initialRevision
					}, 60*time.Second, 2*time.Second).Should(gomega.BeTrue(),
						"StatefulSet should have new revision after template update")

					// Verify CPU updated
					gomega.Eventually(func() resource.Quantity {
						err := f.Client.Get(f.Ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-%s", rbg.Name, "role1"),
							Namespace: f.Namespace,
						}, sts)
						if err != nil {
							return resource.Quantity{}
						}
						return sts.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
					}, 30*time.Second, 1*time.Second).Should(gomega.Equal(resource.MustParse("200m")))

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Deleted(rbg)
				},
			)

			// Test 3: Rollback roleTemplate
			ginkgo.It(
				"rollback roleTemplate to previous version restores workload spec", func() {
					templateV1 := buildBasicPodTemplateSpecV2([]corev1.Container{
						{
							Name:  "app",
							Image: testutils.DefaultImage,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("100m"),
								},
							},
						},
					})

					emptyPatch := buildTemplatePatchV2(map[string]interface{}{})

					rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-rollback", f.Namespace).
						WithRoleTemplates([]workloadsv1alpha2.RoleTemplate{
							{Name: "base", Template: templateV1},
						}).
						WithRoles([]workloadsv1alpha2.RoleSpec{
							wrappersv2.BuildStandaloneRole("role1").
								WithPatchRef("base", &emptyPatch).
								Obj(),
						}).Obj()

					ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Equal(rbg)

					sts := &appsv1.StatefulSet{}
					gomega.Eventually(func() error {
						return f.Client.Get(f.Ctx, types.NamespacedName{
							Name: fmt.Sprintf("%s-%s", rbg.Name, "role1"), Namespace: f.Namespace,
						}, sts)
					}, 30*time.Second, 1*time.Second).Should(gomega.Succeed())
					gomega.Expect(sts.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]).To(
						gomega.Equal(resource.MustParse("100m")))

					revisionV1 := sts.Status.CurrentRevision

					templateV2 := buildBasicPodTemplateSpecV2([]corev1.Container{
						{
							Name:  "app",
							Image: testutils.DefaultImage,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU: resource.MustParse("200m"),
								},
							},
						},
					})

					updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
						rbg.Spec.RoleTemplates[0].Template = templateV2
					})
					f.ExpectRbgV2Equal(rbg)

					var revisionV2 string
					gomega.Eventually(func() bool {
						if err := f.Client.Get(f.Ctx, types.NamespacedName{
							Name: fmt.Sprintf("%s-%s", rbg.Name, "role1"), Namespace: f.Namespace,
						}, sts); err != nil {
							return false
						}
						cpu := sts.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
						if cpu.Equal(resource.MustParse("200m")) && sts.Status.CurrentRevision != revisionV1 {
							revisionV2 = sts.Status.CurrentRevision
							return true
						}
						return false
					}, 60*time.Second, 2*time.Second).Should(gomega.BeTrue())

					// Rollback
					updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
						rbg.Spec.RoleTemplates[0].Template = templateV1
					})
					f.ExpectRbgV2Equal(rbg)

					gomega.Eventually(func() bool {
						if err := f.Client.Get(f.Ctx, types.NamespacedName{
							Name: fmt.Sprintf("%s-%s", rbg.Name, "role1"), Namespace: f.Namespace,
						}, sts); err != nil {
							return false
						}
						cpu := sts.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
						return cpu.Equal(resource.MustParse("100m")) && sts.Status.CurrentRevision != revisionV2
					}, 60*time.Second, 2*time.Second).Should(gomega.BeTrue(),
						"Rollback should restore V1 CPU and create new revision")

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgV2Deleted(rbg)
				},
			)
		},
	)
}

func buildTemplatePatchV2(data map[string]interface{}) runtime.RawExtension {
	return runtime.RawExtension{Raw: []byte(pkgutils.DumpJSON(data))}
}

func buildBasicPodTemplateSpecV2(containers []corev1.Container) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: containers,
		},
	}
}
