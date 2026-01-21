/*
Copyright 2025.

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

package envtest

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

var _ = Describe("Pod Controller", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	var (
		testNs string
	)

	BeforeEach(func() {
		testNs = fmt.Sprintf("test-pod-%d", time.Now().UnixNano())
		createNamespace(testNs)
	})

	AfterEach(func() {
		deleteNamespace(testNs)
	})

	Context("When Pod is restarted with RecreateRBGOnPodRestart policy", func() {
		It("Should trigger RBG restart", func() {
			rbgName := "test-rbg-pod-restart"

			// Create RBG with RecreateRBGOnPodRestart policy
			rbg := &workloadsv1alpha1.RoleBasedGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rbgName,
					Namespace: testNs,
				},
				Spec: workloadsv1alpha1.RoleBasedGroupSpec{
					Roles: []workloadsv1alpha1.RoleSpec{
						{
							Name:          "worker",
							Replicas:      ptr.To(int32(1)),
							RestartPolicy: workloadsv1alpha1.RecreateRBGOnPodRestart,
							Workload: workloadsv1alpha1.WorkloadSpec{
								APIVersion: "apps/v1",
								Kind:       "Deployment",
							},
							Template: &corev1.PodTemplateSpec{
								ObjectMeta: metav1.ObjectMeta{
									Labels: map[string]string{
										"app": "test-worker",
									},
								},
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:  "nginx",
											Image: "nginx:latest",
										},
									},
								},
							},
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, rbg)).Should(Succeed())

			// Wait for Deployment to be created
			deployName := fmt.Sprintf("%s-%s", rbgName, "worker")
			deployLookupKey := types.NamespacedName{Name: deployName, Namespace: testNs}
			createdDeploy := &appsv1.Deployment{}
			Eventually(func() error {
				return k8sClient.Get(ctx, deployLookupKey, createdDeploy)
			}, timeout, interval).Should(Succeed())

			// Create a Pod that belongs to this RBG
			podName := "test-pod-to-restart"
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName,
					Namespace: testNs,
					Labels: map[string]string{
						workloadsv1alpha1.SetNameLabelKey: rbgName,
						workloadsv1alpha1.SetRoleLabelKey: "worker",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "nginx",
							Image: "nginx:latest",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).Should(Succeed())

			// Simulate container restart
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: podName, Namespace: testNs}, pod)
				if err != nil {
					return err
				}
				pod.Status.Phase = corev1.PodRunning
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{
					{
						Name:         "nginx",
						RestartCount: 1,
						State: corev1.ContainerState{
							Running: &corev1.ContainerStateRunning{
								StartedAt: metav1.Now(),
							},
						},
					},
				}
				return k8sClient.Status().Update(ctx, pod)
			}, timeout, interval).Should(Succeed())

			// Verify RBG has RestartInProgress condition
			Eventually(func() bool {
				createdRBG := &workloadsv1alpha1.RoleBasedGroup{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: rbgName, Namespace: testNs}, createdRBG)
				if err != nil {
					return false
				}
				for _, cond := range createdRBG.Status.Conditions {
					if cond.Type == string(workloadsv1alpha1.RoleBasedGroupRestartInProgress) {
						return cond.Status == metav1.ConditionTrue || cond.Reason == "RBGRestartCompleted"
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())
		})
	})
})
