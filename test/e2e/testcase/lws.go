package testcase

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	testutils "sigs.k8s.io/rbgs/test/utils"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func RunLeaderWorkerSetWorkloadTestCases(f *framework.Framework) {
	ginkgo.It("create lws role with engine runtime and without leaderTemplate", func() {
		rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles([]workloadsv1alpha1.RoleSpec{
			wrappers.BuildLwsRole("role-1").
				WithEngineRuntime([]workloadsv1alpha1.EngineRuntime{{ProfileName: testutils.DefaultEngineRuntimeProfileName}}).
				Obj(),
		}).Obj()

		ginkgo.DeferCleanup(func() { dumpLWSDebugInfo(f, rbg) })

		gomega.Expect(testutils.CreatePatioRuntime(f.Ctx, f.Client)).Should(gomega.Succeed())
		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgEqual(rbg)
	})

	ginkgo.It("update lws role.replicas & role.Template", func() {
		rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles([]workloadsv1alpha1.RoleSpec{
			wrappers.BuildLwsRole("role-1").Obj(),
		}).Obj()

		ginkgo.DeferCleanup(func() { dumpLWSDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgEqual(rbg)

		// update
		updateLabel := map[string]string{"update-label": "new"}
		testutils.UpdateRbg(f.Ctx, f.Client, rbg, func(rbg *workloadsv1alpha1.RoleBasedGroup) {
			rbg.Spec.Roles[0].Replicas = ptr.To(*rbg.Spec.Roles[0].Replicas + 1)
			rbg.Spec.Roles[0].TemplateSource.Template.Labels = updateLabel
		})
		f.ExpectRbgEqual(rbg)

		f.ExpectWorkloadPodTemplateLabelContains(rbg, rbg.Spec.Roles[0], updateLabel)
	})

	ginkgo.It("update lws leaderTemplate & workerTemplate", func() {
		rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles([]workloadsv1alpha1.RoleSpec{
			wrappers.BuildLwsRole("role-1").Obj(),
		}).Obj()

		ginkgo.DeferCleanup(func() { dumpLWSDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgEqual(rbg)

		// update
		updateLabel := map[string]string{"update-label": "new"}
		testutils.UpdateRbg(f.Ctx, f.Client, rbg, func(rbg *workloadsv1alpha1.RoleBasedGroup) {
			rbg.Spec.Roles[0].LeaderWorkerSet.PatchLeaderTemplate = ptr.To(wrappers.BuildLWSTemplatePatch(updateLabel))
			rbg.Spec.Roles[0].LeaderWorkerSet.PatchWorkerTemplate = ptr.To(wrappers.BuildLWSTemplatePatch(updateLabel))
		})
		f.ExpectRbgEqual(rbg)

		f.ExpectWorkloadPodTemplateLabelContains(rbg, rbg.Spec.Roles[0], updateLabel, updateLabel)
	})

	ginkgo.It("lws with rollingUpdate", func() {
		rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
			[]workloadsv1alpha1.RoleSpec{
				wrappers.BuildLwsRole("role-1").
					WithReplicas(2).
					WithRollingUpdate(workloadsv1alpha1.RollingUpdate{
						MaxUnavailable: ptr.To(intstr.FromInt32(1)),
						MaxSurge:       ptr.To(intstr.FromInt32(1)),
					}).Obj(),
			}).Obj()

		ginkgo.DeferCleanup(func() { dumpLWSDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgEqual(rbg)

		// update, start rolling update
		updateLabel := map[string]string{"update-label": "new"}
		testutils.UpdateRbg(f.Ctx, f.Client, rbg, func(rbg *workloadsv1alpha1.RoleBasedGroup) {
			rbg.Spec.Roles[0].TemplateSource.Template.Labels = updateLabel
		})
		f.ExpectRbgEqual(rbg)
	})

	ginkgo.It("lws with restartPolicy", func() {
		rbg := wrappers.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
			[]workloadsv1alpha1.RoleSpec{
				wrappers.BuildLwsRole("role-1").
					WithReplicas(2).
					WithRestartPolicy(workloadsv1alpha1.RecreateRBGOnPodRestart).
					Obj(),
			}).Obj()

		ginkgo.DeferCleanup(func() { dumpLWSDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgEqual(rbg)

		gomega.Expect(testutils.DeletePod(f.Ctx, f.Client, f.Namespace, rbg.Name)).Should(gomega.Succeed())

		// wait rbg recreate
		f.ExpectRbgEqual(rbg)
		f.ExpectRbgCondition(rbg, workloadsv1alpha1.RoleBasedGroupRestartInProgress, metav1.ConditionFalse)
	})
}

// dumpLWSDebugInfo collects diagnostic information when LWS is not ready
func dumpLWSDebugInfo(f *framework.Framework, rbg *workloadsv1alpha1.RoleBasedGroup) {
	fmt.Println("\n========== LWS Debug Info ==========")

	for _, role := range rbg.Spec.Roles {
		lwsName := rbg.GetWorkloadName(&role)
		fmt.Printf("\n--- Role: %s, LWS: %s ---\n", role.Name, lwsName)

		// 1. Get LWS status
		lws := &lwsv1.LeaderWorkerSet{}
		if err := f.Client.Get(f.Ctx, client.ObjectKey{Name: lwsName, Namespace: rbg.Namespace}, lws); err != nil {
			fmt.Printf("[LWS] Failed to get LWS: %v\n", err)
		} else {
			fmt.Printf("[LWS] Status: Replicas=%d, ReadyReplicas=%d, UpdatedReplicas=%d\n",
				lws.Status.Replicas, lws.Status.ReadyReplicas, lws.Status.UpdatedReplicas)
			fmt.Printf("[LWS] Conditions:\n")
			for _, cond := range lws.Status.Conditions {
				fmt.Printf("  - Type=%s, Status=%s, Reason=%s, Message=%s\n",
					cond.Type, cond.Status, cond.Reason, cond.Message)
			}
		}

		// 2. Get StatefulSets created by LWS
		stsList := &appsv1.StatefulSetList{}
		if err := f.Client.List(f.Ctx, stsList, client.InNamespace(rbg.Namespace),
			client.MatchingLabels{"leaderworkerset.sigs.k8s.io/name": lwsName}); err != nil {
			fmt.Printf("[STS] Failed to list StatefulSets: %v\n", err)
		} else {
			fmt.Printf("[STS] Found %d StatefulSets\n", len(stsList.Items))
			for _, sts := range stsList.Items {
				fmt.Printf("  - Name=%s, Replicas=%d, ReadyReplicas=%d, CurrentReplicas=%d, UpdatedReplicas=%d\n",
					sts.Name, *sts.Spec.Replicas, sts.Status.ReadyReplicas, sts.Status.CurrentReplicas, sts.Status.UpdatedReplicas)
				for _, cond := range sts.Status.Conditions {
					fmt.Printf("    Condition: Type=%s, Status=%s, Reason=%s, Message=%s\n",
						cond.Type, cond.Status, cond.Reason, cond.Message)
				}
			}
		}

		// 3. Get Pods created by LWS
		podList := &corev1.PodList{}
		if err := f.Client.List(f.Ctx, podList, client.InNamespace(rbg.Namespace),
			client.MatchingLabels{"leaderworkerset.sigs.k8s.io/name": lwsName}); err != nil {
			fmt.Printf("[POD] Failed to list Pods: %v\n", err)
		} else {
			fmt.Printf("[POD] Found %d Pods\n", len(podList.Items))
			for _, pod := range podList.Items {
				fmt.Printf("  - Name=%s, Phase=%s, Ready=%v\n",
					pod.Name, pod.Status.Phase, isPodReady(&pod))
				// Print container statuses
				for _, cs := range pod.Status.ContainerStatuses {
					fmt.Printf("    Container: %s, Ready=%v, RestartCount=%d\n",
						cs.Name, cs.Ready, cs.RestartCount)
					if cs.State.Waiting != nil {
						fmt.Printf("      Waiting: Reason=%s, Message=%s\n",
							cs.State.Waiting.Reason, cs.State.Waiting.Message)
					}
					if cs.State.Terminated != nil {
						fmt.Printf("      Terminated: Reason=%s, ExitCode=%d, Message=%s\n",
							cs.State.Terminated.Reason, cs.State.Terminated.ExitCode, cs.State.Terminated.Message)
					}
				}
				// Print conditions
				fmt.Printf("    Conditions:\n")
				for _, cond := range pod.Status.Conditions {
					fmt.Printf("      - Type=%s, Status=%s, Reason=%s, Message=%s\n",
						cond.Type, cond.Status, cond.Reason, cond.Message)
				}
			}
		}
	}

	fmt.Println("\n========== End Debug Info ==========")
}

func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}
