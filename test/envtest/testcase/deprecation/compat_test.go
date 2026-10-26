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

package deprecation

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var legacySet *workloadsv1alpha2.RoleBasedGroupSet

const gateNamespace = "deprecation-gate"

func newLegacySet() *workloadsv1alpha2.RoleBasedGroupSet {
	return &workloadsv1alpha2.RoleBasedGroupSet{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy-set", Namespace: gateNamespace},
		Spec: workloadsv1alpha2.RoleBasedGroupSetSpec{
			Replicas: ptr.To(int32(2)),
			GroupTemplate: workloadsv1alpha2.RoleBasedGroupTemplateSpec{
				Spec: workloadsv1alpha2.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha2.RoleSpec{legacyStatefulRole("worker")},
				},
			},
		},
	}
}

func legacyStatefulRole(name string) workloadsv1alpha2.RoleSpec {
	return workloadsv1alpha2.RoleSpec{
		Name:     name,
		Replicas: ptr.To(int32(1)),
		Annotations: map[string]string{
			constants.RoleWorkloadTypeAnnotationKey: constants.StatefulSetWorkloadType,
		},
		Pattern: workloadsv1alpha2.Pattern{
			StandalonePattern: &workloadsv1alpha2.StandalonePattern{
				TemplateSource: workloadsv1alpha2.TemplateSource{
					Template: ptr.To(gatePodTemplate()),
				},
			},
		},
	}
}

func gatePodTemplate() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "nginx", Image: "nginx:latest"}},
		},
	}
}

func getSeededSet() *workloadsv1alpha2.RoleBasedGroupSet {
	set := &workloadsv1alpha2.RoleBasedGroupSet{}
	Expect(testutil.K8sClient.Get(
		testutil.Ctx, types.NamespacedName{Name: legacySet.Name, Namespace: gateNamespace}, set,
	)).To(Succeed())
	return set
}

// newReplacementChild builds what RoleBasedGroupSetReconciler.scaleUp submits when
// it replaces or scales a child of the seeded set: a deep copy of the template
// roles plus the controller owner reference.
func newReplacementChild(name string, uid types.UID) *workloadsv1alpha2.RoleBasedGroup {
	set := getSeededSet()
	child := &workloadsv1alpha2.RoleBasedGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: gateNamespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: workloadsv1alpha2.GroupVersion.String(),
				Kind:       "RoleBasedGroupSet",
				Name:       set.Name,
				UID:        uid,
				Controller: ptr.To(true),
			}},
		},
		Spec: workloadsv1alpha2.RoleBasedGroupSpec{
			Roles: func() []workloadsv1alpha2.RoleSpec {
				roles := make([]workloadsv1alpha2.RoleSpec, len(set.Spec.GroupTemplate.Spec.Roles))
				for i := range set.Spec.GroupTemplate.Spec.Roles {
					roles[i] = *set.Spec.GroupTemplate.Spec.Roles[i].DeepCopy()
				}
				return roles
			}(),
		},
	}
	return child
}

var _ = Describe("Deprecated workload compatibility gate", func() {
	It("admits the set controller's replacement child for an un-migrated legacy template", func() {
		set := getSeededSet()
		child := newReplacementChild("legacy-set-1", set.UID)

		// The readiness barrier proved the gate is active, and the webhook reads
		// the parent through the uncached API reader, so no informer warmup is
		// needed: this create must succeed deterministically.
		Expect(testutil.K8sClient.Create(testutil.Ctx, child)).To(Succeed())
	})

	It("still rejects an unrelated new deprecated workload use", func() {
		rbg := &workloadsv1alpha2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "new-legacy-rbg", Namespace: gateNamespace},
			Spec:       workloadsv1alpha2.RoleBasedGroupSpec{Roles: []workloadsv1alpha2.RoleSpec{legacyStatefulRole("worker")}},
		}
		err := testutil.K8sClient.Create(testutil.Ctx, rbg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("new uses are not accepted"))
	})

	It("rejects a child whose controller reference does not match the parent", func() {
		set := getSeededSet()
		child := newReplacementChild("legacy-set-foreign", set.UID+"forged")
		err := testutil.K8sClient.Create(testutil.Ctx, child)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("new uses are not accepted"))
	})

	It("rejects creating a new RoleBasedGroupSet with a deprecated workload template", func() {
		set := newLegacySet()
		set.Name = "new-legacy-set"
		err := testutil.K8sClient.Create(testutil.Ctx, set)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("new uses are not accepted"))
	})

	It("keeps the seeded set writable for migration updates", func() {
		set := getSeededSet()
		updated := set.DeepCopy()
		updated.Spec.Replicas = ptr.To(int32(1))
		Expect(testutil.K8sClient.Update(testutil.Ctx, updated)).To(Succeed())
	})
})
