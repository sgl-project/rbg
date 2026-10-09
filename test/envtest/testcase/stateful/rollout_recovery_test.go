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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/test/envtest/testutil"
)

var _ = Describe("Stateful RoleInstanceSet rollout recovery", func() {
	const (
		timeout  = 25 * time.Second
		interval = 250 * time.Millisecond
	)

	var testNs string

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-stateful-rollout-%d", time.Now().UnixNano())
		testutil.CreateNamespace(testNs)
	})

	AfterEach(func() {
		testutil.DeleteNamespace(testNs)
	})

	It("replaces a stably unhealthy rollout target through the controller requeue path", func() {
		setName := "stale-rollout"
		setKey := types.NamespacedName{Name: setName, Namespace: testNs}

		set := &workloadsv1alpha2.RoleInstanceSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      setName,
				Namespace: testNs,
				Annotations: map[string]string{
					constants.RoleInstancePatternKey: string(constants.StatefulPattern),
				},
			},
			Spec: workloadsv1alpha2.RoleInstanceSetSpec{
				Replicas:            ptr.To[int32](1),
				PodManagementPolicy: constants.ParallelPodManagement,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": setName},
				},
				RoleInstanceTemplate: workloadsv1alpha2.RoleInstanceTemplate{
					RoleInstanceSpec: workloadsv1alpha2.RoleInstanceSpec{
						Components: []workloadsv1alpha2.RoleInstanceComponent{
							{
								Name: "worker",
								Size: ptr.To[int32](1),
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{
										Containers: []corev1.Container{
											{Name: "nginx", Image: "nginx:1.0"},
										},
									},
								},
							},
						},
					},
				},
				UpdateStrategy: workloadsv1alpha2.RoleInstanceSetUpdateStrategy{
					Type:           workloadsv1alpha2.RecreatePodUpdateStrategyType,
					MaxUnavailable: ptr.To(intstr.FromInt32(1)),
					MaxSurge:       ptr.To(intstr.FromInt32(0)),
				},
			},
		}
		Expect(testutil.K8sClient.Create(testutil.Ctx, set)).To(Succeed())

		instanceKey := types.NamespacedName{Name: fmt.Sprintf("%s-0", setName), Namespace: testNs}
		initialInstance := &workloadsv1alpha2.RoleInstance{}
		Eventually(func() error {
			return testutil.K8sClient.Get(testutil.Ctx, instanceKey, initialInstance)
		}, timeout, interval).Should(Succeed(), "initial RoleInstance should be created")

		podKey := types.NamespacedName{Name: fmt.Sprintf("%s-0-worker-0", setName), Namespace: testNs}
		Eventually(func() error {
			return setPodReady(podKey, true)
		}, timeout, interval).Should(Succeed(), "initial pod should become ready")
		Eventually(func() (int32, error) {
			created := &workloadsv1alpha2.RoleInstanceSet{}
			if err := testutil.K8sClient.Get(testutil.Ctx, setKey, created); err != nil {
				return 0, err
			}
			return created.Status.ReadyReplicas, nil
		}, timeout, interval).Should(Equal(int32(1)), "initial instance should become ready")

		Eventually(func() error {
			return setPodReady(podKey, false)
		}, timeout, interval).Should(Succeed(), "initial pod should become unhealthy")
		Eventually(func() (int32, error) {
			created := &workloadsv1alpha2.RoleInstanceSet{}
			if err := testutil.K8sClient.Get(testutil.Ctx, setKey, created); err != nil {
				return 0, err
			}
			return created.Status.ReadyReplicas, nil
		}, timeout, interval).Should(Equal(int32(0)), "controller should observe the unhealthy instance")

		current := &workloadsv1alpha2.RoleInstanceSet{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, setKey, current)).To(Succeed())
		current.Spec.RoleInstanceTemplate.Components[0].Template.Spec.Containers[0].Image = "nginx:2.0"
		Expect(testutil.K8sClient.Update(testutil.Ctx, current)).To(Succeed())

		var updateRevision string
		Eventually(func() (string, error) {
			created := &workloadsv1alpha2.RoleInstanceSet{}
			if err := testutil.K8sClient.Get(testutil.Ctx, setKey, created); err != nil {
				return "", err
			}
			if created.Status.UpdateRevision == "" || created.Status.UpdateRevision == created.Status.CurrentRevision {
				return "", fmt.Errorf("update revision %q has not diverged from current revision %q", created.Status.UpdateRevision, created.Status.CurrentRevision)
			}
			updateRevision = created.Status.UpdateRevision
			return updateRevision, nil
		}, timeout, interval).ShouldNot(BeEmpty(), "update revision should be created")

		oldUID := initialInstance.UID
		Consistently(func() (types.UID, error) {
			instance := &workloadsv1alpha2.RoleInstance{}
			if err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, instance); err != nil {
				return "", err
			}
			return instance.UID, nil
		}, 8*time.Second, interval).Should(Equal(oldUID), "replacement should wait for the stable-unhealthy window")

		Eventually(func() (bool, error) {
			instance := &workloadsv1alpha2.RoleInstance{}
			if err := testutil.K8sClient.Get(testutil.Ctx, instanceKey, instance); err != nil {
				return false, err
			}
			return instance.UID != oldUID && instance.Labels["controller-revision-hash"] == updateRevision, nil
		}, timeout, interval).Should(BeTrue(), "replacement RoleInstance should be created at the update revision")

		finalInstance := &workloadsv1alpha2.RoleInstance{}
		Expect(testutil.K8sClient.Get(testutil.Ctx, instanceKey, finalInstance)).To(Succeed())
		Expect(finalInstance.UID).NotTo(Equal(oldUID))
		Expect(finalInstance.Labels["controller-revision-hash"]).To(Equal(updateRevision))
	})
})

func setPodReady(key types.NamespacedName, ready bool) error {
	pod := &corev1.Pod{}
	if err := testutil.K8sClient.Get(testutil.Ctx, key, pod); err != nil {
		return err
	}
	if ready {
		testutil.SetPodRunningAndReady(pod)
	} else {
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{
			{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.Now(),
			},
			{
				Type:               corev1.ContainersReady,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.Now(),
			},
		}
		for i := range pod.Status.ContainerStatuses {
			pod.Status.ContainerStatuses[i].Ready = false
		}
	}
	return testutil.K8sClient.Status().Update(testutil.Ctx, pod)
}
