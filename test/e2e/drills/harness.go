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

// Package drills is a standalone e2e suite implementing the KEP-531
// Upgrade / Downgrade Drills (keps/531-engine-runtime-profile-injection,
// Test Plan). It is its own go-test entrypoint because it needs the same
// Ordered, phase-sequenced shape as test/e2e/upgrade while running a
// different experiment with a different starting release, and because a
// second RunSpecs call cannot live in the upgrade package's binary.
//
// The hop it drives is main -> this tree: the drill semantics in the KEP are
// about the KEP-531 field surfaces (engineRuntimes[].component and
// .targetContainers, CERP containerPatches and the frozen-profile admission
// gate), which exist only on one side of that hop. The from-release is
// installed from a git worktree of RBGS_DRILL_FROM_REF (default "main"), so
// the CRDs, the controller and the webhook configuration really are the
// pre-KEP ones and not a stripped-down lookalike.
//
// The suite is a sequence, not a set of independent specs: pre-upgrade writes
// against the old CRDs, the chart two-pass upgrade (the controller image in
// pass one, the CRD in pass two — the KEP-531 upgrade order, with the safe
// rollback drilled between the passes), the new-controller render semantics,
// an admission-disabled window with the controller image rolled back, and the
// re-upgrade with the webhook configuration re-applied by hand. Everything
// cluster-mutating lives behind the RBGS_DRILLS gate: nothing in this package
// touches a cluster unless it was invoked through `make test-e2e-drills` (or
// with RBGS_DRILLS=1 set by hand).
package drills

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"sigs.k8s.io/rbgs/test/e2e/framework"
	"sigs.k8s.io/rbgs/test/e2e/upgrade"
	"sigs.k8s.io/rbgs/test/utils"
)

const (
	// gateTimeout matches the upgrade suite's gates: the controller has to
	// start, elect a leader, mint a CA and patch it into the CRDs and the
	// webhook configurations before the cluster accepts RBG writes again.
	gateTimeout  = 5 * time.Minute
	gateInterval = 2 * time.Second
	// renderTimeout bounds the waits on one role's rendered child and its
	// readiness, which need no CA minting and settle well inside the
	// framework's own e2e budget.
	renderTimeout  = 150 * time.Second
	renderInterval = 250 * time.Millisecond

	// cerpCRDName is the ClusterEngineRuntimeProfile CRD the marker reads.
	cerpCRDName = "clusterengineruntimeprofiles.workloads.x-k8s.io"
	// cerpWebhookEntryName is the CERP validating entry the chart ships. The
	// controller never creates ValidatingWebhookConfigurations — it only
	// patches caBundle — so removing and re-applying this entry is a purely
	// client-side operation both ways.
	cerpWebhookEntryName = "vclusterengineruntimeprofile.kb.io"

	// drillServingProbeName never reaches etcd (dry-run probe), so it only
	// has to stay clear of the fixtures.
	drillServingProbeName = "drill-serving-probe"
)

// drillRelease records what the suite installed so teardown removes exactly
// that and nothing else.
var drillRelease struct {
	// worktree is the git worktree holding the from-release chart, empty
	// until checked out.
	worktree string
	// installed is set once `helm install` has been attempted, including on
	// failure: a partial install still leaves a release and CRDs behind.
	installed bool
}

// drillsSelected is the runtime half of the gating. The make target sets
// RBGS_DRILLS=1; a bare `go test ./test/e2e/drills/` stops in BeforeSuite
// instead of installing a release into whatever cluster the KUBECONFIG points
// at.
func drillsSelected() bool { return os.Getenv("RBGS_DRILLS") == "1" }

// drillFromRef is the git ref whose chart and CRDs the drills upgrade from.
// The default is the branch point of this work: main carries the pre-KEP
// field surfaces, which is what every drill's "old" side needs.
func drillFromRef() string { return upgrade.EnvOr("RBGS_DRILL_FROM_REF", "main") }

// fromChartValues is the subset of the from-release chart's default values
// the drills read. The layout is the current controller.* one; the ref
// defaults to main, whose chart already uses it, and an older ref would fail
// on install rather than install something wrong.
type fromChartValues struct {
	Controller struct {
		Image struct {
			Repository string `json:"repository"`
			Tag        string `json:"tag"`
		} `json:"image"`
	} `json:"controller"`
	CRDUpgrade struct {
		Image struct {
			Repository string `json:"repository"`
			Tag        string `json:"tag"`
		} `json:"image"`
	} `json:"crdUpgrade"`
}

var (
	fromChartPathValue string
	fromChartDefaults  = sync.OnceValues(func() (*fromChartValues, error) {
		if fromChartPathValue == "" {
			return nil, fmt.Errorf("the from-release chart has not been checked out yet")
		}
		cmd := exec.Command("helm", "show", "values", fromChartPathValue)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("helm show values %s failed: %w: %s",
				fromChartPathValue, err, strings.TrimSpace(stderr.String()))
		}
		vals := &fromChartValues{}
		if err := yaml.Unmarshal(out, vals); err != nil {
			return nil, fmt.Errorf("could not parse the default values of %s: %w", fromChartPathValue, err)
		}
		return vals, nil
	})
)

// drillFromTag and friends pin the images the from-install runs. They default
// to the from-release chart's own values so selecting a different from-ref
// needs no overrides; the env vars exist for locally built from-images and for
// registries a cluster cannot reach.
func drillFromTag() string {
	if v := os.Getenv("RBGS_DRILL_FROM_TAG"); v != "" {
		return v
	}
	if vals, err := fromChartDefaults(); err == nil {
		return vals.Controller.Image.Tag
	}
	return ""
}

func drillFromRepo() string {
	if v := os.Getenv("RBGS_DRILL_FROM_REPO"); v != "" {
		return v
	}
	if vals, err := fromChartDefaults(); err == nil {
		return vals.Controller.Image.Repository
	}
	return ""
}

func drillFromCRDUpgradeTag() string {
	if v := os.Getenv("RBGS_DRILL_FROM_CRD_UPGRADE_TAG"); v != "" {
		return v
	}
	if v := os.Getenv("RBGS_DRILL_FROM_TAG"); v != "" {
		return v
	}
	if vals, err := fromChartDefaults(); err == nil {
		return vals.CRDUpgrade.Image.Tag
	}
	return ""
}

func drillFromCRDUpgradeRepo() string {
	if v := os.Getenv("RBGS_DRILL_FROM_CRD_UPGRADE_REPO"); v != "" {
		return v
	}
	if vals, err := fromChartDefaults(); err == nil {
		return vals.CRDUpgrade.Image.Repository
	}
	return ""
}

// requireDrillFrom resolves and checks everything the from-install needs.
func requireDrillFrom() error {
	for _, field := range []struct{ what, value string }{
		{"controller.image.tag (or RBGS_DRILL_FROM_TAG)", drillFromTag()},
		{"controller.image.repository (or RBGS_DRILL_FROM_REPO)", drillFromRepo()},
		{"crdUpgrade.image.tag (or RBGS_DRILL_FROM_CRD_UPGRADE_TAG)", drillFromCRDUpgradeTag()},
		{"crdUpgrade.image.repository (or RBGS_DRILL_FROM_CRD_UPGRADE_REPO)", drillFromCRDUpgradeRepo()},
	} {
		if field.value == "" {
			if _, err := fromChartDefaults(); err != nil {
				return err
			}
			return fmt.Errorf("the from-release image is unknown: %s is empty", field.what)
		}
	}
	return nil
}

// resolveDrillFromRef resolves RBGS_DRILL_FROM_REF to a commit, so the
// failure message can tell the operator exactly what is missing.
func resolveDrillFromRef() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--verify", drillFromRef())
	cmd.Dir = upgrade.RepoRoot()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --verify %s failed in %s: %w\n"+
			"the ref has to exist locally; `git fetch origin %s` first if it does not",
			drillFromRef(), upgrade.RepoRoot(), err, drillFromRef())
	}
	return strings.TrimSpace(string(out)), nil
}

// checkoutDrillFromChart materializes the from-release chart from git. Same
// approach as the upgrade suite's checkoutFromChart: rbgs publishes no Helm
// repository, and the chart in the working tree is the one for the version
// under test.
func checkoutDrillFromChart() (string, error) {
	sha, err := resolveDrillFromRef()
	if err != nil {
		return "", err
	}

	base, err := os.MkdirTemp("", "rbg-drills-from-")
	if err != nil {
		return "", fmt.Errorf("could not create a temp dir for the from-release chart: %w", err)
	}
	// git worktree add requires a path that does not exist yet.
	tree := filepath.Join(base, "src")

	ginkgo.By(fmt.Sprintf("checking out %s (%s) into %s", drillFromRef(), sha, tree))
	cmd := exec.Command("git", "worktree", "add", "--detach", tree, sha)
	cmd.Dir = upgrade.RepoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add %s %s failed: %w\n%s",
			tree, sha, err, out)
	}
	drillRelease.worktree = tree

	chart := filepath.Join(tree, "deploy", "helm", "rbgs")
	if _, err := os.Stat(filepath.Join(chart, "Chart.yaml")); err != nil {
		return "", fmt.Errorf("commit %s has no chart at %s: %w", sha, chart, err)
	}
	return chart, nil
}

// fromChartAppVersion reads the from-release chart's recorded appVersion, the
// evidence verifyDrillFromInstall checks the installed release against.
func fromChartAppVersion(chart string) (string, error) {
	out, err := os.ReadFile(filepath.Join(chart, "Chart.yaml"))
	if err != nil {
		return "", fmt.Errorf("could not read the from-release Chart.yaml: %w", err)
	}
	meta := struct {
		AppVersion string `json:"appVersion"`
	}{}
	if err := yaml.Unmarshal(out, &meta); err != nil {
		return "", fmt.Errorf("could not parse the from-release Chart.yaml: %w", err)
	}
	if meta.AppVersion == "" {
		return "", fmt.Errorf("the from-release chart %s records no appVersion", chart)
	}
	return meta.AppVersion, nil
}

// requireCleanDrillCluster fails unless the cluster has no rbgs install at
// all. Stricter than "runs the from-release" on purpose: the drills delete
// CRDs on teardown, and everything they remove must be something they created.
func requireCleanDrillCluster(f *framework.Framework) {
	for _, bin := range []string{"helm", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			if os.Getenv("CI") != "" {
				ginkgo.Fail(bin + " not found in PATH; this suite installs the from-release " +
					"with helm and takes its chart from a git worktree")
			}
			ginkgo.Skip(bin + " not found in PATH; skipping the drill suite")
			return
		}
	}

	if err := upgrade.RequireUpgradeTarget(); err != nil {
		ginkgo.Fail(drillCleanupInstructions(err.Error()))
		return
	}
	if err := requireDrillFrom(); err != nil {
		ginkgo.Fail(drillCleanupInstructions(err.Error()))
		return
	}

	ns := upgrade.ControllerNamespace()
	_, err := f.Clientset.AppsV1().Deployments(ns).Get(
		f.Ctx, upgrade.ManagerDeploymentName, metav1.GetOptions{})
	switch {
	case err == nil:
		ginkgo.Fail(drillCleanupInstructions(fmt.Sprintf(
			"Deployment %s/%s already exists, so this cluster already runs an rbgs controller",
			ns, upgrade.ManagerDeploymentName)))
		return
	case !apierrors.IsNotFound(err):
		ginkgo.Fail(drillCleanupInstructions(fmt.Sprintf(
			"could not read Deployment %s/%s: %v", ns, upgrade.ManagerDeploymentName, err)))
		return
	}

	names, err := upgrade.ListOwnedCRDs(f)
	if err != nil {
		ginkgo.Fail(drillCleanupInstructions(fmt.Sprintf(
			"could not list CustomResourceDefinitions: %v", err)))
		return
	}
	if len(names) > 0 {
		ginkgo.Fail(drillCleanupInstructions(fmt.Sprintf(
			"%d workloads.x-k8s.io CRDs are already registered (%s); the suite installs the "+
				"old CRD bundle itself, so reusing whatever is there would make the starting "+
				"point unknown", len(names), strings.Join(names, ", "))))
		return
	}

	if err := upgrade.RequireNoRelease(ns, upgrade.HelmRelease()); err != nil {
		ginkgo.Fail(drillCleanupInstructions(err.Error()))
	}
}

// installDrillFromRelease installs the pre-KEP release the drills upgrade from.
func installDrillFromRelease(f *framework.Framework, chart string) {
	args := make([]string, 0, 23+len(upgrade.ImagePullSecretSets()))
	args = append(args,
		"install", upgrade.HelmRelease(), chart,
		"--create-namespace", "--namespace", upgrade.ControllerNamespace(),
		// Every value is passed explicitly on the from-release's own value
		// paths, mirroring the upgrade suite: the chart has no values
		// schema, so a wrong path would be accepted as an inert key and
		// would silently install the chart's defaults instead.
		"--set", "controller.image.repository="+drillFromRepo(),
		"--set", "controller.image.tag="+drillFromTag(),
		"--set", "controller.image.pullPolicy=IfNotPresent",
		"--set", "crdUpgrade.image.repository="+drillFromCRDUpgradeRepo(),
		"--set", "crdUpgrade.image.tag="+drillFromCRDUpgradeTag(),
		"--set", "crdUpgrade.image.pullPolicy=IfNotPresent",
		// Not a chart default, set so the hop does not toggle a feature:
		// the upgrade this suite performs passes the same value, and every
		// assertion attributes the interval's changes to the upgrade.
		"--set", "controller.features.portAllocator.enabled=true",
		"--wait", "--timeout", upgrade.HelmTimeout(),
	)
	args = append(args, upgrade.ImagePullSecretSets()...)

	ginkgo.By("running helm " + strings.Join(args, " "))
	out, err := exec.Command("helm", args...).CombinedOutput()

	// Recorded before the error check: a helm install that fails partway
	// still leaves the release and its CRDs behind for teardown to remove.
	drillRelease.installed = true

	if err != nil {
		upgrade.DumpCRDUpgradeJobs(f)
		ginkgo.Fail(fmt.Sprintf("helm install of %s failed: %v\n%s", drillFromRef(), err, out))
	}
	ginkgo.GinkgoWriter.Printf("helm install output:\n%s\n", out)
}

// verifyDrillFromInstall checks the from-install really landed the release the
// drills mean to start from: the images came from the values, the chart is
// the one that was checked out, and the KEP-531 fields the old CRDs must not
// know are absent — the last one is also the preflight half of the upgrade
// marker, so its absence here is what makes the marker's appearance later
// evidence of this upgrade rather than of anything else.
func verifyDrillFromInstall(f *framework.Framework, chart string) {
	ns := upgrade.ControllerNamespace()
	deploy, err := f.Clientset.AppsV1().Deployments(ns).Get(
		f.Ctx, upgrade.ManagerDeploymentName, metav1.GetOptions{})
	gomega.Expect(err).ToNot(gomega.HaveOccurred(),
		"could not read Deployment %s/%s after installing it", ns, upgrade.ManagerDeploymentName)

	image, found := upgrade.ManagerImage(deploy)
	gomega.Expect(found).To(gomega.BeTrue(),
		"Deployment %s/%s has no container whose image looks like %q",
		ns, upgrade.ManagerDeploymentName, upgrade.ControllerImageMarker)
	gomega.Expect(image).To(gomega.HaveSuffix(":"+drillFromTag()),
		"installed controller runs %q, expected the image tagged %q; the chart did not take "+
			"RBGS_DRILL_FROM_TAG / RBGS_DRILL_FROM_REPO", image, drillFromTag())

	appVersion, err := fromChartAppVersion(chart)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	gomega.Expect(upgrade.RequireReleaseAppVersion(ns, upgrade.HelmRelease(), appVersion)).To(gomega.Succeed())

	present, err := kep531MarkerPresent(f)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "could not check the KEP-531 CRD fields")
	gomega.Expect(present).To(gomega.BeFalse(),
		"the KEP-531 CRD fields are present right after installing %s, but they were introduced by "+
			"this tree; there is nothing to upgrade and every drill below would pass vacuously",
		drillFromRef())

	gomega.Expect(upgrade.DeploymentAvailable(deploy)).To(gomega.BeTrue(),
		"Deployment %s/%s is not Available with at least one ready replica (readyReplicas=%d)",
		ns, upgrade.ManagerDeploymentName, deploy.Status.ReadyReplicas)
}

// waitDrillFromServing waits until the freshly installed controller actually
// accepts RBG writes. `helm install --wait` returns once the Deployment is
// Available, which is weaker than the webhook server accepting connections,
// and the gap lands on the first write as `connection refused`.
func waitDrillFromServing(f *framework.Framework) {
	// The probe is a dry-run create, so nothing is persisted, while the
	// apiserver still runs conversion and validation exactly as it would for
	// a real write. The role carries an inline template because the
	// from-release CRD rejects a role without one, and an explicit
	// RoleInstanceSet workload because the from-release's deprecation gate
	// rejects a v1alpha1 create whose workload defaults to the deprecated
	// StatefulSet — a new use, so even the compatibility mode denies it.
	probe := &unstructured.Unstructured{}
	probe.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "workloads.x-k8s.io", Version: "v1alpha1", Kind: "RoleBasedGroup",
	})
	if err := yaml.Unmarshal([]byte(fmt.Sprintf(`{
		"apiVersion": "workloads.x-k8s.io/v1alpha1",
		"kind": "RoleBasedGroup",
		"metadata": {"name": %q, "namespace": %q},
		"spec": {"roles": [{"name": "probe", "replicas": 1,
			"workload": {"apiVersion": "workloads.x-k8s.io/v1alpha2", "kind": "RoleInstanceSet"},
			"template": {"spec": {"containers": [{"name": "probe", "image": %q}]}}}]}
	}`, drillServingProbeName, f.Namespace, utils.DefaultImage)), &probe.Object); err != nil {
		ginkgo.Fail(fmt.Sprintf("could not build the serving probe: %v", err))
	}

	ginkgo.By("waiting for the " + drillFromRef() + " webhooks to serve")
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(f.Client.Create(f.Ctx, probe.DeepCopy(), client.DryRunAll)).To(gomega.Succeed())
	}, gateTimeout, gateInterval).Should(gomega.Succeed(),
		"the %s controller never accepted a v1alpha1 RoleBasedGroup, so its conversion or "+
			"validating webhook is not serving", drillFromTag())
}

// kep531MarkerPresent reports whether the CRD bundle carries the KEP-531
// field surfaces: engineRuntimes[].component on the RBG CRD and
// spec.containerPatches on the CERP CRD. A missing CRD means absent, never an
// error — the marker only claims what is registered right now.
func kep531MarkerPresent(f *framework.Framework) (bool, error) {
	rbg, err := crdHasEngineRuntimeComponent(f)
	if err != nil || !rbg {
		return rbg, err
	}
	return crdHasContainerPatches(f)
}

func crdHasEngineRuntimeComponent(f *framework.Framework) (bool, error) {
	exists, err := upgrade.CRDExists(f, upgrade.RbgCRDName)
	if err != nil || !exists {
		return false, err
	}
	crd := upgrade.NewCRDObject()
	if err := f.Client.Get(f.Ctx, upgrade.ClientObjectKey(upgrade.RbgCRDName), crd); err != nil {
		return false, err
	}
	return anyVersionHasProperty(f, crd, []string{"roles"}, "engineRuntimes", "component")
}

func crdHasContainerPatches(f *framework.Framework) (bool, error) {
	exists, err := upgrade.CRDExists(f, cerpCRDName)
	if err != nil || !exists {
		return false, err
	}
	crd := upgrade.NewCRDObject()
	if err := f.Client.Get(f.Ctx, upgrade.ClientObjectKey(cerpCRDName), crd); err != nil {
		return false, err
	}

	versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if err != nil || !found {
		return false, err
	}
	for _, item := range versions {
		version, _ := item.(map[string]any)
		// The presence of the key is the question, so read the parent map
		// rather than any nested leaf.
		specProps, found, err := unstructured.NestedMap(
			version, "schema", "openAPIV3Schema", "properties", "spec", "properties")
		if err != nil {
			return false, err
		}
		if !found {
			continue
		}
		if _, ok := specProps["containerPatches"]; ok {
			return true, nil
		}
	}
	return false, nil
}

// anyVersionHasProperty checks whether any served version's schema declares
// the given property under spec.<specProp>.<listName>.items.properties.<prop>.
// It is the drill marker's RBG half: spec.roles[].engineRuntimes[].component.
func anyVersionHasProperty(
	_ *framework.Framework, crd *unstructured.Unstructured,
	specProps []string, listName, prop string,
) (bool, error) {
	versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if err != nil || !found {
		return false, err
	}
	for _, item := range versions {
		version, _ := item.(map[string]any)
		path := []string{"schema", "openAPIV3Schema", "properties", "spec", "properties"}
		for _, p := range specProps {
			path = append(path, p, "items", "properties")
		}
		list, found, err := unstructured.NestedMap(version, append(path, listName)...)
		if err != nil || !found {
			continue
		}
		items, found, err := unstructured.NestedMap(list, "items", "properties")
		if err != nil || !found {
			continue
		}
		if _, ok := items[prop]; ok {
			return true, nil
		}
	}
	return false, nil
}

// teardownDrillRelease removes everything the suite installed. Failures are
// logged rather than asserted: teardown runs after the specs have already
// reported, and a leak is caught with a much clearer message by the next
// run's precondition check.
func teardownDrillRelease(f *framework.Framework) {
	defer removeDrillWorktree()

	if !drillRelease.installed {
		return
	}

	ns := upgrade.ControllerNamespace()
	args := []string{"uninstall", upgrade.HelmRelease(), "--namespace", ns, "--wait", "--timeout", upgrade.HelmTimeout()}
	ginkgo.By("running helm " + strings.Join(args, " "))
	if out, err := exec.Command("helm", args...).CombinedOutput(); err != nil {
		ginkgo.GinkgoWriter.Printf("[teardown] helm uninstall failed: %v\n%s\n", err, out)
	}

	// The chart does not own the CRDs — the crd-upgrade hook Job applies
	// them — so helm uninstall leaves every one of them registered.
	names, err := upgrade.ListOwnedCRDs(f)
	if err != nil {
		ginkgo.GinkgoWriter.Printf("[teardown] could not list CRDs to delete: %v\n", err)
		return
	}
	for _, name := range names {
		crd := upgrade.NewCRDObject()
		crd.SetName(name)
		if err := f.Client.Delete(f.Ctx, crd); err != nil && !apierrors.IsNotFound(err) {
			ginkgo.GinkgoWriter.Printf("[teardown] could not delete CRD %s: %v\n", name, err)
		}
	}

	// A CRD delete is accepted long before it is finished: the apiserver has
	// to reap every object of that kind first. Returning early would let the
	// next run's precondition check report a leak that is not one.
	deadline := time.Now().Add(gateTimeout)
	for {
		left, err := upgrade.ListOwnedCRDs(f)
		if err != nil {
			ginkgo.GinkgoWriter.Printf("[teardown] could not re-list CRDs while waiting: %v\n", err)
			return
		}
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			ginkgo.GinkgoWriter.Printf(
				"[teardown] CRDs of %s are still registered after %s: %v\n",
				upgrade.CRDGroupSuffix, gateTimeout, left)
			return
		}
		time.Sleep(gateInterval)
	}
}

func removeDrillWorktree() {
	if drillRelease.worktree == "" {
		return
	}
	tree := drillRelease.worktree
	drillRelease.worktree = ""

	cmd := exec.Command("git", "worktree", "remove", "--force", tree)
	cmd.Dir = upgrade.RepoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		ginkgo.GinkgoWriter.Printf("[teardown] could not remove git worktree %s: %v\n%s\n", tree, err, out)
		return
	}
	if err := os.RemoveAll(filepath.Dir(tree)); err != nil {
		ginkgo.GinkgoWriter.Printf("[teardown] could not remove %s: %v\n", filepath.Dir(tree), err)
	}
}

// cleanupProfiles deletes the cluster-scoped profiles the drills created. The
// frozen-profile gate rejects a delete while a live reference exists, so this
// runs only after the fixture namespace is gone and retries until the
// references have been reaped. Failures are logged, not asserted: teardown
// runs after the specs have already reported, and the next run's precondition
// check catches a leak with a much clearer message.
func cleanupProfiles(f *framework.Framework, names ...string) {
	for _, name := range names {
		deadline := time.Now().Add(gateTimeout)
		for {
			err := f.Client.Delete(f.Ctx, cerpObject(name))
			if err == nil || apierrors.IsNotFound(err) {
				break
			}
			if time.Now().After(deadline) {
				ginkgo.GinkgoWriter.Printf(
					"[teardown] could not delete profile %s after %s: %v (the frozen-profile gate "+
						"may still see a live reference to it)\n", name, gateTimeout, err)
				break
			}
			time.Sleep(gateInterval)
		}
	}
}

// drillCleanupInstructions wraps a diagnosis with what to do about it.
func drillCleanupInstructions(reason string) string {
	return fmt.Sprintf(`this suite installs rbgs from %[2]s itself, upgrades it to the version under
test, and rolls the controller back and forward inside that window, so it needs a
cluster with no rbgs install at all. The precondition was not met:

  %[1]s

Use a throwaway cluster: teardown deletes the %[5]s CRDs, which cascades to
every object of those kinds. To clear an existing install:

  helm uninstall %[3]s --namespace %[4]s
  for c in $(kubectl get crd -o name | grep %[5]s); do kubectl delete "$c"; done

The cluster also needs the LeaderWorkerSet CRDs and four images reachable from
it. The two the suite upgrades TO come from the chart's own default values:

  # drills start from
  docker pull %[6]s:%[7]s
  docker pull %[8]s:%[7]s

  # drills upgrade to
  docker pull %[9]s
  docker pull %[10]s

  RBGS_DRILLS=1 make test-e2e-drills

On kind, kind load docker-image each of them into the cluster first.

To upgrade to a local build instead of what the chart ships:

  make docker-build-controller TAG=e2e-drills
  make docker-build-crd-upgrader TAG=e2e-drills
  RBGS_DRILLS=1 RBGS_TO_TAG=e2e-drills make test-e2e-drills

Overrides: RBGS_DRILLS (the gate), RBGS_DRILL_FROM_REF (default main),
RBGS_DRILL_FROM_TAG, RBGS_DRILL_FROM_REPO, RBGS_DRILL_FROM_CRD_UPGRADE_REPO,
RBGS_DRILL_FROM_CRD_UPGRADE_TAG, RBGS_NAMESPACE, RBGS_RELEASE,
RBGS_HELM_TIMEOUT, RBGS_TO_REPO, RBGS_TO_TAG, RBGS_CRD_UPGRADE_REPO,
RBGS_CRD_UPGRADE_TAG, RBGS_IMAGE_PULL_SECRET (a dockerconfigjson Secret in the
controller namespace, for clusters whose nodes cannot pull anonymously).`,
		reason, drillFromRef(), upgrade.HelmRelease(), upgrade.ControllerNamespace(),
		strings.TrimPrefix(upgrade.CRDGroupSuffix, "."),
		drillFromRepo(), drillFromTag(), drillFromCRDUpgradeRepo(),
		upgrade.ToRepo()+":"+upgrade.ToTag(), upgrade.CrdUpgradeRepo()+":"+upgrade.CrdUpgradeTag())
}

// swapControllerImage rolls the controller Deployment onto the given image tag
// and waits until the swapped install serves again: rollout complete, the
// new process's CA patched into the conversion CRDs and both webhook
// configurations, and conversion round-tripping through the live object the
// probe names. These are the upgrade suite's gates, reused: each one is a
// failure mode that looks like a real regression when it hits.
func swapControllerImage(f *framework.Framework, tag, conversionProbeName string) {
	ns := upgrade.ControllerNamespace()
	gomega.Eventually(func(g gomega.Gomega) {
		deploy := &appsv1.Deployment{}
		g.Expect(f.Client.Get(f.Ctx,
			client.ObjectKey{Namespace: ns, Name: upgrade.ManagerDeploymentName}, deploy)).To(gomega.Succeed())

		swapped := false
		for i := range deploy.Spec.Template.Spec.Containers {
			c := &deploy.Spec.Template.Spec.Containers[i]
			if !strings.Contains(c.Image, upgrade.ControllerImageMarker) {
				continue
			}
			// Only the colon after the last slash separates the tag from the
			// repository; an earlier colon belongs to a registry port
			// (localhost:5000/...).
			repo := c.Image
			if colon := strings.LastIndex(c.Image, ":"); colon > strings.LastIndex(c.Image, "/") {
				if c.Image[colon+1:] == tag {
					return // already on the requested image
				}
				repo = c.Image[:colon]
			}
			c.Image = repo + ":" + tag
			swapped = true
		}
		if !swapped {
			ginkgo.Fail(fmt.Sprintf(
				"no container of %s/%s runs an image containing %q, so the image cannot be swapped",
				ns, upgrade.ManagerDeploymentName, upgrade.ControllerImageMarker))
		}
		g.Expect(f.Client.Update(f.Ctx, deploy)).To(gomega.Succeed())
	}, gateTimeout, gateInterval).Should(gomega.Succeed())

	upgrade.WaitControllerRolledOut(f, tag)
	// The new process mints its own certificate and patches it everywhere on
	// startup; until it has, every RBG write is rejected in ways that look
	// like real regressions.
	upgrade.WaitCRDConversionCABundle(f)
	upgrade.WaitValidatingWebhookCABundle(f)
	upgrade.WaitMutatingWebhookCABundle(f)
	upgrade.WaitConversionActuallyWorks(f, conversionProbeName)
}

// removeCERPWebhookEntry takes the CERP validating entry out of the live
// ValidatingWebhookConfiguration. With that entry gone, CERP updates and
// deletes reach etcd unvalidated — the admission-disabled rollback window of
// KEP-531's drill. The controller never (re-)creates webhook configurations,
// so the removal is what makes the window, and only a manual re-apply ends it.
func removeCERPWebhookEntry(f *framework.Framework) {
	vwc := &admissionv1.ValidatingWebhookConfiguration{}
	gomega.Expect(f.Client.Get(f.Ctx, upgrade.ClientObjectKey(upgrade.ValidatingWebhookName), vwc)).To(gomega.Succeed())

	var kept []admissionv1.ValidatingWebhook
	removed := 0
	for _, wh := range vwc.Webhooks {
		if wh.Name == cerpWebhookEntryName {
			removed++
			continue
		}
		kept = append(kept, wh)
	}
	gomega.Expect(removed).To(gomega.Equal(1),
		"expected exactly one %q entry in %s, found %d; the upgrade under test changed the "+
			"webhook configuration and this drill needs revisiting",
		cerpWebhookEntryName, upgrade.ValidatingWebhookName, removed)

	vwc.Webhooks = kept
	gomega.Expect(f.Client.Update(f.Ctx, vwc)).To(gomega.Succeed())

	restored := &admissionv1.ValidatingWebhookConfiguration{}
	gomega.Expect(f.Client.Get(f.Ctx, upgrade.ClientObjectKey(upgrade.ValidatingWebhookName), restored)).To(gomega.Succeed())
	gomega.Expect(restored.Webhooks).To(gomega.HaveLen(3),
		"%s should carry three entries with the CERP one removed", upgrade.ValidatingWebhookName)
	for _, wh := range restored.Webhooks {
		gomega.Expect(wh.Name).ToNot(gomega.Equal(cerpWebhookEntryName))
	}
}

// reapplyValidatingWebhookConfiguration restores the validating webhook
// configuration from the release artifacts: the chart of the version under
// test, helm-rendered with the same release name and namespace as the
// installed one. config/webhook/manifests.yaml is deliberately not used — it
// is kustomize-flavored with placeholders and not applicable as-is.
//
// The rendered entry carries an empty caBundle on purpose (the chart ships it
// that way); the running controller's WebhookCertReconciler watches this exact
// object by name and patches its caBundle on the next event, which the caller
// waits for through the upgrade suite's gate.
func reapplyValidatingWebhookConfiguration(f *framework.Framework) {
	args := []string{
		"template", upgrade.HelmRelease(), upgrade.ChartPath(),
		"--namespace", upgrade.ControllerNamespace(),
	}
	ginkgo.By("running helm " + strings.Join(args, " "))
	cmd := exec.Command("helm", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		ginkgo.Fail(fmt.Sprintf("helm template failed: %v: %s", err, strings.TrimSpace(stderr.String())))
	}

	rendered := splitManifestDocs(string(out))
	want := upgrade.ValidatingWebhookName
	var found string
	for _, doc := range rendered {
		head := struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}{}
		if err := yaml.Unmarshal([]byte(doc), &head); err != nil {
			continue
		}
		if head.Kind == "ValidatingWebhookConfiguration" && head.Metadata.Name == want {
			found = doc
			break
		}
	}
	if found == "" {
		ginkgo.Fail(fmt.Sprintf(
			"the rendered chart carries no ValidatingWebhookConfiguration named %s; the release "+
				"artifact is not where this drill expects it", want))
	}

	renderedConf := &admissionv1.ValidatingWebhookConfiguration{}
	gomega.Expect(yaml.Unmarshal([]byte(found), renderedConf)).To(gomega.Succeed())
	gomega.Expect(renderedConf.Webhooks).To(gomega.HaveLen(4),
		"the rendered release artifact should carry four validating entries")
	renderedNames := make([]string, 0, len(renderedConf.Webhooks))
	for _, wh := range renderedConf.Webhooks {
		renderedNames = append(renderedNames, wh.Name)
	}
	sort.Strings(renderedNames)
	gomega.Expect(renderedNames).To(gomega.ContainElement(cerpWebhookEntryName),
		"the rendered release artifact does not carry the %q entry the window removed", cerpWebhookEntryName)

	live := &admissionv1.ValidatingWebhookConfiguration{}
	gomega.Expect(f.Client.Get(f.Ctx, upgrade.ClientObjectKey(want), live)).To(gomega.Succeed())
	live.Webhooks = renderedConf.Webhooks
	gomega.Expect(f.Client.Update(f.Ctx, live)).To(gomega.Succeed())
}

// splitManifestDocs splits a multi-document YAML stream on its separators, so
// one resource can be picked out of everything a chart renders.
func splitManifestDocs(stream string) []string {
	var docs []string
	for _, doc := range strings.Split(stream, "\n---") {
		if strings.TrimSpace(strings.TrimPrefix(doc, "\n")) == "" {
			continue
		}
		docs = append(docs, strings.TrimPrefix(doc, "\n"))
	}
	return docs
}

// jsonBytes marshals v once, for the RawExtension-carrying patch shapes; a
// failure here is a programming error in the fixture, so it aborts the suite.
func jsonBytes(v any) []byte {
	raw, err := json.Marshal(v)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	return raw
}
