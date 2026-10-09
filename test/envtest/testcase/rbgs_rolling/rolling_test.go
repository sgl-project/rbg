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
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
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
		Entry("explicit zero percentages", "0%"),
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
