package framework

import (
	"fmt"

	"github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/constants"
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
