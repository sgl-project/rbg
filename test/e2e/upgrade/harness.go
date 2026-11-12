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

// This file is the exported surface of the upgrade harness. The KEP-531
// upgrade/downgrade drill suite (test/e2e/drills) reuses this suite's
// readiness gates, preflight accessors and install/teardown primitives
// instead of duplicating them: those helpers encode the certificate and
// rollout failure modes that took the upgrade suite debugging time to find,
// and a copy would drift. Everything here is a thin alias of the unexported
// original; the drill suite only reaches for the pieces it needs.
//
// Nothing in this file may call fromProfile()/fromGitTag(): those describe the
// release-tag hops this suite upgrades from, and the drills start from the
// branch point of this work instead, with their own install path.

import (
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/test/e2e/framework"
)

// Installed-object names. Exported because the drills patch the controller
// Deployment's image and edit the ValidatingWebhookConfiguration, and a
// hardcoded copy of a name the chart owns would drift silently.
const (
	ManagerDeploymentName = managerDeploymentName
	// ControllerImageMarker identifies the manager container by image rather
	// than by name: the name is not a stable contract, the repository is.
	ControllerImageMarker = controllerImageMarker

	ValidatingWebhookName = validatingWebhookName
	MutatingWebhookName   = mutatingWebhookName

	RbgCRDName    = rbgCRDName
	RbgSetCRDName = rbgSetCRDName
	WarmupCRDName = warmupCRDName
	RisCRDName    = risCRDName
)

// Env accessors and chart locations.
func EnvOr(key, fallback string) string { return envOr(key, fallback) }
func ControllerNamespace() string       { return controllerNamespace() }
func HelmRelease() string               { return helmRelease() }
func HelmTimeout() string               { return helmTimeout() }
func RepoRoot() string                  { return repoRoot() }
func ChartPath() string                 { return chartPath() }
func ToRepo() string                    { return toRepo() }
func ToTag() string                     { return toTag() }
func CrdUpgradeRepo() string            { return crdUpgradeRepo() }
func CrdUpgradeTag() string             { return crdUpgradeTag() }

// ImagePullSecretSets returns the `--set global.imagePullSecrets[0]=…`
// argument pair for clusters whose nodes cannot pull the controller and
// crd-upgrader images anonymously (a private registry). RBGS_IMAGE_PULL_SECRET
// names a kubernetes.io/dockerconfigjson Secret that already exists in the
// controller namespace; unset means no secret is wired and pulls stay
// anonymous.
func ImagePullSecretSets() []string { return imagePullSecretSets() }

func RequireUpgradeTarget() error                           { return requireUpgradeTarget() }
func ManagerImage(deploy *appsv1.Deployment) (string, bool) { return managerImage(deploy) }
func DeploymentAvailable(deploy *appsv1.Deployment) bool    { return deploymentAvailable(deploy) }

// CRD helpers. Unstructured reads, so the caller needs no apiextensions scheme.
func NewCRDObject() *unstructured.Unstructured { return newCRDObject() }

func ClientObjectKey(name string) client.ObjectKey { return clientObjectKey(name) }

func CRDExists(f *framework.Framework, name string) (bool, error) { return crdExists(f, name) }

func ListOwnedCRDs(f *framework.Framework) ([]string, error) { return listOwnedCRDs(f) }

// Helm introspection.
func RequireNoRelease(namespace, release string) error {
	return requireNoRelease(namespace, release)
}

func RequireReleaseAppVersion(namespace, release, wantPrefix string) error {
	return requireReleaseAppVersion(namespace, release, wantPrefix)
}

// Release management. RunHelmUpgrade upgrades the release in place to the
// chart in the working tree, with the images from the chart defaults or the
// RBGS_TO_* / RBGS_CRD_UPGRADE_* overrides. RunHelmUpgradeWith appends extra
// --set overrides first: the drill suite runs it twice for KEP-531's chart
// two-pass upgrade order — pass one with crdUpgrade.enabled=false (controller
// image only), pass two with the defaults (CRD only, the controller image
// already matching so Helm does not restart it).
func RunHelmUpgrade(f *framework.Framework) { runHelmUpgrade(f) }

func RunHelmUpgradeWith(f *framework.Framework, extraSets ...string) {
	runHelmUpgradeWith(f, extraSets...)
}

// ManagerPodNames returns the sorted names of the live controller pods. The
// drill suite compares them across a pass-two upgrade: the controller image
// is unchanged between the passes, so identical names are the evidence the
// CRD-only pass did not restart it.
func ManagerPodNames(f *framework.Framework) ([]string, error) {
	return managerPodNames(f)
}

func DumpCRDUpgradeJobs(f *framework.Framework) { dumpCRDUpgradeJobs(f) }

// WaitForUpgradeReady runs the five ordered readiness gates with the caller's
// new-CRD-bundle marker. markerPresent must report a missing CRD as absent,
// never an error, and the caller must have proved the marker absent before
// upgrading — otherwise its appearance proves nothing.
func WaitForUpgradeReady(
	f *framework.Framework, conversionProbeName, markerDescription string,
	markerPresent func(*framework.Framework) (bool, error),
) {
	waitForUpgradeReadyFor(f, conversionProbeName, bundleMarker{
		description: markerDescription, present: markerPresent,
	})
}

// Image-swap gates. WaitControllerRolledOut requires the Deployment to be
// fully rolled out AND to run the given tag: Available alone is satisfied by
// the pods this is replacing. The drill suite uses it for the rolled-back
// image of the admission-disabled window as well as for the version under
// test.
func WaitControllerRolledOut(f *framework.Framework, tag string) { waitControllerRolledOut(f, tag) }

// Certificate gates. Each one is a distinct failure mode after a controller
// restart or image swap: the new process mints its own certificate and until
// it has patched it everywhere, every RBG write is rejected with an error
// that looks like a real regression.
func WaitCRDConversionCABundle(f *framework.Framework) { waitCRDConversionCABundle(f) }

func WaitValidatingWebhookCABundle(f *framework.Framework) { waitValidatingWebhookCABundle(f) }

func WaitMutatingWebhookCABundle(f *framework.Framework) { waitMutatingWebhookCABundle(f) }

func WaitConversionActuallyWorks(f *framework.Framework, name string) {
	waitConversionActuallyWorks(f, name)
}

// CRDGroupSuffix identifies the CRDs the chart's crd-upgrade hook installs.
const CRDGroupSuffix = crdGroupSuffix

// GroupVersion helpers for the unstructured CRD reads above.
func CRDListGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "apiextensions.k8s.io",
		Version: "v1",
		Kind:    "CustomResourceDefinitionList",
	}
}
