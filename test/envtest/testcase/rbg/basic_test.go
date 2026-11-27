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
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
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

	It("rewrites RoleInstanceSet affinity when only the exclusive topology annotation changes", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("exclusive-toggle", testNs).
			WithRoles([]workloadsv1alpha2.RoleSpec{wrappersv2.BuildStandaloneRole("worker").WithReplicas(0).Obj()}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
		rbgKey := client.ObjectKeyFromObject(rbg)
		risKey := types.NamespacedName{Namespace: testNs, Name: rbg.GetWorkloadName(&rbg.Spec.Roles[0])}
		readRIS := func(g Gomega) *workloadsv1alpha2.RoleInstanceSet {
			ris := &workloadsv1alpha2.RoleInstanceSet{}
			g.Expect(testutil.K8sClient.Get(testutil.Ctx, risKey, ris)).To(Succeed())
			g.Expect(ris.Spec.RoleInstanceTemplate.Components).To(HaveLen(1))
			g.Expect(ris.Status.ObservedGeneration).To(Equal(ris.Generation))
			return ris
		}
		affinity := func(g Gomega) *corev1.Affinity {
			return readRIS(g).Spec.RoleInstanceTemplate.Components[0].Template.Spec.Affinity
		}
		Eventually(affinity, timeout, interval).Should(BeNil())
		findSetCondition := func(ris *workloadsv1alpha2.RoleInstanceSet, typ workloadsv1alpha2.RoleInstanceSetConditionType) *workloadsv1alpha2.RoleInstanceSetCondition {
			for i := range ris.Status.Conditions {
				if ris.Status.Conditions[i].Type == typ {
					return &ris.Status.Conditions[i]
				}
			}
			return nil
		}
		Eventually(func(g Gomega) error {
			ris := readRIS(g)
			progressing := findSetCondition(ris, workloadsv1alpha2.RoleInstanceSetProgressing)
			if progressing == nil || progressing.Status != corev1.ConditionFalse ||
				progressing.Reason != constants.ProgressingReasonConverged ||
				progressing.ObservedGeneration != ris.Generation {
				return fmt.Errorf("RoleInstanceSet %s has not converged: %+v", ris.Name, progressing)
			}
			return nil
		}, timeout, interval).Should(Succeed())
		// Wait for the initial status writes to settle before taking the baseline.
		lastVersion := ""
		stableReads := 0
		Eventually(func(g Gomega) bool {
			version := readRIS(g).ResourceVersion
			if version == lastVersion {
				stableReads++
			} else {
				lastVersion = version
				stableReads = 0
			}
			return stableReads >= 8
		}, timeout, interval).Should(BeTrue())
		// Drain the initial child events before testing the annotation-only watch.
		initialVersion := readRIS(Default).ResourceVersion
		Consistently(func(g Gomega) string {
			return readRIS(g).ResourceVersion
		}, 2*time.Second, interval).Should(Equal(initialVersion))
		setTopology := func(topologyKey string) {
			latest := &workloadsv1alpha2.RoleBasedGroup{}
			Expect(testutil.K8sClient.Get(testutil.Ctx, rbgKey, latest)).To(Succeed())
			before := latest.DeepCopy()
			if topologyKey == "" {
				delete(latest.Annotations, constants.GroupExclusiveTopologyKey)
			} else {
				if latest.Annotations == nil {
					latest.Annotations = make(map[string]string)
				}
				latest.Annotations[constants.GroupExclusiveTopologyKey] = topologyKey
			}
			Expect(testutil.K8sClient.Patch(testutil.Ctx, latest, client.MergeFrom(before))).To(Succeed())
			Expect(latest.Generation).To(Equal(rbg.Generation))
		}
		setTopology(corev1.LabelHostname)
		Eventually(func(g Gomega) {
			rules := affinity(g)
			g.Expect(rules).NotTo(BeNil())
			g.Expect(rules.PodAffinity).NotTo(BeNil())
			g.Expect(rules.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution).To(Equal([]corev1.PodAffinityTerm{{
				TopologyKey: corev1.LabelHostname,
				LabelSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key: constants.GroupUIDLabelKey, Operator: metav1.LabelSelectorOpIn, Values: []string{rbg.GenGroupUniqueKey()},
				}}},
			}}))
		}, timeout, interval).Should(Succeed())
		setTopology("")
		Eventually(affinity, timeout, interval).Should(BeNil())
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

	Context("When the RoleBasedGroup was converted from v1alpha1", func() {
		// The envtest control plane runs no conversion webhook, so the hub object the
		// webhook would have stored is produced by calling ConvertTo directly.
		It("Should apply a named CoordinatedPolicy and still create the role workloads", func() {
			const rbgName = "coord-main"
			roleNames := []string{"prefill", "decode"}

			src := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					CoordinationRequirements: []workloadsv1alpha1.Coordination{
						{
							Name:  "pd-ratio",
							Roles: roleNames,
							Strategy: &workloadsv1alpha1.CoordinationStrategy{
								RollingUpdate: &workloadsv1alpha1.CoordinationRollingUpdate{
									MaxSkew: ptr.To("50%"),
								},
							},
						},
					},
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:           roleNames[0],
							Replicas:       ptr.To(int32(1)),
							TemplateSource: workloadsv1alpha1.TemplateSource{Template: ptr.To(nginxPodTemplate())},
						},
						{
							Name:           roleNames[1],
							Replicas:       ptr.To(int32(1)),
							TemplateSource: workloadsv1alpha1.TemplateSource{Template: ptr.To(nginxPodTemplate())},
						},
					},
				},
			}

			hub := &workloadsv1alpha2.RoleBasedGroup{}
			Expect(src.ConvertTo(hub)).To(Succeed())
			Expect(testutil.K8sClient.Create(testutil.Ctx, hub)).To(Succeed())

			// spec.policies is an associative list keyed by name, so the server-side apply
			// is rejected outright unless every rule carries one. The fake client does not
			// enforce list keys, which is why this is asserted against a real apiserver.
			policy := &workloadsv1alpha2.CoordinatedPolicy{}
			Eventually(func() error {
				return testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{Name: rbgName, Namespace: testNs}, policy)
			}, timeout, interval).Should(Succeed())

			Expect(policy.Spec.Policies).To(HaveLen(1))
			Expect(policy.Spec.Policies[0].Name).To(Equal("pd-ratio"))
			Expect(policy.Spec.Policies[0].Roles).To(Equal(roleNames))
			Expect(policy.Spec.Policies[0].Strategy.RollingUpdate).NotTo(BeNil())
			Expect(policy.Spec.Policies[0].Strategy.RollingUpdate.MaxSkew).
				To(Equal(ptr.To(intstr.FromString("50%"))))
			Expect(policy.OwnerReferences).To(HaveLen(1))
			Expect(policy.OwnerReferences[0].Name).To(Equal(rbgName))

			// The rejected apply used to abort the reconcile before any workload was created.
			for i := range hub.Spec.Roles {
				role := &hub.Spec.Roles[i]
				ris := &workloadsv1alpha2.RoleInstanceSet{}
				Eventually(func() error {
					return testutil.K8sClient.Get(testutil.Ctx, types.NamespacedName{
						Name: hub.GetWorkloadName(role), Namespace: testNs,
					}, ris)
				}, timeout, interval).Should(Succeed(), "no workload created for role %s", role.Name)
			}
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
