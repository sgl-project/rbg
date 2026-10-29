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

package stateful

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var _ = Describe("Stateful RoleInstance orphan Pod recovery", func() {
	const (
		timeout  = 25 * time.Second
		interval = 250 * time.Millisecond
	)

	var testNs string

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-stateful-orphan-gc-%d", time.Now().UnixNano())
		testutil.CreateNamespace(testNs)
	})

	AfterEach(func() {
		testutil.DeleteNamespace(testNs)
	})

	It("recreates a replacement Pod through the controller retry after orphan GC", func() {
		instance := newOrphanGCRecoveryRoleInstance("migrated-0", testNs)
		oldPod := newOrphanGCLegacyPod(instance)

		Expect(testutil.K8sClient.Create(testutil.Ctx, oldPod)).To(Succeed())
		Expect(testutil.K8sClient.Create(testutil.Ctx, instance)).To(Succeed())

		legacyPod := &corev1.Pod{}
		legacyPodKey := client.ObjectKeyFromObject(oldPod)
		Expect(testutil.K8sClient.Get(testutil.Ctx, legacyPodKey, legacyPod)).To(Succeed())
		legacyPodUID := legacyPod.UID

		instanceKey := client.ObjectKeyFromObject(instance)
		currentInstance := &workloadsv1alpha2.RoleInstance{}
		Eventually(func() error {
			return testutil.K8sClient.Get(testutil.Ctx, instanceKey, currentInstance)
		}, timeout, interval).Should(Succeed())

		Consistently(func() ([]string, error) {
			pods := &corev1.PodList{}
			if err := testutil.K8sClient.List(testutil.Ctx, pods, client.InNamespace(testNs)); err != nil {
				return nil, err
			}
			names := make([]string, 0, len(pods.Items))
			for _, pod := range pods.Items {
				names = append(names, pod.Name)
			}
			return names, nil
		}, 2*time.Second, interval).Should(Equal([]string{oldPod.Name}),
			"the foreign same-name Pod must be preserved while it awaits garbage collection")

		Eventually(func() (bool, error) {
			if err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, currentInstance); err != nil {
				return false, err
			}
			for _, condition := range currentInstance.Status.Conditions {
				if condition.Type == workloadsv1alpha2.RoleInstanceFailedScale &&
					condition.Status == corev1.ConditionTrue {
					return true, nil
				}
			}
			return false, nil
		}, timeout, interval).Should(BeTrue(), "the blocked creation should be reported through FailedScale")

		Expect(testutil.K8sClient.Delete(testutil.Ctx, oldPod)).To(Succeed())
		Eventually(func() (bool, error) {
			pod := &corev1.Pod{}
			err := testutil.K8sClient.Get(testutil.Ctx, legacyPodKey, pod)
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			return pod.UID != legacyPodUID, nil
		}, timeout, interval).Should(BeTrue(),
			"the legacy Pod should be gone or replaced by a Pod with a different UID")

		var replacement *corev1.Pod
		Eventually(func() (bool, error) {
			pod := &corev1.Pod{}
			if err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, pod); err != nil {
				return false, err
			}
			if !metav1.IsControlledBy(pod, currentInstance) {
				return false, nil
			}
			replacement = pod
			return true, nil
		}, timeout, interval).Should(BeTrue(),
			"controller-runtime retry should create the replacement Pod without a manual reconcile")

		replacementKey := types.NamespacedName{Namespace: replacement.Namespace, Name: replacement.Name}
		Eventually(func() error {
			return setPodReady(replacementKey, true)
		}, timeout, interval).Should(Succeed(), "the replacement Pod should become ready")

		Eventually(func() (bool, error) {
			instance := &workloadsv1alpha2.RoleInstance{}
			if err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, instance); err != nil {
				return false, err
			}
			ready := false
			failedScale := false
			for _, condition := range instance.Status.Conditions {
				switch condition.Type {
				case workloadsv1alpha2.RoleInstanceReady:
					ready = condition.Status == corev1.ConditionTrue
				case workloadsv1alpha2.RoleInstanceFailedScale:
					failedScale = condition.Status == corev1.ConditionTrue
				}
			}
			return ready && !failedScale, nil
		}, timeout, interval).Should(BeTrue(),
			"the recovered instance should become Ready and clear the transient FailedScale condition")
	})
})

func newOrphanGCRecoveryRoleInstance(name, namespace string) *workloadsv1alpha2.RoleInstance {
	return &workloadsv1alpha2.RoleInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				constants.RoleTypeLabelKey: string(constants.PodTemplateTemplateType),
			},
		},
		Spec: workloadsv1alpha2.RoleInstanceSpec{
			Components: []workloadsv1alpha2.RoleInstanceComponent{{
				Name: "legacy",
				Size: ptr.To(int32(1)),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "main", Image: "rbg:latest"}},
					},
				},
			}},
		},
	}
}

func newOrphanGCLegacyPod(instance *workloadsv1alpha2.RoleInstance) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "StatefulSet",
				Name:       "legacy",
				UID:        types.UID("old-statefulset"),
				Controller: ptr.To(true),
			}},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "main", Image: "rbg:legacy"}},
		},
	}
}
