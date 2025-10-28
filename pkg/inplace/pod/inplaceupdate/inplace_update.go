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

package inplaceupdate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appspub "sigs.k8s.io/rbgs/api/workloads/inplaceupdate/pod"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	inplaceutils "sigs.k8s.io/rbgs/pkg/inplace/pod"
	podadapter "sigs.k8s.io/rbgs/pkg/inplace/pod/clientadapter"
	"sigs.k8s.io/rbgs/pkg/utils/revisionadapter"
)

var (
	inPlaceUpdatePatchRexp = regexp.MustCompile("^/spec/containers/([0-9]+)/image$")
)

type RefreshResult struct {
	RefreshErr    error
	DelayDuration time.Duration
}

type UpdateResult struct {
	InPlaceUpdate      bool
	UpdateErr          error
	DelayDuration      time.Duration
	NewResourceVersion string
}

type UpdateOptions struct {
	GracePeriodSeconds int32
	AdditionalFuncs    []func(*v1.Pod)

	CalculateSpec        func(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) *UpdateSpec
	PatchSpecToPod       func(pod *v1.Pod, spec *UpdateSpec) (*v1.Pod, error)
	CheckUpdateCompleted func(pod *v1.Pod) error
	GetRevision          func(rev *apps.ControllerRevision) string
}

// Interface for managing pods in-place update.
type Interface interface {
	Refresh(pod *v1.Pod, opts *UpdateOptions) RefreshResult
	CanUpdateInPlace(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) bool
	Update(pod *v1.Pod, oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) UpdateResult
}

// UpdateSpec records the images of containers which need to in-place update.
type UpdateSpec struct {
	Revision    string            `json:"revision"`
	Annotations map[string]string `json:"annotations,omitempty"`

	ContainerImages map[string]string `json:"containerImages,omitempty"`
	MetaDataPatch   []byte            `json:"metaDataPatch,omitempty"`
	GraceSeconds    int32             `json:"graceSeconds,omitempty"`

	OldTemplate *v1.PodTemplateSpec `json:"oldTemplate,omitempty"`
	NewTemplate *v1.PodTemplateSpec `json:"newTemplate,omitempty"`
}

type realControl struct {
	podAdapter      podadapter.Adapter
	revisionAdapter revisionadapter.Interface

	// just for test
	now func() metav1.Time
}

func New(c client.Client, revisionAdapter revisionadapter.Interface) Interface {
	return &realControl{podAdapter: &podadapter.AdapterRuntimeClient{Client: c}, revisionAdapter: revisionAdapter, now: metav1.Now}
}

func NewForTypedClient(c clientset.Interface, revisionAdapter revisionadapter.Interface) Interface {
	return &realControl{podAdapter: &podadapter.AdapterTypedClient{Client: c}, revisionAdapter: revisionAdapter, now: metav1.Now}
}

func NewForInformer(informer coreinformers.PodInformer, revisionAdapter revisionadapter.Interface) Interface {
	return &realControl{podAdapter: &podadapter.AdapterInformer{PodInformer: informer}, revisionAdapter: revisionAdapter, now: metav1.Now}
}

func NewForTest(c client.Client, revisionAdapter revisionadapter.Interface, now func() metav1.Time) Interface {
	return &realControl{podAdapter: &podadapter.AdapterRuntimeClient{Client: c}, revisionAdapter: revisionAdapter, now: now}
}

func (c *realControl) Refresh(pod *v1.Pod, opts *UpdateOptions) RefreshResult {
	opts = SetOptionsDefaults(opts)

	if err := c.refreshCondition(pod, opts); err != nil {
		return RefreshResult{RefreshErr: err}
	}

	var delayDuration time.Duration
	var err error
	if gracePeriod, _ := appspub.GetInPlaceUpdateGrace(pod); gracePeriod != "" {
		if delayDuration, err = c.finishGracePeriod(pod, opts); err != nil {
			return RefreshResult{RefreshErr: err}
		}
	}

	return RefreshResult{DelayDuration: delayDuration}
}

func (c *realControl) refreshCondition(pod *v1.Pod, opts *UpdateOptions) error {
	// no need to update condition because of no readiness-gate
	if !inplaceutils.ContainsInPlaceReadinessGate(pod) {
		return nil
	}

	// in-place updating has not completed yet
	if checkErr := opts.CheckUpdateCompleted(pod); checkErr != nil {
		klog.V(6).Infof("Check Pod %s/%s in-place update not completed yet: %v", pod.Namespace, pod.Name, checkErr)
		return nil
	}

	// already ready
	if existingCondition := inplaceutils.GetInPlaceCondition(pod); existingCondition != nil && existingCondition.Status == v1.ConditionTrue {
		return nil
	}

	newCondition := v1.PodCondition{
		Type:               appspub.InPlaceUpdateReady,
		Status:             v1.ConditionTrue,
		LastTransitionTime: c.now(),
	}
	return c.updateCondition(pod, newCondition)
}

func (c *realControl) updateCondition(pod *v1.Pod, condition v1.PodCondition) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		clone, err := c.podAdapter.GetPod(pod.Namespace, pod.Name)
		if err != nil {
			return err
		}

		inplaceutils.SetPodCondition(clone, condition)
		// We only update the ready condition to False, and let Kubelet update it to True
		if condition.Status == v1.ConditionFalse {
			inplaceutils.UpdatePodReadyCondition(clone)
		}
		return c.podAdapter.UpdatePodStatus(clone)
	})
}

func (c *realControl) finishGracePeriod(pod *v1.Pod, opts *UpdateOptions) (time.Duration, error) {
	var delayDuration time.Duration
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		clone, err := c.podAdapter.GetPod(pod.Namespace, pod.Name)
		if err != nil {
			return err
		}

		spec := UpdateSpec{}
		updateSpecJSON, ok := appspub.GetInPlaceUpdateGrace(clone)
		if !ok {
			return nil
		}
		if err := json.Unmarshal([]byte(updateSpecJSON), &spec); err != nil {
			return err
		}
		graceDuration := time.Second * time.Duration(spec.GraceSeconds)

		updateState := appspub.InPlaceUpdateState{}
		updateStateJSON, ok := appspub.GetInPlaceUpdateState(clone)
		if !ok {
			return fmt.Errorf("pod has %s but %s not found", appspub.InPlaceUpdateGraceKey, appspub.InPlaceUpdateStateKey)
		}
		if err := json.Unmarshal([]byte(updateStateJSON), &updateState); err != nil {
			return nil
		}

		if !c.revisionAdapter.EqualToRevisionHash("", clone, spec.Revision) {
			// If revision-hash has changed, just drop this GracePeriodSpec and go through the normal update process again.
			appspub.RemoveInPlaceUpdateGrace(clone)
		} else {
			if span := time.Since(updateState.UpdateTimestamp.Time); span < graceDuration {
				delayDuration = inplaceutils.RoundupSeconds(graceDuration - span)
				return nil
			}

			if clone, err = opts.PatchSpecToPod(clone, &spec); err != nil {
				return err
			}
			appspub.RemoveInPlaceUpdateGrace(clone)
		}

		return c.podAdapter.UpdatePod(clone)
	})

	return delayDuration, err
}

func (c *realControl) CanUpdateInPlace(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) bool {
	opts = SetOptionsDefaults(opts)
	return opts.CalculateSpec(oldRevision, newRevision, opts) != nil
}

func (c *realControl) Update(pod *v1.Pod, oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) UpdateResult {
	opts = SetOptionsDefaults(opts)

	// 1. calculate inplace update spec
	spec := opts.CalculateSpec(oldRevision, newRevision, opts)
	if spec == nil {
		return UpdateResult{}
	}

	// 2. update condition for pod with readiness-gate
	if inplaceutils.ContainsInPlaceReadinessGate(pod) {
		newCondition := v1.PodCondition{
			Type:               appspub.InPlaceUpdateReady,
			LastTransitionTime: c.now(),
			Status:             v1.ConditionFalse,
			Reason:             "StartInPlaceUpdate",
		}
		if err := c.updateCondition(pod, newCondition); err != nil {
			return UpdateResult{InPlaceUpdate: true, UpdateErr: err}
		}
	}

	// update condition for pod with instance-readiness-gate
	if inplaceutils.ContainsInstanceReadinessGate(pod) {
		newCondition := v1.PodCondition{
			Type:               v1alpha1.InstancePodReadyConditionType,
			LastTransitionTime: c.now(),
			Status:             v1.ConditionFalse,

			Reason: "StartInstanceUpdate",
		}
		if err := c.updateCondition(pod, newCondition); err != nil {
			return UpdateResult{InPlaceUpdate: true, UpdateErr: err}
		}
	}

	// 3. update container images
	if err := c.updatePodInPlace(pod, spec, opts); err != nil {
		return UpdateResult{InPlaceUpdate: true, UpdateErr: err}
	}

	var delayDuration time.Duration
	if opts.GracePeriodSeconds > 0 {
		delayDuration = time.Second * time.Duration(opts.GracePeriodSeconds)
	}
	return UpdateResult{InPlaceUpdate: true, DelayDuration: delayDuration}
}

func (c *realControl) updatePodInPlace(pod *v1.Pod, spec *UpdateSpec, opts *UpdateOptions) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		clone, err := c.podAdapter.GetPod(pod.Namespace, pod.Name)
		if err != nil {
			return err
		}

		// update new revision
		c.revisionAdapter.WriteRevisionHash(clone, spec.Revision)
		if clone.Annotations == nil {
			clone.Annotations = map[string]string{}
		}
		for _, f := range opts.AdditionalFuncs {
			f(clone)
		}

		// record old containerStatuses
		inPlaceUpdateState := appspub.InPlaceUpdateState{
			Revision:              spec.Revision,
			UpdateTimestamp:       c.now(),
			LastContainerStatuses: make(map[string]appspub.InPlaceUpdateContainerStatus, len(spec.ContainerImages)),
		}
		for _, c := range clone.Status.ContainerStatuses {
			if _, ok := spec.ContainerImages[c.Name]; ok {
				inPlaceUpdateState.LastContainerStatuses[c.Name] = appspub.InPlaceUpdateContainerStatus{
					ImageID: c.ImageID,
				}
			}
		}
		inPlaceUpdateStateJSON, _ := json.Marshal(inPlaceUpdateState)
		clone.Annotations[appspub.InPlaceUpdateStateKey] = string(inPlaceUpdateStateJSON)

		if spec.GraceSeconds <= 0 {
			if clone, err = opts.PatchSpecToPod(clone, spec); err != nil {
				return err
			}
			appspub.RemoveInPlaceUpdateGrace(clone)
		} else {
			inPlaceUpdateSpecJSON, _ := json.Marshal(spec)
			clone.Annotations[appspub.InPlaceUpdateGraceKey] = string(inPlaceUpdateSpecJSON)
		}

		return c.podAdapter.UpdatePod(clone)
	})
}

// GetTemplateFromRevision returns the pod template parsed from ControllerRevision.
func GetTemplateFromRevision(revision *apps.ControllerRevision) (*v1.PodTemplateSpec, error) {
	var patchObj *struct {
		Spec struct {
			Template v1.PodTemplateSpec `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(revision.Data.Raw, &patchObj); err != nil {
		return nil, err
	}
	return &patchObj.Spec.Template, nil
}
