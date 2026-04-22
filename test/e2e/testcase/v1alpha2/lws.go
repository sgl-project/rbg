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
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	testutils "sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunLeaderWorkerSetWorkloadTestCases(f *framework.Framework) {
	ginkgo.It("create leader-worker role with engine runtime", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildLeaderWorkerRole("role-1").
				WithWorkload("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet").
				WithEngineRuntime([]workloadsv1alpha2.EngineRuntime{{ProfileName: testutils.DefaultEngineRuntimeProfileName}}).
				Obj(),
		}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(testutils.CreatePatioRuntime(f.Ctx, f.Client)).Should(gomega.Succeed())
		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)
	})

	ginkgo.It("update leader-worker role replicas & template", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{
			wrappersv2.BuildLeaderWorkerRole("role-1").
				WithWorkload("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet").
				Obj(),
		}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// update
		updateLabel := map[string]string{"update-label": "new"}
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].Replicas = ptr.To(*rbg.Spec.Roles[0].Replicas + 1)
			rbg.Spec.Roles[0].LeaderWorkerPattern.Template.Labels = updateLabel
		})
		f.ExpectRbgV2Equal(rbg)

		f.ExpectWorkloadV2PodTemplateLabelContains(rbg, rbg.Spec.Roles[0], updateLabel)
	})

	ginkgo.It("lws with rollingUpdate", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole("role-1").
					WithWorkload("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet").
					WithReplicas(2).
					WithRollingUpdate(workloadsv1alpha2.RollingUpdate{
						MaxUnavailable: ptr.To(intstr.FromInt32(1)),
						MaxSurge:       ptr.To(intstr.FromInt32(1)),
					}).Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// update, start rolling update
		updateLabel := map[string]string{"update-label": "new"}
		updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
			rbg.Spec.Roles[0].LeaderWorkerPattern.Template.Labels = updateLabel
		})
		f.ExpectRbgV2Equal(rbg)
	})

	ginkgo.It("lws with restartPolicy", func() {
		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles(
			[]workloadsv1alpha2.RoleSpec{
				wrappersv2.BuildLeaderWorkerRole("role-1").
					WithWorkload("leaderworkerset.x-k8s.io/v1", "LeaderWorkerSet").
					WithReplicas(2).
					WithRestartPolicy(workloadsv1alpha2.RecreateRBGOnPodRestart).
					Obj(),
			}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		gomega.Expect(testutils.DeletePodV2(f.Ctx, f.Client, f.Namespace, rbg.Name)).Should(gomega.Succeed())

		// wait rbg recreate
		f.ExpectRbgV2Equal(rbg)
		f.ExpectRbgV2Condition(rbg, workloadsv1alpha2.RoleBasedGroupRestartInProgress, metav1.ConditionFalse)
	})

	ginkgo.It("leaderWorkerPattern env variables are correctly injected in default RoleInstanceSet mode", func() {
		role := wrappersv2.BuildLeaderWorkerRole("role-1").WithSize(3).Obj()
		// Replace the default nginx container with a busybox one that prints env vars.
		role.LeaderWorkerPattern.Template.Spec.Containers = []corev1.Container{{
			Name:    "env-check",
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "echo CHECK_LEADER_ADDRESS=$RBG_LWP_LEADER_ADDRESS; echo CHECK_WORKER_INDEX=$RBG_LWP_WORKER_INDEX; echo CHECK_GROUP_SIZE=$RBG_LWP_GROUP_SIZE; while true; do sleep 3600; done"},
		}}

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// List all pods for this role.
		podList := &corev1.PodList{}
		gomega.Eventually(func() error {
			return f.Client.List(f.Ctx, podList,
				client.InNamespace(f.Namespace),
				client.MatchingLabels{
					constants.GroupNameLabelKey: rbg.Name,
					constants.RoleNameLabelKey:  role.Name,
				},
			)
		}, testutils.Timeout, testutils.Interval).Should(gomega.Succeed())

		gomega.Expect(podList.Items).To(gomega.HaveLen(3), "expected 3 pods (1 leader + 2 workers)")

		for _, pod := range podList.Items {
			componentName := pod.Labels[constants.ComponentNameLabelKey]
			gomega.Expect(componentName).To(gomega.Or(gomega.Equal("leader"), gomega.Equal("worker")))

			var logs string
			gomega.Eventually(func() error {
				l, err := f.GetPodLogs(pod.Namespace, pod.Name)
				if err != nil {
					return err
				}
				logs = l
				return nil
			}, testutils.Timeout, testutils.Interval).Should(gomega.Succeed())

			// Verify RBG_LWP_GROUP_SIZE is present and equals 3 for all pods.
			gomega.Expect(logs).To(gomega.ContainSubstring("CHECK_GROUP_SIZE=3"),
				fmt.Sprintf("pod %s/%s does not have expected GROUP_SIZE", pod.Namespace, pod.Name))

			// Verify RBG_LWP_LEADER_ADDRESS is present and non-empty.
			addrLine := ""
			for _, line := range strings.Split(logs, "\n") {
				if strings.HasPrefix(line, "CHECK_LEADER_ADDRESS=") {
					addrLine = line
					break
				}
			}
			gomega.Expect(addrLine).NotTo(gomega.BeEmpty(),
				fmt.Sprintf("pod %s/%s does not have LEADER_ADDRESS line", pod.Namespace, pod.Name))
			gomega.Expect(strings.TrimPrefix(addrLine, "CHECK_LEADER_ADDRESS=")).NotTo(gomega.BeEmpty(),
				fmt.Sprintf("pod %s/%s has empty LEADER_ADDRESS value", pod.Namespace, pod.Name))

			if componentName == "leader" {
				gomega.Expect(logs).To(gomega.ContainSubstring("CHECK_WORKER_INDEX=0"),
					fmt.Sprintf("leader pod %s/%s does not have WORKER_INDEX=0", pod.Namespace, pod.Name))
			} else {
				// Worker index must be non-zero (either 1 or 2).
				gomega.Expect(logs).NotTo(gomega.ContainSubstring("CHECK_WORKER_INDEX=0"),
					fmt.Sprintf("worker pod %s/%s should not have WORKER_INDEX=0", pod.Namespace, pod.Name))
				gomega.Expect(logs).To(gomega.MatchRegexp(`CHECK_WORKER_INDEX=[1-9]\d*`),
					fmt.Sprintf("worker pod %s/%s does not have a positive WORKER_INDEX", pod.Namespace, pod.Name))
			}
		}
	})

	ginkgo.It("leaderWorkerPattern runs CPU torchrun distributed job successfully", func() {
		const torchrunTimeout = 10 * time.Minute

		// Shell script that installs torch and then runs a real CPU distributed
		// all-reduce via torchrun, using the three LWP env vars injected by RBG.
		torchScript := `set -e
pip install torch --index-url https://download.pytorch.org/whl/cpu >/dev/null 2>&1 || pip install torch >/dev/null 2>&1
python3 -c "
import os, socket, time, subprocess, sys

addr = os.environ['RBG_LWP_LEADER_ADDRESS']
host = addr.split(':')[0]

for i in range(60):
    try:
        socket.getaddrinfo(host, None)
        print('DNS ready: ' + host)
        break
    except socket.gaierror:
        print('Waiting DNS (' + str(i) + '): ' + host)
        time.sleep(2)
else:
    print('DNS timeout for ' + host)
    sys.exit(1)

with open('/tmp/dist.py', 'w') as f:
    f.write('''
import torch.distributed as dist
import torch
import os
import time

rank = int(os.environ['RANK'])
world_size = int(os.environ['WORLD_SIZE'])
print(f\"[torchrun] Rank {rank}/{world_size} started\")

dist.init_process_group(\"gloo\")
print(f\"[torchrun] Rank {rank}/{world_size} init succeeded\")

tensor = torch.tensor([rank], dtype=torch.float32)
dist.all_reduce(tensor, op=dist.ReduceOp.SUM)
expected = sum(range(world_size))
assert tensor.item() == expected, f\"all_reduce failed: {tensor.item()} != {expected}\"
print(f\"[torchrun] All-reduce passed: {tensor.item()}\")

dist.destroy_process_group()
print(f\"[torchrun] Rank {rank} SUCCESS\")

while True:
    time.sleep(3600)
''')

subprocess.run([
    'torchrun',
    '--nnodes=' + os.environ['RBG_LWP_GROUP_SIZE'],
    '--node_rank=' + os.environ['RBG_LWP_WORKER_INDEX'],
    '--master_addr=' + host,
    '--master_port=29500',
    '/tmp/dist.py'
], check=True)
"`

		role := wrappersv2.BuildLeaderWorkerRole("role-1").WithSize(3).Obj()
		role.LeaderWorkerPattern.Template.Spec.Containers = []corev1.Container{{
			Name:    "torchrun",
			Image:   "python:3.11-slim",
			Command: []string{"sh", "-c"},
			Args:    []string{torchScript},
		}}

		rbg := wrappersv2.BuildBasicRoleBasedGroup("e2e-test", f.Namespace).WithRoles([]workloadsv1alpha2.RoleSpec{role}).Obj()

		f.RegisterDebugFn(func() { dumpDebugInfo(f, rbg) })

		gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
		f.ExpectRbgV2Equal(rbg)

		// Verify all pods logged SUCCESS.
		podList := &corev1.PodList{}
		gomega.Eventually(func() error {
			return f.Client.List(f.Ctx, podList,
				client.InNamespace(f.Namespace),
				client.MatchingLabels{
					constants.GroupNameLabelKey: rbg.Name,
					constants.RoleNameLabelKey:  role.Name,
				},
			)
		}, torchrunTimeout, testutils.Interval).Should(gomega.Succeed())

		gomega.Expect(podList.Items).To(gomega.HaveLen(3), "expected 3 pods (1 leader + 2 workers)")

		for _, pod := range podList.Items {
			gomega.Eventually(func(g gomega.Gomega) {
				logs, err := f.GetPodLogs(pod.Namespace, pod.Name)
				g.Expect(err).Should(gomega.Succeed())
				g.Expect(logs).To(gomega.ContainSubstring("[torchrun] Rank "))
				g.Expect(logs).To(gomega.ContainSubstring("init succeeded"))
				g.Expect(logs).To(gomega.ContainSubstring("[torchrun] All-reduce passed"))
				g.Expect(logs).To(gomega.ContainSubstring("SUCCESS"))
			}, torchrunTimeout, testutils.Interval).Should(gomega.Succeed(),
				fmt.Sprintf("pod %s/%s did not report torchrun SUCCESS", pod.Namespace, pod.Name))
		}
	})
}
