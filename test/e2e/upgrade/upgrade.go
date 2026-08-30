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
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/e2e/framework"
)

const (
	// gateTimeout bounds each step of waitForUpgradeReady. It is generous because
	// the controller has to start, elect a leader, mint a CA and patch it into two
	// CRDs plus the validating webhook config before the cluster accepts RBG writes.
	gateTimeout  = 5 * time.Minute
	gateInterval = 2 * time.Second

	crdUpgradeJobPrefix = "rbgs-crds-upgrade-"
)

func newCRDObject() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "apiextensions.k8s.io",
		Version: "v1",
		Kind:    "CustomResourceDefinition",
	})
	return u
}

func clientObjectKey(name string) client.ObjectKey { return client.ObjectKey{Name: name} }

// controllerStarts counts the controller process starts the suite has caused since the
// pre-upgrade snapshot was taken. Comparisons that span it scale the per-start entries of
// recordedRewrites by this, so a phase that restarts the controller does not have to be
// re-counted by hand in every later comparison.
//
// It is incremented from what the cluster shows, not from what an action is assumed to
// do: an upgrade that leaves the pods in place does not start a controller.
var controllerStarts int64

// runHelmUpgrade upgrades the release in place to the version under test.
//
// Every value is passed explicitly on its current path and --reuse-values is
// deliberately not used. The chart moved every top-level value under controller.*
// between v0.7.0 and now, and the chart has no values schema, so --reuse-values
// would carry the old paths forward as inert keys and silently install chart
// defaults instead of the images under test.
func runHelmUpgrade(f *framework.Framework) {
	before, err := managerPodNames(f)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "could not list the controller pods before upgrading")

	args := []string{
		"upgrade", helmRelease(), chartPath(),
		"--namespace", controllerNamespace(),
		"--set", "controller.image.repository=" + toRepo(),
		"--set", "controller.image.tag=" + toTag(),
		"--set", "controller.image.pullPolicy=IfNotPresent",
		"--set", "crdUpgrade.image.repository=" + crdUpgradeRepo(),
		"--set", "crdUpgrade.image.tag=" + crdUpgradeTag(),
		"--set", "crdUpgrade.image.pullPolicy=IfNotPresent",
		// Not a chart default. It is set because the v0.7.0 install sets it, as the
		// other e2e workflows do: omitting it here would make the hop turn a feature
		// off, and every assertion in this suite reads what the interval changed as
		// something the upgrade did.
		"--set", "controller.features.portAllocator.enabled=true",
		"--wait", "--timeout", helmTimeout(),
	}

	ginkgo.By("running helm " + strings.Join(args, " "))
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		// The crd-upgrade Job is a pre-upgrade hook whose delete policy keeps it on
		// failure, so its pod logs are the first place to look and are otherwise
		// lost by the time the spec reports.
		dumpCRDUpgradeJobs(f)
		ginkgo.Fail(fmt.Sprintf("helm upgrade failed: %v\n%s", err, out))
	}
	ginkgo.GinkgoWriter.Printf("helm upgrade output:\n%s\n", out)

	after, err := managerPodNames(f)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "could not list the controller pods after upgrading")
	if !slices.Equal(before, after) {
		controllerStarts++
		ginkgo.By(fmt.Sprintf("the upgrade replaced the controller pods (%v -> %v)", before, after))
	}
}

// waitForUpgradeReady blocks until the upgraded install can actually serve RBG
// traffic. helm's --wait only waits for the controller Deployment; it knows nothing
// about the certificate plumbing the controller does at startup, and touching any
// RBG before that plumbing is in place fails in ways that look like real churn.
//
// The steps are ordered and each one is a distinct failure mode:
//
//  1. The new CRD bundle really landed. The pre-upgrade hook Job is deleted on
//     success, so its status cannot be checked after the fact; the CRDs are the
//     durable evidence.
//  2. The controller Deployment is rolled out AND running the image under test.
//     Available alone is satisfied by the old pods still serving.
//  3. Conversion caBundle is repopulated. ensure-crds-up-to-date.sh uses
//     `kubectl replace`, which drops spec.conversion.webhook.clientConfig.caBundle
//     for a moment before the controller patches it back.
//  4. Every webhook of the validating webhook configuration has a caBundle. The
//     chart ships it with an empty caBundle and failurePolicy: Fail, so until the
//     controller patches it every RBG create and update is rejected.
//  5. Conversion actually round-trips. A caBundle that is present but wrong is a
//     real failure mode that steps 3 and 4 cannot see, because they only check the
//     field is non-empty.
func waitForUpgradeReady(f *framework.Framework, conversionProbeName string) {
	ginkgo.By("waiting for the new CRD bundle to be applied")
	waitCRDsUpgraded(f)

	ginkgo.By("waiting for the controller to roll out onto the image under test")
	waitControllerRolledOut(f)

	ginkgo.By("waiting for the conversion webhook caBundle to be repopulated")
	waitCRDConversionCABundle(f)

	ginkgo.By("waiting for the validating webhook caBundle to be injected")
	waitValidatingWebhookCABundle(f)

	ginkgo.By("verifying conversion actually round-trips")
	waitConversionActuallyWorks(f, conversionProbeName)
}

// waitCRDsUpgraded uses the warmup CRD as the marker for the new bundle: preflight
// proved it was absent beforehand, so its appearance can only come from this upgrade.
func waitCRDsUpgraded(f *framework.Framework) {
	gomega.Eventually(func() (bool, error) {
		return crdExists(f, warmupCRDName)
	}, gateTimeout, gateInterval).Should(gomega.BeTrue(),
		"CRD %s never appeared, so the crd-upgrade hook did not apply the new CRD bundle", warmupCRDName)
}

func waitControllerRolledOut(f *framework.Framework) {
	ns := controllerNamespace()
	gomega.Eventually(func(g gomega.Gomega) {
		deploy, err := f.Clientset.AppsV1().Deployments(ns).Get(f.Ctx, managerDeploymentName, metav1.GetOptions{})
		g.Expect(err).ToNot(gomega.HaveOccurred())

		image, found := managerImage(deploy)
		g.Expect(found).To(gomega.BeTrue(), "no controller container found in %s/%s", ns, managerDeploymentName)
		g.Expect(image).To(gomega.HaveSuffix(":"+toTag()),
			"controller still runs %q; the upgrade did not replace the image", image)

		g.Expect(deploy.Status.ObservedGeneration).To(gomega.BeNumerically(">=", deploy.Generation),
			"deployment status has not caught up with the new spec")

		want := int32(1)
		if deploy.Spec.Replicas != nil {
			want = *deploy.Spec.Replicas
		}
		g.Expect(deploy.Status.UpdatedReplicas).To(gomega.Equal(want), "not all replicas are on the new template")
		g.Expect(deploy.Status.Replicas).To(gomega.Equal(want), "old replicas are still around")
		g.Expect(deploy.Status.ReadyReplicas).To(gomega.Equal(want), "new replicas are not ready")
	}, gateTimeout, gateInterval).Should(gomega.Succeed())
}

// waitCRDConversionCABundle checks both conversion CRDs inside one Eventually so the
// gate spends one gateTimeout rather than one per CRD: the same controller pass patches
// both, so a second full window would add worst-case runtime and no coverage.
func waitCRDConversionCABundle(f *framework.Framework) {
	gomega.Eventually(func(g gomega.Gomega) {
		for _, crdName := range []string{rbgCRDName, rbgSetCRDName} {
			crd := newCRDObject()
			g.Expect(f.Client.Get(f.Ctx, clientObjectKey(crdName), crd)).To(gomega.Succeed())

			caBundle, found, err := unstructured.NestedString(
				crd.Object, "spec", "conversion", "webhook", "clientConfig", "caBundle")
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(found).To(gomega.BeTrue(), "CRD %s has no conversion webhook caBundle field", crdName)
			g.Expect(caBundle).ToNot(gomega.BeEmpty(), "CRD %s conversion caBundle is empty", crdName)
		}
	}, gateTimeout, gateInterval).Should(gomega.Succeed())
}

func waitValidatingWebhookCABundle(f *framework.Framework) {
	gomega.Eventually(func(g gomega.Gomega) {
		vwc := &admissionv1.ValidatingWebhookConfiguration{}
		g.Expect(f.Client.Get(f.Ctx, clientObjectKey(validatingWebhookName), vwc)).To(gomega.Succeed())
		g.Expect(vwc.Webhooks).ToNot(gomega.BeEmpty())
		for _, wh := range vwc.Webhooks {
			g.Expect(wh.ClientConfig.CABundle).ToNot(gomega.BeEmpty(),
				"webhook %q of %s still has an empty caBundle; with failurePolicy Fail every RBG write is rejected",
				wh.Name, validatingWebhookName)
		}
	}, gateTimeout, gateInterval).Should(gomega.Succeed())
}

// waitConversionActuallyWorks reads one object through both served versions. Reading
// through v1alpha1 goes via the conversion webhook, so this exercises the whole
// path: correct CA, reachable service, working handler.
func waitConversionActuallyWorks(f *framework.Framework, name string) {
	key := client.ObjectKey{Namespace: f.Namespace, Name: name}
	gomega.Eventually(func(g gomega.Gomega) {
		v2 := &workloadsv1alpha2.RoleBasedGroup{}
		g.Expect(f.Client.Get(f.Ctx, key, v2)).To(gomega.Succeed(), "reading %s as v1alpha2 failed", name)

		v1 := &workloadsv1alpha1.RoleBasedGroup{}
		g.Expect(f.Client.Get(f.Ctx, key, v1)).To(gomega.Succeed(),
			"reading %s as v1alpha1 failed, so the conversion webhook is not usable", name)
		g.Expect(v1.Spec.Roles).ToNot(gomega.BeEmpty(), "conversion returned %s with no roles", name)
	}, gateTimeout, gateInterval).Should(gomega.Succeed())
}

// restartController deletes the controller pods and waits until the Deployment is serving
// from replacements.
//
// The current pod names are recorded and required to be gone before the rollout gate runs.
// The Deployment's own status still counts a pod that is being deleted as ready for a
// moment, so waiting on the Deployment alone can be satisfied by the very process this is
// replacing.
//
// The certificate gates are the same ones the upgrade waits on, and for the same reason:
// the new process mints its own certificate, so until it has patched both the validating
// webhook configuration and the conversion CRDs, the caBundle out there belongs to a key
// nobody holds any more. conversionProbeName is the object the round-trip is read through,
// which is what separates a caBundle that is present from one that works.
func restartController(f *framework.Framework, conversionProbeName string) {
	ns := controllerNamespace()
	old, err := managerPodNames(f)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	gomega.Expect(old).ToNot(gomega.BeEmpty(), "no controller pod found in %s", ns)

	for _, name := range old {
		ginkgo.By("deleting controller pod " + name)
		gomega.Expect(
			f.Client.Delete(
				f.Ctx, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
				},
			),
		).To(gomega.Succeed())
	}

	gomega.Eventually(func(g gomega.Gomega) {
		current, err := managerPodNames(f)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(current).ToNot(gomega.BeEmpty())
		for _, name := range current {
			g.Expect(old).ToNot(gomega.ContainElement(name), "controller pod %s has not been replaced yet", name)
		}
	}, gateTimeout, gateInterval).Should(gomega.Succeed())
	controllerStarts++

	waitControllerRolledOut(f)
	// The new process mints its certificate and patches the webhook config again on
	// startup, and until it has, every RBG write is rejected.
	waitValidatingWebhookCABundle(f)
	waitCRDConversionCABundle(f)
	waitConversionActuallyWorks(f, conversionProbeName)
}

// managerPodNames returns the sorted names of the live controller pods, selected through
// the Deployment's own selector rather than a hardcoded label so it cannot drift from the
// chart.
func managerPodNames(f *framework.Framework) ([]string, error) {
	ns := controllerNamespace()
	deploy, err := f.Clientset.AppsV1().Deployments(ns).Get(f.Ctx, managerDeploymentName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	selector, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
	if err != nil {
		return nil, err
	}

	pods := &corev1.PodList{}
	if err := f.Client.List(f.Ctx, pods,
		client.InNamespace(ns), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(pods.Items))
	for i := range pods.Items {
		if pods.Items[i].DeletionTimestamp == nil {
			names = append(names, pods.Items[i].Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// dumpCRDUpgradeJobs prints the logs of the crd-upgrade hook Job pods. Helm keeps
// the Job when the hook fails, which is exactly when this output is needed.
func dumpCRDUpgradeJobs(f *framework.Framework) {
	ns := controllerNamespace()
	jobs := &batchv1.JobList{}
	if err := f.Client.List(f.Ctx, jobs, client.InNamespace(ns)); err != nil {
		ginkgo.GinkgoWriter.Printf("[crd-upgrade] could not list Jobs in %s: %v\n", ns, err)
		return
	}

	for i := range jobs.Items {
		job := &jobs.Items[i]
		if !strings.HasPrefix(job.Name, crdUpgradeJobPrefix) {
			continue
		}
		ginkgo.GinkgoWriter.Printf("[crd-upgrade] Job %s: active=%d succeeded=%d failed=%d\n",
			job.Name, job.Status.Active, job.Status.Succeeded, job.Status.Failed)

		pods := &corev1.PodList{}
		if err := f.Client.List(f.Ctx, pods, client.InNamespace(ns),
			client.MatchingLabels{"job-name": job.Name}); err != nil {
			ginkgo.GinkgoWriter.Printf("[crd-upgrade] could not list pods of Job %s: %v\n", job.Name, err)
			continue
		}
		for j := range pods.Items {
			pod := &pods.Items[j]
			logs, err := f.GetPodLogs(ns, pod.Name)
			if err != nil {
				ginkgo.GinkgoWriter.Printf("[crd-upgrade] could not read logs of pod %s: %v\n", pod.Name, err)
				continue
			}
			ginkgo.GinkgoWriter.Printf("[crd-upgrade] pod %s (phase=%s) logs:\n%s\n", pod.Name, pod.Status.Phase, logs)
		}
	}
}
