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
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

// RunRestartPolicyFieldTestCases covers the interaction between the deprecated
// restartPolicy string field (the v0.7.0 shape) and the restartPolicyConfig
// object that replaced it.
func RunRestartPolicyFieldTestCases(f *framework.Framework) {
	ginkgo.Describe("restart policy field resolution", ginkgo.Label("update"), func() {
		runRestartPolicyResolutionMatrixTest(f)
		runRestartPolicyConfigOverridesLegacyTest(f)
		runLegacyRestartPolicyRecreatesInstanceTest(f)
	})
}

// restartPolicyCase describes one combination of the two fields and the type it
// must resolve to.
type restartPolicyCase struct {
	roleName string
	// legacy is written to the deprecated restartPolicy string field.
	legacy workloadsv1alpha2.RestartPolicyType
	// configType is written to restartPolicyConfig.type. Empty means the config
	// object is left absent entirely.
	configType workloadsv1alpha2.RestartPolicyType
	expected   workloadsv1alpha2.RestartPolicyType
}

// runRestartPolicyResolutionMatrixTest drives every combination of the two fields
// through a single RBG and asserts the resolved type both on the stored RBG (via the
// resolution getters) and on the generated RoleInstance (written by the controller).
// Sharing one RBG keeps the matrix to a single readiness wait.
func runRestartPolicyResolutionMatrixTest(f *framework.Framework) {
	ginkgo.It("restartPolicyConfig.type takes precedence over the deprecated restartPolicy", func() {
		cases := []restartPolicyCase{
			{
				roleName: "legacy-recreate",
				legacy:   workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
				expected: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
			},
			{
				roleName: "legacy-none",
				legacy:   workloadsv1alpha2.RestartPolicyNone,
				expected: workloadsv1alpha2.RestartPolicyNone,
			},
			{
				roleName:   "config-none",
				configType: workloadsv1alpha2.RestartPolicyNone,
				expected:   workloadsv1alpha2.RestartPolicyNone,
			},
			{
				roleName:   "conflict-config-none",
				legacy:     workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
				configType: workloadsv1alpha2.RestartPolicyNone,
				expected:   workloadsv1alpha2.RestartPolicyNone,
			},
			{
				roleName:   "conflict-config-recreate",
				legacy:     workloadsv1alpha2.RestartPolicyNone,
				configType: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
				expected:   workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
			},
			{
				roleName: "both-unset",
				expected: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
			},
		}

		roles := make([]workloadsv1alpha2.RoleSpec, 0, len(cases)+2)
		for _, tc := range cases {
			// Size 1 keeps the matrix to one pod per role; resolution does not
			// depend on the group size.
			rw := wrappersv2.BuildLeaderWorkerRole(tc.roleName).WithReplicas(1).WithSize(1)
			if tc.legacy != "" {
				rw = rw.WithLegacyRestartPolicy(tc.legacy)
			}
			if tc.configType != "" {
				rw = rw.WithRestartPolicy(tc.configType)
			}
			roles = append(roles, rw.Obj())
		}

		// CustomComponentsPattern carries the same pair of fields, so cover the
		// legacy-only path there too.
		ccpTemplate := wrappersv2.BuildBasicPodTemplateSpec()
		roles = append(roles, workloadsv1alpha2.RoleSpec{
			Name:     "ccp-legacy-none",
			Replicas: ptr.To(int32(1)),
			Pattern: workloadsv1alpha2.Pattern{
				CustomComponentsPattern: &workloadsv1alpha2.CustomComponentsPattern{
					RestartPolicy: workloadsv1alpha2.RestartPolicyNone, //nolint:staticcheck // intentional use of deprecated field
					Components: []workloadsv1alpha2.InstanceComponent{
						{Name: "main", Size: ptr.To(int32(1)), Template: ccpTemplate},
					},
				},
			},
		})

		// StandalonePattern exposes neither field and must resolve to None.
		roles = append(roles, wrappersv2.BuildStandaloneRole("standalone").WithReplicas(1).Obj())

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-rp-matrix", f.Namespace).WithRoles(roles).Obj()
		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())

		ginkgo.By("checking every combination resolves to the expected policy on the stored RBG")
		stored := &workloadsv1alpha2.RoleBasedGroup{}
		gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKeyFromObject(rbg), stored)).Should(gomega.Succeed())

		for _, tc := range cases {
			role := findRole(stored, tc.roleName)
			gomega.Expect(role.GetRestartPolicy()).Should(gomega.Equal(tc.expected),
				"role %s: resolved type mismatch", tc.roleName)
			gomega.Expect(role.LeaderWorkerPattern.RestartPolicy).Should(gomega.Equal(tc.legacy), //nolint:staticcheck // intentional use of deprecated field
				"role %s: deprecated restartPolicy must be preserved verbatim", tc.roleName)
		}

		ccpRole := findRole(stored, "ccp-legacy-none")
		gomega.Expect(ccpRole.GetRestartPolicy()).
			Should(gomega.Equal(workloadsv1alpha2.RestartPolicyNone),
				"customComponentsPattern should resolve the legacy field the same way")

		f.ExpectRbgV2Equal(rbg)

		ginkgo.By("checking the resolved policy propagated to every RoleInstance")
		expectedByRole := map[string]workloadsv1alpha2.RestartPolicyType{
			"ccp-legacy-none": workloadsv1alpha2.RestartPolicyNone,
			"standalone":      workloadsv1alpha2.RestartPolicyNone,
		}
		for _, tc := range cases {
			expectedByRole[tc.roleName] = tc.expected
		}

		for roleName, expected := range expectedByRole {
			gomega.Eventually(func() workloadsv1alpha2.RestartPolicyType {
				ri := getRoleInstanceForRole(f, rbg, roleName)
				if ri == nil {
					return ""
				}
				return ri.Spec.GetRestartPolicy()
			}, utils.Timeout, utils.Interval).Should(gomega.Equal(expected),
				"role %s: RoleInstance restart policy mismatch", roleName)
		}
	})
}

// runRestartPolicyConfigOverridesLegacyTest is the behavioural half of the matrix:
// a role that asks for Recreate through the deprecated field but None through
// restartPolicyConfig must behave as None: the kubelet restarts the container in
// the existing Pod, and the controller preserves every Pod in the instance.
func runRestartPolicyConfigOverridesLegacyTest(f *framework.Framework) {
	ginkgo.It("restartPolicyConfig=None suppresses instance recreation requested by the deprecated field", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-rp-config-wins", f.Namespace).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole("role-1").
					WithReplicas(1).
					WithSize(2).
					WithLegacyRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
					WithRestartPolicy(workloadsv1alpha2.RestartPolicyNone).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		instanceName, podUIDs := singleInstancePodUIDs(f, rbg, "role-1", 2)
		waitForInstanceReady(f, instanceName)

		// Target the worker and keep the leader as the survivor. Picking either one
		// at random would make the spec non-deterministic, since leader and worker
		// failures do not necessarily follow the same code path.
		targetPodName := instancePodNameByRole(f, rbg, instanceName, "worker")

		targetPod := &corev1.Pod{}
		gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKey{
			Namespace: f.Namespace, Name: targetPodName,
		}, targetPod)).Should(gomega.Succeed())
		waitForNginxContainerRestart(f, targetPod)

		gomega.Eventually(func() bool {
			pod := &corev1.Pod{}
			if err := f.Client.Get(f.Ctx, client.ObjectKeyFromObject(targetPod), pod); err != nil {
				return false
			}
			if pod.UID != targetPod.UID || pod.Status.Phase != corev1.PodRunning {
				return false
			}
			for _, status := range pod.Status.ContainerStatuses {
				if status.Name == "nginx" {
					return status.Ready && status.State.Running != nil
				}
			}
			return false
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
			"the restarted nginx container should recover in the original Pod")
		waitForInstanceFullyRecovered(f, instanceName)

		// Check after recovery so the instance's readiness gate cannot mask an
		// incorrect Recreate policy. Include the target UID as well as its sibling.
		gomega.Consistently(func() map[string]types.UID {
			return getInstancePodUIDs(f, rbg, instanceName)
		}, 30, 3).Should(gomega.Equal(podUIDs),
			"restartPolicyConfig=None must preserve both the restarted Pod and its sibling")
	})
}

// runLegacyRestartPolicyRecreatesInstanceTest is the v0.7.0 upgrade path end to end:
// a role configured the old way, with only the deprecated restartPolicy string, must
// still recreate the whole instance when a container restarts.
func runLegacyRestartPolicyRecreatesInstanceTest(f *framework.Framework) {
	ginkgo.It("deprecated restartPolicy alone still recreates the whole instance", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-rp-legacy", f.Namespace).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole("role-1").
					WithReplicas(1).
					WithSize(2).
					WithLegacyRestartPolicy(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		instanceName, podUIDs := singleInstancePodUIDs(f, rbg, "role-1", 2)

		// The instance must be Ready before shouldRecreateInstance will act on it.
		waitForInstanceReady(f, instanceName)

		targetPodName := instancePodNameByRole(f, rbg, instanceName, "worker")
		targetPod := &corev1.Pod{}
		gomega.Expect(f.Client.Get(f.Ctx, client.ObjectKey{
			Namespace: f.Namespace, Name: targetPodName,
		}, targetPod)).Should(gomega.Succeed())
		gomega.Expect(utils.RestartNginxContainer(f.Ctx, targetPod)).Should(gomega.Succeed())

		// Every pod in the instance, including the healthy sibling, gets a new UID.
		gomega.Eventually(func() bool {
			current := getInstancePodUIDs(f, rbg, instanceName)
			if len(current) != 2 {
				return false
			}
			for name, oldUID := range podUIDs {
				uid, exists := current[name]
				if !exists || oldUID == uid {
					return false
				}
			}
			return true
		}, utils.Timeout, utils.Interval).Should(gomega.BeTrue(),
			"the whole instance should be recreated with new pod UIDs")
		waitForInstanceFullyRecovered(f, instanceName)
	})
}

// findRole returns the named role from a RBG, failing the spec when absent.
func findRole(rbg *workloadsv1alpha2.RoleBasedGroup, name string) *workloadsv1alpha2.RoleSpec {
	for i := range rbg.Spec.Roles {
		if rbg.Spec.Roles[i].Name == name {
			return &rbg.Spec.Roles[i]
		}
	}
	ginkgo.Fail(fmt.Sprintf("role %s not found in rbg %s", name, rbg.Name))
	return nil
}

// getRoleInstanceForRole returns the first RoleInstance belonging to a role, or nil.
func getRoleInstanceForRole(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, roleName string) *workloadsv1alpha2.RoleInstance {
	riList := &workloadsv1alpha2.RoleInstanceList{}
	if err := f.Client.List(f.Ctx, riList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
			constants.RoleNameLabelKey:  roleName,
		}); err != nil {
		return nil
	}
	if len(riList.Items) == 0 {
		return nil
	}
	return &riList.Items[0]
}

// instancePodNameByRole returns the name of the pod carrying the given
// role label ("leader" or "worker") within an instance. BuildLeaderWorkerRole
// applies these labels through its leader/worker template patches.
func instancePodNameByRole(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup,
	instanceName, podRole string) string {
	podList := &corev1.PodList{}
	gomega.Expect(f.Client.List(f.Ctx, podList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey:        rbg.Name,
			constants.RoleInstanceNameLabelKey: instanceName,
			"role":                             podRole,
		})).Should(gomega.Succeed())

	active := filterActivePods(podList.Items)
	gomega.Expect(active).Should(gomega.HaveLen(1),
		"instance %s should have exactly one %s pod", instanceName, podRole)
	return active[0].Name
}

// singleInstancePodUIDs asserts the role produced exactly one RoleInstance with the
// expected pod count, and returns that instance's name and pod UIDs.
func singleInstancePodUIDs(f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup,
	roleName string, expectedPods int) (string, map[string]types.UID) {
	podList := &corev1.PodList{}
	gomega.Expect(f.Client.List(f.Ctx, podList,
		client.InNamespace(f.Namespace),
		client.MatchingLabels{
			constants.GroupNameLabelKey: rbg.Name,
			constants.RoleNameLabelKey:  roleName,
		})).Should(gomega.Succeed())

	instances := make(map[string]struct{})
	for _, pod := range podList.Items {
		instances[pod.Labels[constants.RoleInstanceNameLabelKey]] = struct{}{}
	}
	gomega.Expect(instances).Should(gomega.HaveLen(1), "role %s should have exactly one instance", roleName)

	var instanceName string
	for name := range instances {
		instanceName = name
	}

	podUIDs := getInstancePodUIDs(f, rbg, instanceName)
	gomega.Expect(podUIDs).Should(gomega.HaveLen(expectedPods))
	return instanceName, podUIDs
}
