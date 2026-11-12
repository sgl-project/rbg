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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/internal/controller/workloads"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var (
	testNs              string
	immutabilityTimeout = 15 * time.Second
	immutabilityPoll    = 250 * time.Millisecond
)

var _ = BeforeEach(func() {
	testNs = fmt.Sprintf("test-immutability-%d", time.Now().UnixNano())
	testutil.CreateNamespace(testNs)
})

var _ = AfterEach(func() {
	testutil.DeleteNamespace(testNs)
})

// makeProfile builds a minimal writable CERP. The spec carries a volume so a
// spec edit has something concrete to add; updateStrategy rides the field the
// KEP deprecated but still accepts — every fixture writes it because the
// enum validation rejects the zero value's empty string.
func makeProfile(name string) *workloadsv1alpha2.ClusterEngineRuntimeProfile {
	return &workloadsv1alpha2.ClusterEngineRuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: workloadsv1alpha2.ClusterEngineRuntimeProfileSpec{
			UpdateStrategy: "NoUpdate",
			Volumes: []corev1.Volume{{Name: name + "-vol", VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			}}},
		},
	}
}

// referencingRBG builds a one-role RBG whose role references the profile —
// no targetContainers, so the reference is legal under any pattern, and the
// standalone pattern keeps the object writable without the role-workload-type
// annotation routing.
func referencingRBG(name, profileName string) *workloadsv1alpha2.RoleBasedGroup {
	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNs},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{{
			Name:     "worker",
			Replicas: ptr.To(int32(1)),
			Pattern: workloadsv1alpha2.Pattern{StandalonePattern: &workloadsv1alpha2.StandalonePattern{
				TemplateSource: workloadsv1alpha2.TemplateSource{
					Template: ptr.To(corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name:  "engine",
						Image: "nginx:latest",
					}}}}),
				},
			}},
			EngineRuntimes: []workloadsv1alpha2.EngineRuntime{{ProfileName: profileName}},
		}}},
	}
}

// referencingRBGSet builds a zero-replica set whose group template embeds
// the reference — a live reference with no child RoleBasedGroup at all.
func referencingRBGSet(name, profileName string) *workloadsv1alpha2.RoleBasedGroupSet {
	return &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNs},
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(0)),
			GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{Spec: workloadsv1alpha2.RoleBasedGroupSpec{
				Roles: []workloadsv1alpha2.RoleSpec{{
					Name:           "worker",
					Replicas:       ptr.To(int32(1)),
					EngineRuntimes: []workloadsv1alpha2.EngineRuntime{{ProfileName: profileName}},
				}},
			}},
		},
	}
}

func getStoredProfile(name string) *workloadsv1alpha2.ClusterEngineRuntimeProfile {
	profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{}
	ExpectWithOffset(1, testutil.K8sClient.Get(
		testutil.Ctx, types.NamespacedName{Name: name}, profile,
	)).To(Succeed())
	return profile
}

// addProfileVolume edits the profile spec — the concrete freeze target.
// The reference-tracking controller writes status concurrently, which bumps
// metadata.resourceVersion between the Get and the Update; retrying the
// conflict lets the admission layer's verdict (not the 409) come back.
func addProfileVolume(profile *workloadsv1alpha2.ClusterEngineRuntimeProfile, name string) error {
	stored := getStoredProfile(profile.Name)
	stored.Spec.Volumes = append(stored.Spec.Volumes, corev1.Volume{
		Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
	err := testutil.K8sClient.Update(testutil.Ctx, stored)
	for i := 0; i < 10 && apierrors.IsConflict(err); i++ {
		stored = getStoredProfile(profile.Name)
		stored.Spec.Volumes = append(stored.Spec.Volumes, corev1.Volume{
			Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
		err = testutil.K8sClient.Update(testutil.Ctx, stored)
	}
	return err
}

func frozenConditionOf(profile *workloadsv1alpha2.ClusterEngineRuntimeProfile) *metav1.Condition {
	cond := meta.FindStatusCondition(profile.Status.Conditions, workloadsv1alpha2.FrozenConditionType)
	ExpectWithOffset(1, cond).NotTo(BeNil(), "the Frozen condition must be present while referenced")
	return cond
}

func profileEventReasons(name string) []string {
	events := &corev1.EventList{}
	ExpectWithOffset(1, testutil.K8sClient.List(testutil.Ctx, events)).To(Succeed())
	var reasons []string
	for _, ev := range events.Items {
		if ev.InvolvedObject.Kind == "ClusterEngineRuntimeProfile" && ev.InvolvedObject.Name == name {
			reasons = append(reasons, ev.Reason)
		}
	}
	return reasons
}

var _ = Describe("Engine runtime profile immutability (KEP-531 Profile Mutability)", func() {
	Context("creation and the unreferenced profile", func() {
		It("creates unrestricted and accepts spec edits while unreferenced", func() {
			profile := makeProfile("imm-free")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			Expect(addProfileVolume(profile, "imm-free-extra")).To(Succeed(),
				"an unreferenced profile is fully editable")

			stored := getStoredProfile("imm-free")
			Expect(stored.Spec.Volumes).To(HaveLen(2))
		})

		It("still accepts the deprecated updateStrategy values", func() {
			rolling := makeProfile("imm-rolling")
			rolling.Spec.UpdateStrategy = "RollingUpdate"
			Expect(testutil.K8sClient.Create(testutil.Ctx, rolling)).To(Succeed(),
				"updateStrategy is deprecated, not rejected — the field stays writable")
			noUpdate := getStoredProfile("imm-rolling")
			Expect(noUpdate.Spec.UpdateStrategy).To(Equal("RollingUpdate"))
		})
	})

	Context("the freeze while referenced", func() {
		It("rejects spec edits and deletes while an RBG references the profile", func() {
			profile := makeProfile("imm-frozen")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			rbg := referencingRBG("freeze-grp", "imm-frozen")
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

			err := addProfileVolume(profile, "frozen-extra")
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsForbidden(err) || apierrors.IsBadRequest(err)).
				To(BeTrue(), "the freeze denies the admission request, got: "+err.Error())
			Expect(err.Error()).To(ContainSubstring("frozen"))
			Expect(err.Error()).To(ContainSubstring("freeze-grp"))

			delErr := testutil.K8sClient.Delete(testutil.Ctx, getStoredProfile("imm-frozen"))
			Expect(delErr).To(HaveOccurred(), "delete is rejected while referenced")
			Expect(delErr.Error()).To(ContainSubstring("frozen"))

			// metadata stays editable under the freeze
			stored := getStoredProfile("imm-frozen")
			stored.Labels = map[string]string{"tier": "gold"}
			Expect(testutil.K8sClient.Update(testutil.Ctx, stored)).To(Succeed())
		})

		It("counts a zero-replica RBGSet group template as a live reference", func() {
			profile := makeProfile("imm-set")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			Expect(testutil.K8sClient.Create(testutil.Ctx, referencingRBGSet("freeze-set", "imm-set"))).To(Succeed())

			Expect(addProfileVolume(profile, "set-extra")).NotTo(Succeed(),
				"the group template references the profile with no child RBG at all")
		})
	})

	Context("reference tracking", func() {
		It("records count, baseline and the Frozen condition; the last reference leaving clears them", func() {
			profile := makeProfile("imm-tracking")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			created := getStoredProfile("imm-tracking")
			Expect(created.Generation).To(Equal(int64(1)))

			Expect(testutil.K8sClient.Create(testutil.Ctx, referencingRBG("bk-grp", "imm-tracking"))).To(Succeed())

			var stored *workloadsv1alpha2.ClusterEngineRuntimeProfile
			Eventually(func(g Gomega) {
				stored = getStoredProfile("imm-tracking")
				g.Expect(stored.Status.ReferenceCount).To(Equal(int32(1)))
			}, immutabilityTimeout, immutabilityPoll).Should(Succeed())
			Expect(stored.Status.ReferencedGeneration).To(Equal(int64(1)),
				"the baseline records metadata.generation when the profile becomes referenced")
			cond := frozenConditionOf(stored)
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal(workloadsv1alpha2.ReasonReferenced))

			// the last reference leaving restores the unreferenced shape
			Expect(testutil.K8sClient.Delete(testutil.Ctx, referencingRBG("bk-grp", "imm-tracking"))).To(Succeed())
			Eventually(func(g Gomega) {
				fresh := getStoredProfile("imm-tracking")
				g.Expect(fresh.Status.ReferenceCount).To(Equal(int32(0)))
				g.Expect(fresh.Status.ReferencedGeneration).To(Equal(int64(0)))
				g.Expect(meta.FindStatusCondition(
					fresh.Status.Conditions, workloadsv1alpha2.FrozenConditionType)).To(BeNil())
			}, immutabilityTimeout, immutabilityPoll).Should(Succeed())

			// editability returns with the last reference
			Expect(addProfileVolume(profile, "unfrozen-extra")).To(Succeed())
		})

		It("survives a controller restart", func() {
			profile := makeProfile("imm-restart")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			Expect(testutil.K8sClient.Create(testutil.Ctx, referencingRBG("restart-grp", "imm-restart"))).To(Succeed())
			Eventually(func(g Gomega) {
				stored := getStoredProfile("imm-restart")
				g.Expect(stored.Status.ReferenceCount).To(Equal(int32(1)))
			}, immutabilityTimeout, immutabilityPoll).Should(Succeed())

			// a restarted manager is a fresh controller instance: the
			// baseline lives in status, so the re-read must keep it
			testutil.StopImmutabilityManager()
			testutil.StartImmutabilityManager()

			Consistently(func(g Gomega) {
				stored := getStoredProfile("imm-restart")
				g.Expect(stored.Status.ReferenceCount).To(Equal(int32(1)))
				g.Expect(stored.Status.ReferencedGeneration).To(Equal(int64(1)))
				g.Expect(frozenConditionOf(stored).Reason).To(Equal(workloadsv1alpha2.ReasonReferenced))
			}, 3*time.Second, immutabilityPoll).Should(Succeed(),
				"the restart neither re-records nor clears the baseline")

			// and the freeze still rejects after the restart
			Expect(addProfileVolume(profile, "restart-extra")).NotTo(Succeed())
		})

		It("migrates a reference to a new profile revision and unfreezes the old one", func() {
			old := makeProfile("imm-v1")
			Expect(testutil.K8sClient.Create(testutil.Ctx, old)).To(Succeed())
			newRevision := makeProfile("imm-v2")
			Expect(testutil.K8sClient.Create(testutil.Ctx, newRevision)).To(Succeed())
			rbg := referencingRBG("migrate-grp", "imm-v1")
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
			Eventually(func(g Gomega) {
				stored := getStoredProfile("imm-v1")
				g.Expect(stored.Status.ReferenceCount).To(Equal(int32(1)))
			}, immutabilityTimeout, immutabilityPoll).Should(Succeed())

			// the migration flow: point the reference at the new revision,
			// then retire the old profile
			migrated := &workloadsv1alpha2.RoleBasedGroup{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				types.NamespacedName{Name: "migrate-grp", Namespace: testNs}, migrated)).To(Succeed())
			migrated.Spec.Roles[0].EngineRuntimes[0].ProfileName = "imm-v2"
			Expect(testutil.K8sClient.Update(testutil.Ctx, migrated)).To(Succeed())

			Eventually(func(g Gomega) {
				fresh := getStoredProfile("imm-v1")
				g.Expect(fresh.Status.ReferenceCount).To(Equal(int32(0)))
				freshV2 := getStoredProfile("imm-v2")
				g.Expect(freshV2.Status.ReferenceCount).To(Equal(int32(1)))
			}, immutabilityTimeout, immutabilityPoll).Should(Succeed())

			Expect(addProfileVolume(old, "retired-extra")).To(Succeed(),
				"the old revision is editable once no reference names it")
			Expect(testutil.K8sClient.Delete(testutil.Ctx, getStoredProfile("imm-v1"))).To(Succeed())
		})
	})

	Context("webhook bypass detection", func() {
		It("warns through the controller when the webhook is absent", func() {
			profile := makeProfile("imm-bypass")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			Expect(testutil.K8sClient.Create(testutil.Ctx, referencingRBG("bypass-grp", "imm-bypass"))).To(Succeed())
			Eventually(func(g Gomega) {
				stored := getStoredProfile("imm-bypass")
				g.Expect(stored.Status.ReferenceCount).To(Equal(int32(1)))
			}, immutabilityTimeout, immutabilityPoll).Should(Succeed())

			// the bypass: uninstall only the CERP freeze entry — the
			// configuration also carries the RBG/RBGSet validators the
			// presence-rule specs ride on, and they must survive this spec.
			// The entry is restored before the spec ends: Ginkgo randomizes
			// spec order, so any later freeze spec must find it back.
			dropCERPWebhookEntry()
			defer restoreCERPWebhookEntry()
			// the apiserver caches admission state briefly; give the deletion a
			// moment to take effect before attempting the unopposed edit
			Eventually(func() error {
				return addProfileVolume(profile, "bypass-extra")
			}, immutabilityTimeout, immutabilityPoll).Should(Succeed(),
				"with the webhook gone the edit goes through — the controller must catch it")

			var drifted *workloadsv1alpha2.ClusterEngineRuntimeProfile
			Eventually(func(g Gomega) {
				drifted = getStoredProfile("imm-bypass")
				cond := meta.FindStatusCondition(drifted.Status.Conditions, workloadsv1alpha2.FrozenConditionType)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Reason).To(Equal(workloadsv1alpha2.ReasonSpecChangedWhileReferenced))
			}, immutabilityTimeout, immutabilityPoll).Should(Succeed(),
				"the drift surfaces as the Frozen condition's reason")
			Expect(drifted.Status.ReferencedGeneration).To(Equal(int64(1)),
				"the baseline stays pinned at the generation the freeze recorded")
			Expect(drifted.Generation).To(Equal(int64(2)))
			Expect(profileEventReasons("imm-bypass")).To(ContainElement(
				workloads.ReasonEngineRuntimeProfileMutatedWhileReferenced),
				"the bypass detection event fires on the profile")
		})
	})

	Context("v1alpha1 round trip", func() {
		// The KEP-531 Test Plan pins the v1alpha2 → v1alpha1 update →
		// v1alpha2 round trip keeping containerPatches and the tracking
		// status fields: the v1alpha1 schema mirrors both, and a None-strategy
		// deployment of the old schema would prune them on write — this suite
		// runs the conversion webhook, so the mirror is exercised end to end.
		It("keeps containerPatches and the tracking status through a v1alpha1 update", func() {
			profile := makeProfile("imm-roundtrip")
			profile.Spec.ContainerPatches = []workloadsv1alpha2.ContainerPatch{{
				JSONPatch: &workloadsv1alpha2.ContainerJSONPatch{
					Op: "add", Path: "/env/-",
					Value: runtime.RawExtension{Raw: []byte(`{"name":"RT_KEEP","value":"v"}`)},
				},
			}}
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			Expect(testutil.K8sClient.Create(
				testutil.Ctx, referencingRBG("roundtrip-grp", "imm-roundtrip"))).To(Succeed())

			// the tracking state lands: count, baseline, Frozen condition
			Eventually(func(g Gomega) {
				stored := getStoredProfile("imm-roundtrip")
				g.Expect(stored.Status.ReferenceCount).To(Equal(int32(1)))
				g.Expect(stored.Status.ReferencedGeneration).To(Equal(int64(1)))
				cond := meta.FindStatusCondition(stored.Status.Conditions, workloadsv1alpha2.FrozenConditionType)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Reason).To(Equal(workloadsv1alpha2.ReasonReferenced))
			}, immutabilityTimeout, immutabilityPoll).Should(Succeed())

			// the v1alpha1 leg: read the profile through the served v1alpha1
			// schema, update it there (a metadata-only edit, so the freeze
			// admits it), then read the result back as v1alpha2.
			v1a1Profile := &workloadsv1alpha1.ClusterEngineRuntimeProfile{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				types.NamespacedName{Name: "imm-roundtrip"}, v1a1Profile)).To(Succeed())
			Expect(v1a1Profile.Spec.ContainerPatches).To(HaveLen(1),
				"the v1alpha1 mirror carries containerPatches")

			v1a1Profile.Labels = map[string]string{"leg": "v1alpha1"}
			Expect(testutil.K8sClient.Update(testutil.Ctx, v1a1Profile)).To(Succeed(),
				"a metadata-only update through v1alpha1 passes the freeze")

			roundTripped := getStoredProfile("imm-roundtrip")
			Expect(roundTripped.Spec.ContainerPatches).To(HaveLen(1))
			Expect(roundTripped.Spec.ContainerPatches[0].JSONPatch.Path).To(Equal("/env/-"))
			Expect(roundTripped.Status.ReferenceCount).To(Equal(int32(1)))
			Expect(roundTripped.Status.ReferencedGeneration).To(Equal(int64(1)))
			cond := meta.FindStatusCondition(roundTripped.Status.Conditions, workloadsv1alpha2.FrozenConditionType)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal(workloadsv1alpha2.ReasonReferenced))
		})

		// KEP-531 Profile Mutability: "a referenced profile's spec cannot
		// change and it cannot be deleted; admission rejects both." The CERP
		// webhook is registered v1alpha2-only with the default
		// matchPolicy=Equivalent, so a v1alpha1 write is admitted through
		// the converted object — these specs pin both halves of that
		// sentence through the v1alpha1 entrypoint, not just the
		// metadata-only pass pinned above.
		It("rejects a spec edit through v1alpha1 while referenced", func() {
			profile := makeProfile("imm-v1a1-edit")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			Expect(testutil.K8sClient.Create(
				testutil.Ctx, referencingRBG("v1a1-edit-grp", "imm-v1a1-edit"))).To(Succeed())

			// wait for the tracking state to land, else the update races the
			// controller's status write and fails on a resourceVersion
			// conflict before the freeze can speak
			Eventually(func(g Gomega) {
				stored := getStoredProfile("imm-v1a1-edit")
				g.Expect(stored.Status.ReferenceCount).To(Equal(int32(1)))
				cond := meta.FindStatusCondition(stored.Status.Conditions, workloadsv1alpha2.FrozenConditionType)
				g.Expect(cond).NotTo(BeNil())
			}, immutabilityTimeout, immutabilityPoll).Should(Succeed())

			v1a1Profile := &workloadsv1alpha1.ClusterEngineRuntimeProfile{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				types.NamespacedName{Name: "imm-v1a1-edit"}, v1a1Profile)).To(Succeed())
			v1a1Profile.Spec.Volumes = append(v1a1Profile.Spec.Volumes, corev1.Volume{
				Name:         "v1a1-frozen-extra",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			})
			err := testutil.K8sClient.Update(testutil.Ctx, v1a1Profile)
			Expect(err).To(HaveOccurred(), "a spec edit through v1alpha1 must hit the freeze")
			Expect(apierrors.IsForbidden(err) || apierrors.IsBadRequest(err)).
				To(BeTrue(), "the freeze denies the admission request, got: "+err.Error())
			Expect(err.Error()).To(ContainSubstring("frozen"))
			Expect(err.Error()).To(ContainSubstring("v1a1-edit-grp"))
		})

		It("rejects delete through v1alpha1 while referenced", func() {
			profile := makeProfile("imm-v1a1-del")
			Expect(testutil.K8sClient.Create(testutil.Ctx, profile)).To(Succeed())
			Expect(testutil.K8sClient.Create(
				testutil.Ctx, referencingRBG("v1a1-del-grp", "imm-v1a1-del"))).To(Succeed())

			v1a1Profile := &workloadsv1alpha1.ClusterEngineRuntimeProfile{}
			Expect(testutil.K8sClient.Get(testutil.Ctx,
				types.NamespacedName{Name: "imm-v1a1-del"}, v1a1Profile)).To(Succeed())
			err := testutil.K8sClient.Delete(testutil.Ctx, v1a1Profile)
			Expect(err).To(HaveOccurred(), "delete through v1alpha1 must hit the freeze")
			Expect(err.Error()).To(ContainSubstring("frozen"))
		})
	})
})
