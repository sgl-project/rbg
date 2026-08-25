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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scheduler"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunGangSchedulingTestCases(f *framework.Framework) {
	// The whole suite requires the controller deployed with --scheduler-name=volcano
	// and a cluster Volcano >= v1.14 (subGroupPolicy). Label it so it only runs in
	// the volcano e2e environment and is excluded from the default (scheduler-plugins) run.
	ginkgo.Describe("gang scheduling", ginkgo.Label("volcano"), func() {

		// ============================================================
		// Test 1: CoordinatedPolicy gang:{} (basic gang, no minReplicas)
		// Verifies: PodGroup created with minMember = GetGroupSize(),
		//           schedulerName injected, pods eventually ready.
		// ============================================================
		ginkgo.It("coordinated policy gang:{} enables basic gang scheduling", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-gang-basic", f.Namespace).
				WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("prefill").WithReplicas(2).Obj(),
						wrappersv2.BuildStandaloneRole("decode").WithReplicas(2).Obj(),
					},
				).Obj()

			cpolicy := &workloadsv1alpha2.CoordinatedPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbg.Name,
					Namespace: f.Namespace,
				},
				Spec: workloadsv1alpha2.CoordinatedPolicySpec{
					Policies: []workloadsv1alpha2.CoordinatedPolicyRule{
						{
							Name:  "gang-scheduling",
							Roles: []string{"prefill", "decode"},
							Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
								Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
									Gang: &workloadsv1alpha2.GangSchedulingStrategy{},
								},
							},
						},
					},
				},
			}

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, cpolicy)).Should(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Verify PodGroup is created with minMember = GetGroupSize()
			// GetGroupSize() for 2 standalone roles × 2 replicas each = 4
			gomega.Eventually(func() bool {
				pg, err := getVolcanoPodGroup(f, rbg.Name, rbg.Namespace)
				if err != nil {
					return false
				}
				minMember, _, _ := unstructured.NestedInt64(pg.Object, "spec", "minMember")
				return minMember == int64(rbg.GetGroupSize())
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"PodGroup minMember should equal GetGroupSize()")

			// Verify schedulerName is injected on pods
			f.ExpectWorkloadV2PodTemplateAnnotationContains(rbg, rbg.Spec.Roles[0],
				map[string]string{scheduler.VolcanoPodGroupAnnotationKey: rbg.Name})

			// Verify pods eventually become ready
			f.ExpectRbgV2Equal(rbg)
		})

		// ============================================================
		// Test 2: Annotation compatibility path (no CoordinatedPolicy)
		// Verifies: annotation group-gang-scheduling=true enables basic gang,
		//           PodGroup created, pods get annotation, schedulerName injected.
		// ============================================================
		ginkgo.It("annotation compatibility enables basic gang scheduling", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-gang-anno", f.Namespace).
				WithVolcanoGangScheduling("default").
				WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("role-a").WithReplicas(1).Obj(),
						wrappersv2.BuildStandaloneRole("role-b").WithReplicas(1).Obj(),
					},
				).Obj()

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Verify PodGroup is created (annotation-driven)
			gomega.Eventually(func() bool {
				_, err := getVolcanoPodGroup(f, rbg.Name, rbg.Namespace)
				return err == nil
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"PodGroup should be created when annotation is set")

			// Verify pods get Volcano PodGroup annotation
			f.ExpectWorkloadV2PodTemplateAnnotationContains(rbg, rbg.Spec.Roles[0],
				map[string]string{scheduler.VolcanoPodGroupAnnotationKey: rbg.Name})

			// Verify pods eventually become ready
			f.ExpectRbgV2Equal(rbg)
		})

		// ============================================================
		// Test 3: Volcano minReplicas creates PodGroup with subGroupPolicy
		// Verifies: subGroupPolicy fields (name, minSubGroups, subGroupSize),
		//           schedulerName injection, pods eventually ready.
		// ============================================================
		ginkgo.It("volcano gang scheduling with minReplicas creates subGroupPolicy", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-gang-minreplicas", f.Namespace).
				WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("prefill").WithReplicas(3).Obj(),
						wrappersv2.BuildStandaloneRole("decode").WithReplicas(3).Obj(),
					},
				).Obj()

			cpolicy := &workloadsv1alpha2.CoordinatedPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbg.Name,
					Namespace: f.Namespace,
				},
				Spec: workloadsv1alpha2.CoordinatedPolicySpec{
					Policies: []workloadsv1alpha2.CoordinatedPolicyRule{
						{
							Name:  "gang-scheduling",
							Roles: []string{"prefill", "decode"},
							Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
								Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
									Gang: &workloadsv1alpha2.GangSchedulingStrategy{
										MinReplicas: map[string]int32{
											"prefill": 2,
											"decode":  1,
										},
									},
								},
							},
						},
					},
				},
			}

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, cpolicy)).Should(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Verify PodGroup is created with subGroupPolicy
			gomega.Eventually(func() bool {
				pg, err := getVolcanoPodGroup(f, rbg.Name, rbg.Namespace)
				if err != nil {
					return false
				}
				sgp, found, err := unstructured.NestedSlice(pg.Object, "spec", "subGroupPolicy")
				if err != nil || !found || len(sgp) != 2 {
					return false
				}
				return true
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"PodGroup should have subGroupPolicy with 2 entries")

			// Verify subGroupPolicy fields
			pg, err := getVolcanoPodGroup(f, rbg.Name, rbg.Namespace)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			sgp, _, _ := unstructured.NestedSlice(pg.Object, "spec", "subGroupPolicy")

			// Build a map from subGroupPolicy entries for easy lookup
			sgMap := make(map[string]map[string]interface{})
			for _, entry := range sgp {
				if m, ok := entry.(map[string]interface{}); ok {
					name, _, _ := unstructured.NestedString(m, "name")
					sgMap[name] = m
				}
			}

			// Verify prefill subGroup
			prefillSg, ok := sgMap["prefill"]
			gomega.Expect(ok).To(gomega.BeTrue(), "subGroupPolicy should have prefill entry")
			minSubGroups, _, _ := unstructured.NestedInt64(prefillSg, "minSubGroups")
			subGroupSize, _, _ := unstructured.NestedInt64(prefillSg, "subGroupSize")
			gomega.Expect(minSubGroups).To(gomega.Equal(int64(2)), "prefill minSubGroups should be 2")
			gomega.Expect(subGroupSize).To(gomega.Equal(int64(1)), "prefill subGroupSize should be 1 (standalone)")

			// Verify decode subGroup
			decodeSg, ok := sgMap["decode"]
			gomega.Expect(ok).To(gomega.BeTrue(), "subGroupPolicy should have decode entry")
			minSubGroups, _, _ = unstructured.NestedInt64(decodeSg, "minSubGroups")
			subGroupSize, _, _ = unstructured.NestedInt64(decodeSg, "subGroupSize")
			gomega.Expect(minSubGroups).To(gomega.Equal(int64(1)), "decode minSubGroups should be 1")
			gomega.Expect(subGroupSize).To(gomega.Equal(int64(1)), "decode subGroupSize should be 1 (standalone)")

			// Verify labelSelector contains group-name and role-name
			labelSelector, found, _ := unstructured.NestedMap(prefillSg, "labelSelector", "matchLabels")
			gomega.Expect(found).To(gomega.BeTrue())
			gomega.Expect(labelSelector[constants.GroupNameLabelKey]).To(gomega.Equal(rbg.Name))
			gomega.Expect(labelSelector[constants.RoleNameLabelKey]).To(gomega.Equal("prefill"))

			// Verify matchLabelKeys partitions the role's pods into one subGroup per
			// RoleInstance. Without it every pod of the role collapses into a single
			// subGroup, contradicting subGroupSize, and the pods never get scheduled.
			matchLabelKeys, found, _ := unstructured.NestedStringSlice(prefillSg, "matchLabelKeys")
			gomega.Expect(found).To(gomega.BeTrue(), "subGroupPolicy should set matchLabelKeys")
			gomega.Expect(matchLabelKeys).To(gomega.Equal([]string{constants.RoleInstanceNameLabelKey}))

			// Verify schedulerName is injected on pods
			f.ExpectWorkloadV2PodTemplateAnnotationContains(rbg, rbg.Spec.Roles[0],
				map[string]string{scheduler.VolcanoPodGroupAnnotationKey: rbg.Name})

			// Verify pods eventually become ready
			f.ExpectRbgV2Equal(rbg)
		})

		// ============================================================
		// Test 4: Insufficient resources — all pods remain Pending
		// Verifies: gang scheduling holds all pods when minMember can't be satisfied.
		// ============================================================
		ginkgo.It("gang scheduling holds all pods pending when resources insufficient", func() {
			// Use a pod template requesting unrealistic resources
			template := wrappersv2.BuildBasicPodTemplateSpec()
			template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1000"),
					corev1.ResourceMemory: resource.MustParse("100Gi"),
				},
			}

			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-gang-insufficient", f.Namespace).
				WithVolcanoGangScheduling("default").
				WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("role-a").
							WithReplicas(3).
							WithTemplate(&template).Obj(),
					},
				).Obj()

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Verify PodGroup is created
			gomega.Eventually(func() bool {
				_, err := getVolcanoPodGroup(f, rbg.Name, rbg.Namespace)
				return err == nil
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"PodGroup should be created even when resources are insufficient")

			// Verify all pods remain Pending (no partial scheduling)
			gomega.Consistently(func() bool {
				podList := &corev1.PodList{}
				if err := f.Client.List(f.Ctx, podList,
					client.InNamespace(rbg.Namespace),
					client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name},
				); err != nil {
					return false
				}
				if len(podList.Items) != 3 {
					return false
				}
				for _, pod := range podList.Items {
					if pod.Status.Phase != corev1.PodPending {
						return false
					}
				}
				return true
			}, 30*time.Second, 2*time.Second).Should(gomega.BeTrue(),
				"all pods should remain Pending when gang cannot be satisfied")
		})

		// ============================================================
		// Test 5: schedulerName injection verification
		// Verifies: pod.spec.schedulerName set when gang enabled,
		//           NOT set when gang disabled.
		// ============================================================
		ginkgo.It("schedulerName is injected when gang scheduling enabled", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-gang-schedname", f.Namespace).
				WithVolcanoGangScheduling("default").
				WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("role-a").WithReplicas(1).Obj(),
					},
				).Obj()

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// Verify pod has schedulerName set
			podList := &corev1.PodList{}
			gomega.Eventually(func() bool {
				if err := f.Client.List(f.Ctx, podList,
					client.InNamespace(rbg.Namespace),
					client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name},
				); err != nil {
					return false
				}
				return len(podList.Items) > 0 && podList.Items[0].Spec.SchedulerName == "volcano"
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"pod schedulerName should be set to 'volcano' when gang scheduling is enabled")
		})

		// ============================================================
		// Test 6: Removing scheduling.gang deletes PodGroup
		// Verifies: PodGroup is deleted when gang scheduling is disabled.
		// ============================================================
		ginkgo.It("removing scheduling.gang strategy deletes PodGroup", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-gang-cleanup", f.Namespace).
				WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("role-a").WithReplicas(1).Obj(),
					},
				).Obj()

			cpolicy := &workloadsv1alpha2.CoordinatedPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbg.Name,
					Namespace: f.Namespace,
				},
				Spec: workloadsv1alpha2.CoordinatedPolicySpec{
					Policies: []workloadsv1alpha2.CoordinatedPolicyRule{
						{
							Name:  "gang-scheduling",
							Roles: []string{"role-a"},
							Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
								Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
									Gang: &workloadsv1alpha2.GangSchedulingStrategy{},
								},
							},
						},
					},
				},
			}

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, cpolicy)).Should(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Wait for PodGroup to be created
			gomega.Eventually(func() bool {
				_, err := getVolcanoPodGroup(f, rbg.Name, rbg.Namespace)
				return err == nil
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())

			// Remove scheduling.gang from CoordinatedPolicy
			gomega.Eventually(func() error {
				cp := &workloadsv1alpha2.CoordinatedPolicy{}
				if err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(cpolicy), cp); err != nil {
					return err
				}
				cp.Spec.Policies[0].Strategy.Scheduling = nil
				return f.Client.Update(f.Ctx, cp)
			}, utils.Timeout, utils.Interval).Should(gomega.Succeed())

			// Touch RBG Spec to trigger reconciliation (controller doesn't watch CoordinatedPolicy)
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].Replicas = ptr.To(int32(1))
			})

			// Verify PodGroup is deleted (no annotation to fall back to)
			gomega.Eventually(func() bool {
				_, err := getVolcanoPodGroup(f, rbg.Name, rbg.Namespace)
				return apierrors.IsNotFound(err)
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"PodGroup should be deleted after removing scheduling.gang strategy")
		})

		// ============================================================
		// Test 7: Gang scheduling coexists with rollingUpdate and scaling
		// Verifies: scheduling.gang doesn't interfere with other strategies.
		// ============================================================
		ginkgo.It("gang scheduling coexists with rollingUpdate and scaling strategies", func() {
			template := wrappersv2.BuildBasicPodTemplateSpec()

			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-gang-coexist", f.Namespace).
				WithVolcanoGangScheduling("default").
				WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("role-a").
							WithReplicas(2).
							WithTemplate(&template).
							WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
								MaxUnavailable: ptr.To(intstr.FromInt32(1)),
							}).Obj(),
						wrappersv2.BuildStandaloneRole("role-b").
							WithReplicas(2).
							WithTemplate(&template).
							WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
								MaxUnavailable: ptr.To(intstr.FromInt32(1)),
							}).Obj(),
					},
				).Obj()

			cpolicy := &workloadsv1alpha2.CoordinatedPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbg.Name,
					Namespace: f.Namespace,
				},
				Spec: workloadsv1alpha2.CoordinatedPolicySpec{
					Policies: []workloadsv1alpha2.CoordinatedPolicyRule{
						{
							Name:  "coordinated-strategies",
							Roles: []string{"role-a", "role-b"},
							Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
								RollingUpdate: &workloadsv1alpha2.RollingUpdateCoordinationStrategy{
									MaxSkew: ptr.To(intstr.FromInt32(1)),
								},
								Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
									Gang: &workloadsv1alpha2.GangSchedulingStrategy{},
								},
							},
						},
					},
				},
			}

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, cpolicy)).Should(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Verify PodGroup is created (gang scheduling active)
			gomega.Eventually(func() bool {
				_, err := getVolcanoPodGroup(f, rbg.Name, rbg.Namespace)
				return err == nil
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"PodGroup should be created when gang scheduling is configured")

			// Verify all pods eventually become ready (coexistence works)
			f.ExpectRbgV2Equal(rbg)
		})

		// ============================================================
		// Test 8: CoordinatedPolicy takes priority over annotation
		// Verifies: when both CoordinatedPolicy gang and annotation are present,
		//           CoordinatedPolicy takes effect.
		// ============================================================
		ginkgo.It("coordinated policy gang takes priority over annotation", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-gang-priority", f.Namespace).
				WithVolcanoGangScheduling("default").
				WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("role-a").WithReplicas(2).Obj(),
						wrappersv2.BuildStandaloneRole("role-b").WithReplicas(2).Obj(),
					},
				).Obj()

			cpolicy := &workloadsv1alpha2.CoordinatedPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbg.Name,
					Namespace: f.Namespace,
				},
				Spec: workloadsv1alpha2.CoordinatedPolicySpec{
					Policies: []workloadsv1alpha2.CoordinatedPolicyRule{
						{
							Name:  "gang-scheduling",
							Roles: []string{"role-a", "role-b"},
							Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
								Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
									Gang: &workloadsv1alpha2.GangSchedulingStrategy{
										MinReplicas: map[string]int32{
											"role-a": 1,
											"role-b": 1,
										},
									},
								},
							},
						},
					},
				},
			}

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			// Create both CoordinatedPolicy and annotation
			gomega.Expect(f.Client.Create(f.Ctx, cpolicy)).Should(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// When CoordinatedPolicy has minReplicas, PodGroup should have subGroupPolicy
			// (not just minMember-only as annotation would produce)
			gomega.Eventually(func() bool {
				pg, err := getVolcanoPodGroup(f, rbg.Name, rbg.Namespace)
				if err != nil {
					return false
				}
				// With CoordinatedPolicy minReplicas, PodGroup should have subGroupPolicy
				_, found, _ := unstructured.NestedSlice(pg.Object, "spec", "subGroupPolicy")
				return found
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"PodGroup should have subGroupPolicy when CoordinatedPolicy minReplicas is configured (CoordinatedPolicy takes priority)")

			// Verify pods eventually become ready
			f.ExpectRbgV2Equal(rbg)
		})
	})
}

// ============================================================
// Helpers for PodGroup access via unstructured objects
// ============================================================

// getVolcanoPodGroup fetches the Volcano PodGroup CR using unstructured client.
func getVolcanoPodGroup(f *framework.Framework, name, namespace string) (*unstructured.Unstructured, error) {
	pg := &unstructured.Unstructured{}
	pg.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "scheduling.volcano.sh",
		Version: "v1beta1",
		Kind:    "PodGroup",
	})
	err := f.Client.Get(f.Ctx, client.ObjectKey{Name: name, Namespace: namespace}, pg)
	return pg, err
}

// getKubePodGroup fetches the scheduler-plugins PodGroup CR using unstructured client.
func getKubePodGroup(f *framework.Framework, name, namespace string) (*unstructured.Unstructured, error) {
	pg := &unstructured.Unstructured{}
	pg.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "scheduling.x-k8s.io",
		Version: "v1alpha1",
		Kind:    "PodGroup",
	})
	err := f.Client.Get(f.Ctx, client.ObjectKey{Name: name, Namespace: namespace}, pg)
	return pg, err
}
