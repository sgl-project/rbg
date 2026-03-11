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
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	inplaceutil "sigs.k8s.io/rbgs/pkg/inplace/instance"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/clientdapter"
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
	AdditionalFuncs    []func(instance *workloadsv1alpha2.RoleInstance)

	InjectRoleInstanceIdentity       func(instance *workloadsv1alpha2.RoleInstance)
	CalculateSpec                    func(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) *UpdateSpec
	PatchSpecToRoleInstance          func(instance *workloadsv1alpha2.RoleInstance, spec *UpdateSpec, state *inplaceapi.InPlaceUpdateState) (*workloadsv1alpha2.RoleInstance, error)
	CheckRoleInstanceUpdateCompleted func(instance *workloadsv1alpha2.RoleInstance) error
	CheckComponentUpdateCompleted    func(instance *workloadsv1alpha2.RoleInstance) error
	GetRevision                      func(rev *apps.ControllerRevision) string
}

// Interface for managing RoleInstance in-place update.
type Interface interface {
	CanUpdateInPlace(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) bool
	Update(instance *workloadsv1alpha2.RoleInstance, oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) UpdateResult
	Refresh(instance *workloadsv1alpha2.RoleInstance, opts *UpdateOptions) RefreshResult
}

// UpdateSpec records the images of containers which need to in-place update.
type UpdateSpec struct {
	Revision    string                                  `json:"revision"`
	OldTemplate *workloadsv1alpha2.RoleInstanceTemplate `json:"oldTemplate,omitempty"`
	NewTemplate *workloadsv1alpha2.RoleInstanceTemplate `json:"newTemplate,omitempty"`
}

type realControl struct {
	clientAdapter   clientdapter.Adapter
	revisionAdapter revisionadapter.Interface
}

func New(c client.Client, revisionAdapter revisionadapter.Interface) Interface {
	return &realControl{clientAdapter: &clientdapter.AdapterRuntimeClient{Client: c}, revisionAdapter: revisionAdapter}
}

func (c *realControl) Refresh(instance *workloadsv1alpha2.RoleInstance, opts *UpdateOptions) RefreshResult {
	opts = SetOptionsDefaults(opts)
	if err := opts.CheckRoleInstanceUpdateCompleted(instance); err != nil {
		return RefreshResult{}
	}

	if !containsReadinessGate(instance) {
		return RefreshResult{}
	}

	newCondition := workloadsv1alpha2.RoleInstanceCondition{
		Type:               workloadsv1alpha2.RoleInstanceInPlaceUpdateReady,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}
	err := c.updateCondition(instance, newCondition)
	return RefreshResult{RefreshErr: err}
}

func (c *realControl) updateCondition(instance *workloadsv1alpha2.RoleInstance, condition workloadsv1alpha2.RoleInstanceCondition) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		clone, err := c.clientAdapter.GetRoleInstance(instance.Namespace, instance.Name)
		if err != nil {
			return err
		}

		if hasEqualCondition(clone, &condition) {
			return nil
		}

		inplaceutil.SetRoleInstanceCondition(clone, condition)
		// We only update the ready condition to False, and let RoleInstance controller update it to True
		if condition.Status == v1.ConditionFalse {
			inplaceutil.SetRoleInstanceNotReadyCondition(clone)
		}
		return c.clientAdapter.UpdateRoleInstanceStatus(clone)
	})
}

func (c *realControl) CanUpdateInPlace(oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) bool {
	opts = SetOptionsDefaults(opts)
	return opts.CalculateSpec(oldRevision, newRevision, opts) != nil
}

func (c *realControl) Update(instance *workloadsv1alpha2.RoleInstance, oldRevision, newRevision *apps.ControllerRevision, opts *UpdateOptions) UpdateResult {
	opts = SetOptionsDefaults(opts)

	// 1. calculate inplace update spec
	spec := opts.CalculateSpec(oldRevision, newRevision, opts)
	if spec == nil {
		return UpdateResult{}
	}

	// 2. update condition for RoleInstance with readiness-gate
	if containsReadinessGate(instance) {
		newCondition := workloadsv1alpha2.RoleInstanceCondition{
			Type:               workloadsv1alpha2.RoleInstanceInPlaceUpdateReady,
			LastTransitionTime: metav1.NewTime(time.Now()),
			Status:             v1.ConditionFalse,
			Reason:             "StartInPlaceUpdate",
		}
		if err := c.updateCondition(instance, newCondition); err != nil {
			return UpdateResult{InPlaceUpdate: true, UpdateErr: err}
		}
	}

	// 3. update role instance
	newResourceVersion, err := c.updateRoleInstanceInPlace(instance, spec, opts)
	if err != nil {
		return UpdateResult{InPlaceUpdate: true, UpdateErr: err}
	}
	return UpdateResult{InPlaceUpdate: true, NewResourceVersion: newResourceVersion}
}

func (c *realControl) updateRoleInstanceInPlace(instance *workloadsv1alpha2.RoleInstance, spec *UpdateSpec, opts *UpdateOptions) (string, error) {
	var newResourceVersion string
	retryErr := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		clone, err := c.clientAdapter.GetRoleInstance(instance.Namespace, instance.Name)
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

		clone, err = opts.PatchSpecToRoleInstance(clone, spec, &inPlaceUpdateState)
		if err != nil {
			return err
		}

		newInstance, updateErr := c.clientAdapter.UpdateRoleInstance(clone)
		if updateErr == nil {
			newResourceVersion = newInstance.ResourceVersion
		}
		return updateErr
	})
	return newResourceVersion, retryErr
}

// GetTemplateFromRevision returns the RoleInstance template parsed from ControllerRevision.
func GetTemplateFromRevision(revision *apps.ControllerRevision) (*workloadsv1alpha2.RoleInstanceTemplate, error) {
	var patchObj *struct {
		Spec struct {
			Template workloadsv1alpha2.RoleInstanceTemplate `json:"roleInstanceTemplate"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(revision.Data.Raw, &patchObj); err != nil {
		return nil, err
	}
	return &patchObj.Spec.Template, nil
}

// InjectVersionedRoleInstanceSpec injects RoleInstanceSpec for the newly-create or inplace-update RoleInstance.
func InjectVersionedRoleInstanceSpec(instance *workloadsv1alpha2.RoleInstance) {
	InjectRoleInstanceReadinessGate(instance)
}

// InjectRoleInstanceReadinessGate injects InPlaceUpdateReady into instance.spec.readinessGates
func InjectRoleInstanceReadinessGate(instance *workloadsv1alpha2.RoleInstance) {
	for _, r := range instance.Spec.ReadinessGates {
		if r.ConditionType == workloadsv1alpha2.RoleInstanceInPlaceUpdateReady {
			return
		}
	}
	instance.Spec.ReadinessGates = append(instance.Spec.ReadinessGates, workloadsv1alpha2.RoleInstanceReadinessGate{ConditionType: workloadsv1alpha2.RoleInstanceInPlaceUpdateReady})
}

func containsReadinessGate(instance *workloadsv1alpha2.RoleInstance) bool {
	for _, r := range instance.Spec.ReadinessGates {
		if r.ConditionType == workloadsv1alpha2.RoleInstanceInPlaceUpdateReady {
			return true
		}
	}
	return false
}

func hasEqualCondition(instance *workloadsv1alpha2.RoleInstance, newCondition *workloadsv1alpha2.RoleInstanceCondition) bool {
	oldCondition := inplaceutil.GetRoleInstanceCondition(instance, newCondition.Type)
	isEqual := oldCondition != nil && oldCondition.Status == newCondition.Status &&
		oldCondition.Reason == newCondition.Reason && oldCondition.Message == newCondition.Message
	return isEqual
}
