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

package lifecycle

import (
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	podadapter "sigs.k8s.io/rbgs/pkg/inplace/pod/clientadapter"
	podreadiness "sigs.k8s.io/rbgs/pkg/inplace/pod/readiness"
)

type Interface interface {
	UpdatePodLifecycle(pg *workloadsv1alpha2.RoleInstance, pod *v1.Pod, markNotReady bool) (updated bool, gotPod *v1.Pod, err error)
}

func New(c client.Client) Interface {
	adp := &podadapter.AdapterRuntimeClient{Client: c}
	return &realControl{
		adp:                 adp,
		podReadinessControl: podreadiness.NewForAdapter(adp),
	}
}

type realControl struct {
	adp                 podadapter.Adapter
	podReadinessControl podreadiness.Interface
}

func (c *realControl) UpdatePodLifecycle(_ *workloadsv1alpha2.RoleInstance, pod *v1.Pod, markNotReady bool) (bool, *v1.Pod, error) {
	if !c.needUpdatePodStatus(pod, markNotReady) {
		return false, nil, nil
	}
	var (
		err     error
		updated bool
	)
	if c.podReadinessControl.GetCondition(pod) == nil {
		updated, err = c.podReadinessControl.AddNotReadyKey(pod, getReadinessMessage("InstanceReady"))
	} else {
		if markNotReady {
			updated, err = c.podReadinessControl.AddNotReadyKey(pod, getReadinessMessage("InstanceReady"))
		} else {
			updated, err = c.podReadinessControl.RemoveNotReadyKey(pod, getReadinessMessage("InstanceReady"))
		}
	}
	if err != nil {
		return updated, nil, err
	}
	return updated, pod, nil
}

func (c *realControl) needUpdatePodStatus(pod *v1.Pod, markNotReady bool) bool {
	if c.podReadinessControl.GetCondition(pod) == nil {
		return true
	}
	return markNotReady != c.podReadinessControl.ContainsNotReadyKey(pod, getReadinessMessage("InstanceReady"))
}

// nolint
func getReadinessMessage(key string) podreadiness.Message {
	return podreadiness.Message{UserAgent: "Lifecycle", Key: key}
}
