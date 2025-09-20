package framework

import (
	"fmt"

	"github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/utils"
)

func (f *Framework) ExpectRbgSetEqual(rbgSet *v1alpha1.RoleBasedGroupSet) {
	logger := log.FromContext(f.Ctx).WithValues("rbgSet", rbgSet.Name)
	newRbgSet := &v1alpha1.RoleBasedGroupSet{}
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{
					Name:      rbgSet.Name,
					Namespace: rbgSet.Namespace,
				}, newRbgSet,
			)
			if err != nil {
				if !apierrors.IsNotFound(err) {
					logger.Error(err, "get rbgset error")
				}
				return false
			}

			// List all child RoleBasedGroup instances associated with this RoleBasedGroupSet.
			var rbglist v1alpha1.RoleBasedGroupList
			selector, _ := labels.Parse(fmt.Sprintf("%s=%s", v1alpha1.SetRBGSetNameLabelKey, newRbgSet.Name))
			err = f.Client.List(
				f.Ctx, &rbglist, client.InNamespace(newRbgSet.Namespace),
				client.MatchingLabelsSelector{Selector: selector},
			)
			if err != nil {
				logger.Error(err, "Failed to list child RoleBasedGroups")
			}
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// check if the rbg instance number is equal to the rbgset.replicas
			expected, actual := int(*rbgSet.Spec.Replicas), len(rbglist.Items)
			if expected != actual {
				logger.Info(fmt.Sprintf("rbg instance not equal, expected: %d, got: %d", expected, actual))
			}
			return expected == actual
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

func (f *Framework) ExpectRbgSetDeleted(rbgSet *v1alpha1.RoleBasedGroupSet) {
	newRbg := &v1alpha1.RoleBasedGroup{}
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{
					Name:      rbgSet.Name,
					Namespace: rbgSet.Namespace,
				}, newRbg,
			)
			return apierrors.IsNotFound(err)
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

func (f *Framework) ExpectRbgSetCondition(
	rbgSet *v1alpha1.RoleBasedGroupSet,
	conditionType v1alpha1.RoleBasedGroupConditionType, conditionStatus metav1.ConditionStatus,
) bool {
	logger := log.FromContext(f.Ctx)

	newRbgSet := &v1alpha1.RoleBasedGroupSet{}
	gomega.Eventually(
		func() error {
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{
					Name:      rbgSet.Name,
					Namespace: rbgSet.Namespace,
				}, newRbgSet,
			)
			if err != nil {
				logger.V(1).Error(err, "get rbgset error")
			}
			return err
		}, utils.Timeout, utils.Interval,
	).ToNot(gomega.HaveOccurred())

	for _, condition := range newRbgSet.Status.Conditions {
		if condition.Type == string(conditionType) && condition.Status == conditionStatus {
			return true
		}
	}
	return false
}

func (f *Framework) ExpectRbgAnnotation(
	rbgSet *v1alpha1.RoleBasedGroupSet,
	anno map[string]string,
) bool {
	logger := log.FromContext(f.Ctx)
	var rbglist v1alpha1.RoleBasedGroupList

	gomega.Eventually(
		func() bool {
			selector, _ := labels.Parse(fmt.Sprintf("%s=%s", v1alpha1.SetRBGSetNameLabelKey, rbgSet.Name))
			err := f.Client.List(
				f.Ctx, &rbglist, client.InNamespace(rbgSet.Namespace),
				client.MatchingLabelsSelector{Selector: selector},
			)
			if err != nil {
				logger.Error(err, "Failed to list child RoleBasedGroups")
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
