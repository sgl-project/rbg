package framework

import (
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	pkgutils "sigs.k8s.io/rbgs/pkg/utils"
	"sigs.k8s.io/rbgs/test/utils"
)

func (f *Framework) ExpectRBGRevisionEqual(rbg *v1alpha1.RoleBasedGroup) {
	logger := log.FromContext(f.Ctx).WithValues("rbg", rbg.Name)
	gomega.Eventually(
		func() bool {
			selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.GroupNameLabelKey: rbg.Name,
				},
			})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			// Fetch the live object so parent.GetUID() returns the real API-server-assigned UID,
			// not the fake UID set in test wrappers (e.g. "rbg-test-uid").
			liveRbg := &v1alpha1.RoleBasedGroup{}
			if err := f.Client.Get(f.Ctx, client.ObjectKey{Name: rbg.Name, Namespace: rbg.Namespace}, liveRbg); err != nil {
				logger.Error(err, "failed to get live rbg for revision check")
				return false
			}

			revisions, err := pkgutils.ListRevisions(f.Ctx, f.Client, liveRbg, selector)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			if len(revisions) == 0 {
				logger.Info("no revision found")
				return false
			}

			current := pkgutils.GetHighestRevision(revisions)
			gomega.Expect(current).ToNot(gomega.BeNil())

			// NewRevision requires v1alpha2 RBG (storage version); fetch the stored object.
			rbgV2 := &workloadsv1alpha2.RoleBasedGroup{}
			if err := f.Client.Get(f.Ctx, client.ObjectKey{Name: rbg.Name, Namespace: rbg.Namespace}, rbgV2); err != nil {
				logger.Error(err, "failed to get rbg as v1alpha2")
				return false
			}
			expect, err := pkgutils.NewRevision(f.Ctx, f.Client, rbgV2, nil)
			if err != nil {
				logger.Error(err, "failed to get expect revision")
				return false
			}
			return current.Labels[constants.GroupRevisionLabelKey] == expect.Labels[constants.GroupRevisionLabelKey] &&
				pkgutils.EqualRevision(current, expect)
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

func (f *Framework) ExpectRevisionDeleted(rbg *v1alpha1.RoleBasedGroup) {
	gomega.Eventually(
		func() bool {
			selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
				MatchLabels: map[string]string{
					constants.GroupNameLabelKey: rbg.Name,
				},
			})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			// Use a bare metav1.Object with just the name/namespace so that a nil UID
			// matches only unowned revisions — which is fine since we're checking for deletion.
			liveRbg := &v1alpha1.RoleBasedGroup{}
			_ = f.Client.Get(f.Ctx, client.ObjectKey{Name: rbg.Name, Namespace: rbg.Namespace}, liveRbg)

			revisions, err := pkgutils.ListRevisions(f.Ctx, f.Client, liveRbg, selector)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			return len(revisions) == 0
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}
