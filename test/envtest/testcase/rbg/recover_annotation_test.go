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

package rbg

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apps "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/csaupgrade"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/utils"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
	wrappers "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// A controller build that still copied rbg.workloads.x-k8s.io/recover from a role
// onto its RoleInstanceSet leaves the key behind as rbg-owned metadata. These specs
// seed that state and check that the next reconcile removes the key, and nothing
// else, on both writers: server-side apply on the ordinary path and the merge
// patch of the selective-membership path.
var _ = Describe("Recover annotation on a role", func() {
	const (
		timeout    = 30 * time.Second
		interval   = 200 * time.Millisecond
		foreignKey = "example.com/owner"
	)
	var (
		namespace string
		groupKey  client.ObjectKey
		setKey    client.ObjectKey
		hashKey   string
	)

	BeforeEach(func() {
		namespace = fmt.Sprintf("recover-annotation-%d", time.Now().UnixNano())
		testutil.CreateNamespace(namespace)

		role := wrappers.BuildStandaloneRole("worker").WithReplicas(3).
			WithAnnotations(map[string]string{
				constants.RoleInstanceRecoverAnnotationKey: "true",
				"team": "a",
			}).Obj()
		group := wrappers.BuildBasicRoleBasedGroup("group", namespace).
			WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()
		Expect(testutil.K8sClient.Create(testutil.Ctx, group)).To(Succeed())

		groupKey = client.ObjectKeyFromObject(group)
		setKey = client.ObjectKey{Namespace: namespace, Name: group.GetWorkloadName(&role)}
		hashKey = fmt.Sprintf(constants.RoleRevisionLabelKeyFmt, role.Name)
	})

	AfterEach(func() { testutil.DeleteNamespace(namespace) })

	getSet := func(g Gomega) *workloadsv1alpha2.RoleInstanceSet {
		set := &workloadsv1alpha2.RoleInstanceSet{}
		g.Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, set)).To(Succeed())
		return set
	}
	instanceCount := func(g Gomega) int {
		list := &workloadsv1alpha2.RoleInstanceList{}
		g.Expect(testutil.K8sClient.List(testutil.Ctx, list, client.InNamespace(namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: groupKey.Name})).To(Succeed())
		count := 0
		for i := range list.Items {
			if list.Items[i].DeletionTimestamp == nil {
				count++
			}
		}
		return count
	}
	revisionNames := func(g Gomega) []string {
		list := &apps.ControllerRevisionList{}
		g.Expect(testutil.K8sClient.List(testutil.Ctx, list, client.InNamespace(namespace))).To(Succeed())
		names := make([]string, 0, len(list.Items))
		for i := range list.Items {
			names = append(names, list.Items[i].Name)
		}
		sort.Strings(names)
		return names
	}
	updateGroup := func(mutate func(*workloadsv1alpha2.RoleBasedGroup)) {
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			group := &workloadsv1alpha2.RoleBasedGroup{}
			if err := testutil.K8sClient.Get(testutil.Ctx, groupKey, group); err != nil {
				return err
			}
			mutate(group)
			return testutil.K8sClient.Update(testutil.Ctx, group)
		})).To(Succeed())
	}
	// settledRevisions waits until the role is running and its revisions have stopped
	// changing, and returns what must stay as it is from then on.
	settledRevisions := func(instances int) (string, []string) {
		Eventually(func(g Gomega) {
			g.Expect(instanceCount(g)).To(Equal(instances))
			g.Expect(getSet(g).Labels[hashKey]).NotTo(BeEmpty())
		}, timeout, interval).Should(Succeed())
		var names []string
		Eventually(func(g Gomega) {
			names = revisionNames(g)
			g.Expect(names).NotTo(BeEmpty())
		}, timeout, interval).Should(Succeed())
		Consistently(func(g Gomega) { g.Expect(revisionNames(g)).To(Equal(names)) },
			2*time.Second, interval).Should(Succeed())
		return getSet(Default).Labels[hashKey], names
	}
	addForeignAnnotation := func() {
		set := getSet(Default)
		old := set.DeepCopy()
		set.Annotations[foreignKey] = "someone-else"
		Expect(testutil.K8sClient.Patch(testutil.Ctx, set, client.MergeFrom(old),
			client.FieldOwner("someone-else"))).To(Succeed())
	}
	// reconcileGroup makes the controller render the role again without changing it:
	// a metadata annotation on the group is not part of any revision.
	reconcileGroup := func() {
		updateGroup(func(group *workloadsv1alpha2.RoleBasedGroup) {
			if group.Annotations == nil {
				group.Annotations = map[string]string{}
			}
			group.Annotations["example.com/touched"] = time.Now().Format(time.RFC3339Nano)
		})
	}
	expectOnlyRecoverRemoved := func(hash string, revisions []string) {
		Eventually(func(g Gomega) {
			set := getSet(g)
			g.Expect(set.Annotations).NotTo(HaveKey(constants.RoleInstanceRecoverAnnotationKey))
			g.Expect(set.Annotations).To(HaveKeyWithValue(foreignKey, "someone-else"))
			g.Expect(set.Annotations).To(HaveKeyWithValue("team", "a"))
		}, timeout, interval).Should(Succeed())
		Consistently(func(g Gomega) {
			set := getSet(g)
			g.Expect(set.Annotations).NotTo(HaveKey(constants.RoleInstanceRecoverAnnotationKey))
			g.Expect(set.Annotations).To(HaveKeyWithValue(foreignKey, "someone-else"))
			g.Expect(set.Labels[hashKey]).To(Equal(hash))
			g.Expect(revisionNames(g)).To(Equal(revisions))
		}, 3*time.Second, interval).Should(Succeed())
	}

	// appliedAnnotationKeys lists the annotations the rbg field manager owns on the
	// set through server-side apply.
	appliedAnnotationKeys := func(set *workloadsv1alpha2.RoleInstanceSet) []string {
		var keys []string
		for _, entry := range set.ManagedFields {
			if entry.Manager != utils.FieldManager || entry.Operation != metav1.ManagedFieldsOperationApply ||
				entry.Subresource != "" || entry.FieldsV1 == nil {
				continue
			}
			var fields map[string]map[string]map[string]any
			Expect(json.Unmarshal(entry.FieldsV1.Raw, &fields)).To(Succeed())
			for key := range fields["f:metadata"]["f:annotations"] {
				keys = append(keys, strings.TrimPrefix(key, "f:"))
			}
		}
		return keys
	}

	It("is removed from a RoleInstanceSet that carries it through server-side apply", func() {
		hash, revisions := settledRevisions(3)
		Expect(getSet(Default).Annotations).NotTo(HaveKey(constants.RoleInstanceRecoverAnnotationKey))
		addForeignAnnotation()

		// What the earlier build left behind: the key among the annotations that the
		// rbg field manager owns through server-side apply. Writing it as that
		// manager and then running the controller's own Update-to-Apply ownership
		// migration yields exactly that, without touching the spec.
		generation := getSet(Default).Generation
		Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
			set := getSet(Default)
			old := set.DeepCopy()
			set.Annotations[constants.RoleInstanceRecoverAnnotationKey] = "true"
			if err := testutil.K8sClient.Patch(testutil.Ctx, set, client.MergeFrom(old),
				client.FieldOwner(utils.FieldManager)); err != nil {
				return err
			}
			patch, err := csaupgrade.UpgradeManagedFieldsPatch(set, sets.New(utils.FieldManager), utils.FieldManager)
			if err != nil || patch == nil {
				return err
			}
			return testutil.K8sClient.Patch(testutil.Ctx, set, client.RawPatch(types.JSONPatchType, patch))
		})).To(Succeed())
		Expect(appliedAnnotationKeys(getSet(Default))).To(ContainElement(constants.RoleInstanceRecoverAnnotationKey))

		reconcileGroup()

		expectOnlyRecoverRemoved(hash, revisions)
		Expect(getSet(Default).Generation).To(Equal(generation))
	})

	It("is removed from a RoleInstanceSet on the selective-membership path", func() {
		settledRevisions(3)

		// Selective scale-down keeps the role on the conditional merge-patch writer
		// for as long as the exclusion annotation is set.
		updateGroup(func(group *workloadsv1alpha2.RoleBasedGroup) {
			group.Spec.Roles[0].Replicas = ptr.To[int32](2)
			group.Spec.Roles[0].Annotations[constants.ExcludedOrdinalsAnnotationKey] = "1"
		})
		Eventually(func(g Gomega) {
			members, err := getSet(g).CommittedOrdinals()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(members).To(Equal([]int{0, 2}))
		}, timeout, interval).Should(Succeed())
		hash, revisions := settledRevisions(2)
		Expect(getSet(Default).Annotations).NotTo(HaveKey(constants.RoleInstanceRecoverAnnotationKey))
		addForeignAnnotation()

		// This writer owns its metadata through Update entries of the rbg field manager.
		set := getSet(Default)
		old := set.DeepCopy()
		set.Annotations[constants.RoleInstanceRecoverAnnotationKey] = "true"
		Expect(testutil.K8sClient.Patch(testutil.Ctx, set, client.MergeFrom(old),
			client.FieldOwner(utils.FieldManager))).To(Succeed())
		Expect(set.Annotations).To(HaveKeyWithValue(constants.RoleInstanceRecoverAnnotationKey, "true"))

		reconcileGroup()

		expectOnlyRecoverRemoved(hash, revisions)
		members, err := getSet(Default).CommittedOrdinals()
		Expect(err).NotTo(HaveOccurred())
		Expect(members).To(Equal([]int{0, 2}))
	})
})
