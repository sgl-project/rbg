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

// schedulerPluginsProfileName is the kube-scheduler profile the upstream
// scheduler-plugins chart installs, and therefore what the e2e job passes to
// --scheduler-profile-name. Pods must land on it for coscheduling to see them.
const schedulerPluginsProfileName = "scheduler-plugins-scheduler"

// gangBackend describes one gang scheduling implementation. Every backend needs its
// own cluster (a different scheduler) and its own controller flags, so each is behind
// a ginkgo label and gets its own e2e job.
type gangBackend struct {
	// label is the ginkgo label gating the specs, and the Makefile filter selecting them.
	label string
	// suffix keeps object names distinct so both backends can share a namespace.
	suffix string
	// podGroupGVK is the PodGroup CR this backend reconciles.
	podGroupGVK schema.GroupVersionKind
	// schedulerName is the value expected on pod.spec.schedulerName.
	schedulerName string
	// perRoleMinimums reports whether the backend supports gang minReplicas.
	perRoleMinimums bool
	// enableAnnotation turns on gang scheduling through the annotation path.
	enableAnnotation func(*wrappersv2.RoleBasedGroupWrapper) *wrappersv2.RoleBasedGroupWrapper
	// expectPodMarker asserts the pod template carries whatever ties pods to the PodGroup.
	expectPodMarker func(*framework.Framework, *workloadsv1alpha2.RoleBasedGroup, workloadsv1alpha2.RoleSpec)
}

func gangBackends() []gangBackend {
	return []gangBackend{
		{
			label:  "volcano",
			suffix: "vc",
			podGroupGVK: schema.GroupVersionKind{
				Group: "scheduling.volcano.sh", Version: "v1beta1", Kind: "PodGroup",
			},
			schedulerName:   "volcano",
			perRoleMinimums: true,
			enableAnnotation: func(w *wrappersv2.RoleBasedGroupWrapper) *wrappersv2.RoleBasedGroupWrapper {
				return w.WithVolcanoGangScheduling("default")
			},
			expectPodMarker: func(
				f *framework.Framework,
				rbg *workloadsv1alpha2.RoleBasedGroup,
				role workloadsv1alpha2.RoleSpec,
			) {
				f.ExpectWorkloadV2PodTemplateAnnotationContains(rbg, role,
					map[string]string{scheduler.VolcanoPodGroupAnnotationKey: rbg.Name})
			},
		},
		{
			label:  "scheduler-plugins",
			suffix: "sp",
			podGroupGVK: schema.GroupVersionKind{
				Group: "scheduling.x-k8s.io", Version: "v1alpha1", Kind: "PodGroup",
			},
			schedulerName:   schedulerPluginsProfileName,
			perRoleMinimums: false,
			enableAnnotation: func(w *wrappersv2.RoleBasedGroupWrapper) *wrappersv2.RoleBasedGroupWrapper {
				return w.WithGangScheduling()
			},
			expectPodMarker: func(
				f *framework.Framework,
				rbg *workloadsv1alpha2.RoleBasedGroup,
				role workloadsv1alpha2.RoleSpec,
			) {
				// Upstream coscheduling reads only the second key; Koordinator/ACK
				// reads only the first. Both must be present.
				f.ExpectWorkloadV2PodTemplateLabelContains(rbg, role, map[string]string{
					scheduler.KubePodGroupLabelKey:         rbg.Name,
					scheduler.KubePodGroupUpstreamLabelKey: rbg.Name,
				})
			},
		},
	}
}

func RunGangSchedulingTestCases(f *framework.Framework) {
	for _, backend := range gangBackends() {
		runGangSchedulingBackend(f, backend)
	}
}

// runGangSchedulingBackend registers the gang specs for one backend. Each Describe is
// labelled so it only runs in the e2e job that installs the matching scheduler and
// deploys the controller with the matching --scheduler-name.
func runGangSchedulingBackend(f *framework.Framework, b gangBackend) {
	name := func(base string) string { return fmt.Sprintf("%s-%s", base, b.suffix) }

	ginkgo.Describe("gang scheduling ["+b.label+"]", ginkgo.Label(b.label), func() {

		// ============================================================
		// CoordinatedPolicy gang:{} (basic gang, no minReplicas)
		// Verifies: PodGroup created with minMember = GetGroupSize(),
		//           pods carry the PodGroup marker, pods eventually ready.
		// ============================================================
		ginkgo.It("coordinated policy gang:{} enables basic gang scheduling", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup(name("e2e-gang-basic"), f.Namespace).
				WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("prefill").WithReplicas(2).Obj(),
						wrappersv2.BuildStandaloneRole("decode").WithReplicas(2).Obj(),
					},
				).Obj()

			cpolicy := gangPolicy(rbg.Name, f.Namespace, []string{"prefill", "decode"}, nil)

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, cpolicy)).Should(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Verify PodGroup is created with minMember = GetGroupSize()
			// GetGroupSize() for 2 standalone roles × 2 replicas each = 4
			gomega.Eventually(func() bool {
				pg, err := getPodGroup(f, b.podGroupGVK, rbg.Name, rbg.Namespace)
				if err != nil {
					return false
				}
				minMember, _, _ := unstructured.NestedInt64(pg.Object, "spec", "minMember")
				return minMember == int64(rbg.GetGroupSize())
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"PodGroup minMember should equal GetGroupSize()")

			b.expectPodMarker(f, rbg, rbg.Spec.Roles[0])

			// Verify pods eventually become ready
			f.ExpectRbgV2Equal(rbg)
		})

		// ============================================================
		// Annotation compatibility path (no CoordinatedPolicy)
		// Verifies: annotation group-gang-scheduling=true enables basic gang,
		//           PodGroup created, pods carry the PodGroup marker.
		// ============================================================
		ginkgo.It("annotation compatibility enables basic gang scheduling", func() {
			rbg := b.enableAnnotation(
				wrappersv2.BuildBasicRoleBasedGroup(name("e2e-gang-anno"), f.Namespace),
			).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-a").WithReplicas(1).Obj(),
					wrappersv2.BuildStandaloneRole("role-b").WithReplicas(1).Obj(),
				},
			).Obj()

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Verify PodGroup is created (annotation-driven)
			gomega.Eventually(func() bool {
				_, err := getPodGroup(f, b.podGroupGVK, rbg.Name, rbg.Namespace)
				return err == nil
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"PodGroup should be created when annotation is set")

			b.expectPodMarker(f, rbg, rbg.Spec.Roles[0])

			// Verify pods eventually become ready
			f.ExpectRbgV2Equal(rbg)
		})

		// ============================================================
		// Gang actually gates placement.
		// This is the only spec that fails when the PodGroup exists but the
		// scheduler never associates the pods with it (wrong label key, wrong
		// schedulerName): a group one pod larger than the cluster can place must
		// leave *every* pod Pending, not len(nodes) of them.
		// ============================================================
		ginkgo.It("gang gates placement when the whole group cannot fit", func() {
			nodes := &corev1.NodeList{}
			gomega.Expect(f.Client.List(f.Ctx, nodes)).Should(gomega.Succeed())
			gomega.Expect(nodes.Items).ToNot(gomega.BeEmpty())
			replicas := int32(len(nodes.Items) + 1)

			// Control group: same shape, gang disabled. It proves the cluster really
			// can place some of these pods, so the gang assertion below is meaningful
			// rather than passing because nothing was schedulable in the first place.
			control := wrappersv2.BuildBasicRoleBasedGroup(name("e2e-gang-control"), f.Namespace).
				WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("role-a").
							WithReplicas(replicas).
							WithTemplate(ptr.To(oneReplicaPerNodeTemplate(name("e2e-gang-control")))).Obj(),
					},
				).Obj()

			rbg := b.enableAnnotation(
				wrappersv2.BuildBasicRoleBasedGroup(name("e2e-gang-gated"), f.Namespace),
			).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-a").
						WithReplicas(replicas).
						WithTemplate(ptr.To(oneReplicaPerNodeTemplate(name("e2e-gang-gated")))).Obj(),
				},
			).Obj()

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, control) })
			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, control)).Should(gomega.Succeed())
			gomega.Eventually(func() int {
				return countScheduledPods(f, control.Name)
			}, utils.Timeout, utils.Interval).Should(gomega.BeNumerically(">", 0),
				"without gang scheduling at least one pod of an oversized group must be placed")

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			gomega.Eventually(func() bool {
				_, err := getPodGroup(f, b.podGroupGVK, rbg.Name, rbg.Namespace)
				return err == nil
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(), "PodGroup should be created")

			gomega.Eventually(func() int32 {
				return countPods(f, rbg.Name)
			}, utils.Timeout, utils.Interval).Should(gomega.Equal(replicas))

			gomega.Consistently(func() int {
				return countScheduledPods(f, rbg.Name)
			}, 45*time.Second, 3*time.Second).Should(gomega.BeZero(),
				"gang scheduling must hold every pod back while the group cannot fit")
		})

		// ============================================================
		// schedulerName injection
		// Verifies: pod.spec.schedulerName points at the gang scheduler,
		//           otherwise the default scheduler ignores the PodGroup.
		// ============================================================
		ginkgo.It("schedulerName is injected when gang scheduling enabled", func() {
			rbg := b.enableAnnotation(
				wrappersv2.BuildBasicRoleBasedGroup(name("e2e-gang-schedname"), f.Namespace),
			).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-a").WithReplicas(1).Obj(),
				},
			).Obj()

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			podList := &corev1.PodList{}
			gomega.Eventually(func() bool {
				if err := f.Client.List(f.Ctx, podList,
					client.InNamespace(rbg.Namespace),
					client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name},
				); err != nil {
					return false
				}
				return len(podList.Items) > 0 && podList.Items[0].Spec.SchedulerName == b.schedulerName
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"pod schedulerName should be set to %q when gang scheduling is enabled", b.schedulerName)
		})

		// ============================================================
		// Removing scheduling.gang deletes the PodGroup
		// ============================================================
		ginkgo.It("removing scheduling.gang strategy deletes PodGroup", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup(name("e2e-gang-cleanup"), f.Namespace).
				WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("role-a").WithReplicas(1).Obj(),
					},
				).Obj()

			cpolicy := gangPolicy(rbg.Name, f.Namespace, []string{"role-a"}, nil)

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, cpolicy)).Should(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Wait for PodGroup to be created
			gomega.Eventually(func() bool {
				_, err := getPodGroup(f, b.podGroupGVK, rbg.Name, rbg.Namespace)
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

			// No RBG update here on purpose: the controller watches CoordinatedPolicy,
			// so editing the policy alone must trigger the reconcile.
			// Verify PodGroup is deleted (no annotation to fall back to)
			gomega.Eventually(func() bool {
				_, err := getPodGroup(f, b.podGroupGVK, rbg.Name, rbg.Namespace)
				return apierrors.IsNotFound(err)
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"PodGroup should be deleted after removing scheduling.gang strategy")
		})

		// ============================================================
		// Gang scheduling coexists with rollingUpdate and scaling
		// ============================================================
		ginkgo.It("gang scheduling coexists with rollingUpdate and scaling strategies", func() {
			template := wrappersv2.BuildBasicPodTemplateSpec()

			rbg := b.enableAnnotation(
				wrappersv2.BuildBasicRoleBasedGroup(name("e2e-gang-coexist"), f.Namespace),
			).WithRoles(
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

			cpolicy := gangPolicy(rbg.Name, f.Namespace, []string{"role-a", "role-b"}, nil)
			cpolicy.Spec.Policies[0].Name = "coordinated-strategies"
			cpolicy.Spec.Policies[0].Strategy.RollingUpdate = &workloadsv1alpha2.RollingUpdateCoordinationStrategy{
				MaxSkew: ptr.To(intstr.FromInt32(1)),
			}
			cpolicy.Spec.Policies[0].Strategy.Scaling = &workloadsv1alpha2.ScalingCoordinationStrategy{
				MaxSkew: ptr.To(intstr.FromInt32(1)),
			}

			ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, cpolicy)).Should(gomega.Succeed())
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

			// Verify PodGroup is created (gang scheduling active)
			gomega.Eventually(func() bool {
				_, err := getPodGroup(f, b.podGroupGVK, rbg.Name, rbg.Namespace)
				return err == nil
			}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
				"PodGroup should be created when gang scheduling is configured")

			// Verify all pods eventually become ready (coexistence works)
			f.ExpectRbgV2Equal(rbg)
		})

		if b.perRoleMinimums {
			// ============================================================
			// minReplicas creates a PodGroup with subGroupPolicy
			// Verifies: subGroupPolicy fields (name, minSubGroups, subGroupSize),
			//           label selector and matchLabelKeys, pods eventually ready.
			// ============================================================
			ginkgo.It("gang scheduling with minReplicas creates subGroupPolicy", func() {
				rbg := wrappersv2.BuildBasicRoleBasedGroup(name("e2e-gang-minreplicas"), f.Namespace).
					WithRoles(
						[]workloadsv1alpha2.RoleSpec{
							wrappersv2.BuildStandaloneRole("prefill").WithReplicas(3).Obj(),
							wrappersv2.BuildStandaloneRole("decode").WithReplicas(3).Obj(),
						},
					).Obj()

				cpolicy := gangPolicy(rbg.Name, f.Namespace, []string{"prefill", "decode"},
					map[string]int32{"prefill": 2, "decode": 1})

				ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

				gomega.Expect(f.Client.Create(f.Ctx, cpolicy)).Should(gomega.Succeed())
				gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

				// Verify PodGroup is created with subGroupPolicy
				gomega.Eventually(func() bool {
					pg, err := getPodGroup(f, b.podGroupGVK, rbg.Name, rbg.Namespace)
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
				pg, err := getPodGroup(f, b.podGroupGVK, rbg.Name, rbg.Namespace)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				sgp, _, _ := unstructured.NestedSlice(pg.Object, "spec", "subGroupPolicy")

				// Build a map from subGroupPolicy entries for easy lookup
				sgMap := make(map[string]map[string]interface{})
				for _, entry := range sgp {
					if m, ok := entry.(map[string]interface{}); ok {
						sgName, _, _ := unstructured.NestedString(m, "name")
						sgMap[sgName] = m
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

				b.expectPodMarker(f, rbg, rbg.Spec.Roles[0])

				// Verify pods eventually become ready
				f.ExpectRbgV2Equal(rbg)
			})

			// ============================================================
			// CoordinatedPolicy takes priority over the annotation
			// Verifies: with both present, the policy's minReplicas wins, which the
			//           annotation path alone can never produce.
			// ============================================================
			ginkgo.It("coordinated policy gang takes priority over annotation", func() {
				rbg := b.enableAnnotation(
					wrappersv2.BuildBasicRoleBasedGroup(name("e2e-gang-priority"), f.Namespace),
				).WithRoles(
					[]workloadsv1alpha2.RoleSpec{
						wrappersv2.BuildStandaloneRole("role-a").WithReplicas(2).Obj(),
						wrappersv2.BuildStandaloneRole("role-b").WithReplicas(2).Obj(),
					},
				).Obj()

				cpolicy := gangPolicy(rbg.Name, f.Namespace, []string{"role-a", "role-b"},
					map[string]int32{"role-a": 1, "role-b": 1})

				ginkgo.DeferCleanup(func() { dumpDebugInfo(f, rbg) })

				// Create both CoordinatedPolicy and annotation
				gomega.Expect(f.Client.Create(f.Ctx, cpolicy)).Should(gomega.Succeed())
				gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

				// When CoordinatedPolicy has minReplicas, PodGroup should have subGroupPolicy
				// (not just minMember-only as annotation would produce)
				gomega.Eventually(func() bool {
					pg, err := getPodGroup(f, b.podGroupGVK, rbg.Name, rbg.Namespace)
					if err != nil {
						return false
					}
					_, found, _ := unstructured.NestedSlice(pg.Object, "spec", "subGroupPolicy")
					return found
				}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
					"PodGroup should have subGroupPolicy when CoordinatedPolicy minReplicas is configured")

				// Verify pods eventually become ready
				f.ExpectRbgV2Equal(rbg)
			})
		} else {
			// ============================================================
			// minReplicas is rejected at admission on a backend that cannot honour it.
			// Without this the policy is admitted and the controller only errors during
			// reconcile, where nobody looks. Also the only e2e coverage of the
			// CoordinatedPolicy validating webhook being registered at all.
			// ============================================================
			ginkgo.It("minReplicas is rejected because the backend cannot honour it", func() {
				cpolicy := gangPolicy(name("e2e-gang-reject"), f.Namespace, []string{"role-a"},
					map[string]int32{"role-a": 1})

				err := f.Client.Create(f.Ctx, cpolicy)
				gomega.Expect(err).To(gomega.HaveOccurred(),
					"a CoordinatedPolicy with gang minReplicas must be rejected")
				gomega.Expect(err.Error()).To(gomega.ContainSubstring("per-role gang minimums"))
			})
		}
	})
}

// gangPolicy builds a CoordinatedPolicy with a single gang scheduling rule.
// minReplicas may be nil for whole-group gang scheduling.
func gangPolicy(
	name, namespace string, roles []string, minReplicas map[string]int32,
) *workloadsv1alpha2.CoordinatedPolicy {
	return &workloadsv1alpha2.CoordinatedPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: workloadsv1alpha2.CoordinatedPolicySpec{
			Policies: []workloadsv1alpha2.CoordinatedPolicyRule{
				{
					Name:  "gang-scheduling",
					Roles: roles,
					Strategy: workloadsv1alpha2.CoordinatedPolicyStrategy{
						Scheduling: &workloadsv1alpha2.SchedulingCoordinationStrategy{
							Gang: &workloadsv1alpha2.GangSchedulingStrategy{MinReplicas: minReplicas},
						},
					},
				},
			},
		},
	}
}

// oneReplicaPerNodeTemplate returns a pod template that refuses to share a node with
// another pod of the same group, so a group of nodeCount+1 replicas can never be fully
// placed no matter how much spare CPU the cluster has.
func oneReplicaPerNodeTemplate(groupName string) corev1.PodTemplateSpec {
	template := wrappersv2.BuildBasicPodTemplateSpec()
	// Volcano's backfill action allocates BestEffort tasks directly, without any gang
	// readiness check, so a pod with no requests escapes the gang. Any non-zero request
	// keeps the task on the allocate path where the gang plugin governs placement.
	template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")},
	}
	template.Spec.Affinity = &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
				{
					TopologyKey: corev1.LabelHostname,
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{constants.GroupNameLabelKey: groupName},
					},
				},
			},
		},
	}
	return template
}

func countPods(f *framework.Framework, groupName string) int32 {
	podList := &corev1.PodList{}
	if err := f.Client.List(f.Ctx, podList,
		client.InNamespace(f.Namespace),
		client.MatchingLabels{constants.GroupNameLabelKey: groupName},
	); err != nil {
		return -1
	}
	return int32(len(podList.Items))
}

func countScheduledPods(f *framework.Framework, groupName string) int {
	podList := &corev1.PodList{}
	if err := f.Client.List(f.Ctx, podList,
		client.InNamespace(f.Namespace),
		client.MatchingLabels{constants.GroupNameLabelKey: groupName},
	); err != nil {
		return -1
	}
	scheduled := 0
	for _, pod := range podList.Items {
		if pod.Spec.NodeName != "" {
			scheduled++
		}
	}
	return scheduled
}

// getPodGroup fetches a PodGroup CR of the given kind using the unstructured client.
func getPodGroup(
	f *framework.Framework, gvk schema.GroupVersionKind, name, namespace string,
) (*unstructured.Unstructured, error) {
	pg := &unstructured.Unstructured{}
	pg.SetGroupVersionKind(gvk)
	err := f.Client.Get(f.Ctx, client.ObjectKey{Name: name, Namespace: namespace}, pg)
	return pg, err
}
