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

package upgrade

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/podcache"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/utils"
)

// RunPodCacheSpecs extends the upgrade suite only for its source-built cache target.
func RunPodCacheSpecs(f *framework.Framework) {
	ginkgo.It("[pod-cache] completes and times out Warmup actions with filtered caching", func() {
		requirePodCacheUpgrade()
		node := readyNode(f)
		completed := newNodeWarmup("up-cache-warmup", f.Namespace, node)
		completed.Spec.TargetNodes.ImagePreload = &workloadsv1alpha2.ImagePreloadAction{
			Images: []string{utils.DefaultImage},
		}
		createWarmup(f, completed)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(f.Client.Get(f.Ctx, client.ObjectKeyFromObject(completed), completed)).To(gomega.Succeed())
			g.Expect(completed.Status.Phase).To(gomega.Equal(workloadsv1alpha2.WarmupJobPhaseCompleted))
			g.Expect(completed.Status.Succeeded).To(gomega.Equal(int32(1)))
		}, utils.Timeout, utils.Interval).Should(gomega.Succeed())
		pods := &corev1.PodList{}
		gomega.Expect(f.Client.List(f.Ctx, pods, client.InNamespace(f.Namespace),
			client.MatchingLabels{constants.WarmupNameLabelKey: completed.Name})).To(gomega.Succeed())
		gomega.Expect(pods.Items).To(gomega.HaveLen(1))
		gomega.Expect(pods.Items[0].Labels).To(gomega.HaveKeyWithValue(constants.ManagedLabelKey, constants.ManagedLabelValue))
		gomega.Expect(pods.Items[0].Status.Phase).To(gomega.Equal(corev1.PodSucceeded))

		const timeoutSeconds int64 = 30
		timedOut := newNodeWarmup("up-cache-warmup-timeout", f.Namespace, node)
		timedOut.Spec.Policies = &workloadsv1alpha2.WarmupPolicies{GlobalTimeoutSeconds: ptr.To(timeoutSeconds)}
		timedOut.Spec.TargetNodes.CustomizedAction = &workloadsv1alpha2.CustomizedAction{
			Containers: []corev1.Container{{
				Name:            "wait",
				Image:           utils.DefaultImage,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"sleep", strconv.FormatInt(2*timeoutSeconds, 10)},
			}},
		}
		createWarmup(f, timedOut)
		gomega.Eventually(func(g gomega.Gomega) {
			pods := &corev1.PodList{}
			g.Expect(f.Client.List(f.Ctx, pods, client.InNamespace(f.Namespace),
				client.MatchingLabels{constants.WarmupNameLabelKey: timedOut.Name})).To(gomega.Succeed())
			g.Expect(pods.Items).To(gomega.HaveLen(1))
			g.Expect(pods.Items[0].Status.Phase).To(gomega.Equal(corev1.PodRunning))
			g.Expect(pods.Items[0].Labels).To(gomega.HaveKeyWithValue(constants.ManagedLabelKey, constants.ManagedLabelValue))
		}, utils.Timeout, utils.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(f.Client.Get(f.Ctx, client.ObjectKeyFromObject(timedOut), timedOut)).To(gomega.Succeed())
			g.Expect(timedOut.Status.Phase).To(gomega.Equal(workloadsv1alpha2.WarmupJobPhaseFailed))
			g.Expect(timedOut.Status.Conditions).To(gomega.ContainElement(gomega.And(
				gomega.HaveField("Type", "Failed"), gomega.HaveField("Status", metav1.ConditionTrue),
				gomega.HaveField("Reason", "GlobalTimeoutExceeded"),
			)))
		}, utils.Timeout, utils.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("[pod-cache] switches cache scope in both directions without disturbing Pods", func() {
		requirePodCacheUpgrade()
		before := waitQuiesced(f)
		for _, scope := range []string{podcache.ScopeAll, podcache.ScopeFiltered} {
			ginkgo.By("switching Pod cache scope to " + scope)
			runHelmUpgradeWithScope(f, scope)
			after := waitQuiesced(f)
			assertWorkloadsUnchanged(f, before, after)
			before = after
		}
	})

	ginkgo.It("[pod-cache] reports a migration command failure without failing the upgrade", func() {
		requirePodCacheUpgrade()
		before := waitQuiesced(f)
		job := runHelmUpgradeWithScope(f, podcache.ScopeFiltered, "--set", "podLabelMigration.timeout=1ns")
		gomega.Expect(job).NotTo(gomega.BeNil())
		gomega.Expect(podLabelMigrationLogs(f, job.Name)).To(gomega.ContainSubstring("WARNING: Pod label migration failed"))
		assertWorkloadsUnchanged(f, before, waitQuiesced(f))
	})

	ginkgo.It("[pod-cache] stops the upgrade when the migration hook cannot start", func() {
		requirePodCacheUpgrade()
		before := waitQuiesced(f)
		beforePods, err := managerPodNames(f)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		beforeJobs := podLabelMigrationJobs(f)
		nodes, err := f.Clientset.CoreV1().Nodes().List(f.Ctx, metav1.ListOptions{LabelSelector: unschedulableNodeLabel + "=true"})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(nodes.Items).To(gomega.BeEmpty(), "the hook's test node selector must be unschedulable")

		// A Job deadline bounds the test while Helm waits for the unschedulable hook.
		selector := strings.ReplaceAll(unschedulableNodeLabel, ".", `\.`)
		args := []string{
			"upgrade", helmRelease(), chartPath(), "--namespace", controllerNamespace(),
			"--set", "crdUpgrade.enabled=false,podLabelMigration.enabled=true,podLabelMigration.activeDeadlineSeconds=15",
			"--set-string", "controller.nodeSelector." + selector + "=true",
			"--wait", "--timeout", "90s",
		}
		ginkgo.By("running an upgrade with an unschedulable migration hook")
		out, err := exec.Command("helm", args...).CombinedOutput()
		gomega.Expect(err).To(gomega.HaveOccurred(), "Helm should fail when the hook Job cannot start: %s", out)
		var failedJobs []batchv1.Job
		for _, job := range listPodLabelMigrationJobs(f) {
			if !beforeJobs[string(job.UID)] {
				failedJobs = append(failedJobs, job)
			}
		}
		gomega.Expect(failedJobs).To(gomega.HaveLen(1), "the failed upgrade must reach the migration hook")
		job := failedJobs[0]
		gomega.Expect(job.Spec.Template.Spec.NodeSelector).To(gomega.HaveKeyWithValue(unschedulableNodeLabel, "true"))
		gomega.Expect(job.Status.Conditions).To(gomega.ContainElement(gomega.And(
			gomega.HaveField("Type", batchv1.JobFailed), gomega.HaveField("Status", corev1.ConditionTrue),
			gomega.HaveField("Reason", "DeadlineExceeded"),
		)))
		afterPods, err := managerPodNames(f)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(afterPods).To(gomega.Equal(beforePods), "the controller must not roll out after a failed pre-upgrade hook")

		runHelmUpgradeWithScope(f, podcache.ScopeFiltered)
		_, err = f.Clientset.CoreV1().ServiceAccounts(controllerNamespace()).Get(f.Ctx, job.Spec.Template.Spec.ServiceAccountName, metav1.GetOptions{})
		gomega.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue(), "a successful retry should remove the temporary hook RBAC")
		assertWorkloadsUnchanged(f, before, waitQuiesced(f))
	})
}

// Check an immediate rollback, before later specs change the upgraded workloads.
func runPodCacheRollbackSpec(f *framework.Framework) {
	ginkgo.It("[pod-cache] keeps Pods visible when rolling back to the previous release", func() {
		requirePodCacheUpgrade()
		before := waitQuiesced(f)
		revision := firstHelmRevision()
		restored := false
		ginkgo.DeferCleanup(func() {
			if !restored {
				runHelmUpgradeWithScope(f, podcache.ScopeFiltered)
			}
		})
		args := []string{"rollback", helmRelease(), strconv.Itoa(revision), "--namespace", controllerNamespace(), "--wait", "--timeout", helmTimeout()}
		ginkgo.By("running helm " + fmt.Sprint(args))
		out, err := exec.Command("helm", args...).CombinedOutput()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "helm rollback failed: %s", out)
		waitControllerRolledOut(f, fromTag())
		afterRollback := waitQuiesced(f)
		assertPodsUnchanged(before, afterRollback)
		runHelmUpgradeWithScope(f, podcache.ScopeFiltered)
		assertPodsUnchanged(afterRollback, waitQuiesced(f))
		restored = true
	})
}

func runHelmUpgradeWithScope(f *framework.Framework, scope string, extraSets ...string) *batchv1.Job {
	args := append([]string{"--set", "controller.podCacheScope=" + scope}, extraSets...)
	job := runHelmUpgrade(f, args...)
	waitControllerRolledOut(f, toTag())
	return job
}

func requirePodCacheUpgrade() {
	if !podLabelMigrationEnabled() {
		ginkgo.Skip("only the source-built Pod cache upgrade runs these checks")
	}
}

func assertWorkloadsUnchanged(f *framework.Framework, before, after map[string]RBGSnapshot) {
	fs := &findings{}
	checkSnapshotDiff(fs, f, before, after, recordedRewrites{})
	fs.report()
}

func assertPodsUnchanged(before, after map[string]RBGSnapshot) {
	fs := &findings{}
	checkSameRBGSet(fs, before, after)
	checkNoPodChurn(fs, before, after)
	checkNoRestarts(fs, before, after)
	checkPodMetadataStable(fs, before, after)
	checkStillReady(fs, before, after)
	fs.report()
}

func firstHelmRevision() int {
	out, err := exec.Command("helm", "history", helmRelease(), "--namespace", controllerNamespace(), "--output", "json").Output()
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "could not read Helm release history")
	var history []struct {
		Revision int `json:"revision"`
	}
	gomega.Expect(json.Unmarshal(out, &history)).To(gomega.Succeed())
	first := 0
	for _, entry := range history {
		if entry.Revision > 0 && (first == 0 || entry.Revision < first) {
			first = entry.Revision
		}
	}
	gomega.Expect(first).To(gomega.BeNumerically(">", 0), "Helm release has no original revision")
	return first
}

func readyNode(f *framework.Framework) string {
	nodes, err := f.Clientset.CoreV1().Nodes().List(f.Ctx, metav1.ListOptions{})
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	for _, node := range nodes.Items {
		for _, condition := range node.Status.Conditions {
			if !node.Spec.Unschedulable && condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				return node.Name
			}
		}
	}
	ginkgo.Fail("no ready schedulable node for Warmup")
	return ""
}

func newNodeWarmup(name, namespace, node string) *workloadsv1alpha2.RoleBasedGroupWarmup {
	return &workloadsv1alpha2.RoleBasedGroupWarmup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: workloadsv1alpha2.RoleBasedGroupWarmupSpec{
			TargetNodes: &workloadsv1alpha2.TargetNodes{NodeNames: []string{node}},
		},
	}
}

func createWarmup(f *framework.Framework, warmup *workloadsv1alpha2.RoleBasedGroupWarmup) {
	gomega.Expect(f.Client.Create(f.Ctx, warmup)).To(gomega.Succeed())
	ginkgo.DeferCleanup(func() {
		gomega.Expect(client.IgnoreNotFound(f.Client.Delete(f.Ctx, warmup))).To(gomega.Succeed())
	})
}
