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

package restart_policy

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apps "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

// A RoleInstanceSet's ControllerRevision is derived from the whole
// roleInstanceTemplate, so any change to how a field is serialized moves the
// revision hash even when the effective configuration is identical. That happens
// on a controller upgrade: an older release stored the restart policy as the
// `restartPolicy` string, this one stores it under `restartPolicyConfig`.
//
// A revision change on its own is acceptable. Recreating the RoleInstances is not,
// because the pod template did not change and the workload has no reason to restart.
// These specs pin that boundary.
var _ = Describe("RoleInstanceSet revision stability", func() {
	const ns = "default"

	// legacyShapedSet stores the policy through the deprecated string field, which
	// is the shape a pre-restartPolicyConfig release wrote.
	legacyShapedSet := func(name string) *workloadsv1alpha2.RoleInstanceSet {
		return &workloadsv1alpha2.RoleInstanceSet{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: workloadsv1alpha2.RoleInstanceSetSpec{
				Replicas: ptr.To(int32(1)),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				RoleInstanceTemplate: workloadsv1alpha2.RoleInstanceTemplate{
					RoleInstanceSpec: workloadsv1alpha2.RoleInstanceSpec{
						RestartPolicy: workloadsv1alpha2.RestartPolicyNone, //nolint:staticcheck // the pre-upgrade shape is the point
						Components: []workloadsv1alpha2.RoleInstanceComponent{{
							Name: "main",
							Size: ptr.To(int32(1)),
							Template: corev1.PodTemplateSpec{
								ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "main", Image: "nginx:latest"}},
								},
							},
						}},
					},
				},
			},
		}
	}

	instanceUIDs := func(setName string) map[string]types.UID {
		list := &workloadsv1alpha2.RoleInstanceList{}
		Expect(testutil.K8sClient.List(testutil.Ctx, list, client.InNamespace(ns))).To(Succeed())
		out := map[string]types.UID{}
		for i := range list.Items {
			ri := &list.Items[i]
			for _, o := range ri.OwnerReferences {
				if o.Name == setName {
					out[ri.Name] = ri.UID
				}
			}
		}
		return out
	}

	revisionsFor := func(setName string) []string {
		crList := &apps.ControllerRevisionList{}
		Expect(testutil.K8sClient.List(testutil.Ctx, crList, client.InNamespace(ns))).To(Succeed())
		var names []string
		for i := range crList.Items {
			cr := &crList.Items[i]
			for _, o := range cr.OwnerReferences {
				if o.Name == setName {
					names = append(names, fmt.Sprintf("%s(rev=%d)", cr.Name, cr.Revision))
				}
			}
		}
		return names
	}

	It("keeps RoleInstances when only the restart policy serialization changes", func() {
		name := "revision-stability"
		Expect(testutil.K8sClient.Create(testutil.Ctx, legacyShapedSet(name))).To(Succeed())

		By("waiting for the controller to create the RoleInstance")
		var before map[string]types.UID
		Eventually(func() int {
			before = instanceUIDs(name)
			return len(before)
		}, 60*time.Second, time.Second).Should(BeNumerically(">", 0))

		stored := &workloadsv1alpha2.RoleInstanceSet{}
		key := client.ObjectKey{Namespace: ns, Name: name}
		Expect(testutil.K8sClient.Get(testutil.Ctx, key, stored)).To(Succeed())
		beforeRevision := stored.Status.CurrentRevision
		GinkgoWriter.Printf("before: instances=%v currentRevision=%q revisions=%v\n",
			before, beforeRevision, revisionsFor(name))

		By("re-serializing the same policy under restartPolicyConfig")
		Eventually(func() error {
			live := &workloadsv1alpha2.RoleInstanceSet{}
			if err := testutil.K8sClient.Get(testutil.Ctx, key, live); err != nil {
				return err
			}
			live.Spec.RoleInstanceTemplate.RestartPolicy = "" //nolint:staticcheck // clearing the pre-upgrade shape
			live.Spec.RoleInstanceTemplate.RestartPolicyConfig = &workloadsv1alpha2.RestartPolicyConfig{
				Type:             workloadsv1alpha2.RestartPolicyNone,
				BaseDelaySeconds: ptr.To(int32(30)),
				MaxDelaySeconds:  ptr.To(int32(600)),
			}
			return testutil.K8sClient.Update(testutil.Ctx, live)
		}, 30*time.Second, time.Second).Should(Succeed())

		By("checking the RoleInstances are not recreated")
		Consistently(func() map[string]types.UID {
			return instanceUIDs(name)
		}, 45*time.Second, 3*time.Second).Should(Equal(before),
			"a serialization-only change must not recreate the RoleInstances")

		after := &workloadsv1alpha2.RoleInstanceSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, key, after)).To(Succeed())
		GinkgoWriter.Printf("after: instances=%v currentRevision=%q updateRevision=%q revisions=%v\n",
			instanceUIDs(name), after.Status.CurrentRevision, after.Status.UpdateRevision,
			revisionsFor(name))

		// The revision does move, which is what makes the check above meaningful:
		// the set really was re-revisioned and still did not roll.
		Expect(after.Status.UpdateRevision).NotTo(Equal(beforeRevision),
			"the reserialized template should produce a new revision")

		Expect(testutil.K8sClient.Delete(testutil.Ctx, after)).To(Succeed())
	})

	It("resolves the same effective policy from either field shape", func() {
		legacy := workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart, //nolint:staticcheck
		}
		Expect(legacy.GetRestartPolicy()).To(Equal(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart))

		current := workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{
				Type: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart,
			},
		}
		Expect(current.GetRestartPolicy()).To(Equal(workloadsv1alpha2.RecreateRoleInstanceOnPodRestart))

		// An explicit config type wins over the deprecated field.
		both := workloadsv1alpha2.RoleInstanceSpec{
			RestartPolicy: workloadsv1alpha2.RecreateRoleInstanceOnPodRestart, //nolint:staticcheck
			RestartPolicyConfig: &workloadsv1alpha2.RestartPolicyConfig{
				Type: workloadsv1alpha2.RestartPolicyNone,
			},
		}
		Expect(both.GetRestartPolicy()).To(Equal(workloadsv1alpha2.RestartPolicyNone))
	})
})
