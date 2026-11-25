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

package immutability

import (
	"fmt"
	neturl "net/url"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/internal/controller/workloads"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

// dropCERPWebhookEntry removes only the CERP freeze entry from the installed
// configuration — the RBG/RBGSet validators stay, so later specs in the suite
// keep their admission. Callers must restoreCERPWebhookEntry afterwards.
func dropCERPWebhookEntry() {
	vwc := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	ExpectWithOffset(1, testutil.K8sClient.Get(testutil.Ctx,
		types.NamespacedName{Name: "validating-webhook-configuration"}, vwc)).To(Succeed())
	remaining := vwc.Webhooks[:0]
	for _, wh := range vwc.Webhooks {
		if wh.Name != "vclusterengineruntimeprofile.kb.io" {
			remaining = append(remaining, wh)
		}
	}
	vwc.Webhooks = remaining
	ExpectWithOffset(1, testutil.K8sClient.Update(testutil.Ctx, vwc)).To(Succeed())
}

// restoreCERPWebhookEntry re-applies the CERP freeze entry from the suite's
// manifests — the release-artifact re-application the KEP's rollback drill
// names as its own step. The clientConfig (host/port/caBundle) is copied from
// a surviving entry so the rebuilt rule reaches the same local webhook server.
func restoreCERPWebhookEntry() {
	vwc := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	ExpectWithOffset(1, testutil.K8sClient.Get(testutil.Ctx,
		types.NamespacedName{Name: "validating-webhook-configuration"}, vwc)).To(Succeed())
	for _, wh := range vwc.Webhooks {
		if wh.Name == "vclusterengineruntimeprofile.kb.io" {
			return
		}
	}
	var donor *admissionregistrationv1.ValidatingWebhook
	for i := range vwc.Webhooks {
		if vwc.Webhooks[i].Name == "vrolebasedgroup.kb.io" {
			donor = &vwc.Webhooks[i]
			break
		}
	}
	Expect(donor).NotTo(BeNil(), "the RBG validating entry survives to donate its clientConfig")
	// The donor's clientConfig reaches the RBG validator's endpoint; only
	// its transport (URL/service + caBundle) is reusable — the rebuilt entry
	// must point at the CERP validator's own path. envtest rewrites the
	// manifests to the URL form, so both forms are handled.
	const cerpValidatePath = "/validate-workloads-x-k8s-io-v1alpha2-clusterengineruntimeprofile"
	cc := admissionregistrationv1.WebhookClientConfig{CABundle: donor.ClientConfig.CABundle}
	switch {
	case donor.ClientConfig.URL != nil:
		u, err := neturl.Parse(*donor.ClientConfig.URL)
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		u.Path = cerpValidatePath
		cc.URL = ptr.To(u.String())
	case donor.ClientConfig.Service != nil:
		svc := *donor.ClientConfig.Service
		svc.Path = ptr.To(cerpValidatePath)
		cc.Service = &svc
	default:
		Fail("the donor webhook entry carries neither URL nor Service clientConfig")
	}
	entry := admissionregistrationv1.ValidatingWebhook{
		Name:                    "vclusterengineruntimeprofile.kb.io",
		AdmissionReviewVersions: []string{"v1"},
		SideEffects:             ptrToNone(),
		FailurePolicy:           ptrToFail(),
		ClientConfig:            cc,
		Rules: []admissionregistrationv1.RuleWithOperations{{
			Operations: []admissionregistrationv1.OperationType{
				admissionregistrationv1.Create,
				admissionregistrationv1.Update,
				admissionregistrationv1.Delete,
			},
			Rule: admissionregistrationv1.Rule{
				APIGroups:   []string{"workloads.x-k8s.io"},
				APIVersions: []string{"v1alpha2"},
				Resources:   []string{"clusterengineruntimeprofiles"},
			},
		}},
	}
	vwc.Webhooks = append(vwc.Webhooks, entry)
	ExpectWithOffset(1, testutil.K8sClient.Update(testutil.Ctx, vwc)).To(Succeed())

	// The apiserver ingests webhook configuration changes asynchronously, so
	// the very next admission-reliant request could still race the stale
	// (dropped) configuration. Block until the freeze is demonstrably
	// enforcing again — spec order must not matter in this suite.
	requireCERPFreezeEnforcing()
}

// requireCERPFreezeEnforcing probes admission with a sacrificial referenced
// profile until a spec edit is actually rejected, then tears the probe down.
func requireCERPFreezeEnforcing() {
	// Unique names: a probe left behind by an earlier failure must not block
	// the next one (the profile is cluster-scoped).
	suffix := time.Now().UnixNano()
	profile := makeProfile(fmt.Sprintf("admission-probe-%d", suffix))
	ExpectWithOffset(1, testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
	rbg := referencingRBG(fmt.Sprintf("admission-probe-grp-%d", suffix), profile.Name)
	ExpectWithOffset(1, testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

	EventuallyWithOffset(1, func() bool {
		return apierrors.IsForbidden(addProfileVolume(profile, "probe-extra"))
	}, immutabilityTimeout, immutabilityPoll).Should(BeTrue(),
		"the freeze webhook must be enforcing before the suite proceeds")

	ExpectWithOffset(1, testutil.K8sClient.Delete(testutil.Ctx, rbg)).To(Succeed())
	EventuallyWithOffset(1, func(g Gomega) {
		g.Expect(getStoredProfile(profile.Name).Status.ReferenceCount).To(Equal(int32(0)))
	}, immutabilityTimeout, immutabilityPoll).Should(Succeed())
	ExpectWithOffset(1, testutil.K8sClient.Delete(
		testutil.Ctx, getStoredProfile(profile.Name))).To(Succeed())
}

func ptrToNone() *admissionregistrationv1.SideEffectClass {
	none := admissionregistrationv1.SideEffectClassNone
	return &none
}

func ptrToFail() *admissionregistrationv1.FailurePolicyType {
	fail := admissionregistrationv1.Fail
	return &fail
}

// The specs here pin the remaining KEP-531 Profile Mutability behaviors the
// main immutability specs do not reach: the uncached admission read (a
// just-created reference is visible to an immediately following profile
// update), the fail-closed registration (a webhook down under
// failurePolicy: Fail blocks the write rather than passing it), and the
// same-name recreate's effect on the tracking baseline.
var _ = Describe("admission internals", func() {
	It("sees a just-created reference without cache lag (uncached lookup)", func() {
		profile := makeProfile("imm-uncached")
		Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())

		// No sleep, no Eventually warm-up: the RBG Create returns once the
		// object is stored, and the very next profile update must consult
		// the stored reference through the uncached API reader — a cached
		// read that had not ingested the RBG yet would admit the edit.
		Expect(testutil.K8sClient.Create(testutil.Ctx, referencingRBG("uncached-grp", "imm-uncached"))).To(Succeed())

		err := addProfileVolume(profile, "uncached-extra")
		Expect(err).To(HaveOccurred(),
			"the update immediately after the reference's create must be rejected")
		Expect(apierrors.IsForbidden(err) || apierrors.IsBadRequest(err)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("uncached-grp"),
			"the rejection names the just-created referencing object")
	})

	It("fails closed while the webhook is down (failurePolicy: Fail)", func() {
		profile := makeProfile("imm-failclosed")
		Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
		Expect(testutil.K8sClient.Create(testutil.Ctx, referencingRBG("failclosed-grp", "imm-failclosed"))).To(Succeed())
		Eventually(func(g Gomega) {
			stored := getStoredProfile("imm-failclosed")
			g.Expect(stored.Status.ReferenceCount).To(Equal(int32(1)))
		}, immutabilityTimeout, immutabilityPoll).Should(Succeed())

		// Stop the manager: the webhook endpoint goes away while the
		// registration stays, so an unreachable webhook must block the
		// write (fail closed) rather than pass it.
		testutil.StopImmutabilityManager()
		defer testutil.StartImmutabilityManager()

		err := addProfileVolume(profile, "failclosed-extra")
		Expect(err).To(HaveOccurred(), "an unreachable webhook under failurePolicy: Fail blocks the write")
		Expect(apierrors.IsInternalError(err) || apierrors.IsServiceUnavailable(err) ||
			apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err)).To(BeTrue(),
			"the failure is the webhook call itself, not an admission rejection: "+err.Error())

		delErr := testutil.K8sClient.Delete(testutil.Ctx, getStoredProfile("imm-failclosed"))
		Expect(delErr).To(HaveOccurred(), "delete is equally blocked while the webhook is down")
	})

	It("rebases the tracking baseline when a same-name recreate is adopted (the watch's blind spot)", func() {
		// The accepted deletion interleaving's exception: a referenced
		// profile deleted inside the webhook-absent window and recreated
		// under the same name resolves the dangling reference against the
		// recreated object. The tracking state treats the recreate as a
		// first adoption: the baseline records the recreated object's
		// generation, and no drift event ever fires (a CREATE is not a
		// spec change on a referenced profile).
		profile := makeProfile("imm-recreate")
		Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
		Expect(testutil.K8sClient.Create(testutil.Ctx, referencingRBG("recreate-grp", "imm-recreate"))).To(Succeed())
		Eventually(func(g Gomega) {
			stored := getStoredProfile("imm-recreate")
			g.Expect(stored.Status.ReferenceCount).To(Equal(int32(1)))
			g.Expect(stored.Status.ReferencedGeneration).To(Equal(int64(1)))
		}, immutabilityTimeout, immutabilityPoll).Should(Succeed())

		// open the bypass window: drop only the CERP freeze entry
		dropCERPWebhookEntry()
		defer restoreCERPWebhookEntry()

		// delete + same-name recreate with a different spec, before any
		// reconcile can clear the tracking state
		Eventually(func() error {
			return testutil.K8sClient.Delete(testutil.Ctx, getStoredProfile("imm-recreate"))
		}, immutabilityTimeout, immutabilityPoll).Should(Succeed())

		recreated := makeProfile("imm-recreate")
		recreated.Spec.Volumes = append(recreated.Spec.Volumes, corev1.Volume{
			Name: "recreated-extra", VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		Expect(testutil.K8sClient.Create(testutil.Ctx, recreated)).To(Succeed())

		// the reference resolves against the recreated object: count 1
		// again, baseline rebaselined at the recreated generation (1), and
		// no drift — the condition stays at reason Referenced, the
		// mutation-while-referenced event never fires.
		Eventually(func(g Gomega) {
			stored := getStoredProfile("imm-recreate")
			g.Expect(stored.Status.ReferenceCount).To(Equal(int32(1)))
			cond := meta.FindStatusCondition(stored.Status.Conditions, workloadsv1alpha2.FrozenConditionType)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Reason).To(Equal(workloadsv1alpha2.ReasonReferenced))
		}, immutabilityTimeout, immutabilityPoll).Should(Succeed())
		Expect(profileEventReasons("imm-recreate")).NotTo(ContainElement(
			workloads.ReasonEngineRuntimeProfileMutatedWhileReferenced),
			"a CREATE is not a spec change: the recreate fires no drift signal")
	})

	It("carries the updateStrategy deprecation marker in both served schemas", func() {
		// controller-gen renders the doc comment's Deprecated: marker into
		// the CRD's OpenAPI description; pinning it keeps the deprecation
		// visible to schema-reading tooling in both served versions.
		crd := &apiextensionsv1.CustomResourceDefinition{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			types.NamespacedName{Name: "clusterengineruntimeprofiles.workloads.x-k8s.io"}, crd)).To(Succeed())

		for _, version := range []string{"v1alpha1", "v1alpha2"} {
			var served *apiextensionsv1.CustomResourceDefinitionVersion
			for i := range crd.Spec.Versions {
				if crd.Spec.Versions[i].Name == version {
					served = &crd.Spec.Versions[i]
					break
				}
			}
			Expect(served).NotTo(BeNil(), "version %s must be served", version)

			// the typed client hands back OpenAPIV3Schema as a struct, not Raw
			props := served.Schema.OpenAPIV3Schema.Properties["spec"].Properties
			updateStrategy, ok := props["updateStrategy"]
			Expect(ok).To(BeTrue(), "%s spec.updateStrategy must exist in the schema", version)
			Expect(updateStrategy.Description).To(ContainSubstring("Deprecated"),
				"the %s updateStrategy description carries the deprecation marker", version)
		}
	})
})
