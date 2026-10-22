/*
Copyright 2025 The RBG Authors.

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

package rbg

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var _ = Describe("RoleBasedGroup Controller", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	var (
		testNs string
	)

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-rbg-%d", time.Now().UnixNano())
		testutil.CreateNamespace(testNs)
	})

	AfterEach(func() {
		testutil.DeleteNamespace(testNs)
	})

	Context("资源状态与兼容性", func() {
		It("依赖等待保持原创建顺序，并在依赖解除后刷新状态", func() {
			rbg := &workloadsv1alpha2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Name: "status-dependency", Namespace: testNs}}
			for _, name := range []string{"primary", "secondary"} {
				role := workloadsv1alpha2.RoleSpec{
					Name: name, Replicas: ptr.To[int32](1),
					Pattern: workloadsv1alpha2.Pattern{StandalonePattern: &workloadsv1alpha2.StandalonePattern{TemplateSource: workloadsv1alpha2.TemplateSource{Template: ptr.To(nginxPodTemplate())}}},
				}
				if name == "secondary" {
					role.Dependencies = []string{"primary"}
				}
				rbg.Spec.Roles = append(rbg.Spec.Roles, role)
			}
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
			key := client.ObjectKeyFromObject(rbg)
			Eventually(func(g Gomega) {
				g.Expect(testutil.K8sClient.Get(testutil.Ctx, key, rbg)).To(Succeed())
				progress := apimeta.FindStatusCondition(rbg.Status.Conditions, "Progressing")
				g.Expect(progress).NotTo(BeNil())
				g.Expect(progress.Reason).To(Equal(workloadsv1alpha2.RoleBasedGroupReasonWaitingForDependencies))
				g.Expect(apimeta.IsStatusConditionTrue(rbg.Status.Conditions, "Accepted")).To(BeTrue())
				g.Expect(apimeta.IsStatusConditionTrue(rbg.Status.Conditions, "Ready")).To(BeFalse())
			}, timeout, interval).Should(Succeed())
			secondary := &workloadsv1alpha2.RoleInstanceSet{}
			Expect(apierrors.IsNotFound(testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{Namespace: testNs, Name: rbg.GetWorkloadName(&rbg.Spec.Roles[1])}, secondary))).To(BeTrue())

			for _, role := range []string{"primary", "secondary"} {
				Eventually(func(g Gomega) {
					pods := &corev1.PodList{}
					g.Expect(testutil.K8sClient.List(testutil.Ctx, pods, client.InNamespace(testNs), client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name, constants.RoleNameLabelKey: role})).To(Succeed())
					g.Expect(pods.Items).To(HaveLen(1))
					testutil.SetPodRunningAndReady(&pods.Items[0])
					g.Expect(testutil.K8sClient.Status().Update(testutil.Ctx, &pods.Items[0])).To(Succeed())
				}, timeout, interval).Should(Succeed())
			}
			Eventually(func(g Gomega) {
				g.Expect(testutil.K8sClient.Get(testutil.Ctx, key, rbg)).To(Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(rbg.Status.Conditions, "Ready")).To(BeTrue())
				g.Expect(apimeta.FindStatusCondition(rbg.Status.Conditions, "Progressing").Reason).To(Equal(workloadsv1alpha2.RoleBasedGroupReasonConverged))
			}, timeout, interval).Should(Succeed())
		})

		It("通过子状态 watch 刷新 Ready，并保留 phase 与自定义条件 schema", func() {
			rbg := &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "status-watch", Namespace: testNs},
				Spec:       workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{{Name: "worker", Replicas: ptr.To[int32](1), Pattern: workloadsv1alpha2.Pattern{StandalonePattern: &workloadsv1alpha2.StandalonePattern{TemplateSource: workloadsv1alpha2.TemplateSource{Template: ptr.To(nginxPodTemplate())}}}}}},
			}
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
			key := client.ObjectKeyFromObject(rbg)
			setKey := types.NamespacedName{Namespace: testNs, Name: rbg.GetWorkloadName(&rbg.Spec.Roles[0])}
			Eventually(func(g Gomega) {
				g.Expect(testutil.K8sClient.Get(testutil.Ctx, key, rbg)).To(Succeed())
				g.Expect(rbg.Status.Phase).To(Equal(workloadsv1alpha2.RoleBasedGroupPhaseRunning))
				g.Expect(apimeta.IsStatusConditionTrue(rbg.Status.Conditions, "Ready")).To(BeFalse())
				g.Expect(apimeta.IsStatusConditionTrue(rbg.Status.Conditions, "Accepted")).To(BeTrue())
			}, timeout, interval).Should(Succeed())
			generation := rbg.Generation
			pods := &corev1.PodList{}
			Eventually(func(g Gomega) {
				g.Expect(testutil.K8sClient.List(testutil.Ctx, pods, client.InNamespace(testNs), client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).To(Succeed())
				g.Expect(pods.Items).To(HaveLen(1))
			}, timeout, interval).Should(Succeed())
			podKey := client.ObjectKeyFromObject(&pods.Items[0])
			Eventually(func() error {
				pod := &corev1.Pod{}
				if err := testutil.K8sClient.Get(testutil.Ctx, podKey, pod); err != nil {
					return err
				}
				testutil.SetPodRunningAndReady(pod)
				return testutil.K8sClient.Status().Update(testutil.Ctx, pod)
			}, timeout, interval).Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(testutil.K8sClient.Get(testutil.Ctx, key, rbg)).To(Succeed())
				g.Expect(rbg.Generation).To(Equal(generation))
				g.Expect(apimeta.IsStatusConditionTrue(rbg.Status.Conditions, "Ready")).To(BeTrue())
				g.Expect(rbg.Status.Phase).To(Equal(workloadsv1alpha2.RoleBasedGroupPhaseRunning))
				set := &workloadsv1alpha2.RoleInstanceSet{}
				g.Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, set)).To(Succeed())
				g.Expect(set.Status.Phase).To(Equal(workloadsv1alpha2.RoleInstanceSetPhaseRunning))
			}, timeout, interval).Should(Succeed())

			// 外部条件不要求新增字段，也不收紧自定义 reason。
			raw := []byte(`{"status":{"conditions":[{"type":"example.com/External","status":"Unknown","reason":"external reason unchanged"}]}}`)
			set := &workloadsv1alpha2.RoleInstanceSet{ObjectMeta: metav1.ObjectMeta{Name: setKey.Name, Namespace: testNs}}
			Expect(testutil.K8sClient.Status().Patch(testutil.Ctx, set, client.RawPatch(types.MergePatchType, raw))).To(Succeed())
			instances := &workloadsv1alpha2.RoleInstanceList{}
			Expect(testutil.K8sClient.List(testutil.Ctx, instances, client.InNamespace(testNs), client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name})).To(Succeed())
			Expect(instances.Items).To(HaveLen(1))
			Expect(testutil.K8sClient.Status().Patch(testutil.Ctx, &instances.Items[0], client.RawPatch(types.MergePatchType, raw))).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, set)).To(Succeed())
				found := false
				for _, c := range set.Status.Conditions {
					if c.Type == "example.com/External" {
						found = c.Reason == "external reason unchanged"
					}
				}
				g.Expect(found).To(BeTrue())
				oldVersion := &workloadsv1alpha1.RoleBasedGroup{}
				g.Expect(testutil.K8sClient.Get(testutil.Ctx, key, oldVersion)).To(Succeed())
				g.Expect(oldVersion.Status.Phase).To(Equal(workloadsv1alpha1.RoleBasedGroupPhaseRunning))
			}, timeout, interval).Should(Succeed())

			// 仅 Pod 状态下降，父 spec 不变也须重新报告 NotReady。
			Eventually(func() error {
				pod := &corev1.Pod{}
				if err := testutil.K8sClient.Get(testutil.Ctx, podKey, pod); err != nil {
					return err
				}
				for i := range pod.Status.Conditions {
					if pod.Status.Conditions[i].Type == corev1.PodReady {
						pod.Status.Conditions[i].Status = corev1.ConditionFalse
					}
				}
				return testutil.K8sClient.Status().Update(testutil.Ctx, pod)
			}, timeout, interval).Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(testutil.K8sClient.Get(testutil.Ctx, key, rbg)).To(Succeed())
				g.Expect(rbg.Generation).To(Equal(generation))
				g.Expect(apimeta.IsStatusConditionTrue(rbg.Status.Conditions, "Ready")).To(BeFalse())
			}, timeout, interval).Should(Succeed())
		})
	})

	Context("When creating a RoleBasedGroup", func() {
		It("Should create child Deployment for each role", func() {
			rbgName := "test-rbg-basic"

			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(1)),
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							TemplateSource: workloadsv1alpha1.TemplateSource{
								Template: ptr.To(corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{
										Containers: []corev1.Container{
											{
												Name:  "nginx",
												Image: "nginx:latest",
											},
										},
									},
								}),
							},
						},
					},
				},
			}

			// Create RoleBasedGroup
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			// Verify RoleBasedGroup is created
			rbgLookupKey := types.NamespacedName{Name: rbgName, Namespace: testNs}
			createdRBG := &workloadsv1alpha1.RoleBasedGroup{}

			Eventually(func() error {
				return testutil.K8sClient.Get(testutil.Ctx, rbgLookupKey, createdRBG)
			}, timeout, interval).Should(Succeed())

			Expect(createdRBG.Spec.Roles).To(HaveLen(1))
			Expect(createdRBG.Spec.Roles[0].Name).To(Equal("worker"))
		})

		It("Should reject v1alpha2 custom components with duplicate names", func() {
			rbg := &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "duplicate-v1alpha2-components",
					Namespace: testNs,
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(1)),
							Pattern: workloadsv1alpha2.Pattern{
								CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
									Components: []workloadsv1alpha2.InstanceComponent{
										{
											Name:     "worker",
											Template: nginxPodTemplate(),
										},
										{
											Name:     "worker",
											Template: nginxPodTemplate(),
										},
									},
								},
							},
						},
					},
				},
			}

			expectInvalidCreate(rbg)
		})

		It("Should reject v1alpha1 components with duplicate names", func() {
			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "duplicate-v1alpha1-components",
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(1)),
							Components: []workloadsv1alpha1.InstanceComponent{
								{
									Name:     "worker",
									Template: nginxPodTemplate(),
								},
								{
									Name:     "worker",
									Template: nginxPodTemplate(),
								},
							},
						},
					},
				},
			}

			expectInvalidCreate(rbg)
		})

		It("Should reject RoleInstance components with duplicate names", func() {
			instance := &workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "duplicate-roleinstance-components",
					Namespace: testNs,
				},
				Spec: workloadsv1alpha2.RoleInstanceSpec{
					Components: []workloadsv1alpha2.RoleInstanceComponent{
						{
							Name:     "worker",
							Template: nginxPodTemplate(),
						},
						{
							Name:     "worker",
							Template: nginxPodTemplate(),
						},
					},
				},
			}

			expectInvalidCreate(instance)
		})

		It("Should allow v1alpha2 templateRef without patch", func() {
			rbgName := "template-ref-without-patch"
			rbg := &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					RoleTemplates: []workloadsv1alpha2.RoleTemplate{
						{
							Name:     "base",
							Template: nginxPodTemplate(),
						},
					},
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(0)),
							Pattern: workloadsv1alpha2.Pattern{
								LeaderWorkerPattern: &workloadsv1alpha2.LeaderWorkerPattern{
									Size: ptr.To(int32(1)),
									TemplateSource: workloadsv1alpha2.TemplateSource{
										TemplateRef: &workloadsv1alpha2.TemplateRef{Name: "base"},
									},
								},
							},
						},
					},
				},
			}

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			created := &workloadsv1alpha2.RoleBasedGroup{}
			Eventually(func() error {
				if err := testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{Name: rbgName, Namespace: testNs}, created); err != nil {
					return err
				}
				if !apimeta.IsStatusConditionTrue(created.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupReady)) {
					return fmt.Errorf("RoleBasedGroup %s/%s Ready condition is not true: %v", testNs, rbgName, created.Status.Conditions)
				}
				return nil
			}, timeout, interval).Should(Succeed())
		})

		It("Should reject v1alpha2 role rolloutStrategy with invalid update type", func() {
			rbg := &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-rbg-update-type",
					Namespace: testNs,
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(1)),
							RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
								Type: workloadsv1alpha2.RollingUpdateStrategyType,
								RollingUpdate: &workloadsv1alpha2.RollingUpdate{
									Type: workloadsv1alpha2.UpdateStrategyType("DefinitelyNotARealStrategy"),
								},
							},
							Pattern: workloadsv1alpha2.Pattern{
								StandalonePattern: &workloadsv1alpha2.StandalonePattern{
									TemplateSource: workloadsv1alpha2.TemplateSource{
										Template: ptr.To(nginxPodTemplate()),
									},
								},
							},
						},
					},
				},
			}

			expectInvalidCreate(rbg)
		})

		It("Should reject RoleInstanceSet with invalid update strategy type", func() {
			ris := &workloadsv1alpha2.RoleInstanceSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-ris-update-type",
					Namespace: testNs,
				},
				Spec: workloadsv1alpha2.RoleInstanceSetSpec{
					Replicas: ptr.To(int32(0)),
					RoleInstanceTemplate: workloadsv1alpha2.RoleInstanceTemplate{
						RoleInstanceSpec: workloadsv1alpha2.RoleInstanceSpec{
							Components: []workloadsv1alpha2.RoleInstanceComponent{
								{
									Name:     "worker",
									Size:     ptr.To(int32(1)),
									Template: nginxPodTemplate(),
								},
							},
						},
					},
					UpdateStrategy: workloadsv1alpha2.RoleInstanceSetUpdateStrategy{
						Type: workloadsv1alpha2.UpdateStrategyType("DefinitelyNotARealStrategy"),
					},
				},
			}

			expectInvalidCreate(ris)
		})

		It("Should accept all valid v1alpha2 update strategy types", func() {
			for _, updateType := range []workloadsv1alpha2.UpdateStrategyType{
				workloadsv1alpha2.RecreatePodUpdateStrategyType,
				workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
				// Deprecated but still a valid enum value.
				"InPlaceOnly",
			} {
				rbg := &workloadsv1alpha2.RoleBasedGroup{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-rbg-update-type-" + strings.ToLower(string(updateType)),
						Namespace: testNs,
					},
					Spec: workloadsv1alpha2.RoleBasedGroupSpec{
						Roles: []workloadsv1alpha2.RoleSpec{
							{
								Name:     "worker",
								Replicas: ptr.To(int32(1)),
								RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
									Type: workloadsv1alpha2.RollingUpdateStrategyType,
									RollingUpdate: &workloadsv1alpha2.RollingUpdate{
										Type: updateType,
									},
								},
								Pattern: workloadsv1alpha2.Pattern{
									StandalonePattern: &workloadsv1alpha2.StandalonePattern{
										TemplateSource: workloadsv1alpha2.TemplateSource{
											Template: ptr.To(nginxPodTemplate()),
										},
									},
								},
							},
						},
					},
				}

				Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())
			}
		})

		It("Should allow updating a valid v1alpha2 rolloutStrategy", func() {
			rbgName := "valid-rbg-update-path"
			rbg := &workloadsv1alpha2.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{
						{
							Name:     "worker",
							Replicas: ptr.To(int32(1)),
							RolloutStrategy: &workloadsv1alpha2.RolloutStrategy{
								Type: workloadsv1alpha2.RollingUpdateStrategyType,
								RollingUpdate: &workloadsv1alpha2.RollingUpdate{
									Type: workloadsv1alpha2.RecreatePodUpdateStrategyType,
								},
							},
							Pattern: workloadsv1alpha2.Pattern{
								StandalonePattern: &workloadsv1alpha2.StandalonePattern{
									TemplateSource: workloadsv1alpha2.TemplateSource{
										Template: ptr.To(nginxPodTemplate()),
									},
								},
							},
						},
					},
				},
			}

			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).Should(Succeed())

			// Flip an unrelated spec field to prove that updates to objects carrying
			// an enum-valid update type are not rejected by the CRD validation.
			Eventually(func() error {
				created := &workloadsv1alpha2.RoleBasedGroup{}
				if err := testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{Name: rbgName, Namespace: testNs}, created); err != nil {
					return err
				}
				created.Spec.Roles[0].RolloutStrategy.RollingUpdate.Paused = true
				return testutil.K8sClient.Update(testutil.Ctx, created)
			}, timeout, interval).Should(Succeed())
		})
	})
})

func nginxPodTemplate() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "nginx",
					Image: "nginx:latest",
				},
			},
		},
	}
}

func expectInvalidCreate(obj client.Object) {
	err := testutil.K8sClient.Create(testutil.Ctx, obj)
	Expect(err).To(HaveOccurred())
	Expect(apierrors.IsInvalid(err)).To(BeTrue(), "got %v", err)
}
