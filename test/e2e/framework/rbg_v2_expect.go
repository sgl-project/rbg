package framework

import (
	"fmt"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/constants"
	"sigs.k8s.io/rbgs/test/e2e/framework/workloads"
	"sigs.k8s.io/rbgs/test/utils"
)

// ExpectRbgV2Equal waits until all roles' workloads are ready and RBG condition is Ready.
func (f *Framework) ExpectRbgV2Equal(rbg *workloadsv1alpha2.RoleBasedGroup) {
	logger := log.FromContext(f.Ctx).WithValues("rbg", rbg.Name)
	newRbg := &workloadsv1alpha2.RoleBasedGroup{}
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{Name: rbg.Name, Namespace: rbg.Namespace}, newRbg,
			)
			if err != nil {
				if !apierrors.IsNotFound(err) {
					logger.Error(err, "get rbg v2 error")
				}
				return false
			}
			return true
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())

	// Check each role's workload
	for _, role := range rbg.Spec.Roles {
		wlType := workloadTypeFromRoleV2(role)
		wlCheck, err := workloads.NewWorkloadEqualCheckerV2(f.Ctx, f.Client, wlType)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		gomega.Eventually(
			func() bool {
				err := wlCheck.ExpectWorkloadEqualV2(rbg, role)
				if err != nil {
					logger.V(1).Info("v2 workload not equal, wait next time", "reason", err.Error())
				}
				return err == nil
			}, utils.Timeout, utils.Interval,
		).Should(gomega.BeTrue())
	}

	// Check RBG status ready
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{Name: rbg.Name, Namespace: rbg.Namespace}, newRbg,
			)
			if err != nil {
				return false
			}
			ready := meta.IsStatusConditionTrue(newRbg.Status.Conditions, string(workloadsv1alpha2.RoleBasedGroupReady))
			if !ready {
				logger.V(1).Info(fmt.Sprintf("rbg v2 not ready, status: %+v", newRbg.Status))
			}
			return ready
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

// ExpectRbgV2Deleted waits until the RBG and all its Pods are deleted.
func (f *Framework) ExpectRbgV2Deleted(rbg *workloadsv1alpha2.RoleBasedGroup) {
	logger := log.FromContext(f.Ctx).WithValues("rbg", rbg.Name)

	newRbg := &workloadsv1alpha2.RoleBasedGroup{}
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{Name: rbg.Name, Namespace: rbg.Namespace}, newRbg,
			)
			return apierrors.IsNotFound(err)
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())

	gomega.Eventually(
		func() bool {
			podList := &corev1.PodList{}
			err := f.Client.List(
				f.Ctx, podList,
				client.InNamespace(rbg.Namespace),
				client.MatchingLabels{constants.GroupNameLabelKey: rbg.Name},
			)
			if err != nil {
				logger.Error(err, "failed to list pods")
				return false
			}
			if len(podList.Items) > 0 {
				logger.V(1).Info("waiting for pods to be deleted", "remainingPods", len(podList.Items))
				return false
			}
			return true
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

// ExpectRbgV2Condition checks that the RBG has the specified condition with the specified status.
func (f *Framework) ExpectRbgV2Condition(
	rbg *workloadsv1alpha2.RoleBasedGroup,
	conditionType workloadsv1alpha2.RoleBasedGroupConditionType,
	conditionStatus metav1.ConditionStatus,
) {
	logger := log.FromContext(f.Ctx)
	newRbg := &workloadsv1alpha2.RoleBasedGroup{}
	gomega.Eventually(
		func() bool {
			err := f.Client.Get(
				f.Ctx, client.ObjectKey{Name: rbg.Name, Namespace: rbg.Namespace}, newRbg,
			)
			if err != nil {
				logger.V(1).Error(err, "get rbg v2 error")
				return false
			}
			for _, condition := range newRbg.Status.Conditions {
				if condition.Type == string(conditionType) && condition.Status == conditionStatus {
					return true
				}
			}
			return false
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

// ExpectWorkloadV2NotExist waits until the workload for the given role does not exist.
func (f *Framework) ExpectWorkloadV2NotExist(rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec) {
	wlType := workloadTypeFromRoleV2(role)
	wlCheck, err := workloads.NewWorkloadEqualCheckerV2(f.Ctx, f.Client, wlType)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	gomega.Eventually(
		func() bool {
			return wlCheck.ExpectWorkloadNotExistV2(rbg, role) == nil
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

// ExpectWorkloadV2PodTemplateLabelContains checks that the workload's pod template has the given labels.
func (f *Framework) ExpectWorkloadV2PodTemplateLabelContains(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec,
	labels ...map[string]string,
) {
	wlType := workloadTypeFromRoleV2(role)
	wlCheck, err := workloads.NewWorkloadEqualCheckerV2(f.Ctx, f.Client, wlType)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	gomega.Eventually(
		func() bool {
			return wlCheck.ExpectPodTemplateLabelContainsV2(rbg, role, labels...) == nil
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

// ExpectWorkloadV2ExclusiveTopology checks that the workload has the correct exclusive topology affinity.
func (f *Framework) ExpectWorkloadV2ExclusiveTopology(
	rbg *workloadsv1alpha2.RoleBasedGroup, role workloadsv1alpha2.RoleSpec, topologyKey string,
) {
	wlType := workloadTypeFromRoleV2(role)
	wlCheck, err := workloads.NewWorkloadEqualCheckerV2(f.Ctx, f.Client, wlType)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	err = wlCheck.ExpectTopologyAffinityV2(rbg, role, topologyKey)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
}

// workloadTypeFromRoleV2 maps a v1alpha2 RoleSpec to the corresponding workload type string.
func workloadTypeFromRoleV2(role workloadsv1alpha2.RoleSpec) string {
	if role.IsLeaderWorkerPattern() {
		return constants.LeaderWorkerSetWorkloadType
	}
	// StandalonePattern defaults to StatefulSet (RoleInstanceSet in v1alpha2)
	return constants.RoleInstanceSetWorkloadType
}
