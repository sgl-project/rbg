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

package deprecationv1a1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	v1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	v2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var _ = Describe("Restart configuration conversion contract", func() {
	It("preserves a v2 policy and budget when read through v1alpha1", func() {
		cli, ctx := testutil.K8sClient, testutil.Ctx
		template := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:latest"}}}}
		original := &v2.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Name: "contract-restart-roundtrip", Namespace: testutil.ContractNamespace}, Spec: v2.RoleBasedGroupSpec{Roles: []v2.RoleSpec{{Name: "worker", Replicas: ptr.To[int32](1), Pattern: v2.Pattern{CustomComponentsPattern: &v2.CustomComponentsPattern{RestartPolicyConfig: &v2.RestartPolicyConfig{Type: v2.RecreateRoleInstanceOnPodRestartFromCreation, BaseDelaySeconds: ptr.To[int32](7), MaxDelaySeconds: ptr.To[int32](17), MaxGroupRestarts: ptr.To[int32](0)}, Components: []v2.InstanceComponent{{Name: "rank", Size: ptr.To[int32](2), Template: template}}}}}}}}
		Expect(cli.Create(ctx, original)).To(Succeed())
		key := client.ObjectKeyFromObject(original)

		// v1alpha1 only carries the effective policy and the budget; the backoff
		// delays are v1alpha2-only, so they are intentionally not exposed here.
		spoke := &v1.RoleBasedGroup{}
		Expect(cli.Get(ctx, key, spoke)).To(Succeed())
		Expect(spoke.Spec.Roles[0].RestartPolicy).To(Equal(v1.RecreateRoleInstanceOnPodRestartFromCreation))
		Expect(spoke.Spec.Roles[0].MaxGroupRestarts).To(Equal(ptr.To[int32](0)))

		// A component-backed role has no role-level template, so the spec change is
		// made through v1alpha2: v1alpha1 still requires a role-level template.
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			stored := &v2.RoleBasedGroup{}
			if err := cli.Get(ctx, key, stored); err != nil {
				return err
			}
			stored.Spec.Roles[0].Replicas = ptr.To[int32](2)
			return cli.Update(ctx, stored)
		})).To(Succeed())

		Expect(cli.Get(ctx, key, spoke)).To(Succeed())
		Expect(spoke.Spec.Roles[0].RestartPolicy).To(Equal(v1.RecreateRoleInstanceOnPodRestartFromCreation))
		Expect(spoke.Spec.Roles[0].MaxGroupRestarts).To(Equal(ptr.To[int32](0)))
		Expect(spoke.Spec.Roles[0].Replicas).To(Equal(ptr.To[int32](2)))
	})

	It("accepts the policy and budget through v1alpha1, and rejects invalid budgets at admission", func() {
		cli, ctx := testutil.K8sClient, testutil.Ctx
		template := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:latest"}}}}
		// v1alpha1 requires a role-level template for this shape; conversion uses
		// the per-component templates for a native RoleInstanceSet role.
		role := v1.RoleSpec{
			Name:             "worker",
			Replicas:         ptr.To[int32](1),
			Workload:         v1.WorkloadSpec{APIVersion: v2.GroupVersion.String(), Kind: "RoleInstanceSet"},
			TemplateSource:   v1.TemplateSource{Template: &template},
			RestartPolicy:    v1.RecreateRoleInstanceOnPodRestartFromCreation,
			MaxGroupRestarts: ptr.To[int32](2),
			Components:       []v1.InstanceComponent{{Name: "rank", Size: ptr.To[int32](2), Template: template}},
		}
		rbg := &v1.RoleBasedGroup{ObjectMeta: metav1.ObjectMeta{Name: "contract-restart-alpha1", Namespace: testutil.ContractNamespace}, Spec: v1.RoleBasedGroupSpec{Roles: []v1.RoleSpec{role}}}
		Expect(cli.Create(ctx, rbg)).To(Succeed())
		hub := &v2.RoleBasedGroup{}
		Expect(cli.Get(ctx, client.ObjectKeyFromObject(rbg), hub)).To(Succeed())
		Expect(hub.Spec.Roles[0].GetRestartPolicy()).To(Equal(v2.RecreateRoleInstanceOnPodRestartFromCreation))
		Expect(hub.Spec.Roles[0].GetRestartPolicyConfig().MaxGroupRestarts).To(Equal(ptr.To[int32](2)))
		// v1alpha1 cannot set the backoff delays, so the hub schema defaults apply
		// (30s base, 600s cap) instead of carrying a v1alpha1 value.
		Expect(hub.Spec.Roles[0].GetRestartPolicyConfig().BaseDelaySeconds).To(Equal(ptr.To[int32](30)), "v1alpha1 has no backoff fields; the hub default applies")
		Expect(hub.Spec.Roles[0].GetRestartPolicyConfig().MaxDelaySeconds).To(Equal(ptr.To[int32](600)))

		invalid := rbg.DeepCopy()
		invalid.Name = "contract-restart-invalid-none"
		invalid.ResourceVersion = ""
		invalid.UID = ""
		invalid.Spec.Roles[0].RestartPolicy = v1.NoneRestartPolicy
		Expect(cli.Create(ctx, invalid)).NotTo(Succeed())

		// The RoleSpec CEL rule also rejects a budget without an explicit policy.
		unset := rbg.DeepCopy()
		unset.Name = "contract-restart-invalid-unset"
		unset.ResourceVersion = ""
		unset.UID = ""
		unset.Spec.Roles[0].RestartPolicy = ""
		Expect(cli.Create(ctx, unset)).NotTo(Succeed())
	})
})
