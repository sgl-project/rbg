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

// Package upgrade is a standalone e2e suite that proves a v0.7.0 install can be
// upgraded to the version under test without disturbing already-running RBG pods.
//
// Every other e2e suite installs the version under test from scratch, so none of
// them can catch an upgrade-only regression. The regression this suite exists to
// catch is "upgrading disturbs what is already running", which has four plausible
// causes in this codebase:
//
//   - The inputs to the revision hash change. The hash helper itself only moved
//     between packages, but the RoleInstanceSet template the controller builds is
//     assembled fresh each release: it now carries restartPolicyConfig where v0.7.0
//     carried the restartPolicy string. A template that serializes differently makes
//     the new controller read every stored spec as modified.
//   - A new CRD default. The apiserver applies structural defaults on read, so a new
//     default on a field whose parent object is present silently changes the spec the
//     new controller sees. Phase 4 sees this: the v1alpha1 write path materializes
//     restartPolicyConfig, and the apiserver then fills in the delay fields defaulted
//     inside it.
//   - The v1alpha1 conversion webhook writing a different v1alpha2 shape than
//     v0.7.0's webhook did, which lands on any object created or updated through
//     v1alpha1.
//   - A controller-side default changing, which rewrites the resources derived from an
//     object whose own spec nobody touched. sharedServiceSelection is the live example:
//     v0.7.0 read an unset field as All, the current release resolves it to LeaderOnly,
//     and the shared Service selector is patched in place accordingly.
//
// The first three show up as pod churn. The fourth does not touch a pod at all -- it
// removes the worker endpoints from a Service that keeps its name, its UID and its
// cluster IP -- which is why the assertions reach past pod identity to the pods'
// labels, the Services in front of them and the endpoints behind those.
//
// The assertions are deliberately strict, down to owner generations and
// ControllerRevision names. They are detectors: when one fires, the finding belongs
// in the controller, and the check must not be loosened to accommodate the behavior
// it just caught.
//
// The suite runs as one Ordered container because its phases are a sequence, not
// independent specs: create pre-upgrade fixtures, snapshot them, run the upgrade,
// then assert against the snapshot. It exec's `helm upgrade` itself rather than
// having CI drive two go-test invocations, so the snapshot lives in memory across
// the upgrade instead of being serialized somewhere.
//
// The world the upgrade lands on is deliberately not fully converged: one fixture is
// stuck Pending and another is halted half-way through a rollout, because every other
// fixture is quiet by then and a quiet cluster is the one state an upgrade is least
// likely to arrive in.
// Phase 3 then re-runs the same detectors twice more, once after restarting the
// upgraded controller and once after a second identical `helm upgrade`, so that a
// controller which is only quiet because it has not yet reconciled from cold, or one
// that rewrites specs on every apply, is not read as a clean result.
//
// The cluster must have no rbgs install at all when the suite starts: it installs the
// release it upgrades from itself, from a git worktree of that tag, and removes it
// again on teardown. That precondition is what makes the teardown safe, since
// removing the install means deleting CRDs and so cascading to every object of those
// kinds. A precondition check fails fast with cleanup instructions otherwise, because
// against a cluster already running the version under test every assertion here would
// pass vacuously.
//
// The version upgraded TO is whatever the chart in the working tree ships by default, so
// the images this suite upgrades to are the ones a user gets. That is why it belongs to
// the release gate rather than the per-PR e2e workflow: the chart carries the published
// images only once a release bumps it. RBGS_TO_* overrides the target for a local build.
//
// One value is deliberately not a chart default: portAllocator is enabled, on the v0.7.0
// install and on the upgrade alike, which is how the other e2e workflows install rbgs. So
// this is a feature-enabled upgrade rather than a stock one. What it is not is a feature
// being toggled by the hop, which would put a configuration change inside the interval
// every assertion here attributes to the upgrade.
//
// What this suite does NOT prove:
//   - Only the v0.7.0 -> current single hop. Nothing about v0.6.x -> current.
//   - Nothing about storage version migration: both versions store v1alpha2. If a
//     future release changes the storage version, this suite will not cover it.
//   - Single node, and nothing about leader election. The chart ships
//     controller.replicaCount 2 with --leader-elect on both versions, so the upgrade
//     does replace a two-replica Deployment -- but nothing here observes which replica
//     holds the lease, and the per-start rewrite accounting in recordedRewrites assumes
//     one leader start per rollout. Nothing about multi-node scheduling either, or
//     rollout behavior under real load.
//   - Only the field combinations in fixtures.go. Gang scheduling, GPU/model
//     workloads, PVC-backed roles and large LeaderWorkerSet sizes are excluded.
//   - The observation ends when two samples taken settleDuration apart agree, so it
//     bounds what is seen: a regression that only rolls pods on a later periodic resync
//     would not be caught here.
//   - Nothing about downgrade, and nothing about a v0.7.0 controller reading
//     objects after the new CRDs land.
package upgrade

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/yaml"

	"sigs.k8s.io/rbgs/test/e2e/framework"
)

const (
	managerDeploymentName = "rbgs-controller-manager"
	// controllerImageMarker identifies the manager container by image rather than by
	// name: both charts name the container after the chart, so the name is not a
	// stable contract, while the image repository is what the tag assertion is about.
	controllerImageMarker = "rbgs-controller"

	rbgCRDName    = "rolebasedgroups.workloads.x-k8s.io"
	rbgSetCRDName = "rolebasedgroupsets.workloads.x-k8s.io"
	// warmupCRDName does not exist in v0.7.0. Its absence before the upgrade and
	// presence after it is the only evidence in this suite that cannot be faked by
	// setting an image tag.
	warmupCRDName = "rolebasedgroupwarmups.workloads.x-k8s.io"

	validatingWebhookName = "rbgs-validating-webhook-configuration"

	// fromAppVersionPrefix is what the v0.7.0 chart records as its appVersion.
	fromAppVersionPrefix = "0.7.0"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// controllerNamespace is where the chart installed the controller.
func controllerNamespace() string { return envOr("RBGS_NAMESPACE", "rbgs-system") }

// helmRelease is the Helm release name to upgrade.
func helmRelease() string { return envOr("RBGS_RELEASE", "rbgs") }

// fromGitTag is the git tag whose chart the suite installs before upgrading. The
// chart, not just the images: the values layout changed after v0.7.0, so installing
// with the chart in the working tree would not reproduce the old install.
func fromGitTag() string { return envOr("RBGS_FROM_GIT_TAG", "v0.7.0") }

// fromTag is the controller image tag the suite installs before the upgrade, and
// fromRepo/fromCRDUpgradeRepo are where those images come from. They are overridable
// so a cluster that cannot reach Docker Hub can point at a mirror.
func fromTag() string  { return envOr("RBGS_FROM_TAG", "v0.7.0-888ade7") }
func fromRepo() string { return envOr("RBGS_FROM_REPO", "rolebasedgroup/rbgs-controller") }
func fromCRDUpgradeRepo() string {
	return envOr("RBGS_FROM_CRD_UPGRADE_REPO", "rolebasedgroup/rbgs-upgrade-crd")
}

// chartValues is the subset of the chart's default values this suite reads.
type chartValues struct {
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

// chartDefaults reads the chart's default values once. The error is returned rather
// than asserted because the accessors below are also called while building failure
// messages, where an assertion would replace the real diagnosis with this one.
var chartDefaults = sync.OnceValues(readChartDefaults)

func readChartDefaults() (*chartValues, error) {
	cmd := exec.Command("helm", "show", "values", chartPath())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("helm show values %s failed: %w: %s",
			chartPath(), err, strings.TrimSpace(stderr.String()))
	}

	vals := &chartValues{}
	if err := yaml.Unmarshal(out, vals); err != nil {
		return nil, fmt.Errorf("could not parse the default values of %s: %w", chartPath(), err)
	}
	return vals, nil
}

// chartDefault returns one field of the chart defaults, or "" when they could not be
// read at all. requireUpgradeTarget turns that "" into the underlying error.
func chartDefault(pick func(*chartValues) string) string {
	vals, err := chartDefaults()
	if err != nil {
		return ""
	}
	return pick(vals)
}

// toRepo and toTag identify the controller image to upgrade to, and default to what
// the chart in the working tree ships.
//
// Those defaults are the case worth testing. At release time the chart already carries
// the images being published, so an upgrade with no overrides at all is exactly the
// `helm upgrade` a user will run. RBGS_TO_* exists for a locally built image, which the
// chart cannot know about.
func toRepo() string {
	return envOr("RBGS_TO_REPO", chartDefault(func(v *chartValues) string {
		return v.Controller.Image.Repository
	}))
}

func toTag() string {
	return envOr("RBGS_TO_TAG", chartDefault(func(v *chartValues) string {
		return v.Controller.Image.Tag
	}))
}

func crdUpgradeRepo() string {
	return envOr("RBGS_CRD_UPGRADE_REPO", chartDefault(func(v *chartValues) string {
		return v.CRDUpgrade.Image.Repository
	}))
}

// crdUpgradeTag follows RBGS_TO_TAG before the chart default, because a local build
// tags both images together: setting only RBGS_TO_TAG must not leave the hook Job on a
// published image while the controller runs the local one.
func crdUpgradeTag() string {
	return envOr("RBGS_CRD_UPGRADE_TAG", envOr("RBGS_TO_TAG", chartDefault(func(v *chartValues) string {
		return v.CRDUpgrade.Image.Tag
	})))
}

// requireUpgradeTarget checks the images the upgrade will use are all known, so a
// missing one fails here instead of as a confusing `helm upgrade` error later.
func requireUpgradeTarget() error {
	for _, field := range []struct{ what, value string }{
		{"controller.image.repository (or RBGS_TO_REPO)", toRepo()},
		{"controller.image.tag (or RBGS_TO_TAG)", toTag()},
		{"crdUpgrade.image.repository (or RBGS_CRD_UPGRADE_REPO)", crdUpgradeRepo()},
		{"crdUpgrade.image.tag (or RBGS_CRD_UPGRADE_TAG)", crdUpgradeTag()},
	} {
		if field.value != "" {
			continue
		}
		// The chart is where these come from unless overridden, so a chart that could
		// not be read is the real diagnosis and an empty field is only its symptom.
		if _, err := chartDefaults(); err != nil {
			return err
		}
		return fmt.Errorf("the image to upgrade to is unknown: %s is empty", field.what)
	}
	return nil
}

func helmTimeout() string { return envOr("RBGS_HELM_TIMEOUT", "10m") }

func managerImage(deploy *appsv1.Deployment) (string, bool) {
	containers := deploy.Spec.Template.Spec.Containers
	for _, c := range containers {
		if strings.Contains(c.Image, controllerImageMarker) {
			return c.Image, true
		}
	}
	if len(containers) == 1 {
		return containers[0].Image, true
	}
	return "", false
}

func deploymentAvailable(deploy *appsv1.Deployment) bool {
	if deploy.Status.ReadyReplicas < 1 {
		return false
	}
	for _, cond := range deploy.Status.Conditions {
		if cond.Type == appsv1.DeploymentAvailable {
			return cond.Status == "True"
		}
	}
	return false
}

// helmReleaseInfo is the subset of `helm list -o json` this suite reads.
type helmReleaseInfo struct {
	Name       string `json:"name"`
	AppVersion string `json:"app_version"`
}

// helmReleases lists the releases in a namespace. Stderr is captured because
// exec.Output discards it, and a bare "exit status 1" gives nothing to act on.
func helmReleases(namespace string) ([]helmReleaseInfo, error) {
	cmd := exec.Command("helm", "list", "--namespace", namespace, "--output", "json")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("helm list -n %s failed: %w: %s", namespace, err, strings.TrimSpace(stderr.String()))
	}

	var releases []helmReleaseInfo
	if err := json.Unmarshal(out, &releases); err != nil {
		return nil, fmt.Errorf("could not parse `helm list -n %s -o json` output: %w", namespace, err)
	}
	return releases, nil
}

// requireNoRelease reports an error when the release already exists, which is the
// helm-side half of the clean-cluster precondition: `helm install` would fail on it
// anyway, but failing here says why in terms the operator can act on.
func requireNoRelease(namespace, release string) error {
	releases, err := helmReleases(namespace)
	if err != nil {
		return err
	}
	for _, r := range releases {
		if r.Name == release {
			return fmt.Errorf("helm release %q already exists in namespace %s (appVersion %q)",
				release, namespace, r.AppVersion)
		}
	}
	return nil
}

// requireReleaseAppVersion checks the installed Helm release reports the expected
// appVersion. An image tag check can be satisfied by an operator retagging an image;
// this checks the chart that was actually installed.
func requireReleaseAppVersion(namespace, release, wantPrefix string) error {
	releases, err := helmReleases(namespace)
	if err != nil {
		return err
	}

	for _, r := range releases {
		if r.Name != release {
			continue
		}
		if !strings.HasPrefix(strings.TrimPrefix(r.AppVersion, "v"), wantPrefix) {
			return fmt.Errorf("helm release %q in namespace %s reports appVersion %q, expected one "+
				"starting with %q", release, namespace, r.AppVersion, wantPrefix)
		}
		return nil
	}
	return fmt.Errorf("no helm release named %q in namespace %s; set RBGS_RELEASE / RBGS_NAMESPACE "+
		"if it is installed elsewhere", release, namespace)
}

// crdExists reports whether a CRD is registered. It uses an unstructured read so
// the suite does not need its own scheme for apiextensions.
func crdExists(f *framework.Framework, name string) (bool, error) {
	crd := newCRDObject()
	err := f.Client.Get(f.Ctx, clientObjectKey(name), crd)
	switch {
	case err == nil:
		return true, nil
	case apierrors.IsNotFound(err):
		return false, nil
	default:
		return false, err
	}
}

// repoRoot locates the repository root so the suite can point helm at the chart in
// the working tree, which is the chart for the version under test.
func repoRoot() string {
	_, thisFile, _, ok := runtime.Caller(0)
	gomega.Expect(ok).To(gomega.BeTrue(), "could not resolve the path of this source file")

	root := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	chart := filepath.Join(root, "deploy", "helm", "rbgs", "Chart.yaml")
	_, err := os.Stat(chart)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "expected to find the chart at %s", chart)
	return root
}

func chartPath() string { return filepath.Join(repoRoot(), "deploy", "helm", "rbgs") }
