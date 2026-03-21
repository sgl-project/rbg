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

package v1alpha2

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
)

// dumpDebugInfo collects diagnostic information only when test fails.
func dumpDebugInfo(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup) {
	if rbg == nil {
		return
	}
	if !ginkgo.CurrentSpecReport().Failed() {
		return
	}
	fmt.Println("\n========== Debug Info (v1alpha2) ==========")
	fmt.Printf("RBG: %s/%s\n", rbg.Namespace, rbg.Name)

	currentRBG := &workloadsv1alpha2.RoleBasedGroup{}
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

		switch role.Workload.String() {
		case constants.DeploymentWorkloadType:
			dumpV2DeploymentWorkload(f, rbg.Namespace, workloadName)
		case constants.StatefulSetWorkloadType:
			dumpV2StatefulSetWorkload(f, rbg.Namespace, workloadName)
		case constants.LeaderWorkerSetWorkloadType:
			dumpV2LWSWorkload(f, rbg.Namespace, workloadName)
		case constants.RoleInstanceSetWorkloadType:
			dumpV2RoleInstanceSetWorkload(f, rbg.Namespace, workloadName)
		default:
			dumpV2RoleInstanceSetWorkload(f, rbg.Namespace, workloadName)
		}

		dumpV2RolePods(f, rbg, role.Name, workloadName)
	}

	fmt.Println("\n========== End Debug Info (v1alpha2) ==========")
}

// dumpDebugInfoForRBGSet collects diagnostic information for RoleBasedGroupSet only when test fails.
func dumpDebugInfoForRBGSet(f *framework.Framework, rbgset *workloadsv1alpha2.RoleBasedGroupSet) {
	if rbgset == nil {
		return
	}
	if !ginkgo.CurrentSpecReport().Failed() {
		return
	}
	fmt.Println("\n========== RBGSet Debug Info (v1alpha2) ==========")
	fmt.Printf("RBGSet: %s/%s\n", rbgset.Namespace, rbgset.Name)

	currentRBGSet := &workloadsv1alpha2.RoleBasedGroupSet{}
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

	rbgList := &workloadsv1alpha2.RoleBasedGroupList{}
	if err := f.Client.List(f.Ctx, rbgList, client.InNamespace(rbgset.Namespace),
		client.MatchingLabels{constants.GroupSetNameLabelKey: rbgset.Name}); err != nil {
		fmt.Printf("[RBGSet] Failed to list RBGs: %v\n", err)
	} else {
		fmt.Printf("[RBGSet] Found %d RBGs\n", len(rbgList.Items))
		for i := range rbgList.Items {
			fmt.Printf("\n=== Child RBG: %s ===\n", rbgList.Items[i].Name)
			dumpDebugInfo(f, &rbgList.Items[i])
		}
	}

	fmt.Println("\n========== End RBGSet Debug Info (v1alpha2) ==========")
}

func dumpV2LWSWorkload(f *framework.Framework, namespace, lwsName string) {
	lws := &lwsv1.LeaderWorkerSet{}
	if err := f.Client.Get(f.Ctx, client.ObjectKey{Name: lwsName, Namespace: namespace}, lws); err != nil {
		fmt.Printf("[LWS] Failed to get LWS: %v\n", err)
	} else {
		fmt.Printf("[LWS] Status: Replicas=%d, ReadyReplicas=%d, UpdatedReplicas=%d\n",
			lws.Status.Replicas, lws.Status.ReadyReplicas, lws.Status.UpdatedReplicas)
		for _, cond := range lws.Status.Conditions {
			fmt.Printf("  - Type=%s, Status=%s, Reason=%s, Message=%s\n",
				cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}
}

func dumpV2DeploymentWorkload(f *framework.Framework, namespace, deployName string) {
	deploy := &appsv1.Deployment{}
	if err := f.Client.Get(f.Ctx, client.ObjectKey{Name: deployName, Namespace: namespace}, deploy); err != nil {
		fmt.Printf("[Deployment] Failed to get Deployment: %v\n", err)
	} else {
		fmt.Printf("[Deployment] Status: Replicas=%d, ReadyReplicas=%d, AvailableReplicas=%d, UpdatedReplicas=%d\n",
			*deploy.Spec.Replicas, deploy.Status.ReadyReplicas, deploy.Status.AvailableReplicas, deploy.Status.UpdatedReplicas)
		for _, cond := range deploy.Status.Conditions {
			fmt.Printf("  - Type=%s, Status=%s, Reason=%s, Message=%s\n",
				cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}
}

func dumpV2RoleInstanceSetWorkload(f *framework.Framework, namespace, risName string) {
	ris := &workloadsv1alpha2.RoleInstanceSet{}
	if err := f.Client.Get(f.Ctx, client.ObjectKey{Name: risName, Namespace: namespace}, ris); err != nil {
		fmt.Printf("[RoleInstanceSet] Failed to get RoleInstanceSet: %v\n", err)
	} else {
		fmt.Printf("[RoleInstanceSet] Status: Replicas=%d, ReadyReplicas=%d, CurrentReplicas=%d, UpdatedReplicas=%d\n",
			ris.Status.Replicas, ris.Status.ReadyReplicas, ris.Status.CurrentReplicas, ris.Status.UpdatedReplicas)
		fmt.Printf("[RoleInstanceSet] CurrentRevision=%s, UpdateRevision=%s\n",
			ris.Status.CurrentRevision, ris.Status.UpdateRevision)
		for _, cond := range ris.Status.Conditions {
			fmt.Printf("  - Type=%s, Status=%s, Reason=%s, Message=%s\n",
				cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}
}

func dumpV2StatefulSetWorkload(f *framework.Framework, namespace, stsName string) {
	sts := &appsv1.StatefulSet{}
	if err := f.Client.Get(f.Ctx, client.ObjectKey{Name: stsName, Namespace: namespace}, sts); err != nil {
		fmt.Printf("[StatefulSet] Failed to get StatefulSet: %v\n", err)
	} else {
		fmt.Printf("[StatefulSet] Status: Replicas=%d, ReadyReplicas=%d, CurrentReplicas=%d, UpdatedReplicas=%d\n",
			*sts.Spec.Replicas, sts.Status.ReadyReplicas, sts.Status.CurrentReplicas, sts.Status.UpdatedReplicas)
		fmt.Printf("[StatefulSet] CurrentRevision=%s, UpdateRevision=%s\n",
			sts.Status.CurrentRevision, sts.Status.UpdateRevision)
		for _, cond := range sts.Status.Conditions {
			fmt.Printf("  - Type=%s, Status=%s, Reason=%s, Message=%s\n",
				cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}
}

func dumpV2RolePods(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, roleName, workloadName string) {
	podList := &corev1.PodList{}
	selectors := []client.MatchingLabels{
		{constants.GroupNameLabelKey: rbg.Name, constants.RoleNameLabelKey: roleName},
		{constants.GroupNameLabelKey: rbg.Name, constants.RoleNameLabelKey: roleName},
		{"leaderworkerset.sigs.k8s.io/name": workloadName},
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
		ready := isV2PodReady(&pod)
		fmt.Printf("  - Name=%s, Phase=%s, Ready=%v\n", pod.Name, pod.Status.Phase, ready)
		for _, cs := range pod.Status.ContainerStatuses {
			fmt.Printf("    Container: %s, Ready=%v, RestartCount=%d\n", cs.Name, cs.Ready, cs.RestartCount)
			if cs.State.Waiting != nil {
				fmt.Printf("      Waiting: Reason=%s, Message=%s\n", cs.State.Waiting.Reason, cs.State.Waiting.Message)
			}
			if cs.State.Terminated != nil {
				fmt.Printf("      Terminated: Reason=%s, ExitCode=%d, Message=%s\n",
					cs.State.Terminated.Reason, cs.State.Terminated.ExitCode, cs.State.Terminated.Message)
			}
		}
	}
}

func isV2PodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}
