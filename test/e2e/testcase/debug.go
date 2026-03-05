package testcase

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/e2e/framework"
)

// dumpDebugInfo collects diagnostic information only when test fails
// It prints workload status, pod status, and events for all roles in the RBG
func dumpDebugInfo(f *framework.Framework, rbg *workloadsv1alpha1.RoleBasedGroup) {
	if rbg == nil {
		return
	}
	// Only dump debug info when test fails
	if !ginkgo.CurrentSpecReport().Failed() {
		return
	}
	fmt.Println("\n========== Debug Info ==========")
	fmt.Printf("RBG: %s/%s\n", rbg.Namespace, rbg.Name)

	// Get and print RBG status
	currentRBG := &workloadsv1alpha1.RoleBasedGroup{}
	if err := f.Client.Get(f.Ctx, client.ObjectKey{Name: rbg.Name, Namespace: rbg.Namespace}, currentRBG); err != nil {
		fmt.Printf("[RBG] Failed to get RBG: %v\n", err)
	} else {
		fmt.Printf("[RBG] Status: TotalRoles=%d, RoleStatuses=%d\n",
			len(currentRBG.Spec.Roles), len(currentRBG.Status.RoleStatuses))
		fmt.Printf("[RBG] Conditions:\n")
		for _, cond := range currentRBG.Status.Conditions {
			fmt.Printf("  - Type=%s, Status=%s, Reason=%s, Message=%s\n",
				cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}

	for _, role := range rbg.Spec.Roles {
		workloadName := rbg.GetWorkloadName(&role)
		fmt.Printf("\n--- Role: %s, Workload: %s ---\n", role.Name, workloadName)

		// Determine workload type and dump accordingly
		switch {
		case role.Workload.Kind == "LeaderWorkerSet" || role.LeaderWorkerSet != nil:
			dumpLWSWorkload(f, rbg.Namespace, workloadName)
		case role.Workload.Kind == "Deployment":
			dumpDeploymentWorkload(f, rbg.Namespace, workloadName)
		case role.Workload.Kind == "InstanceSet":
			dumpInstanceSetWorkload(f, rbg.Namespace, workloadName, role.Name)
		default: // StatefulSet is default
			dumpStatefulSetWorkload(f, rbg.Namespace, workloadName)
		}

		// Print pods for this role
		dumpRolePods(f, rbg, role.Name, workloadName)
	}

	fmt.Println("\n========== End Debug Info ==========")
}

// dumpDebugInfoForRBGSet collects diagnostic information for RoleBasedGroupSet only when test fails
func dumpDebugInfoForRBGSet(f *framework.Framework, rbgset *workloadsv1alpha1.RoleBasedGroupSet) {
	if rbgset == nil {
		return
	}
	// Only dump debug info when test fails
	if !ginkgo.CurrentSpecReport().Failed() {
		return
	}
	fmt.Println("\n========== RBGSet Debug Info ==========")
	fmt.Printf("RBGSet: %s/%s\n", rbgset.Namespace, rbgset.Name)

	// Get and print RBGSet status
	currentRBGSet := &workloadsv1alpha1.RoleBasedGroupSet{}
	if err := f.Client.Get(f.Ctx, client.ObjectKey{Name: rbgset.Name, Namespace: rbgset.Namespace}, currentRBGSet); err != nil {
		fmt.Printf("[RBGSet] Failed to get RBGSet: %v\n", err)
	} else {
		fmt.Printf("[RBGSet] Status: Replicas=%d, ReadyReplicas=%d\n",
			currentRBGSet.Status.Replicas, currentRBGSet.Status.ReadyReplicas)
		fmt.Printf("[RBGSet] Conditions:\n")
		for _, cond := range currentRBGSet.Status.Conditions {
			fmt.Printf("  - Type=%s, Status=%s, Reason=%s, Message=%s\n",
				cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}

	// List all RBGs owned by this RBGSet
	rbgList := &workloadsv1alpha1.RoleBasedGroupList{}
	if err := f.Client.List(f.Ctx, rbgList, client.InNamespace(rbgset.Namespace),
		client.MatchingLabels{workloadsv1alpha1.SetNameLabelKey: rbgset.Name}); err != nil {
		fmt.Printf("[RBGSet] Failed to list RBGs: %v\n", err)
	} else {
		fmt.Printf("[RBGSet] Found %d RBGs\n", len(rbgList.Items))
		for _, rbg := range rbgList.Items {
			fmt.Printf("\n=== Child RBG: %s ===\n", rbg.Name)
			dumpDebugInfo(f, &rbg)
		}
	}

	fmt.Println("\n========== End RBGSet Debug Info ==========")
}

func dumpLWSWorkload(f *framework.Framework, namespace, lwsName string) {
	// Get LWS status
	lws := &lwsv1.LeaderWorkerSet{}
	if err := f.Client.Get(f.Ctx, client.ObjectKey{Name: lwsName, Namespace: namespace}, lws); err != nil {
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

	// Get StatefulSets created by LWS
	stsList := &appsv1.StatefulSetList{}
	if err := f.Client.List(f.Ctx, stsList, client.InNamespace(namespace),
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
}

func dumpDeploymentWorkload(f *framework.Framework, namespace, deployName string) {
	deploy := &appsv1.Deployment{}
	if err := f.Client.Get(f.Ctx, client.ObjectKey{Name: deployName, Namespace: namespace}, deploy); err != nil {
		fmt.Printf("[Deployment] Failed to get Deployment: %v\n", err)
	} else {
		fmt.Printf("[Deployment] Status: Replicas=%d, ReadyReplicas=%d, AvailableReplicas=%d, UpdatedReplicas=%d\n",
			deploy.Status.Replicas, deploy.Status.ReadyReplicas, deploy.Status.AvailableReplicas, deploy.Status.UpdatedReplicas)
		fmt.Printf("[Deployment] Conditions:\n")
		for _, cond := range deploy.Status.Conditions {
			fmt.Printf("  - Type=%s, Status=%s, Reason=%s, Message=%s\n",
				cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}

	// Get ReplicaSets
	rsList := &appsv1.ReplicaSetList{}
	if err := f.Client.List(f.Ctx, rsList, client.InNamespace(namespace)); err != nil {
		fmt.Printf("[ReplicaSet] Failed to list ReplicaSets: %v\n", err)
	} else {
		for _, rs := range rsList.Items {
			for _, ownerRef := range rs.OwnerReferences {
				if ownerRef.Kind == "Deployment" && ownerRef.Name == deployName {
					fmt.Printf("  [RS] Name=%s, Replicas=%d, ReadyReplicas=%d, AvailableReplicas=%d\n",
						rs.Name, *rs.Spec.Replicas, rs.Status.ReadyReplicas, rs.Status.AvailableReplicas)
				}
			}
		}
	}
}

func dumpStatefulSetWorkload(f *framework.Framework, namespace, stsName string) {
	sts := &appsv1.StatefulSet{}
	if err := f.Client.Get(f.Ctx, client.ObjectKey{Name: stsName, Namespace: namespace}, sts); err != nil {
		fmt.Printf("[StatefulSet] Failed to get StatefulSet: %v\n", err)
	} else {
		fmt.Printf("[StatefulSet] Status: Replicas=%d, ReadyReplicas=%d, CurrentReplicas=%d, UpdatedReplicas=%d\n",
			*sts.Spec.Replicas, sts.Status.ReadyReplicas, sts.Status.CurrentReplicas, sts.Status.UpdatedReplicas)
		fmt.Printf("[StatefulSet] CurrentRevision=%s, UpdateRevision=%s\n",
			sts.Status.CurrentRevision, sts.Status.UpdateRevision)
		fmt.Printf("[StatefulSet] Conditions:\n")
		for _, cond := range sts.Status.Conditions {
			fmt.Printf("  - Type=%s, Status=%s, Reason=%s, Message=%s\n",
				cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}
}

func dumpInstanceSetWorkload(f *framework.Framework, namespace, instanceSetName, roleName string) {
	// Get InstanceSet status
	instanceSet := &workloadsv1alpha1.InstanceSet{}
	if err := f.Client.Get(f.Ctx, client.ObjectKey{Name: instanceSetName, Namespace: namespace}, instanceSet); err != nil {
		fmt.Printf("[InstanceSet] Failed to get InstanceSet: %v\n", err)
	} else {
		fmt.Printf("[InstanceSet] Status: Replicas=%d, ReadyReplicas=%d, AvailableReplicas=%d, UpdatedReplicas=%d\n",
			instanceSet.Status.Replicas, instanceSet.Status.ReadyReplicas, instanceSet.Status.AvailableReplicas, instanceSet.Status.UpdatedReplicas)
		fmt.Printf("[InstanceSet] Conditions:\n")
		for _, cond := range instanceSet.Status.Conditions {
			fmt.Printf("  - Type=%s, Status=%s, Reason=%s, Message=%s\n",
				cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}

	// List Instances
	instanceList := &workloadsv1alpha1.InstanceList{}
	if err := f.Client.List(f.Ctx, instanceList, client.InNamespace(namespace),
		client.MatchingLabels{workloadsv1alpha1.SetInstanceNameLabelKey: instanceSetName}); err != nil {
		fmt.Printf("[Instance] Failed to list Instances: %v\n", err)
	} else {
		fmt.Printf("[Instance] Found %d Instances\n", len(instanceList.Items))
		for _, inst := range instanceList.Items {
			// Check InstanceReady condition
			ready := false
			for _, cond := range inst.Status.Conditions {
				if cond.Type == workloadsv1alpha1.InstanceReady && cond.Status == corev1.ConditionTrue {
					ready = true
					break
				}
			}
			fmt.Printf("  - Name=%s, Ready=%v\n", inst.Name, ready)
		}
	}
}

func dumpRolePods(f *framework.Framework, rbg *workloadsv1alpha1.RoleBasedGroup, roleName, workloadName string) {
	podList := &corev1.PodList{}

	// Try to find pods by different labels based on workload type
	selectors := []client.MatchingLabels{
		{workloadsv1alpha1.SetNameLabelKey: rbg.Name, workloadsv1alpha1.SetRoleLabelKey: roleName},
		{"leaderworkerset.sigs.k8s.io/name": workloadName},
		{"app.kubernetes.io/instance": workloadName},
	}

	var foundPods []corev1.Pod
	for _, selector := range selectors {
		if err := f.Client.List(f.Ctx, podList, client.InNamespace(rbg.Namespace), selector); err == nil && len(podList.Items) > 0 {
			foundPods = podList.Items
			break
		}
	}

	if len(foundPods) == 0 {
		fmt.Printf("[POD] No pods found for role %s\n", roleName)
		return
	}

	fmt.Printf("[POD] Found %d Pods\n", len(foundPods))
	for _, pod := range foundPods {
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

func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}
