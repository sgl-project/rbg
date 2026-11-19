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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	v1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	v2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var _ = Describe("Restart configuration conversion contract", func() {
	DescribeTable("keeps the v2 budget on GET but drops it on a v1alpha1 spec update", func(budget int32) {
		cli, ctx := testutil.K8sClient, testutil.Ctx
		template := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:latest"}}}}
		original := &v2.RoleBasedGroup{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("contract-restart-v2-only-%d", budget), Namespace: gateNamespace},
			Spec: v2.RoleBasedGroupSpec{Roles: []v2.RoleSpec{{
				Name: "worker", Replicas: ptr.To[int32](1),
				Pattern: v2.Pattern{LeaderWorkerPattern: &v2.LeaderWorkerPattern{
					Size: ptr.To[int32](2), TemplateSource: v2.TemplateSource{Template: &template},
					RestartPolicyConfig: &v2.RestartPolicyConfig{
						Type: v2.RecreateRoleInstanceOnPodRestart, ActivateAfter: v2.RestartPolicyActivateAfterCreated,
						BaseDelaySeconds: ptr.To[int32](7), MaxDelaySeconds: ptr.To[int32](17), MaxGroupRestarts: ptr.To(budget),
					},
				}},
			}}},
		}
		Expect(cli.Create(ctx, original)).To(Succeed())
		key := client.ObjectKeyFromObject(original)

		stored := &v2.RoleBasedGroup{}
		Expect(cli.Get(ctx, key, stored)).To(Succeed())
		before := stored.DeepCopy()
		Expect(before.Spec.Roles[0].GetRawRestartPolicyConfig().MaxGroupRestarts).To(Equal(ptr.To(budget)))

		// Use the native leaderWorker shape: conversion exposes its pod template
		// at role level in v1alpha1. Only the legacy restartPolicy type is exposed.
		spoke := &v1.RoleBasedGroup{}
		Expect(cli.Get(ctx, key, spoke)).To(Succeed())
		Expect(spoke.Spec.Roles).To(HaveLen(1))
		Expect(spoke.Spec.Roles[0].LeaderWorkerSet).NotTo(BeNil())
		Expect(spoke.Spec.Roles[0].Template).NotTo(BeNil())
		Expect(spoke.Spec.Roles[0].RestartPolicy).To(Equal(v1.RecreateRoleInstanceOnPodRestart))

		// Inspect the actual spoke wire response, not just a typed client that
		// would silently ignore unknown fields during JSON decoding.
		wire := &unstructured.Unstructured{}
		wire.SetGroupVersionKind(v1.GroupVersion.WithKind("RoleBasedGroup"))
		Expect(cli.Get(ctx, key, wire)).To(Succeed())
		roles, found, err := unstructured.NestedSlice(wire.Object, "spec", "roles")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(roles).To(HaveLen(1))
		Expect(roles[0]).NotTo(HaveKey("maxGroupRestarts"))
		Expect(roles[0]).NotTo(HaveKey("restartPolicyConfig"))

		Expect(cli.Get(ctx, key, stored)).To(Succeed())
		Expect(stored.Spec.Roles[0].GetRawRestartPolicyConfig()).To(Equal(before.Spec.Roles[0].GetRawRestartPolicyConfig()),
			"v1alpha1 GET must not modify the persisted v2 restart configuration")
		Expect(stored.ResourceVersion).To(Equal(before.ResourceVersion))

		// A full v1alpha1 read-modify-write is intentionally lossy for v2-only
		// fields, even when the edit only changes the replica count.
		Expect(retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			if err := cli.Get(ctx, key, spoke); err != nil {
				return err
			}
			spoke.Spec.Roles[0].Replicas = ptr.To[int32](2)
			return cli.Update(ctx, spoke)
		})).To(Succeed())

		Expect(cli.Get(ctx, key, stored)).To(Succeed())
		Expect(stored.Spec.Roles[0].Replicas).To(Equal(ptr.To[int32](2)))
		Expect(stored.Spec.Roles[0].GetRawRestartPolicyConfig()).NotTo(BeNil())
		Expect(stored.Spec.Roles[0].GetRawRestartPolicyConfig().Type).To(Equal(v2.RecreateRoleInstanceOnPodRestart))
		Expect(stored.Spec.Roles[0].GetRawRestartPolicyConfig().MaxGroupRestarts).To(BeNil(),
			"v1alpha1 spec updates drop the unsupported budget, including explicit zero")
	},
		Entry("explicit zero", int32(0)),
		Entry("positive budget", int32(2)),
	)
})
