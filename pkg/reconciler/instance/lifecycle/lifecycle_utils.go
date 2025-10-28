package lifecycle

import (
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	podadapter "sigs.k8s.io/rbgs/pkg/inplace/pod/clientadapter"
	podreadiness "sigs.k8s.io/rbgs/pkg/inplace/pod/readiness"
)

type Interface interface {
	UpdatePodLifecycle(pg *v1alpha1.Instance, pod *v1.Pod, markNotReady bool) (updated bool, gotPod *v1.Pod, err error)
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

func (c *realControl) UpdatePodLifecycle(_ *v1alpha1.Instance, pod *v1.Pod, markNotReady bool) (bool, *v1.Pod, error) {
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
