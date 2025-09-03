package framework

import (
	"github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/utils"
)

func (f *Framework) ExpectRbgSetEqual(rbgSet *v1alpha1.RoleBasedGroupSet) {
	logger := log.FromContext(f.Ctx).WithValues("rbgSet", rbgSet.Name)
	newRbg := &v1alpha1.RoleBasedGroup{}
	gomega.Eventually(func() bool {
		err := f.Client.Get(f.Ctx, client.ObjectKey{
			Name:      rbgSet.Name,
			Namespace: rbgSet.Namespace,
		}, newRbg)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "get rbgset error")
			}
			return false
		}
		return true
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())

	// check rbg exists
	rbgList := &v1alpha1.RoleBasedGroupList{}
	err := f.Client.List(f.Ctx, rbgList, &client.ListOptions{})
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	// check if the rbg instance number is equal to the rbgset.replicas
	gomega.Expect(*rbgSet.Spec.Replicas).To(gomega.BeEquivalentTo(len(rbgList.Items)))
}

func (f *Framework) ExpectRbgSetDeleted(rbgSet *v1alpha1.RoleBasedGroupSet) {
	newRbg := &v1alpha1.RoleBasedGroup{}
	gomega.Eventually(func() bool {
		err := f.Client.Get(f.Ctx, client.ObjectKey{
			Name:      rbgSet.Name,
			Namespace: rbgSet.Namespace,
		}, newRbg)
		return apierrors.IsNotFound(err)
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

func (f *Framework) ExpectRbgSetCondition(rbgSet *v1alpha1.RoleBasedGroupSet,
	conditionType v1alpha1.RoleBasedGroupConditionType, conditionStatus metav1.ConditionStatus) bool {
	logger := log.FromContext(f.Ctx)

	newRbg := &v1alpha1.RoleBasedGroup{}
	gomega.Eventually(func() bool {
		err := f.Client.Get(f.Ctx, client.ObjectKey{
			Name:      rbgSet.Name,
			Namespace: rbgSet.Namespace,
		}, newRbg)
		if err != nil {
			logger.V(1).Error(err, "get rbgset error")
		}
		return err == nil
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())

	for _, condition := range newRbg.Status.Conditions {
		if condition.Type == string(conditionType) && condition.Status == conditionStatus {
			return true
		}

	}
	return false
}
