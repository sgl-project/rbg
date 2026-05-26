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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunStabilityTestCases(f *framework.Framework) {
	ginkgo.Describe("stability", func() {

		ginkgo.It("updating one role does not affect other roles and updated role is stable after update", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-a").WithReplicas(2).Obj(),
					wrappersv2.BuildStandaloneRole("role-b").WithReplicas(2).Obj(),
				}).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// Record role-b pod UIDs before update
			roleBPodUIDs := getPodUIDsForRole(f, rbg, "role-b")
			gomega.Expect(roleBPodUIDs).Should(gomega.HaveLen(2))

			// Update role-a template only
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].StandalonePattern.Template.Labels = map[string]string{"update": "v2"}
			})

			// Wait for role-a to finish updating
			f.ExpectRbgV2Equal(rbg)

			// Verify role-b pods are completely untouched
			newRoleBPodUIDs := getPodUIDsForRole(f, rbg, "role-b")
			gomega.Expect(newRoleBPodUIDs).Should(gomega.Equal(roleBPodUIDs),
				"role-b pod UIDs should not change when role-a is updated")

			// Verify role-a is stable after update (no extra restarts)
			roleAPodUIDs := getPodUIDsForRole(f, rbg, "role-a")
			gomega.Consistently(func() map[string]types.UID {
				return getPodUIDsForRole(f, rbg, "role-a")
			}, 15, 2).Should(gomega.Equal(roleAPodUIDs),
				"role-a pods should be stable after update completes (no extra restarts)")

			// Also verify role-b remains stable
			gomega.Consistently(func() map[string]types.UID {
				return getPodUIDsForRole(f, rbg, "role-b")
			}, 15, 2).Should(gomega.Equal(roleBPodUIDs),
				"role-b pods should remain stable over time")
		})

		ginkgo.It("scaling one role does not affect other roles and scaled role is stable after scaling", func() {
			rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
				[]workloadsv1alpha2.RoleSpec{
					wrappersv2.BuildStandaloneRole("role-a").WithReplicas(2).Obj(),
					wrappersv2.BuildStandaloneRole("role-b").WithReplicas(2).Obj(),
				}).Obj()

			f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			// Record all pod UIDs before scaling
			roleAPodUIDs := getPodUIDsForRole(f, rbg, "role-a")
			roleBPodUIDs := getPodUIDsForRole(f, rbg, "role-b")
			gomega.Expect(roleAPodUIDs).Should(gomega.HaveLen(2))
			gomega.Expect(roleBPodUIDs).Should(gomega.HaveLen(2))

			// Scale role-a from 2 to 3
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].Replicas = ptr.To(int32(3))
			})

			// Wait for role-a scaling complete
			f.ExpectRbgV2Equal(rbg)

			// Verify role-b pods are completely untouched
			newRoleBPodUIDs := getPodUIDsForRole(f, rbg, "role-b")
			gomega.Expect(newRoleBPodUIDs).Should(gomega.Equal(roleBPodUIDs),
				"role-b pod UIDs should not change when role-a is scaled")

			// Verify role-a's original pods are not recreated (only new pod added)
			newRoleAPodUIDs := getPodUIDsForRole(f, rbg, "role-a")
			gomega.Expect(newRoleAPodUIDs).Should(gomega.HaveLen(3))
			for name, uid := range roleAPodUIDs {
				gomega.Expect(newRoleAPodUIDs).Should(gomega.HaveKeyWithValue(name, uid),
					"role-a original pod %s should not be recreated during scale-up", name)
			}

			// Verify both roles are stable after scaling
			finalRoleAPodUIDs := getPodUIDsForRole(f, rbg, "role-a")
			gomega.Consistently(func() map[string]types.UID {
				return getPodUIDsForRole(f, rbg, "role-a")
			}, 15, 2).Should(gomega.Equal(finalRoleAPodUIDs),
				"role-a pods should be stable after scaling completes")

			gomega.Consistently(func() map[string]types.UID {
				return getPodUIDsForRole(f, rbg, "role-b")
			}, 15, 2).Should(gomega.Equal(roleBPodUIDs),
				"role-b pods should remain stable over time")
		})
	})
}

// getPodUIDsForRole returns a map of pod name -> UID for all pods belonging to a specific role.
func getPodUIDsForRole(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, roleName string) map[string]types.UID {
	podList := &corev1.PodList{}
	err := f.Client.List(f.Ctx, podList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
			constants.RoleNameLabelKey:  roleName,
		},
	)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	result := make(map[string]types.UID, len(podList.Items))
	for _, pod := range podList.Items {
		if pod.DeletionTimestamp == nil {
			result[pod.Name] = pod.UID
		}
	}
	return result
}

// getActivePodUIDsForRole returns UIDs of active (non-terminated) pods for a role.
func getActivePodUIDsForRole(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, roleName string) map[string]types.UID {
	podList := &corev1.PodList{}
	err := f.Client.List(f.Ctx, podList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
			constants.RoleNameLabelKey:  roleName,
		},
	)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	result := make(map[string]types.UID, len(podList.Items))
	for _, pod := range podList.Items {
		if pod.DeletionTimestamp == nil && pod.Status.Phase != corev1.PodFailed && pod.Status.Phase != corev1.PodSucceeded {
			result[pod.Name] = pod.UID
		}
	}
	return result
}
