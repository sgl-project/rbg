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
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	inplaceapi "sigs.k8s.io/rbgs/api/workloads/inplaceupdate/instance"
	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	inplaceutil "sigs.k8s.io/rbgs/pkg/utils/inplace/instance"
	"sigs.k8s.io/rbgs/pkg/utils/inplace/instance/clientdapter"
	"sigs.k8s.io/rbgs/pkg/utils/revisionadapter"
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
	AdditionalFuncs    []func(instance *appsv1alpha1.Instance)

	InjectInstanceIdentity        func(instance *appsv1alpha1.Instance)
	CalculateSpec                 func(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) *UpdateSpec
	PatchSpecToInstance           func(instance *appsv1alpha1.Instance, spec *UpdateSpec, state *inplaceapi.InPlaceUpdateState) (*appsv1alpha1.Instance, error)
	CheckInstanceUpdateCompleted  func(instance *appsv1alpha1.Instance) error
	CheckComponentUpdateCompleted func(instance *appsv1alpha1.Instance) error
	GetRevision                   func(rev *apps.ControllerRevision) string
}

// Interface for managing Instance in-place update.
type Interface interface {
	CanUpdateInPlace(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) bool
	Update(instance *appsv1alpha1.Instance, oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) UpdateResult
	Refresh(instance *appsv1alpha1.Instance, opts *UpdateOptions) RefreshResult
}

// UpdateSpec records the images of containers which need to in-place update.
type UpdateSpec struct {
	Revision    string                         `json:"revision"`
	OldTemplate *appsv1alpha1.InstanceTemplate `json:"oldTemplate,omitempty"`
	NewTemplate *appsv1alpha1.InstanceTemplate `json:"newTemplate,omitempty"`
}

type realControl struct {
	clientAdapter   clientdapter.Adapter
	revisionAdapter revisionadapter.Interface
}

func New(c client.Client, revisionAdapter revisionadapter.Interface) Interface {
	return &realControl{clientAdapter: &clientdapter.AdapterRuntimeClient{Client: c}, revisionAdapter: revisionAdapter}
}

func (c *realControl) Refresh(instance *appsv1alpha1.Instance, opts *UpdateOptions) RefreshResult {
	opts = SetOptionsDefaults(opts)
	if err := opts.CheckInstanceUpdateCompleted(instance); err != nil {
		return RefreshResult{}
	}

	if !containsReadinessGate(instance) {
		return RefreshResult{}
	}

	newCondition := appsv1alpha1.InstanceCondition{
		Type:               appsv1alpha1.InstanceInPlaceUpdateReady,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}
	err := c.updateCondition(instance, newCondition)
	return RefreshResult{RefreshErr: err}
}

func (c *realControl) updateCondition(instance *appsv1alpha1.Instance, condition appsv1alpha1.InstanceCondition) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		clone, err := c.clientAdapter.GetInstance(instance.Namespace, instance.Name)
		if err != nil {
			return err
		}

		if hasEqualCondition(clone, &condition) {
			return nil
		}

		inplaceutil.SetInstanceCondition(clone, condition)
		// We only update the ready condition to False, and let Instance controller update it to True
		if condition.Status == v1.ConditionFalse {
			inplaceutil.SetInstanceNotReadyCondition(clone)
		}
		return c.clientAdapter.UpdateInstanceStatus(clone)
	})
}

func (c *realControl) CanUpdateInPlace(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) bool {
	opts = SetOptionsDefaults(opts)
	return opts.CalculateSpec(oldRevision, newRevision, opts) != nil
}

func (c *realControl) Update(instance *appsv1alpha1.Instance, oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) UpdateResult {
	opts = SetOptionsDefaults(opts)

	// 1. calculate inplace update spec
	spec := opts.CalculateSpec(oldRevision, newRevision, opts)
	if spec == nil {
		return UpdateResult{}
	}

	// 2. update condition for Instance with readiness-gate
	if containsReadinessGate(instance) {
		newCondition := appsv1alpha1.InstanceCondition{
			Type:               appsv1alpha1.InstanceInPlaceUpdateReady,
			LastTransitionTime: metav1.NewTime(time.Now()),
			Status:             v1.ConditionFalse,
			Reason:             "StartInPlaceUpdate",
		}
		if err := c.updateCondition(instance, newCondition); err != nil {
			return UpdateResult{InPlaceUpdate: true, UpdateErr: err}
		}
	}

	// 3. update instance
	newResourceVersion, err := c.updateInstanceInPlace(instance, spec, opts)
	if err != nil {
		return UpdateResult{InPlaceUpdate: true, UpdateErr: err}
	}
	return UpdateResult{InPlaceUpdate: true, NewResourceVersion: newResourceVersion}
}

func (c *realControl) updateInstanceInPlace(instance *appsv1alpha1.Instance, spec *UpdateSpec, opts *UpdateOptions) (string, error) {
	var newResourceVersion string
	retryErr := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		clone, err := c.clientAdapter.GetInstance(instance.Namespace, instance.Name)
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

		inPlaceUpdateState := inplaceapi.InPlaceUpdateState{
			Revision:        spec.Revision,
			UpdateTimestamp: metav1.NewTime(time.Now()),
		}
		inPlaceUpdateStateJSON, _ := json.Marshal(inPlaceUpdateState)
		clone.Annotations[inplaceapi.InPlaceUpdateStateKey] = string(inPlaceUpdateStateJSON)

		clone, err = opts.PatchSpecToInstance(clone, spec, &inPlaceUpdateState)
		if err != nil {
			return err
		}

		newInstance, updateErr := c.clientAdapter.UpdateInstance(clone)
		if updateErr == nil {
			newResourceVersion = newInstance.ResourceVersion
		}
		return updateErr
	})
	return newResourceVersion, retryErr
}

// GetTemplateFromRevision returns the Instance template parsed from ControllerRevision.
func GetTemplateFromRevision(revision *apps.ControllerRevision) (*appsv1alpha1.InstanceTemplate, error) {
	var patchObj *struct {
		Spec struct {
			Template appsv1alpha1.InstanceTemplate `json:"instanceTemplate"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(revision.Data.Raw, &patchObj); err != nil {
		return nil, err
	}
	return &patchObj.Spec.Template, nil
}

// InjectVersionedInstanceSpec injects InstanceSpec for the newly-create or inplace-update Instance.
func InjectVersionedInstanceSpec(instance *appsv1alpha1.Instance) {
	InjectInstanceReadinessGate(instance)
}

// InjectInstanceReadinessGate injects InPlaceUpdateReady into instance.spec.readinessGates
func InjectInstanceReadinessGate(instance *appsv1alpha1.Instance) {
	for _, r := range instance.Spec.ReadinessGates {
		if r.ConditionType == appsv1alpha1.InstanceInPlaceUpdateReady {
			return
		}
	}
	instance.Spec.ReadinessGates = append(instance.Spec.ReadinessGates, appsv1alpha1.InstanceReadinessGate{ConditionType: appsv1alpha1.InstanceInPlaceUpdateReady})
}

func containsReadinessGate(instance *appsv1alpha1.Instance) bool {
	for _, r := range instance.Spec.ReadinessGates {
		if r.ConditionType == appsv1alpha1.InstanceInPlaceUpdateReady {
			return true
		}
	}
	return false
}

func hasEqualCondition(instance *appsv1alpha1.Instance, newCondition *appsv1alpha1.InstanceCondition) bool {
	oldCondition := inplaceutil.GetInstanceCondition(instance, newCondition.Type)
	isEqual := oldCondition != nil && oldCondition.Status == newCondition.Status &&
		oldCondition.Reason == newCondition.Reason && oldCondition.Message == newCondition.Message
	return isEqual
}
