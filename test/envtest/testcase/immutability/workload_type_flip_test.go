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

// The workload-type annotation is mutable, and a role written under
// RoleInstanceSet and flipped to a deprecated surface afterwards used to look
// like a hole in write-time rejection. It is not: both webhooks intercept
// CREATE and UPDATE and re-validate the whole object on each, so the flip
// itself is a rejected spec update. The render-time re-run of the shared
// validation function covers only the webhook-absent window — an uninstalled
// webhook configuration, or objects written during an old-controller window
// reaching the new renderer by reconcile.
var _ = Describe("workload-type annotation flip to a deprecated surface", func() {
	flipRBG := func(name string) *workloadsv1alpha2.RoleBasedGroup {
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
				EngineRuntimes: []workloadsv1alpha2.EngineRuntime{{
					ProfileName:      "flip-profile",
					TargetContainers: []workloadsv1alpha2.EngineRuntimeTargetContainer{{Name: "engine"}},
				}},
			}}},
		}
	}

	It("accepts the role under the default RoleInstanceSet routing, then rejects the flip at admission", func() {
		rbg := flipRBG("flip-to-deprecated")
		Expect(testutil.K8sClient.Create(testutil.Ctx, rbg)).To(Succeed())

		// bare targetContainers under standalonePattern is the legal shape the
		// default routing admits; now flip the annotation to a deprecated
		// surface — the write is an UPDATE, and the webhook re-validates the
		// whole object, new fields included.
		stored := &workloadsv1alpha2.RoleBasedGroup{}
		Expect(testutil.K8sClient.Get(testutil.Ctx,
			types.NamespacedName{Name: rbg.Name, Namespace: testNs}, stored)).To(Succeed())
		stored.Spec.Roles[0].Annotations = map[string]string{
			constants.RoleWorkloadTypeAnnotationKey: constants.DeploymentWorkloadType,
		}
		err := testutil.K8sClient.Update(testutil.Ctx, stored)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(
			"component and targetContainers are only defined for the RoleInstanceSet render path"))
	})

	It("rejects the flipped shape at create too (no write-then-flip needed)", func() {
		rbg := flipRBG("flip-at-create")
		rbg.Spec.Roles[0].Annotations = map[string]string{
			constants.RoleWorkloadTypeAnnotationKey: constants.LeaderWorkerSetWorkloadType,
		}
		err := testutil.K8sClient.Create(testutil.Ctx, rbg)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(
			"component and targetContainers are only defined for the RoleInstanceSet render path"))
	})
})
