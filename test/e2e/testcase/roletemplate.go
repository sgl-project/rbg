package testcase

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	testutils "sigs.k8s.io/rbgs/test/utils"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func RunRoleTemplateTestCases(f *framework.Framework) {
	ginkgo.Describe(
		"roletemplate controller", func() {

			// Test 1: Basic functionality - create RBG with templateRef and verify workloads
			// Also covers empty templatePatch case
			ginkgo.It(
				"create rbg with roleTemplates and verify workloads", func() {
					baseTemplate := wrappers.BuildBasicPodTemplateSpec().
						WithContainers([]corev1.Container{
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
						}).Obj()

					// Role1: patch CPU to 200m
					role1Patch := buildTemplatePatch(map[string]interface{}{
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

					// Role2: empty patch (use base template as-is)
					emptyPatch := buildTemplatePatch(map[string]interface{}{})

					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-roletemplate", f.Namespace).
						WithRoleTemplates([]workloadsv1alpha1.RoleTemplate{
							{
								Name:     "base-template",
								Template: baseTemplate,
							},
						}).
						WithRoles([]workloadsv1alpha1.RoleSpec{
							wrappers.BuildBasicRole("role1").
								WithTemplateRef("base-template").
								WithTemplatePatch(role1Patch).
								WithWorkload(workloadsv1alpha1.StatefulSetWorkloadType).
								Obj(),
							wrappers.BuildBasicRole("role2").
								WithTemplateRef("base-template").
								WithTemplatePatch(emptyPatch).
								WithWorkload(workloadsv1alpha1.StatefulSetWorkloadType).
								Obj(),
						}).Obj()

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgEqual(rbg)

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

					// Verify role2: empty patch, should use base template CPU 100m
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
					f.ExpectRbgDeleted(rbg)
				},
			)

			// Test 2: Update roleTemplate triggers rollout and increments ControllerRevision
			ginkgo.It(
				"update roleTemplate triggers rollout and increments revision", func() {
					baseTemplate := wrappers.BuildBasicPodTemplateSpec().
						WithContainers([]corev1.Container{
							{
								Name:  "app",
								Image: testutils.DefaultImage,
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("100m"),
									},
								},
							},
						}).Obj()

					emptyPatch := buildTemplatePatch(map[string]interface{}{})

					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-update-template", f.Namespace).
						WithRoleTemplates([]workloadsv1alpha1.RoleTemplate{
							{Name: "base", Template: baseTemplate},
						}).
						WithRoles([]workloadsv1alpha1.RoleSpec{
							wrappers.BuildBasicRole("role1").
								WithTemplateRef("base").
								WithTemplatePatch(emptyPatch).
								WithWorkload(workloadsv1alpha1.StatefulSetWorkloadType).
								Obj(),
						}).Obj()

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgEqual(rbg)

					// Get initial state
					sts := &appsv1.StatefulSet{}
					gomega.Eventually(func() error {
						return f.Client.Get(f.Ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-%s", rbg.Name, "role1"),
							Namespace: f.Namespace,
						}, sts)
					}, 30*time.Second, 1*time.Second).Should(gomega.Succeed())

					initialRevision := sts.Status.CurrentRevision

					// Get initial ControllerRevision number
					selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
						MatchLabels: map[string]string{
							workloadsv1alpha1.SetNameLabelKey: rbg.Name,
						},
					})
					gomega.Expect(err).ToNot(gomega.HaveOccurred())

					var initialRevisionNum int64
					gomega.Eventually(func() bool {
						revisions, err := utils.ListRevisions(f.Ctx, f.Client, rbg, selector)
						if err != nil || len(revisions) == 0 {
							return false
						}
						initialRevisionNum = utils.GetHighestRevision(revisions).Revision
						return true
					}, 30*time.Second, 1*time.Second).Should(gomega.BeTrue())

					// Update roleTemplate
					updatedTemplate := wrappers.BuildBasicPodTemplateSpec().
						WithContainers([]corev1.Container{
							{
								Name:  "app",
								Image: testutils.DefaultImage,
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("200m"),
									},
								},
							},
						}).Obj()

					testutils.UpdateRbg(f.Ctx, f.Client, rbg, func(rbg *workloadsv1alpha1.RoleBasedGroup) {
						rbg.Spec.RoleTemplates[0].Template = updatedTemplate
					})
					f.ExpectRbgEqual(rbg)

					// Verify rollout happens (new revision)
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

					// Verify ControllerRevision version incremented
					gomega.Eventually(func() bool {
						revisions, err := utils.ListRevisions(f.Ctx, f.Client, rbg, selector)
						if err != nil || len(revisions) == 0 {
							return false
						}
						newRevisionNum := utils.GetHighestRevision(revisions).Revision
						return newRevisionNum > initialRevisionNum
					}, 60*time.Second, 2*time.Second).Should(gomega.BeTrue(),
						"ControllerRevision version should increment after roleTemplate update")

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgDeleted(rbg)
				},
			)

			// Test 3: Mixed mode - some roles use templateRef, others use traditional template
			ginkgo.It(
				"create rbg with mixed mode - templateRef and traditional template coexist", func() {
					baseTemplate := wrappers.BuildBasicPodTemplateSpec().
						WithContainers([]corev1.Container{
							{
								Name:  "app",
								Image: testutils.DefaultImage,
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("100m"),
									},
								},
							},
						}).Obj()

					emptyPatch := buildTemplatePatch(map[string]interface{}{})

					traditionalTemplate := wrappers.BuildBasicPodTemplateSpec().
						WithContainers([]corev1.Container{
							{
								Name:  "app",
								Image: testutils.DefaultImage,
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("200m"),
									},
								},
							},
						}).Obj()

					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-mixed-mode", f.Namespace).
						WithRoleTemplates([]workloadsv1alpha1.RoleTemplate{
							{Name: "base", Template: baseTemplate},
						}).
						WithRoles([]workloadsv1alpha1.RoleSpec{
							wrappers.BuildBasicRole("templated-role").
								WithTemplateRef("base").
								WithTemplatePatch(emptyPatch).
								WithWorkload(workloadsv1alpha1.StatefulSetWorkloadType).
								Obj(),
							wrappers.BuildBasicRole("traditional-role").
								WithTemplate(traditionalTemplate).
								WithWorkload(workloadsv1alpha1.StatefulSetWorkloadType).
								Obj(),
						}).Obj()

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgEqual(rbg)

					// Verify templated role (CPU 100m from templateRef)
					sts1 := &appsv1.StatefulSet{}
					gomega.Eventually(func() error {
						return f.Client.Get(f.Ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-%s", rbg.Name, "templated-role"),
							Namespace: f.Namespace,
						}, sts1)
					}, 30*time.Second, 1*time.Second).Should(gomega.Succeed())
					gomega.Expect(sts1.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]).To(
						gomega.Equal(resource.MustParse("100m")))

					// Verify traditional role (CPU 200m from inline template)
					sts2 := &appsv1.StatefulSet{}
					gomega.Eventually(func() error {
						return f.Client.Get(f.Ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-%s", rbg.Name, "traditional-role"),
							Namespace: f.Namespace,
						}, sts2)
					}, 30*time.Second, 1*time.Second).Should(gomega.Succeed())
					gomega.Expect(sts2.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]).To(
						gomega.Equal(resource.MustParse("200m")))

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgDeleted(rbg)
				},
			)

			// Test 4: LWS workload with templateRef and two-layer patch
			ginkgo.It(
				"create LWS workload with templateRef and verify two-layer patch", func() {
					// Base template
					baseTemplate := wrappers.BuildBasicPodTemplateSpec().
						WithContainers([]corev1.Container{
							{
								Name:  "app",
								Image: testutils.DefaultImage,
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("100m"),
										corev1.ResourceMemory: resource.MustParse("128Mi"),
									},
								},
							},
						}).Obj()

					// First layer: role-level templatePatch
					rolePatch := buildTemplatePatch(map[string]interface{}{
						"spec": map[string]interface{}{
							"containers": []map[string]interface{}{
								{
									"name": "app",
									"resources": map[string]interface{}{
										"requests": map[string]interface{}{
											"memory": "256Mi", // Override memory
										},
									},
								},
							},
						},
					})

					// Second layer: leader/worker patches
					leaderPatch := buildTemplatePatch(map[string]interface{}{
						"metadata": map[string]interface{}{
							"labels": map[string]interface{}{
								"node-type": "leader",
							},
						},
					})
					workerPatch := buildTemplatePatch(map[string]interface{}{
						"metadata": map[string]interface{}{
							"labels": map[string]interface{}{
								"node-type": "worker",
							},
						},
					})

					leaderPatchExt := runtime.RawExtension{Raw: leaderPatch.Raw}
					workerPatchExt := runtime.RawExtension{Raw: workerPatch.Raw}

					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-lws-templateref", f.Namespace).
						WithRoleTemplates([]workloadsv1alpha1.RoleTemplate{
							{Name: "lws-base", Template: baseTemplate},
						}).
						WithRoles([]workloadsv1alpha1.RoleSpec{
							wrappers.BuildBasicRole("lws-role").
								WithTemplateRef("lws-base").
								WithTemplatePatch(rolePatch).
								WithWorkload(workloadsv1alpha1.LeaderWorkerSetWorkloadType).
								WithLeaderWorkerTemplate(&leaderPatchExt, &workerPatchExt).
								Obj(),
						}).Obj()

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgEqual(rbg)

					// Verify LWS resource
					lws := &lwsv1.LeaderWorkerSet{}
					gomega.Eventually(func() error {
						return f.Client.Get(f.Ctx, types.NamespacedName{
							Name:      fmt.Sprintf("%s-%s", rbg.Name, "lws-role"),
							Namespace: f.Namespace,
						}, lws)
					}, 30*time.Second, 1*time.Second).Should(gomega.Succeed())

					// Verify Leader: memory from templatePatch (256Mi), CPU from base (100m)
					gomega.Expect(lws.Spec.LeaderWorkerTemplate.LeaderTemplate).NotTo(gomega.BeNil())
					leaderContainer := lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers[0]
					gomega.Expect(leaderContainer.Resources.Requests[corev1.ResourceCPU]).To(
						gomega.Equal(resource.MustParse("100m")))
					gomega.Expect(leaderContainer.Resources.Requests[corev1.ResourceMemory]).To(
						gomega.Equal(resource.MustParse("256Mi")))
					gomega.Expect(lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Labels["node-type"]).To(
						gomega.Equal("leader"))

					// Verify Worker: same resources, different label
					workerContainer := lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0]
					gomega.Expect(workerContainer.Resources.Requests[corev1.ResourceCPU]).To(
						gomega.Equal(resource.MustParse("100m")))
					gomega.Expect(workerContainer.Resources.Requests[corev1.ResourceMemory]).To(
						gomega.Equal(resource.MustParse("256Mi")))
					gomega.Expect(lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Labels["node-type"]).To(
						gomega.Equal("worker"))

					gomega.Expect(f.Client.Delete(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgDeleted(rbg)
				},
			)

			// Test 5: Rollback roleTemplate to previous version restores workload spec
			ginkgo.It(
				"rollback roleTemplate to previous version restores workload spec", func() {
					// V1: CPU 100m
					templateV1 := wrappers.BuildBasicPodTemplateSpec().
						WithContainers([]corev1.Container{
							{
								Name:  "app",
								Image: testutils.DefaultImage,
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("100m"),
									},
								},
							},
						}).Obj()

					emptyPatch := buildTemplatePatch(map[string]interface{}{})

					rbg := wrappers.BuildBasicRoleBasedGroup("e2e-rollback", f.Namespace).
						WithRoleTemplates([]workloadsv1alpha1.RoleTemplate{
							{Name: "base", Template: templateV1},
						}).
						WithRoles([]workloadsv1alpha1.RoleSpec{
							wrappers.BuildBasicRole("role1").
								WithTemplateRef("base").
								WithTemplatePatch(emptyPatch).
								WithWorkload(workloadsv1alpha1.StatefulSetWorkloadType).
								Obj(),
						}).Obj()

					gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
					f.ExpectRbgEqual(rbg)

					// Verify V1 CPU
					sts := &appsv1.StatefulSet{}
					gomega.Eventually(func() error {
						return f.Client.Get(f.Ctx, types.NamespacedName{
							Name: fmt.Sprintf("%s-%s", rbg.Name, "role1"), Namespace: f.Namespace,
						}, sts)
					}, 30*time.Second, 1*time.Second).Should(gomega.Succeed())
					gomega.Expect(sts.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]).To(
						gomega.Equal(resource.MustParse("100m")))

					revisionV1 := sts.Status.CurrentRevision

					// Update to V2: CPU 200m
					templateV2 := wrappers.BuildBasicPodTemplateSpec().
						WithContainers([]corev1.Container{
							{
								Name:  "app",
								Image: testutils.DefaultImage,
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("200m"),
									},
								},
							},
						}).Obj()

					testutils.UpdateRbg(f.Ctx, f.Client, rbg, func(rbg *workloadsv1alpha1.RoleBasedGroup) {
						rbg.Spec.RoleTemplates[0].Template = templateV2
					})
					f.ExpectRbgEqual(rbg)

					// Wait for V2
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

					// Rollback to V1
					testutils.UpdateRbg(f.Ctx, f.Client, rbg, func(rbg *workloadsv1alpha1.RoleBasedGroup) {
						rbg.Spec.RoleTemplates[0].Template = templateV1
					})
					f.ExpectRbgEqual(rbg)

					// Verify rollback: CPU back to 100m with new revision
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
					f.ExpectRbgDeleted(rbg)
				},
			)
		},
	)
}

func buildTemplatePatch(data map[string]interface{}) runtime.RawExtension {
	return runtime.RawExtension{Raw: []byte(utils.DumpJSON(data))}
}
