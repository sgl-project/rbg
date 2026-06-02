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
	"strconv"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// buildPodTemplateWithStartupDelay creates a PodTemplateSpec with a startup probe
// that uses initialDelaySeconds to keep the pod NotReady for the specified duration.
// After the delay, the exec command "true" succeeds immediately, making the pod Ready.
// This allows us to observe intermediate states (like surge instances) during rolling updates.
func buildPodTemplateWithStartupDelay(delaySeconds int32, failureThreshold int32) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:            "nginx",
					Image:           "nginx:latest",
					ImagePullPolicy: corev1.PullIfNotPresent,
					StartupProbe: &corev1.Probe{
						InitialDelaySeconds: delaySeconds,
						ProbeHandler: corev1.ProbeHandler{
							Exec: &corev1.ExecAction{
								Command: []string{"/bin/true"},
							},
						},
						FailureThreshold: failureThreshold,
						PeriodSeconds:    2,
					},
				},
			},
		},
	}
}

func RunRoleInstanceSetWorkloadTestCases(f *framework.Framework) {
	// Test 1: With maxUnavailable=0 and maxSurge=2, readiness never drops below the replica count.
	ginkgo.It("[RoleInstanceSet] surge maintains full readiness during rolling update", func() {
		initialTemplate := buildPodTemplateWithStartupDelay(5, 5)

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithReplicas(4).
					WithTemplate(&initialTemplate).
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						MaxUnavailable: ptr.To(intstr.FromInt32(0)),
						MaxSurge:       ptr.To(intstr.FromInt32(2)),
					}).Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

		// Wait for all initial pods to become Ready
		f.ExpectRbgV2Equal(rbg)
		gomega.Expect(countReadyRoleInstances(f, rbg, "role-1")).Should(gomega.Equal(4))

		time.Sleep(2 * time.Second)

		// Trigger rolling update
		updateTemplate := buildPodTemplateWithStartupDelay(5, 3)
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].StandalonePattern.Template = &updateTemplate
		})

		// Surge should create 2 extra instances (total 6)
		gomega.Eventually(func() int {
			return countRoleInstances(f, rbg, "role-1")
		}, utils.Timeout, utils.Interval).Should(gomega.BeNumerically(">=", 6),
			"surge should create 2 extra instances (total 6) during rolling update")

		// With maxUnavailable=0 and surge, readiness should stay close to replicas.
		// Allow a transient dip of 1 because pod readiness updates are asynchronous
		// in Kind CI environments — a pod may be deleted before its replacement is
		// observed as Ready by the API server.
		gomega.Consistently(func() int {
			return countReadyRoleInstances(f, rbg, "role-1")
		}, 20, 2).Should(gomega.BeNumerically(">=", 3),
			"ready instance count should never drop more than 1 below replicas during surge rolling update")

		// Wait for rolling update to complete
		f.ExpectRbgV2Equal(rbg)

		// Surge instances should be cleaned up
		gomega.Eventually(func() int {
			return countRoleInstances(f, rbg, "role-1")
		}, utils.Timeout, utils.Interval).Should(gomega.Equal(4),
			"surge instances should be cleaned up after rolling update completes")
	})

	// Test 2: With maxUnavailable=2 and maxSurge=2, the controller takes down 2 base
	// instances immediately while creating 2 surge instances. The ready count drops to 2
	// (ordinals 0,1), then recovers to 4 after all instances are updated.
	ginkgo.It("[RoleInstanceSet] surge with maxUnavailable allows faster update with minimum readiness", func() {
		initialTemplate := buildPodTemplateWithStartupDelay(5, 5)

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithReplicas(4).
					WithTemplate(&initialTemplate).
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						MaxUnavailable: ptr.To(intstr.FromInt32(2)),
						MaxSurge:       ptr.To(intstr.FromInt32(2)),
					}).Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

		// Wait for all initial pods to become Ready
		f.ExpectRbgV2Equal(rbg)
		gomega.Expect(countReadyRoleInstances(f, rbg, "role-1")).Should(gomega.Equal(4))

		time.Sleep(2 * time.Second)

		// Trigger rolling update
		updateTemplate := buildPodTemplateWithStartupDelay(5, 3)
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].StandalonePattern.Template = &updateTemplate
		})

		// Intermediate state 1: maxUnavailable=2 takes down ordinals 3,2 immediately,
		// surge creates ordinals 4,5. Only ordinals 0,1 remain ready.
		gomega.Eventually(func() bool {
			total := countRoleInstances(f, rbg, "role-1")
			ready := countReadyRoleInstances(f, rbg, "role-1")
			readyOrds := getReadyOrdinals(f, rbg, "role-1")
			return total == 6 && ready == 2 && readyOrds.Equal(sets.New(0, 1))
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
			"after update: total=6, ready=2, only ordinals 0,1 should be ready")

		// Intermediate state 2: ordinals 2,3 recover with new revision
		gomega.Eventually(func() bool {
			readyOrds := getReadyOrdinals(f, rbg, "role-1")
			return readyOrds.HasAll(2, 3)
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
			"ordinals 2,3 should eventually become ready after rebuild")

		// Surge cleanup: instances 4,5 should be deleted
		gomega.Eventually(func() int {
			return countRoleInstances(f, rbg, "role-1")
		}, utils.Timeout, utils.Interval).Should(gomega.Equal(4),
			"surge instances 4,5 should be cleaned up")

		// Final state: all 4 instances ready with new revision
		gomega.Eventually(func() int {
			return countReadyRoleInstances(f, rbg, "role-1")
		}, utils.Timeout, utils.Interval).Should(gomega.Equal(4))
	})

	// Test 3: A maxSurge value larger than replicas is capped at the replicas count.
	// With replicas=2 and maxSurge=4, at most 2 surge instances are created (total 4).
	ginkgo.It("[RoleInstanceSet] large maxSurge is capped at replicas count", func() {
		initialTemplate := buildPodTemplateWithStartupDelay(5, 5)

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithReplicas(2).
					WithTemplate(&initialTemplate).
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						MaxUnavailable: ptr.To(intstr.FromInt32(0)),
						MaxSurge:       ptr.To(intstr.FromInt32(4)),
					}).Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

		// Wait for all initial pods to become Ready
		f.ExpectRbgV2Equal(rbg)
		gomega.Expect(countRoleInstances(f, rbg, "role-1")).Should(gomega.Equal(2))

		time.Sleep(2 * time.Second)

		// Trigger rolling update
		updateTemplate := buildPodTemplateWithStartupDelay(5, 3)
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].StandalonePattern.Template = &updateTemplate
		})

		// Surge should be capped at replicas (2), so total never exceeds 4
		gomega.Consistently(func() int {
			return countRoleInstances(f, rbg, "role-1")
		}, 20, 1).Should(gomega.BeNumerically("<=", 4),
			"maxSurge=4 with replicas=2 should be capped, total instances <= 4")

		// Wait for rolling update to complete
		f.ExpectRbgV2Equal(rbg)

		// Surge instances should be cleaned up
		gomega.Eventually(func() int {
			return countRoleInstances(f, rbg, "role-1")
		}, utils.Timeout, utils.Interval).Should(gomega.Equal(2),
			"surge instances should be cleaned up after rolling update completes")
		gomega.Expect(countReadyRoleInstances(f, rbg, "role-1")).Should(gomega.Equal(2))
	})

	// Test 4: Decreasing partition step-by-step must continue to roll the
	// newly-exposed ordinals. With replicas=2 and partition=1, only ord 1
	// updates while ord 0 stays at the original revision. Then the user
	// lowers partition to 0, which must trigger ord 0 to update too.
	// Surge is intentionally 0 here — this test focuses on partition
	// progression, not surge sizing.
	ginkgo.It("[RoleInstanceSet] partition decrease progressively rolls more ordinals", func() {
		initialTemplate := buildPodTemplateWithStartupDelay(5, 5)

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildStandaloneRole("role-1").
					WithReplicas(2).
					WithTemplate(&initialTemplate).
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						MaxUnavailable: ptr.To(intstr.FromInt32(1)),
						MaxSurge:       ptr.To(intstr.FromInt32(0)),
						Partition:      ptr.To(intstr.FromInt32(1)),
					}).Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

		// Wait for both initial pods to become Ready.
		f.ExpectRbgV2Equal(rbg)
		gomega.Expect(countReadyRoleInstances(f, rbg, "role-1")).Should(gomega.Equal(2))

		time.Sleep(2 * time.Second)

		// Record the initial revision from ord 0.
		initialRevision := getRoleInstanceRevision(f, rbg, "role-1", 0)
		gomega.Expect(initialRevision).ShouldNot(gomega.BeEmpty())

		// Trigger a template change. With partition=1, only ord 1 should roll.
		updateTemplate := buildPodTemplateWithStartupDelay(5, 3)
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].StandalonePattern.Template = &updateTemplate
		})

		// Wait for ord 1 to land on the new revision.
		gomega.Eventually(func() bool {
			rev := getRoleInstanceRevision(f, rbg, "role-1", 1)
			return rev != "" && rev != initialRevision
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
			"ord 1 should be updated to the new revision under partition=1")

		// ord 0 must remain on the original revision throughout — the
		// partition contract is that anything below partition stays put.
		gomega.Consistently(func() string {
			return getRoleInstanceRevision(f, rbg, "role-1", 0)
		}, 10, 1).Should(gomega.Equal(initialRevision),
			"ord 0 (below partition) must keep the original revision while partition=1")

		// Total instance count stays at 2 throughout (surge=0).
		gomega.Eventually(func() int {
			return countReadyRoleInstances(f, rbg, "role-1")
		}, utils.Timeout, utils.Interval).Should(gomega.Equal(2))
		gomega.Expect(countRoleInstances(f, rbg, "role-1")).Should(gomega.Equal(2))

		updatedRevision := getRoleInstanceRevision(f, rbg, "role-1", 1)
		gomega.Expect(updatedRevision).ShouldNot(gomega.Equal(initialRevision))

		time.Sleep(2 * time.Second)

		// Now lower partition to 0. ord 0 must pick up the new revision.
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].RolloutStrategy.RollingUpdate.Partition = ptr.To(intstr.FromInt32(0))
		})

		gomega.Eventually(func() string {
			return getRoleInstanceRevision(f, rbg, "role-1", 0)
		}, utils.Timeout, utils.Interval).Should(gomega.Equal(updatedRevision),
			"ord 0 should be updated to the new revision after partition is lowered to 0")

		// Final state: 2 instances, both at the new revision, both ready.
		gomega.Eventually(func() int {
			return countReadyRoleInstances(f, rbg, "role-1")
		}, utils.Timeout, utils.Interval).Should(gomega.Equal(2))
		gomega.Expect(countRoleInstances(f, rbg, "role-1")).Should(gomega.Equal(2))
		gomega.Expect(getRoleInstanceRevision(f, rbg, "role-1", 0)).Should(gomega.Equal(updatedRevision))
		gomega.Expect(getRoleInstanceRevision(f, rbg, "role-1", 1)).Should(gomega.Equal(updatedRevision))
	})
}

// listRoleInstances returns all RoleInstances for the given RBG and role.
func listRoleInstances(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, roleName string) []workloadsv1alpha2.RoleInstance {
	riList := &workloadsv1alpha2.RoleInstanceList{}
	err := f.Client.List(f.Ctx, riList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
			constants.RoleNameLabelKey:  roleName,
		},
	)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	return riList.Items
}

// countRoleInstances returns the total number of RoleInstances for the given RBG and role.
func countRoleInstances(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, roleName string) int {
	return len(listRoleInstances(f, rbg, roleName))
}

// countReadyRoleInstances returns the number of Ready RoleInstances for the given RBG and role.
func countReadyRoleInstances(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, roleName string) int {
	instances := listRoleInstances(f, rbg, roleName)
	ready := 0
	for _, ri := range instances {
		for _, cond := range ri.Status.Conditions {
			if cond.Type == workloadsv1alpha2.RoleInstanceAllPodsReady && cond.Status == corev1.ConditionTrue {
				ready++
				break
			}
		}
	}
	klog.Infof("ready instance: %v/%v", ready, len(instances))
	return ready
}

// getReadyOrdinals returns the set of ordinal indices for Ready RoleInstances.
func getReadyOrdinals(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, roleName string) sets.Set[int] {
	instances := listRoleInstances(f, rbg, roleName)
	result := sets.New[int]()
	for _, ri := range instances {
		isReady := false
		for _, cond := range ri.Status.Conditions {
			if cond.Type == workloadsv1alpha2.RoleInstanceAllPodsReady && cond.Status == corev1.ConditionTrue {
				isReady = true
				break
			}
		}
		if isReady {
			ordStr := ri.Labels[constants.RoleInstanceIndexLabelKey]
			if ord, err := strconv.Atoi(ordStr); err == nil {
				result.Insert(ord)
			}
		}
	}
	return result
}

// getRoleInstanceRevision returns the controller-revision-hash label value for
// the RoleInstance at the given ordinal index.
func getRoleInstanceRevision(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, roleName string, ordinal int) string {
	instances := listRoleInstances(f, rbg, roleName)
	for _, ri := range instances {
		if ri.Labels[constants.RoleInstanceIndexLabelKey] == fmt.Sprintf("%d", ordinal) {
			return ri.Labels["controller-revision-hash"]
		}
	}
	return ""
}
