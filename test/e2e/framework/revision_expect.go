package framework

import (
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	pkgutils "sigs.k8s.io/rbgs/pkg/utils"
	"sigs.k8s.io/rbgs/test/utils"
)

func (f *Framework) ExpectRBGRevisionEqual(rbg *v1alpha1.RoleBasedGroup) {
	logger := log.FromContext(f.Ctx).WithValues("rbg", rbg.Name)
	gomega.Eventually(
		func() bool {
			selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
				MatchLabels: map[string]string{
					v1alpha1.SetNameLabelKey: rbg.Name,
				},
			})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			revisions, err := pkgutils.ListRevisions(f.Ctx, f.Client, rbg, selector)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			if len(revisions) == 0 {
				logger.Info("no revision found")
				return false
			}

			current := pkgutils.GetHighestRevision(revisions)
			gomega.Expect(current).ToNot(gomega.BeNil())

			expect, err := pkgutils.NewRevision(f.Ctx, f.Client, rbg, nil)
			if err != nil {
				logger.Error(err, "failed to get expect revision")
				return false
			}
			return current.Labels[v1alpha1.RevisionLabelKey] == expect.Labels[v1alpha1.RevisionLabelKey] &&
				pkgutils.EqualRevision(current, expect)
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

func (f *Framework) ExpectRevisionDeleted(rbg *v1alpha1.RoleBasedGroup) {
	gomega.Eventually(
		func() bool {
			selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
				MatchLabels: map[string]string{
					v1alpha1.SetNameLabelKey: rbg.Name,
				},
			})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			revisions, err := pkgutils.ListRevisions(f.Ctx, f.Client, rbg, selector)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			return len(revisions) == 0
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}
