package framework

import (
	"fmt"

	"github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/utils"
)

// rbg related

func (f *Framework) ExpectRbgEqual(rbg *v1alpha1.RoleBasedGroup) {
	logger := log.FromContext(f.Ctx).WithValues("rbg", rbg.Name)
	newRbg := &v1alpha1.RoleBasedGroup{}
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{
					Name:      rbg.Name,
					Namespace: rbg.Namespace,
				}, newRbg,
			)
			if err != nil {
				if !apierrors.IsNotFound(err) {
					logger.Error(err, "get rbg error")
				}
				return false
			}
			return true
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())

	// check workload exists
	for _, role := range rbg.Spec.Roles {
		wlCheck, err := NewWorkloadEqualChecker(f.Ctx, f.Client, role.Workload.String())
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		gomega.Eventually(
			func() bool {
				err := wlCheck.ExpectWorkloadEqual(rbg, role)
				if err != nil {
					logger.V(1).Info("workload not equal, wait next time", "reason", err.Error())
				}
				return err == nil
			}, utils.Timeout, utils.Interval,
		).Should(gomega.BeTrue())
	}

	// check rbg status ready
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{
					Name:      rbg.Name,
					Namespace: rbg.Namespace,
				}, newRbg,
			)
			if err != nil {
				if !apierrors.IsNotFound(err) {
					logger.Error(err, "get rbg error")
				}
				return false
			}
			ready := meta.IsStatusConditionTrue(newRbg.Status.Conditions, string(v1alpha1.RoleBasedGroupReady))
			if !ready {
				logger.V(1).Info(fmt.Sprintf("rbg not ready, wait next time. status: %+v", newRbg.Status))
			}
			return ready

		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())

}

func (f *Framework) ExpectRbgDeleted(rbg *v1alpha1.RoleBasedGroup) {
	newRbg := &v1alpha1.RoleBasedGroup{}
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{
					Name:      rbg.Name,
					Namespace: rbg.Namespace,
				}, newRbg,
			)

			return apierrors.IsNotFound(err)

		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

func (f *Framework) ExpectRbgCondition(
	rbg *v1alpha1.RoleBasedGroup,
	conditionType v1alpha1.RoleBasedGroupConditionType, conditionStatus metav1.ConditionStatus,
) bool {
	logger := log.FromContext(f.Ctx)

	newRbg := &v1alpha1.RoleBasedGroup{}
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{
					Name:      rbg.Name,
					Namespace: rbg.Namespace,
				}, newRbg,
			)
			if err != nil {
				logger.V(1).Error(err, "get rbg error")
			}
			return err == nil
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())

	for _, condition := range newRbg.Status.Conditions {
		if condition.Type == string(conditionType) && condition.Status == conditionStatus {
			return true
		}

	}
	return false
}

func (f *Framework) ExpectWorkloadLabelContains(
	rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec,
	labels ...map[string]string,
) {
	wlCheck, err := NewWorkloadEqualChecker(f.Ctx, f.Client, role.Workload.String())
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	gomega.Eventually(
		func() bool {
			return wlCheck.ExpectLabelContains(rbg, role, labels...) == nil
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

func (f *Framework) ExpectWorkloadAnnotationContains(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec, annotations ...map[string]string) {
	wlCheck, err := NewWorkloadEqualChecker(f.Ctx, f.Client, role.Workload.String())
	gomega.Expect(err).To(gomega.BeNil())

	gomega.Eventually(func() bool {
		return wlCheck.ExpectAnnotationContains(rbg, role, annotations...) == nil
	}, utils.Timeout, utils.Interval).Should(gomega.BeTrue())
}

func (f *Framework) ExpectWorkloadNotExist(rbg *v1alpha1.RoleBasedGroup, role v1alpha1.RoleSpec) {
	wlCheck, err := NewWorkloadEqualChecker(f.Ctx, f.Client, role.Workload.String())
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	gomega.Eventually(
		func() bool {
			return wlCheck.ExpectWorkloadNotExist(rbg, role) == nil
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}
