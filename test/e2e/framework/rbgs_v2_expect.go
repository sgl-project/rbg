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

package framework

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/utils"
)

// ExpectRbgSetV2Equal waits until the RBGSet has the expected number of child RBGs.
func (f *Framework) ExpectRbgSetV2Equal(rbgSet *workloadsv1alpha2.RoleBasedGroupSet) {
	logger := log.FromContext(f.Ctx).WithValues("rbgSet", rbgSet.Name)
	newRbgSet := &workloadsv1alpha2.RoleBasedGroupSet{}
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{Name: rbgSet.Name, Namespace: rbgSet.Namespace}, newRbgSet,
			)
			if err != nil {
				if !apierrors.IsNotFound(err) {
					logger.Error(err, "get rbgset v2 error")
				}
				return false
			}

			var rbglist workloadsv1alpha2.RoleBasedGroupList
			selector, _ := labels.Parse(fmt.Sprintf("%s=%s", constants.GroupSetNameLabelKey, newRbgSet.Name))
			err = f.Client.List(
				f.Ctx, &rbglist, client.InNamespace(newRbgSet.Namespace),
				client.MatchingLabelsSelector{Selector: selector},
			)
			if err != nil {
				logger.Error(err, "failed to list child RoleBasedGroups")
			}
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			expected, actual := int(*rbgSet.Spec.Replicas), len(rbglist.Items)
			if expected != actual {
				logger.Info(fmt.Sprintf("rbg v2 instance not equal, expected: %d, got: %d", expected, actual))
			}
			return expected == actual
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

// ExpectRbgSetV2Deleted waits until the RBGSet is deleted.
func (f *Framework) ExpectRbgSetV2Deleted(rbgSet *workloadsv1alpha2.RoleBasedGroupSet) {
	newRbgSet := &workloadsv1alpha2.RoleBasedGroupSet{}
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{Name: rbgSet.Name, Namespace: rbgSet.Namespace}, newRbgSet,
			)
			return apierrors.IsNotFound(err)
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

// ExpectRbgV2AnnotationFromSet checks that child RBGs of the given set have the expected annotations.
func (f *Framework) ExpectRbgV2AnnotationFromSet(
	rbgSet *workloadsv1alpha2.RoleBasedGroupSet,
	anno map[string]string,
) bool {
	logger := log.FromContext(f.Ctx)
	var rbglist workloadsv1alpha2.RoleBasedGroupList

	gomega.Eventually(
		func() bool {
			selector, _ := labels.Parse(fmt.Sprintf("%s=%s", constants.GroupSetNameLabelKey, rbgSet.Name))
			err := f.Client.List(
				f.Ctx, &rbglist, client.InNamespace(rbgSet.Namespace),
				client.MatchingLabelsSelector{Selector: selector},
			)
			if err != nil {
				logger.Error(err, "failed to list child v2 RoleBasedGroups")
			}
			return len(rbglist.Items) > 0
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())

	rbgAnno := rbglist.Items[0].Annotations
	for k, v := range anno {
		rv, found := rbgAnno[k]
		if !found || rv != v {
			return false
		}
	}
	return true
}

// ListRbgSetV2Children returns the child RoleBasedGroups of the given set, sorted by name so
// that repeated polls inside an assertion see a stable ordering.
func (f *Framework) ListRbgSetV2Children(
	rbgSet *workloadsv1alpha2.RoleBasedGroupSet,
) []workloadsv1alpha2.RoleBasedGroup {
	var rbglist workloadsv1alpha2.RoleBasedGroupList
	selector, _ := labels.Parse(fmt.Sprintf("%s=%s", constants.GroupSetNameLabelKey, rbgSet.Name))
	gomega.Expect(f.Client.List(
		f.Ctx, &rbglist, client.InNamespace(rbgSet.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
	)).Should(gomega.Succeed())

	children := rbglist.Items
	sort.Slice(children, func(i, j int) bool { return children[i].Name < children[j].Name })
	return children
}

// CountRbgSetV2ReadyChildren reports how many of the given children are actually serving, which
// means a true Ready condition and no deletionTimestamp. The deletionTimestamp half matters
// because a recreate rollout deletes with foreground propagation, so a group stays visible with
// its Ready condition still true for as long as its Pods take to terminate. Counting such a
// group would let an availability assertion pass on capacity that is already on its way out.
func CountRbgSetV2ReadyChildren(children []workloadsv1alpha2.RoleBasedGroup) int {
	ready := 0
	for i := range children {
		if !children[i].DeletionTimestamp.IsZero() {
			continue
		}
		if meta.IsStatusConditionTrue(
			children[i].Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupReady),
		) {
			ready++
		}
	}
	return ready
}

// SplitRbgSetV2Children splits children into the base groups, ordinals below replicas, and the
// surge groups, ordinals at or above replicas. Surge groups carry no label of their own, so the
// ordinal is the only thing that tells them apart, exactly as in the controller.
func SplitRbgSetV2Children(
	children []workloadsv1alpha2.RoleBasedGroup, replicas int,
) (base, surge []workloadsv1alpha2.RoleBasedGroup) {
	for i := range children {
		if RbgSetV2ChildOrdinal(children[i]) >= replicas {
			surge = append(surge, children[i])
			continue
		}
		base = append(base, children[i])
	}
	return base, surge
}

// RbgSetV2ChildByOrdinal returns the child carrying the given groupset-index, or nil when that
// ordinal does not exist. A nil return is how a test observes a group that has been deleted and
// not yet recreated.
func RbgSetV2ChildByOrdinal(
	children []workloadsv1alpha2.RoleBasedGroup, ordinal int,
) *workloadsv1alpha2.RoleBasedGroup {
	for i := range children {
		if RbgSetV2ChildOrdinal(children[i]) == ordinal {
			return &children[i]
		}
	}
	return nil
}

// RbgSetV2ChildOrdinal parses the groupset-index label. A missing or unparseable label yields
// -1, which keeps such a child out of the surge bucket.
func RbgSetV2ChildOrdinal(rbg workloadsv1alpha2.RoleBasedGroup) int {
	ordinal, err := strconv.Atoi(rbg.Labels[constants.GroupSetIndexLabelKey])
	if err != nil {
		return -1
	}
	return ordinal
}

// RbgSetV2ChildUIDs maps each child name to its UID. A recreate rolling update keeps the
// deterministic name and replaces the UID, so this is what tells a recreated group apart from
// the one it replaced.
func RbgSetV2ChildUIDs(children []workloadsv1alpha2.RoleBasedGroup) map[string]types.UID {
	uids := make(map[string]types.UID, len(children))
	for i := range children {
		uids[children[i].Name] = children[i].UID
	}
	return uids
}

// ExpectRbgSetV2AllReady waits until every expected group exists and is ready, then returns
// their UIDs so a later rollout can be shown to have replaced them.
func (f *Framework) ExpectRbgSetV2AllReady(
	rbgSet *workloadsv1alpha2.RoleBasedGroupSet,
) map[string]types.UID {
	expected := int(*rbgSet.Spec.Replicas)
	var uids map[string]types.UID
	gomega.Eventually(
		func() bool {
			children := f.ListRbgSetV2Children(rbgSet)
			if len(children) != expected || CountRbgSetV2ReadyChildren(children) != expected {
				return false
			}
			uids = RbgSetV2ChildUIDs(children)
			return true
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
	return uids
}

// ExpectRbgSetV2ChildrenStable asserts that the given groups keep their identity and stay ready
// for a sustained window. It is what separates a converged set from one that is still churning,
// so it is used both before a rollout is triggered and after it finishes.
func (f *Framework) ExpectRbgSetV2ChildrenStable(
	rbgSet *workloadsv1alpha2.RoleBasedGroupSet, uids map[string]types.UID,
) {
	gomega.Consistently(
		func() map[string]types.UID {
			children := f.ListRbgSetV2Children(rbgSet)
			if CountRbgSetV2ReadyChildren(children) != len(uids) {
				return nil
			}
			return RbgSetV2ChildUIDs(children)
		}, 15, 2,
	).Should(gomega.Equal(uids))
}
