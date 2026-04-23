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

package v1alpha2

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	pkgutils "sigs.k8s.io/rbgs/pkg/utils"
	"sigs.k8s.io/rbgs/test/e2e/framework"
	testutils "sigs.k8s.io/rbgs/test/utils"
	wrappersv2 "sigs.k8s.io/rbgs/test/wrappers/v1alpha2"
)

func RunSharedServiceSelectionTestCases(f *framework.Framework) {
	ginkgo.Describe("shared service selection", func() {
		ginkgo.It("should select only leader pod and update the selector in place when disabled", func() {
			role := wrappersv2.BuildLeaderWorkerRole("decode").Obj()
			role.LeaderWorkerPattern.TemplateSource.Template = &corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "main",
							Image:           "registry.k8s.io/pause:3.10",
							ImagePullPolicy: corev1.PullIfNotPresent,
						},
					},
				},
			}
			role.LeaderWorkerPattern.SharedServiceSelection = ptr.To(workloadsv1alpha2.SharedServiceSelectionLeaderOnly)

			rbg := wrappersv2.BuildBasicRoleBasedGroup("shared-service-selection", f.Namespace).
				WithRoles([]workloadsv1alpha2.RoleSpec{role}).
				Obj()

			f.RegisterDebugFn(func() {
				dumpDebugInfo(f, rbg)
				dumpSharedServiceSelectionDebugInfo(f, rbg, role.Name)
			})

			ginkgo.By("Creating an RBG with LeaderOnly shared service selection")
			gomega.Expect(f.Client.Create(f.Ctx, rbg)).Should(gomega.Succeed())
			f.ExpectRbgV2Equal(rbg)

			svcName, err := pkgutils.GetCompatibleHeadlessServiceName(f.Ctx, f.Client, rbg, &rbg.Spec.Roles[0])
			gomega.Expect(err).Should(gomega.Succeed())

			ginkgo.By("Verifying the shared service only selects the leader pod")
			leaderOnlyService := expectServiceSelector(
				f,
				rbg.Namespace,
				svcName,
				map[string]string{
					constants.GroupNameLabelKey:     rbg.Name,
					constants.RoleNameLabelKey:      role.Name,
					constants.ComponentNameLabelKey: "leader",
				},
			)

			podsByComponent := expectLeaderWorkerPods(f, rbg.Namespace, rbg.Name, role.Name)
			expectEndpointSliceTargets(
				f,
				rbg.Namespace,
				svcName,
				podIPsByName(podsByComponent),
				[]string{podsByComponent["leader"].Name},
			)

			ginkgo.By("Removing shared service selection and verifying the Service is updated in place")
			updateRbgV2(f, rbg, func(rbg *workloadsv1alpha2.RoleBasedGroup) {
				rbg.Spec.Roles[0].LeaderWorkerPattern.SharedServiceSelection = nil
			})
			f.ExpectRbgV2Equal(rbg)

			defaultService := expectServiceSelector(
				f,
				rbg.Namespace,
				svcName,
				map[string]string{
					constants.GroupNameLabelKey: rbg.Name,
					constants.RoleNameLabelKey:  role.Name,
				},
			)
			gomega.Expect(defaultService.UID).Should(gomega.Equal(leaderOnlyService.UID))

			expectEndpointSliceTargets(
				f,
				rbg.Namespace,
				svcName,
				podIPsByName(podsByComponent),
				[]string{
					podsByComponent["leader"].Name,
					podsByComponent["worker"].Name,
				},
			)
		})
	})
}

func expectServiceSelector(
	f *framework.Framework, namespace, name string, expectedSelector map[string]string,
) *corev1.Service {
	logger := log.FromContext(f.Ctx).WithValues("service", client.ObjectKey{Namespace: namespace, Name: name})

	svc := &corev1.Service{}
	gomega.Eventually(func() bool {
		if err := f.Client.Get(f.Ctx, client.ObjectKey{Namespace: namespace, Name: name}, svc); err != nil {
			logger.Error(err, "failed to get service")
			return false
		}
		if !reflect.DeepEqual(svc.Spec.Selector, expectedSelector) {
			logger.V(1).Info(
				"waiting for service selector",
				"expectedSelector", expectedSelector,
				"actualSelector", svc.Spec.Selector,
			)
			return false
		}
		return true
	}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

	return svc.DeepCopy()
}

func expectLeaderWorkerPods(
	f *framework.Framework, namespace, groupName, roleName string,
) map[string]corev1.Pod {
	logger := log.FromContext(f.Ctx).WithValues("groupName", groupName, "roleName", roleName)

	podsByComponent := map[string]corev1.Pod{}
	gomega.Eventually(func() bool {
		podList := &corev1.PodList{}
		if err := f.Client.List(
			f.Ctx,
			podList,
			client.InNamespace(namespace),
			client.MatchingLabels{
				constants.GroupNameLabelKey: groupName,
				constants.RoleNameLabelKey:  roleName,
			},
		); err != nil {
			logger.Error(err, "failed to list pods")
			return false
		}

		next := map[string]corev1.Pod{}
		for _, pod := range podList.Items {
			component := pod.Labels[constants.ComponentNameLabelKey]
			if component == "" || pod.Status.PodIP == "" {
				logger.V(1).Info("waiting for labeled leader-worker pods", "pod", pod.Name, "component", component, "podIP", pod.Status.PodIP)
				return false
			}
			next[component] = pod
		}

		if len(next) != 2 {
			logger.V(1).Info("waiting for two leader-worker pods", "podCount", len(next))
			return false
		}
		if _, ok := next["leader"]; !ok {
			return false
		}
		if _, ok := next["worker"]; !ok {
			return false
		}

		podsByComponent = next
		return true
	}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())

	return podsByComponent
}

func expectEndpointSliceTargets(
	f *framework.Framework, namespace, svcName string, podIPs map[string]string, expectedPodNames []string,
) {
	logger := log.FromContext(f.Ctx).WithValues("serviceName", svcName)

	expected := append([]string(nil), expectedPodNames...)
	sort.Strings(expected)

	gomega.Eventually(func() bool {
		sliceList := &discoveryv1.EndpointSliceList{}
		if err := f.Client.List(
			f.Ctx,
			sliceList,
			client.InNamespace(namespace),
			client.MatchingLabels{discoveryv1.LabelServiceName: svcName},
		); err != nil {
			logger.Error(err, "failed to list EndpointSlices")
			return false
		}

		found := make([]string, 0, len(expected))
		seen := map[string]struct{}{}
		for _, slice := range sliceList.Items {
			for _, endpoint := range slice.Endpoints {
				podName := resolveEndpointPodName(endpoint, podIPs)
				if podName == "" {
					logger.V(1).Info("waiting for endpoint target references", "endpoint", fmt.Sprintf("%+v", endpoint))
					return false
				}
				if _, ok := seen[podName]; ok {
					continue
				}
				seen[podName] = struct{}{}
				found = append(found, podName)
			}
		}

		sort.Strings(found)
		if !reflect.DeepEqual(found, expected) {
			logger.V(1).Info("waiting for EndpointSlice targets", "expectedPods", expected, "actualPods", found)
			return false
		}
		return true
	}, testutils.Timeout, testutils.Interval).Should(gomega.BeTrue())
}

func resolveEndpointPodName(endpoint discoveryv1.Endpoint, podIPs map[string]string) string {
	if endpoint.TargetRef != nil && endpoint.TargetRef.Kind == "Pod" && endpoint.TargetRef.Name != "" {
		return endpoint.TargetRef.Name
	}

	for _, address := range endpoint.Addresses {
		for podName, podIP := range podIPs {
			if podIP == address {
				return podName
			}
		}
	}

	return ""
}

func podIPsByName(podsByComponent map[string]corev1.Pod) map[string]string {
	podIPs := make(map[string]string, len(podsByComponent))
	for _, pod := range podsByComponent {
		podIPs[pod.Name] = pod.Status.PodIP
	}
	return podIPs
}

func dumpSharedServiceSelectionDebugInfo(
	f *framework.Framework, rbg *workloadsv1alpha2.RoleBasedGroup, roleName string,
) {
	role := findRoleByName(rbg, roleName)
	if role == nil {
		fmt.Printf("[SharedServiceSelection] Role %s not found in RBG %s\n", roleName, rbg.Name)
		return
	}

	svcName, err := pkgutils.GetCompatibleHeadlessServiceName(f.Ctx, f.Client, rbg, role)
	if err != nil {
		fmt.Printf("[SharedServiceSelection] Failed to resolve Service name: %v\n", err)
		return
	}

	svc := &corev1.Service{}
	if err := f.Client.Get(f.Ctx, client.ObjectKey{Namespace: rbg.Namespace, Name: svcName}, svc); err != nil {
		fmt.Printf("[SharedServiceSelection] Failed to get Service %s: %v\n", svcName, err)
	} else {
		fmt.Printf("[SharedServiceSelection] Service %s selector=%v uid=%s\n", svc.Name, svc.Spec.Selector, svc.UID)
	}

	sliceList := &discoveryv1.EndpointSliceList{}
	if err := f.Client.List(
		f.Ctx,
		sliceList,
		client.InNamespace(rbg.Namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: svcName},
	); err != nil {
		fmt.Printf("[SharedServiceSelection] Failed to list EndpointSlices: %v\n", err)
		return
	}

	fmt.Printf("[SharedServiceSelection] Found %d EndpointSlices for Service %s\n", len(sliceList.Items), svcName)
	for _, slice := range sliceList.Items {
		fmt.Printf("[SharedServiceSelection] EndpointSlice %s endpoints=%d\n", slice.Name, len(slice.Endpoints))
		for _, endpoint := range slice.Endpoints {
			targetName := ""
			if endpoint.TargetRef != nil {
				targetName = endpoint.TargetRef.Name
			}
			fmt.Printf("[SharedServiceSelection] endpoint target=%s addresses=%v ready=%v\n",
				targetName, endpoint.Addresses, endpoint.Conditions.Ready)
		}
	}
}

func findRoleByName(rbg *workloadsv1alpha2.RoleBasedGroup, roleName string) *workloadsv1alpha2.RoleSpec {
	for i := range rbg.Spec.Roles {
		if rbg.Spec.Roles[i].Name == roleName {
			return &rbg.Spec.Roles[i]
		}
	}
	return nil
}
