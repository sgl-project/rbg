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

package utils

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

const (
	Timeout  = 60 * time.Second
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

func DeletePod(ctx context.Context, rclient client.Client, namespace string, rbgName string) error {
	logger := log.FromContext(ctx)
	// list pod
	podList := &v1.PodList{}
	if err := rclient.List(
		ctx, podList, client.InNamespace(namespace), client.MatchingLabels{
			workloadsv1alpha1.SetNameLabelKey: rbgName,
		},
	); err != nil {
		logger.V(1).Error(err, "list pod error")
		return err
	}

	if len(podList.Items) == 0 {
		err := fmt.Errorf("no pod belongs to rbg %s, can not delete pod", rbgName)
		logger.V(1).Error(err, "pod is empty")
		return err
	}

	err := rclient.Delete(
		ctx, &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podList.Items[0].Name,
				Namespace: namespace,
			},
		},
	)
	if err != nil {
		logger.V(1).Error(err, "delete pod error")
	}
	return err
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
