/*
Copyright 2025 The RBG Authors.

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

package utils

import (
	"context"
	"time"

	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubecontroller "k8s.io/kubernetes/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

const (
	Timeout  = 150 * time.Second
	Interval = time.Millisecond * 250

	DefaultImage                    = "registry.cn-hangzhou.aliyuncs.com/acs-sample/nginx:latest"
	DefaultEngineRuntimeProfileName = "patio-runtime"
)

func CreatePatioRuntime(ctx context.Context, rclient client.Client) error {
	runtime := workloadsv1alpha1.ClusterEngineRuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name: DefaultEngineRuntimeProfileName,
		},
		Spec: workloadsv1alpha1.ClusterEngineRuntimeProfileSpec{
			Volumes: []v1.Volume{
				{
					Name: "patio-group-config",
					VolumeSource: v1.VolumeSource{
						EmptyDir: &v1.EmptyDirVolumeSource{},
					},
				},
			},
			Containers: []v1.Container{
				{
					Name:  "patio-runtime",
					Image: "registry-cn-hangzhou.ack.aliyuncs.com/dev/patio-runtime:v0.2.0",
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "patio-group-config",
							MountPath: "/etc/patio",
						},
					},
				},
			},
			UpdateStrategy: workloadsv1alpha1.NoUpdateStrategy,
		},
	}

	if err := rclient.Create(ctx, &runtime); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	return nil
}


func UpdateRbg(
	ctx context.Context, rclient client.Client, rbg *workloadsv1alpha1.RoleBasedGroup,
	updateFunc func(rbg *workloadsv1alpha1.RoleBasedGroup),
) {
	logger := log.FromContext(ctx)

	gomega.Eventually(
		func() bool {
			err := rclient.Get(
				ctx, client.ObjectKey{
					Name:      rbg.Name,
					Namespace: rbg.Namespace,
				}, rbg,
			)
			if err != nil {
				logger.V(1).Error(err, "get rbg error")
				return false
			}
			updateFunc(rbg)

			err = rclient.Update(ctx, rbg)
			if err != nil {
				logger.V(1).Error(err, "update rbg error")
			}
			return err == nil
		}, Timeout, Interval,
	).Should(gomega.BeTrue())
}

func UpdateRbgSet(
	ctx context.Context, rclient client.Client, rbgset *workloadsv1alpha1.RoleBasedGroupSet,
	updateFunc func(rbgset *workloadsv1alpha1.RoleBasedGroupSet),
) {
	logger := log.FromContext(ctx)

	gomega.Eventually(
		func() bool {
			err := rclient.Get(
				ctx, client.ObjectKey{
					Name:      rbgset.Name,
					Namespace: rbgset.Namespace,
				}, rbgset,
			)
			if err != nil {
				logger.V(1).Error(err, "get rbg error")
				return false
			}
			updateFunc(rbgset)

			err = rclient.Update(ctx, rbgset)
			if err != nil {
				logger.V(1).Error(err, "update rbgset error")
			}
			return err == nil
		}, Timeout, Interval,
	).Should(gomega.BeTrue())
}

func MapContains(m map[string]string, key, value string) bool {
	for k, v := range m {
		if k == key && v == value {
			return true
		}
	}
	return false
}

// SetPodEvicted simulates a Pod being evicted by updating its status subresource.
// This is used in e2e tests to trigger inactive pod handling.
func SetPodEvicted(ctx context.Context, rclient client.Client, pod *v1.Pod) error {
	pod.Status.Phase = v1.PodFailed
	pod.Status.Reason = "Evicted"
	pod.Status.Message = "The node was low on resource: ephemeral-storage. Evicted."
	return rclient.Status().Update(ctx, pod)
}

// SetPodUnexpectedAdmissionError simulates a Pod with unexpected admission error.
func SetPodUnexpectedAdmissionError(ctx context.Context, rclient client.Client, pod *v1.Pod) error {
	pod.Status.Phase = v1.PodFailed
	pod.Status.Reason = "UnexpectedAdmissionError"
	pod.Status.Message = "Pod rejected by admission webhook"
	return rclient.Status().Update(ctx, pod)
}

// SetPodFailed simulates a Pod in Failed state with generic error.
func SetPodFailed(ctx context.Context, rclient client.Client, pod *v1.Pod) error {
	pod.Status.Phase = v1.PodFailed
	pod.Status.Reason = "Error"
	pod.Status.Message = "Container exited with error"
	return rclient.Status().Update(ctx, pod)
}

// GetActivePodCount returns the count of active pods for a given RBG.
// Uses the native Kubernetes IsPodActive function for consistency.
func GetActivePodCount(ctx context.Context, rclient client.Client, namespace, rbgName string) (int, error) {
	podList := &v1.PodList{}
	if err := rclient.List(ctx, podList,
		client.InNamespace(namespace),
		client.MatchingLabels{constants.GroupNameLabelKey: rbgName}); err != nil {
		return 0, err
	}

	// Use native IsPodActive from k8s.io/kubernetes/pkg/controller
	// Pod is active if: Phase != Succeeded && Phase != Failed && DeletionTimestamp == nil
	count := 0
	for i := range podList.Items {
		if kubecontroller.IsPodActive(&podList.Items[i]) {
			count++
		}
	}
	return count, nil
}

