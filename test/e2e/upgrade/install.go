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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	wrappersv1 "sigs.k8s.io/rbgs/test/wrappers/v1alpha1"
)

// crdGroupSuffix identifies the CRDs this suite owns. Matching on the group rather
// than a hardcoded list keeps teardown correct when a release adds or drops a kind,
// and it does not catch the LeaderWorkerSet CRDs the cluster is expected to provide:
// those live in leaderworkerset.x-k8s.io.
const crdGroupSuffix = ".workloads.x-k8s.io"

// servingProbeName is the throwaway object waitFromReleaseServing submits. It never
// reaches etcd, so the name only has to stay clear of the fixtures.
const servingProbeName = "up-serving-probe"

// fromRelease records what the suite created so teardown can remove exactly that and
// nothing else.
var fromRelease struct {
	// worktree is the git worktree holding the old chart, empty until checked out.
	worktree string
	// installed is set once `helm install` has been attempted, including when it
	// failed: a partial install still leaves a release and CRDs behind.
	installed bool
}

// requireCleanCluster fails unless the cluster has no rbgs install at all.
//
// This is a stricter precondition than "already runs the release being upgraded
// from", and deliberately so. The suite installs that release itself, which means
// teardown has to delete CRDs; deleting a CRD cascades to every object of that kind
// in the cluster. Proving up front that none of those CRDs existed is what makes the
// teardown safe by construction -- everything it removes was created here.
//
// It is also the check that keeps the suite honest. Against a cluster already running
// the version under test every assertion downstream would pass while proving nothing,
// which is a worse outcome than failing.
//
// Nothing here mutates the cluster. When the precondition fails the operator gets the
// cleanup commands and decides whether to run them.
func requireCleanCluster(f *framework.Framework) {
	for _, bin := range []string{"helm", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			// In CI a missing binary is a broken job, not a reason to report success.
			if os.Getenv("CI") != "" {
				ginkgo.Fail(bin + " not found in PATH; this suite installs the old release with helm " +
					"and takes its chart from a git worktree")
			}
			ginkgo.Skip(bin + " not found in PATH; skipping the upgrade suite")
			return
		}
	}

	if err := requireUpgradeTarget(); err != nil {
		ginkgo.Fail(cleanupInstructions(err.Error()))
		return
	}

	ns := controllerNamespace()
	_, err := f.Clientset.AppsV1().Deployments(ns).Get(f.Ctx, managerDeploymentName, metav1.GetOptions{})
	switch {
	case err == nil:
		ginkgo.Fail(cleanupInstructions(fmt.Sprintf(
			"Deployment %s/%s already exists, so this cluster already runs an rbgs controller",
			ns, managerDeploymentName)))
		return
	case !apierrors.IsNotFound(err):
		ginkgo.Fail(cleanupInstructions(fmt.Sprintf(
			"could not read Deployment %s/%s: %v", ns, managerDeploymentName, err)))
		return
	}

	names, err := listOwnedCRDs(f)
	if err != nil {
		ginkgo.Fail(cleanupInstructions(fmt.Sprintf("could not list CustomResourceDefinitions: %v", err)))
		return
	}
	if len(names) > 0 {
		ginkgo.Fail(cleanupInstructions(fmt.Sprintf(
			"%d %s CRDs are already registered (%s); the suite installs the old CRD bundle itself, so "+
				"reusing whatever is there would make the starting point unknown",
			len(names), strings.TrimPrefix(crdGroupSuffix, "."), strings.Join(names, ", "))))
		return
	}

	if err := requireNoRelease(ns, helmRelease()); err != nil {
		ginkgo.Fail(cleanupInstructions(err.Error()))
	}
}

// installFromRelease installs the release the upgrade starts from.
//
// It runs in BeforeSuite rather than being a documented manual step so that one
// `make test-e2e-upgrade` is the whole workflow, locally and in CI. Automating it is
// only safe because requireCleanCluster already proved there is nothing here to
// overwrite.
func installFromRelease(f *framework.Framework) {
	chart := checkoutFromChart()

	args := []string{
		"install", helmRelease(), chart,
		"--create-namespace", "--namespace", controllerNamespace(),
		// These are the v0.7.0 value paths, which are NOT the current ones: every
		// top-level key moved under controller.* afterwards. The chart has no values
		// schema, so passing current paths here would be accepted as inert keys and
		// would quietly install the chart's own default images instead of the pinned
		// ones.
		"--set", "image.repository=" + fromRepo(),
		"--set", "image.tag=" + fromTag(),
		"--set", "image.pullPolicy=IfNotPresent",
		"--set", "crdUpgrade.repository=" + fromCRDUpgradeRepo(),
		"--set", "crdUpgrade.tag=" + fromTag(),
		"--set", "crdUpgrade.imagePullPolicy=IfNotPresent",
		"--set", "portAllocator.enabled=true",
		"--wait", "--timeout", helmTimeout(),
	}

	ginkgo.By("running helm " + strings.Join(args, " "))
	out, err := exec.Command("helm", args...).CombinedOutput()

	// Recorded before the error is checked, because a helm install that fails partway
	// still leaves the release and its CRDs behind for teardown to remove.
	fromRelease.installed = true

	if err != nil {
		dumpCRDUpgradeJobs(f)
		ginkgo.Fail(fmt.Sprintf("helm install of %s failed: %v\n%s", fromTag(), err, out))
	}
	ginkgo.GinkgoWriter.Printf("helm install output:\n%s\n", out)
}

// checkoutFromChart materializes the chart of the release being upgraded from.
//
// The chart has to come from git: rbgs publishes no Helm repository, so there is
// nothing to `helm pull`, and the chart in the working tree is the one for the
// version under test. Installing with that would not reproduce the old install at
// all, since the values layout changed after v0.7.0.
func checkoutFromChart() string {
	base, err := os.MkdirTemp("", "rbg-upgrade-from-")
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "could not create a temp dir for the old chart")

	// git worktree add requires a path that does not exist yet, so it gets a
	// subdirectory of the temp dir rather than the temp dir itself.
	tree := filepath.Join(base, "src")

	ginkgo.By(fmt.Sprintf("checking out %s into %s", fromGitTag(), tree))
	cmd := exec.Command("git", "worktree", "add", "--detach", tree, fromGitTag())
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		ginkgo.Fail(fmt.Sprintf(
			"git worktree add %s %s failed: %v\n%s\nthe tag has to be present locally; a shallow "+
				"clone needs `git fetch --tags --unshallow` first",
			tree, fromGitTag(), err, out))
	}
	fromRelease.worktree = tree

	chart := filepath.Join(tree, "deploy", "helm", "rbgs")
	_, err = os.Stat(filepath.Join(chart, "Chart.yaml"))
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "tag %s has no chart at %s", fromGitTag(), chart)
	return chart
}

// verifyOnFromRelease checks the install actually landed the release the suite means
// to upgrade from. A wrong tag or a chart that ignored the values would otherwise
// leave a cluster running some other version, and every assertion downstream would
// pass vacuously.
func verifyOnFromRelease(f *framework.Framework) {
	ns := controllerNamespace()
	deploy, err := f.Clientset.AppsV1().Deployments(ns).Get(f.Ctx, managerDeploymentName, metav1.GetOptions{})
	gomega.Expect(err).ToNot(gomega.HaveOccurred(),
		"could not read Deployment %s/%s after installing it", ns, managerDeploymentName)

	image, found := managerImage(deploy)
	gomega.Expect(found).To(gomega.BeTrue(),
		"Deployment %s/%s has no container whose image looks like %q", ns, managerDeploymentName, controllerImageMarker)
	gomega.Expect(image).To(gomega.HaveSuffix(":"+fromTag()),
		"installed controller runs %q, expected the image tagged %q; the chart did not take "+
			"RBGS_FROM_TAG / RBGS_FROM_REPO", image, fromTag())

	// Unannotated on purpose: the error already says whether helm could not be reached,
	// reported a different appVersion, or found no release at all. An annotation naming
	// one of those turns the other two into a wrong diagnosis.
	gomega.Expect(requireReleaseAppVersion(ns, helmRelease(), fromAppVersionPrefix)).To(gomega.Succeed())

	// The one piece of evidence in this suite that an image tag cannot fake: this CRD
	// does not exist in the release being upgraded from, so its appearance later can
	// only come from the upgrade under test.
	exists, err := crdExists(f, warmupCRDName)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "could not check whether CRD %s exists", warmupCRDName)
	gomega.Expect(exists).To(gomega.BeFalse(),
		"CRD %s exists right after installing %s, but it was introduced later; there is nothing to "+
			"upgrade and every assertion in this suite would pass vacuously", warmupCRDName, fromGitTag())

	gomega.Expect(deploymentAvailable(deploy)).To(gomega.BeTrue(),
		"Deployment %s/%s is not Available with at least one ready replica (readyReplicas=%d)",
		ns, managerDeploymentName, deploy.Status.ReadyReplicas)
}

// waitFromReleaseServing waits until the freshly installed controller actually serves
// its webhooks.
//
// `helm install --wait` returns once the Deployment reports Available, which is weaker
// than the webhook server on 9443 accepting connections. The gap is short but real, and
// it lands on the first v1alpha1 write: that goes through the conversion webhook and
// fails with `connection refused` when it loses the race.
//
// The probe is a dry-run create, so nothing is persisted and there is nothing to clean
// up, while the apiserver still runs conversion and validation exactly as it would for
// a real write. Both webhooks declare sideEffects: None, which is what makes a dry-run
// request admissible for them.
func waitFromReleaseServing(f *framework.Framework) {
	probe := wrappersv1.BuildBasicRoleBasedGroup(servingProbeName, f.Namespace).
		WithRoles(
			[]workloadsv1alpha1.RoleSpec{
				wrappersv1.BuildBasicRole("probe").WithReplicas(1).Obj(),
			},
		).Obj()

	ginkgo.By("waiting for the " + fromTag() + " webhooks to serve")
	gomega.Eventually(
		func(g gomega.Gomega) {
			g.Expect(f.Client.Create(f.Ctx, probe.DeepCopy(), client.DryRunAll)).To(gomega.Succeed())
		}, gateTimeout, gateInterval,
	).Should(gomega.Succeed(),
		"the %s controller never accepted a v1alpha1 RoleBasedGroup, so its conversion or validating "+
			"webhook is not serving", fromTag())
}

// teardownFromRelease removes everything the suite installed, so the next run finds
// the clean cluster requireCleanCluster demands.
//
// Every failure here is logged rather than asserted. Teardown runs after the specs
// have already reported, so a failed assertion at this point adds a second failure
// that competes with the finding the run exists to surface -- and a leak is caught
// anyway, with a much clearer message, by the next run's precondition check.
func teardownFromRelease(f *framework.Framework) {
	defer removeFromWorktree()

	if !fromRelease.installed {
		return
	}

	ns := controllerNamespace()
	args := []string{"uninstall", helmRelease(), "--namespace", ns, "--wait", "--timeout", helmTimeout()}
	ginkgo.By("running helm " + strings.Join(args, " "))
	if out, err := exec.Command("helm", args...).CombinedOutput(); err != nil {
		ginkgo.GinkgoWriter.Printf("[teardown] helm uninstall failed: %v\n%s\n", err, out)
	}

	// The chart does not own the CRDs -- they are applied by the crd-upgrade hook Job,
	// so helm uninstall leaves every one of them registered.
	names, err := listOwnedCRDs(f)
	if err != nil {
		ginkgo.GinkgoWriter.Printf("[teardown] could not list CRDs to delete: %v\n", err)
		return
	}
	for _, name := range names {
		crd := newCRDObject()
		crd.SetName(name)
		if err := f.Client.Delete(f.Ctx, crd); err != nil && !apierrors.IsNotFound(err) {
			ginkgo.GinkgoWriter.Printf("[teardown] could not delete CRD %s: %v\n", name, err)
		}
	}

	// A CRD delete is accepted long before it is finished: the apiserver has to reap
	// every object of that kind first. Returning here would let the next run's
	// precondition check see CRDs that are on their way out and report a leak that is
	// not one -- or, worse, let the next install race the reaping.
	deadline := time.Now().Add(gateTimeout)
	for {
		left, err := listOwnedCRDs(f)
		if err != nil {
			ginkgo.GinkgoWriter.Printf("[teardown] could not re-list CRDs while waiting: %v\n", err)
			return
		}
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			ginkgo.GinkgoWriter.Printf(
				"[teardown] CRDs of %s are still registered after %s: %v\n", crdGroupSuffix, gateTimeout, left)
			return
		}
		time.Sleep(gateInterval)
	}
}

func removeFromWorktree() {
	if fromRelease.worktree == "" {
		return
	}
	tree := fromRelease.worktree
	fromRelease.worktree = ""

	cmd := exec.Command("git", "worktree", "remove", "--force", tree)
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		ginkgo.GinkgoWriter.Printf("[teardown] could not remove git worktree %s: %v\n%s\n", tree, err, out)
		return
	}
	if err := os.RemoveAll(filepath.Dir(tree)); err != nil {
		ginkgo.GinkgoWriter.Printf("[teardown] could not remove %s: %v\n", filepath.Dir(tree), err)
	}
}

// listOwnedCRDs returns the registered CRDs of the group this suite installs, sorted.
// It reads unstructured so the suite does not need a scheme for apiextensions.
func listOwnedCRDs(f *framework.Framework) ([]string, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "apiextensions.k8s.io",
		Version: "v1",
		Kind:    "CustomResourceDefinitionList",
	})
	if err := f.Client.List(f.Ctx, list); err != nil {
		return nil, err
	}

	var names []string
	for i := range list.Items {
		if name := list.Items[i].GetName(); strings.HasSuffix(name, crdGroupSuffix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// cleanupInstructions wraps a diagnosis with what to do about it.
func cleanupInstructions(reason string) string {
	return fmt.Sprintf(`this suite installs rbgs %[2]s itself and upgrades it to the version under test,
so it needs a cluster with no rbgs install at all. The precondition was not met:

  %[1]s

Use a throwaway cluster: teardown deletes the %[5]s CRDs, which cascades to
every object of those kinds. To clear an existing install:

  helm uninstall %[3]s --namespace %[4]s
  for c in $(kubectl get crd -o name | grep %[5]s); do kubectl delete "$c"; done

The cluster also needs the LeaderWorkerSet CRDs and these four images reachable from it.
The two being upgraded TO come from the chart's own default values, so they need no
build and no override:

  # upgrading from
  docker pull %[6]s:%[2]s
  docker pull %[7]s:%[2]s

  # upgrading to
  docker pull %[8]s
  docker pull %[9]s

  make test-e2e-upgrade

On kind, kind load docker-image each of them into the cluster first.

To upgrade to a local build instead of what the chart ships:

  make docker-build-controller TAG=e2e-upgrade
  make docker-build-crd-upgrader TAG=e2e-upgrade
  RBGS_TO_TAG=e2e-upgrade make test-e2e-upgrade

Overrides: RBGS_NAMESPACE, RBGS_RELEASE, RBGS_HELM_TIMEOUT, RBGS_FROM_GIT_TAG,
RBGS_FROM_TAG, RBGS_FROM_REPO, RBGS_FROM_CRD_UPGRADE_REPO, RBGS_TO_REPO,
RBGS_TO_TAG, RBGS_CRD_UPGRADE_REPO, RBGS_CRD_UPGRADE_TAG.`,
		reason, fromTag(), helmRelease(), controllerNamespace(),
		strings.TrimPrefix(crdGroupSuffix, "."), fromRepo(), fromCRDUpgradeRepo(),
		toRepo()+":"+toTag(), crdUpgradeRepo()+":"+crdUpgradeTag())
}
