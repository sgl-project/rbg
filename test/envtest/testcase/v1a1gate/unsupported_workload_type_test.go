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
package v1a1gate

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

// Role workload types no reconciler supports (#624), through the real webhook: the
// production v1alpha2 validator behind the apiserver, the uncached parent lookup,
// and v1alpha1 writes routed through conversion by the default Equivalent match.
var _ = describeUnsupportedWorkloadTypes("compatibility", "compat", testutil.EnableV1a2Gate)
var _ = describeUnsupportedWorkloadTypes("strict", "strict", testutil.EnableV1a2StrictGate)

const unsupportedType = "apps/v1/DaemonSet"

func v1a2Role(name, workloadType string) workloadsv1alpha2.RoleSpec {
	role := workloadsv1alpha2.RoleSpec{
		Name:     name,
		Replicas: ptr.To(int32(1)),
		Pattern: workloadsv1alpha2.Pattern{
			StandalonePattern: &workloadsv1alpha2.StandalonePattern{TemplateSource: workloadsv1alpha2.TemplateSource{}},
		},
	}
	if workloadType != "" {
		role.Annotations = map[string]string{constants.RoleWorkloadTypeAnnotationKey: workloadType}
	}
	return role
}

func v1a2RBG(name string, roles ...workloadsv1alpha2.RoleSpec) *workloadsv1alpha2.RoleBasedGroup {
	return &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gateNamespace},
		Spec:       workloadsv1alpha2.RoleBasedGroupSpec{Roles: roles},
	}
}

func v1a2Set(name string, roles ...workloadsv1alpha2.RoleSpec) *workloadsv1alpha2.RoleBasedGroupSet {
	return &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gateNamespace},
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(1)),
			GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: roles},
			},
		},
	}
}

func setControllerRef(set *workloadsv1alpha2.RoleBasedGroupSet, uid types.UID) []metav1.OwnerReference {
	return []metav1.OwnerReference{{
		APIVersion: workloadsv1alpha2.GroupVersion.String(),
		Kind:       "RoleBasedGroupSet",
		Name:       set.Name,
		UID:        uid,
		Controller: ptr.To(true),
	}}
}

// setChild is the RoleBasedGroup the set controller creates for ordinal 0, pinned
// to revision when it is not empty.
func setChild(set *workloadsv1alpha2.RoleBasedGroupSet, uid types.UID, revision string, roles ...workloadsv1alpha2.RoleSpec) *workloadsv1alpha2.RoleBasedGroup {
	child := v1a2RBG(set.Name+"-0", roles...)
	child.OwnerReferences = setControllerRef(set, uid)
	if revision != "" {
		child.Labels = map[string]string{constants.GroupSetRevisionLabelKey: revision}
	}
	return child
}

func describeUnsupportedWorkloadTypes(mode, suffix string, enableGate func()) bool {
	return Describe("unsupported role workload types in "+mode+" mode", Ordered, func() {
		named := func(name string) string { return name + "-" + suffix }

		var storedSet, heldSet *workloadsv1alpha2.RoleBasedGroupSet
		heldRevision := named("rbgs-held")
		retainedRevision := named("rbgs-retained")

		BeforeAll(func() {
			// Seed what predates the check with the gate offline: a set whose
			// template stores an unsupported type, and an RBG that stores one.
			// The deferred call brings the gate back in this mode even if a seed
			// fails.
			defer enableGate()
			testutil.DisableV1a2Gate()

			storedSet = v1a2Set(named("unsup-stored-set"), v1a2Role("worker", unsupportedType))
			Expect(testutil.K8sClient.Create(testutil.Ctx, storedSet)).To(Succeed())
			Expect(testutil.K8sClient.Create(testutil.Ctx,
				v1a2RBG(named("unsup-grandfathered"), v1a2Role("worker", unsupportedType)))).To(Succeed())

			// A set mid-rollout: the live template has moved to RoleInstanceSet,
			// and the revision children below the partition are re-created from
			// still carries the unsupported type and a deprecated one. The set
			// also retains an older revision with the unsupported type that its
			// status no longer names.
			heldSet = v1a2Set(named("unsup-held-set"), v1a2Role("worker", ""), v1a2Role("legacy", ""))
			Expect(testutil.K8sClient.Create(testutil.Ctx, heldSet)).To(Succeed())
			createRevision := func(name string, revision int64, roles ...workloadsv1alpha2.RoleSpec) {
				data, err := json.Marshal(&workloadsv1alpha2.RoleBasedGroupTemplateSpec{
					Spec: workloadsv1alpha2.RoleBasedGroupSpec{Roles: roles},
				})
				ExpectWithOffset(1, err).NotTo(HaveOccurred())
				ExpectWithOffset(1, testutil.K8sClient.Create(testutil.Ctx, &appsv1.ControllerRevision{
					ObjectMeta: metav1.ObjectMeta{
						Name: name, Namespace: gateNamespace,
						OwnerReferences: setControllerRef(heldSet, heldSet.UID),
					},
					Data:     runtime.RawExtension{Raw: data},
					Revision: revision,
				})).To(Succeed())
			}
			createRevision(retainedRevision, 1, v1a2Role("worker", unsupportedType))
			createRevision(heldRevision, 2,
				v1a2Role("worker", unsupportedType),
				v1a2Role("legacy", constants.StatefulSetWorkloadType))
			// No set controller runs in this suite, so write the status it persists
			// before re-creating children: the held revision is current, the live
			// template's revision is the update revision.
			heldSet.Status.CurrentRevision = heldRevision
			heldSet.Status.UpdateRevision = named("rbgs-live")
			Expect(testutil.K8sClient.Status().Update(testutil.Ctx, heldSet)).To(Succeed())
		})

		AfterAll(func() {
			testutil.EnableV1a2Gate()
		})

		expectUnsupported := func(err error, workloadType string) {
			ExpectWithOffset(1, err).To(HaveOccurred())
			ExpectWithOffset(1, err.Error()).To(ContainSubstring(`workload type "` + workloadType + `" is not supported`))
		}

		It("rejects a v1alpha2 create whose role names an unsupported type", func() {
			err := testutil.K8sClient.Create(testutil.Ctx, v1a2RBG(named("unsup-create"), v1a2Role("worker", unsupportedType)))
			expectUnsupported(err, unsupportedType)
		})

		It("rejects a v1alpha2 update that changes a role to an unsupported type", func() {
			rbg := v1a2RBG(named("unsup-update"), v1a2Role("worker", ""))
			Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())
			rbg.Spec.Roles[0].Annotations = map[string]string{constants.RoleWorkloadTypeAnnotationKey: unsupportedType}
			expectUnsupported(testutil.K8sClient.Update(testutil.Ctx, rbg), unsupportedType)
		})

		It("keeps an RBG that already stores the type writable and fixable", func() {
			rbg := &workloadsv1alpha2.RoleBasedGroup{}
			key := client.ObjectKey{Namespace: gateNamespace, Name: named("unsup-grandfathered")}
			Expect(testutil.K8sClient.Get(testutil.Ctx, key, rbg)).To(Succeed())
			rbg.Spec.Roles[0].Replicas = ptr.To(int32(2))
			Expect(testutil.K8sClient.Update(testutil.Ctx, rbg)).To(Succeed())

			Expect(testutil.K8sClient.Get(testutil.Ctx, key, rbg)).To(Succeed())
			rbg.Spec.Roles[0].Annotations[constants.RoleWorkloadTypeAnnotationKey] = constants.RoleInstanceSetWorkloadType
			Expect(testutil.K8sClient.Update(testutil.Ctx, rbg)).To(Succeed())
		})

		It("rejects a v1alpha1 write whose workload converts to an unsupported type, with a v1alpha1 remedy", func() {
			rbg := buildV1a1RBG(named("unsup-v1a1"))
			rbg.Spec.Roles[0].Workload = workloadsv1alpha1.WorkloadSpec{APIVersion: "apps/v1", Kind: "DaemonSet"}
			err := testutil.K8sClient.Create(testutil.Ctx, rbg)
			expectUnsupported(err, unsupportedType)
			Expect(err.Error()).To(ContainSubstring(
				"set workload.apiVersion=workloads.x-k8s.io/v1alpha2 and workload.kind=RoleInstanceSet there, or submit the object as v1alpha2"))
		})

		It("admits a set child that inherits the type its set stores", func() {
			Expect(testutil.K8sClient.Create(testutil.Ctx,
				setChild(storedSet, storedSet.UID, "", v1a2Role("worker", unsupportedType)))).To(Succeed())
		})

		It("rejects a set child whose owner reference carries another UID", func() {
			child := setChild(storedSet, "forged-uid", "", v1a2Role("worker", unsupportedType))
			child.Name = named("unsup-forged")
			expectUnsupported(testutil.K8sClient.Create(testutil.Ctx, child), unsupportedType)
		})

		It("admits a set child re-created from a held revision the live template has moved off", func() {
			Expect(testutil.K8sClient.Create(testutil.Ctx,
				setChild(heldSet, heldSet.UID, heldRevision, v1a2Role("worker", unsupportedType)))).To(Succeed())
		})

		It("rejects a set child whose label names no revision of its set", func() {
			child := setChild(heldSet, heldSet.UID, named("rbgs-missing"), v1a2Role("worker", unsupportedType))
			child.Name = named("unsup-no-revision")
			expectUnsupported(testutil.K8sClient.Create(testutil.Ctx, child), unsupportedType)
		})

		It("rejects a set child copied from a retained revision the set status does not name", func() {
			child := setChild(heldSet, heldSet.UID, retainedRevision, v1a2Role("worker", unsupportedType))
			child.Name = named("unsup-retained")
			expectUnsupported(testutil.K8sClient.Create(testutil.Ctx, child), unsupportedType)
		})

		It("rejects a RoleBasedGroupSet create whose template names an unsupported type", func() {
			err := testutil.K8sClient.Create(testutil.Ctx, v1a2Set(named("unsup-set-create"), v1a2Role("worker", unsupportedType)))
			expectUnsupported(err, unsupportedType)
		})

		It("rejects a RoleBasedGroupSet update that changes a template role to an unsupported type", func() {
			set := v1a2Set(named("unsup-set-update"), v1a2Role("worker", ""))
			Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())
			set.Spec.GroupTemplate.Spec.Roles[0].Annotations = map[string]string{constants.RoleWorkloadTypeAnnotationKey: unsupportedType}
			expectUnsupported(testutil.K8sClient.Update(testutil.Ctx, set), unsupportedType)
		})

		It("decides a deprecated type in the held revision by the mode", func() {
			child := setChild(heldSet, heldSet.UID, heldRevision, v1a2Role("legacy", constants.StatefulSetWorkloadType))
			child.Name = named("unsup-held-legacy")
			err := testutil.K8sClient.Create(testutil.Ctx, child)
			if mode == "compatibility" {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("is deprecated and not enabled on this cluster"))
		})
	})
}
