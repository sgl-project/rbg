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

package rbgs_rolling

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/inplace/pod/inplaceupdate"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

var _ = Describe("RoleBasedGroupSet rolling update", func() {
	var testNs string

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-rbgs-rolling-%d", time.Now().UnixNano())
		testutil.CreateNamespace(testNs)
	})

	AfterEach(func() {
		testutil.DeleteNamespace(testNs)
	})

	DescribeTable("falls back to maxUnavailable=1 when both budgets resolve to zero",
		func(maxUnavailable string) {
			setName := "rbgs-zero-budget"
			set := buildRollingSet(setName, testNs, 3, 1, 0, 0, 0)
			set.Spec.RolloutStrategy.MaxUnavailable = ptr.To(intstr.FromString(maxUnavailable))
			set.Spec.RolloutStrategy.MaxSurge = ptr.To(intstr.FromString("0%"))
			Expect(testutil.K8sClient.Create(testutil.Ctx, set)).Should(Succeed())
			initial := waitRollout(testNs, setName, rolloutComplete)
			oldRevision := initial.Status.CurrentRevision
			Expect(oldRevision).To(Equal(initial.Status.UpdateRevision))
			Expect(initial.Status.UpdatedReplicas).To(Equal(int32(3)))
			Expect(initial.Status.ExpectedUpdatedReplicas).To(Equal(int32(3)))
			for _, child := range fetchChildren(testNs, setName) {
				Expect(child.Labels[constants.GroupSetRevisionLabelKey]).To(Equal(oldRevision))
			}
			revisions := &appsv1.ControllerRevisionList{}
			Expect(testutil.K8sClient.List(testutil.Ctx, revisions, client.InNamespace(testNs),
				client.MatchingLabels{constants.GroupSetNameLabelKey: setName})).Should(Succeed())
			Expect(revisions.Items).To(HaveLen(1))
			Expect(revisions.Items[0].Name).To(Equal(oldRevision))

			updateSetTemplate(testNs, setName, func(set *workloadsv1alpha2.RoleBasedGroupSet) {
				setTemplateImage(set, updatedImage)
			})
			newRevision := waitUpdateRevision(testNs, setName, oldRevision)

			// Withhold each replacement's Pod readiness to expose the effective delete budget.
			for ordinal := 2; ordinal >= 0; ordinal-- {
				child := waitChildRevision(testNs, setName, ordinal, newRevision)
				Expect(childImage(child)).To(Equal(updatedImage))
				Eventually(func(g Gomega) {
					set := mustFetchSet(testNs, setName)
					g.Expect(set.Status.ReadyReplicas).To(Equal(int32(2)))
					g.Expect(set.Status.UpdatedReplicas).To(Equal(int32(3 - ordinal)))
				}, timeout, interval).Should(Succeed())
				Consistently(func(g Gomega) {
					children := fetchChildren(testNs, setName)
					g.Expect(children).To(HaveLen(3))
					for lower := 0; lower < ordinal; lower++ {
						g.Expect(children).To(HaveKey(lower))
						g.Expect(children[lower].Labels[constants.GroupSetRevisionLabelKey]).To(Equal(oldRevision))
					}
					set := mustFetchSet(testNs, setName)
					g.Expect(set.Status.CurrentRevision).To(Equal(oldRevision))
					g.Expect(set.Status.Replicas).To(Equal(int32(3)))
					g.Expect(set.Status.ReadyReplicas).To(Equal(int32(2)))
					g.Expect(set.Status.UpdatedReplicas).To(Equal(int32(3 - ordinal)))
					g.Expect(set.Spec.RolloutStrategy.MaxUnavailable.String()).To(Equal(maxUnavailable))
					g.Expect(set.Spec.RolloutStrategy.MaxSurge.String()).To(Equal("0%"))
					state, err := rollingStateOf(testNs, setName)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(state).To(Equal(rolloutInProgress))
				}, 3*time.Second, interval).Should(Succeed())
				waitChildReady(testNs, childName(setName, ordinal))
			}

			final := waitRollout(testNs, setName, rolloutComplete)
			Expect(final.Status.CurrentRevision).To(Equal(newRevision))
			Expect(final.Status.UpdatedReadyReplicas).To(Equal(int32(3)))
		},
		// This suite has no webhook: explicit zero exercises persisted-object fallback,
		// not admission acceptance of a newly submitted zero-budget strategy.
		Entry("legacy explicit zero percentages without admission", "0%"),
		Entry("a positive unavailable percentage rounds down to zero", "1%"),
	)

	It("does not reopen a finished rollout when a group is deleted", func() {
		setName := "rbgs-fault"
		Expect(testutil.K8sClient.Create(testutil.Ctx,
			buildRollingSet(setName, testNs, 2, 1, 1, 0, 0))).Should(Succeed())
		revision := waitRollout(testNs, setName, rolloutComplete).Status.CurrentRevision
		deletedUID := deleteChild(testNs, setName, 1)

		Consistently(func() (string, error) {
			return rollingStateOf(testNs, setName)
		}, 5*time.Second, interval).Should(Equal(rolloutComplete))

		rebuilt := waitChildRevision(testNs, setName, 1, revision)
		Expect(rebuilt.UID).NotTo(Equal(deletedUID))
		Eventually(func() int32 {
			return mustFetchSet(testNs, setName).Status.ReadyReplicas
		}, timeout, interval).Should(Equal(int32(1)))
		Consistently(func(g Gomega) {
			set := mustFetchSet(testNs, setName)
			g.Expect(set.Status.ReadyReplicas).To(Equal(int32(1)))
			g.Expect(set.Status.CurrentRevision).To(Equal(revision))
			g.Expect(set.Status.UpdateRevision).To(Equal(revision))
			state, err := rollingStateOf(testNs, setName)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(state).To(Equal(rolloutComplete))
		}, 3*time.Second, interval).Should(Succeed())

		set := waitRollout(testNs, setName, rolloutComplete)
		Expect(set.Status.CurrentRevision).To(Equal(revision))
		Expect(set.Status.UpdateRevision).To(Equal(revision))
	})

	DescribeTable("paces InPlaceUpdate role scaling until downstream workloads are ready", func(pattern constants.InstancePatternType) {
		setName := "rbgs-inplace"
		set := buildRollingSet(setName, testNs, 3, 1, 1, 0, 1)
		set.Spec.RolloutStrategy.Type = workloadsv1alpha2.InPlaceUpdateStrategyType
		for i := range set.Spec.GroupTemplate.Spec.Roles {
			role := &set.Spec.GroupTemplate.Spec.Roles[i]
			role.Annotations = map[string]string{constants.RoleInstancePatternKey: string(pattern)}
			role.StandalonePattern.Template.Spec.TerminationGracePeriodSeconds = ptr.To(int64(0))
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, set)).Should(Succeed())
		initial := waitRollout(testNs, setName, rolloutComplete)
		oldRevision := initial.Status.CurrentRevision
		initialChildren := fetchChildren(testNs, setName)

		updateSetTemplate(testNs, setName, func(set *workloadsv1alpha2.RoleBasedGroupSet) {
			for i := range set.Spec.GroupTemplate.Spec.Roles {
				set.Spec.GroupTemplate.Spec.Roles[i].Replicas = ptr.To(int32(2))
			}
		})
		newRevision := waitUpdateRevision(testNs, setName, oldRevision)
		for ordinal := 2; ordinal >= 1; ordinal-- {
			child := waitChildRevision(testNs, setName, ordinal, newRevision)
			Expect(child.UID).To(Equal(initialChildren[ordinal].UID))
			Consistently(func(g Gomega) {
				children := fetchChildren(testNs, setName)
				g.Expect(children).To(HaveLen(3))
				for lower := 0; lower < ordinal; lower++ {
					g.Expect(children).To(HaveKey(lower))
					g.Expect(children[lower].Labels[constants.GroupSetRevisionLabelKey]).To(Equal(oldRevision))
				}
				for index, initialChild := range initialChildren {
					g.Expect(children[index].UID).To(Equal(initialChild.UID))
				}
				g.Expect(mustFetchSet(testNs, setName).Status.CurrentRevision).To(Equal(oldRevision))
			}, 3*time.Second, interval).Should(Succeed())
			waitChildReady(testNs, child.Name)
		}
		partitioned := waitRollout(testNs, setName, "False=PartitionComplete")
		Expect(partitioned.Status.UpdatedReadyReplicas).To(Equal(int32(2)))
		Expect(partitioned.Status.CurrentRevision).To(Equal(oldRevision))

		updateSetTemplate(testNs, setName, func(set *workloadsv1alpha2.RoleBasedGroupSet) {
			set.Spec.RolloutStrategy.Partition = ptr.To(intstr.FromInt32(0))
		})
		waitChildRevision(testNs, setName, 0, newRevision)
		final := waitRollout(testNs, setName, rolloutComplete)
		Expect(final.Status.CurrentRevision).To(Equal(newRevision))
		Expect(final.Status.UpdatedReadyReplicas).To(Equal(int32(3)))
		for index, child := range fetchChildren(testNs, setName) {
			Expect(child.UID).To(Equal(initialChildren[index].UID))
		}
	},
		Entry("stateful role replica changes", constants.StatefulPattern),
		Entry("stateless role replica changes", constants.StatelessPattern),
	)

	DescribeTable("paces real downstream updates before touching a lower RBG", func(imageOnly bool, pattern constants.InstancePatternType) {
		setName := "rbgs-downstream"
		set := buildRollingSet(setName, testNs, 2, 1, 1, 0, 0)
		set.Spec.RolloutStrategy.Type = workloadsv1alpha2.InPlaceUpdateStrategyType
		role := &set.Spec.GroupTemplate.Spec.Roles[0]
		role.Annotations = map[string]string{constants.RoleInstancePatternKey: string(pattern)}
		role.RolloutStrategy = &workloadsv1alpha2.RolloutStrategy{
			Type: workloadsv1alpha2.RollingUpdateStrategyType,
			RollingUpdate: &workloadsv1alpha2.RollingUpdate{
				Type: workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType,
			},
		}
		role.StandalonePattern.Template.Spec.TerminationGracePeriodSeconds = ptr.To(int64(0))
		Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())
		oldRevision := waitRollout(testNs, setName, rolloutComplete).Status.CurrentRevision
		before := fetchChildren(testNs, setName)
		initial := map[int]downstream{}
		for ordinal, child := range before {
			initial[ordinal] = downstreamOf(Default, child)
			Expect(initial[ordinal].pod.Status.ContainerStatuses).To(HaveLen(1))
			Expect(initial[ordinal].pod.Status.ContainerStatuses[0].ImageID).NotTo(BeEmpty())
			Expect(initial[ordinal].pod.Status.ContainerStatuses[0].ContainerID).NotTo(BeEmpty())
		}

		updatedEnv := []corev1.EnvVar{{Name: "ROLLED", Value: "true"}}
		updateSetTemplate(testNs, setName, func(set *workloadsv1alpha2.RoleBasedGroupSet) {
			if imageOnly {
				setTemplateImage(set, updatedImage)
			} else {
				set.Spec.GroupTemplate.Spec.Roles[0].StandalonePattern.Template.Spec.Containers[0].Env = updatedEnv
			}
		})
		newRevision := waitUpdateRevision(testNs, setName, oldRevision)
		for ordinal := 1; ordinal >= 0; ordinal-- {
			child := waitChildRevision(testNs, setName, ordinal, newRevision)
			old := initial[ordinal]
			// Wait for the real RIS -> RI -> Pod update, not merely an RBG template patch.
			assertBlocked := func(g Gomega) {
				children := fetchChildren(testNs, setName)
				g.Expect(children).To(HaveLen(2))
				for index, original := range before {
					g.Expect(children).To(HaveKey(index))
					g.Expect(children[index].UID).To(Equal(original.UID))
					if index < ordinal {
						g.Expect(children[index].Spec).To(Equal(original.Spec))
						g.Expect(children[index].Generation).To(Equal(original.Generation))
						g.Expect(children[index].Labels[constants.GroupSetRevisionLabelKey]).To(Equal(oldRevision))
					}
				}
				current := downstreamOf(g, children[ordinal])
				g.Expect(current.set.UID).To(Equal(old.set.UID))
				g.Expect(current.set.Spec.UpdateStrategy.Type).To(Equal(workloadsv1alpha2.InPlaceIfPossibleUpdateStrategyType))
				g.Expect(current.instance.UID).To(Equal(old.instance.UID))
				if imageOnly {
					g.Expect(current.pod.UID).To(Equal(old.pod.UID))
					g.Expect(current.pod.Spec.Containers[0].Image).To(Equal(updatedImage))
					g.Expect(current.pod.Status.ContainerStatuses).To(Equal(old.pod.Status.ContainerStatuses))
					g.Expect(current.pod.Annotations).To(HaveKey(constants.InPlaceUpdateStateKey))
					g.Expect(inplaceupdate.DefaultCheckInPlaceUpdateCompleted(current.pod)).To(HaveOccurred())
					g.Expect(podConditionStatus(current.pod, constants.InPlaceUpdateReady)).To(Equal(corev1.ConditionFalse))
				} else {
					// InPlaceIfPossible preserves RI but env cannot be patched into a Pod.
					g.Expect(current.pod.UID).NotTo(Equal(old.pod.UID))
					// The controller also injects component/group identity environment variables.
					g.Expect(current.pod.Spec.Containers[0].Env).To(ContainElements(updatedEnv))
					g.Expect(current.pod.Spec.Containers[0].Image).To(Equal(old.pod.Spec.Containers[0].Image))
				}
				g.Expect(podConditionStatus(current.pod, corev1.PodReady)).NotTo(Equal(corev1.ConditionTrue))
				g.Expect(meta.IsStatusConditionTrue(children[ordinal].Status.Conditions,
					string(workloadsv1alpha2.RoleBasedGroupReady))).To(BeFalse())
				g.Expect(mustFetchSet(testNs, setName).Status.CurrentRevision).To(Equal(oldRevision))
				state, err := rollingStateOf(testNs, setName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(state).To(Equal(rolloutInProgress))
			}
			Eventually(assertBlocked, timeout, interval).Should(Succeed())
			Consistently(assertBlocked, 3*time.Second, interval).Should(Succeed())

			// Only now simulate kubelet observing the new image. Gates remain controller-owned;
			// waitChildReady recomputes PodReady after those gates become true.
			if imageOnly {
				current := downstreamOf(Default, child)
				GinkgoWriter.Printf("Before kubelet completion pattern=%s ordinal=%d RI=%s baselines=%v\n",
					pattern, ordinal, current.instance.Name, current.instance.Status.InPlaceUpdateContainerBaselines)
				Eventually(func() error { return simulateChildKubelet(testNs, child.Name, true) }, timeout, interval).Should(Succeed())
			}
			waitChildReady(testNs, child.Name)
			current := downstreamOf(Default, child)
			if imageOnly {
				status := current.pod.Status.ContainerStatuses[0]
				Expect(status.Image).To(Equal(updatedImage))
				Expect(status.ImageID).NotTo(Equal(old.pod.Status.ContainerStatuses[0].ImageID))
				Expect(status.ContainerID).NotTo(Equal(old.pod.Status.ContainerStatuses[0].ContainerID))
				Expect(status.RestartCount).To(Equal(old.pod.Status.ContainerStatuses[0].RestartCount + 1))
				Expect(inplaceupdate.DefaultCheckInPlaceUpdateCompleted(current.pod)).To(Succeed())
			}
			GinkgoWriter.Printf("UID evidence pattern=%s imageOnly=%t ordinal=%d RBG=%s->%s RIS=%s->%s RI=%s->%s Pod=%s->%s\n",
				pattern, imageOnly, ordinal, before[ordinal].UID, child.UID, old.set.UID, current.set.UID,
				old.instance.UID, current.instance.UID, old.pod.UID, current.pod.UID)
		}
		final := waitRollout(testNs, setName, rolloutComplete)
		Expect(final.Status.CurrentRevision).To(Equal(newRevision))
		Expect(final.Status.UpdatedReadyReplicas).To(Equal(int32(2)))
		for ordinal, child := range fetchChildren(testNs, setName) {
			current := downstreamOf(Default, child)
			Expect(child.UID).To(Equal(before[ordinal].UID))
			Expect(current.set.UID).To(Equal(initial[ordinal].set.UID))
			Expect(current.instance.UID).To(Equal(initial[ordinal].instance.UID))
			if imageOnly {
				Expect(current.pod.UID).To(Equal(initial[ordinal].pod.UID))
			} else {
				Expect(current.pod.UID).NotTo(Equal(initial[ordinal].pod.UID))
			}
		}
	},
		Entry("stateful env rolls Pods while retaining RI", false, constants.StatefulPattern),
		Entry("stateless env rolls Pods while retaining RI", false, constants.StatelessPattern),
		Entry("stateful image-only updates RI and Pod in place", true, constants.StatefulPattern),
		Entry("stateless image-only updates RI and Pod in place", true, constants.StatelessPattern),
	)

	It("defaults an empty rollout strategy through the CRD without a webhook", func() {
		set := buildSet("rbgs-default", testNs, 1, 0, &workloadsv1alpha2.GroupSetRolloutStrategy{})
		Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())
		stored := mustFetchSet(testNs, set.Name)
		Expect(stored.Spec.RolloutStrategy.Type).To(Equal(workloadsv1alpha2.InPlaceUpdateStrategyType))
		Expect(stored.Spec.RolloutStrategy.MaxUnavailable).To(Equal(ptr.To(intstr.FromInt32(1))))
		Expect(stored.Spec.RolloutStrategy.MaxSurge).To(Equal(ptr.To(intstr.FromInt32(0))))
		Expect(stored.Spec.RolloutStrategy.Partition).To(Equal(ptr.To(intstr.FromInt32(0))))
		final := waitRollout(testNs, set.Name, rolloutComplete)
		Expect(meta.FindStatusCondition(final.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupSetRolling)).Status).
			To(Equal(metav1.ConditionFalse))
	})

	It("updates metadata in place without relying on a child generation change", func() {
		setName := "rbgs-inplace-metadata"
		set := buildRollingSet(setName, testNs, 2, 0, 1, 0, 0)
		set.Spec.RolloutStrategy.Type = workloadsv1alpha2.InPlaceUpdateStrategyType
		set.Spec.GroupTemplate.Labels = map[string]string{"remove": "old", "version": "old"}
		set.Spec.GroupTemplate.Annotations = map[string]string{"remove": "old", "version": "old"}
		Expect(testutil.K8sClient.Create(testutil.Ctx, set)).Should(Succeed())
		oldRevision := waitRollout(testNs, setName, rolloutComplete).Status.CurrentRevision
		before := fetchChildren(testNs, setName)
		updateSetTemplate(testNs, setName, func(set *workloadsv1alpha2.RoleBasedGroupSet) {
			set.Spec.GroupTemplate.Labels = map[string]string{"add": "new", "version": "new"}
			set.Spec.GroupTemplate.Annotations = map[string]string{"add": "new", "version": "new"}
		})
		newRevision := waitUpdateRevision(testNs, setName, oldRevision)
		final := waitRollout(testNs, setName, rolloutComplete)
		Expect(final.Status.CurrentRevision).To(Equal(newRevision))
		for index, child := range fetchChildren(testNs, setName) {
			Expect(child.UID).To(Equal(before[index].UID))
			Expect(child.Generation).To(Equal(before[index].Generation))
			Expect(child.Labels[constants.GroupSetRevisionLabelKey]).To(Equal(newRevision))
			for _, metadata := range []map[string]string{child.Labels, child.Annotations} {
				Expect(metadata).To(HaveKeyWithValue("add", "new"))
				Expect(metadata).To(HaveKeyWithValue("version", "new"))
				Expect(metadata).NotTo(HaveKey("remove"))
			}
		}
	})

	It("keeps the legacy in-place path for a set without a rollout strategy", func() {
		setName := "rbgs-legacy"
		Expect(testutil.K8sClient.Create(testutil.Ctx, buildSet(setName, testNs, 2, 0, nil))).Should(Succeed())

		uids := map[int]types.UID{}
		Eventually(func() error {
			children := fetchChildren(testNs, setName)
			if len(children) != 2 {
				return fmt.Errorf("%d children exist, want 2", len(children))
			}
			for ordinal, child := range children {
				if _, ok := child.Labels[constants.GroupSetRevisionLabelKey]; ok {
					return fmt.Errorf("ordinal %d carries a revision label without a rollout strategy", ordinal)
				}
				uids[ordinal] = child.UID
			}
			return nil
		}, timeout, interval).Should(Succeed())

		set := mustFetchSet(testNs, setName)
		Expect(set.Status.CurrentRevision).To(BeEmpty())
		Expect(set.Status.UpdateRevision).To(BeEmpty())
		Expect(meta.FindStatusCondition(set.Status.Conditions,
			string(workloadsv1alpha2.RoleBasedGroupSetRolling))).To(BeNil())

		updateSetTemplate(testNs, setName, func(set *workloadsv1alpha2.RoleBasedGroupSet) {
			set.Spec.GroupTemplate.Spec.RoleTemplates = []workloadsv1alpha2.RoleTemplate{{
				Name:     "shared",
				Template: wrappersv2.BuildBasicPodTemplateSpec(),
			}}
		})

		Eventually(func() error {
			children := fetchChildren(testNs, setName)
			if len(children) != 2 {
				return fmt.Errorf("%d children exist, want 2", len(children))
			}
			for ordinal, child := range children {
				if child.UID != uids[ordinal] {
					return fmt.Errorf("ordinal %d was recreated", ordinal)
				}
				if len(child.Spec.RoleTemplates) != 1 || child.Spec.RoleTemplates[0].Name != "shared" {
					return fmt.Errorf("ordinal %d has roleTemplates %v, want the shared template",
						ordinal, child.Spec.RoleTemplates)
				}
			}
			return nil
		}, timeout, interval).Should(Succeed())
	})
})
