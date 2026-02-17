/*
Copyright 2026 The RBG Authors.
Copyright 2019 The Kruise Authors.
Copyright 2016 The Kubernetes Authors.

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

package statefulmode

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/record"
	sigsclient "sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

// InstanceControlInterface defines the interface for managing instances
type InstanceControlInterface interface {
	// CreateInstance creates a new Instance
	CreateInstance(ctx context.Context, namespace string, instance *workloadsv1alpha1.Instance, object metav1.Object) error
	// DeleteInstance deletes the Instance
	DeleteInstance(ctx context.Context, namespace string, instanceName string, object metav1.Object) error
	// PatchInstance patches the Instance
	PatchInstance(ctx context.Context, namespace, instanceName string, data []byte, object metav1.Object) error
}

// realInstanceControl implements InstanceControlInterface
type realInstanceControl struct {
	client   sigsclient.Client
	recorder record.EventRecorder
}

func (r *realInstanceControl) CreateInstance(ctx context.Context, namespace string, instance *workloadsv1alpha1.Instance, object metav1.Object) error {
	return r.client.Create(ctx, instance)
}

func (r *realInstanceControl) DeleteInstance(ctx context.Context, namespace string, instanceName string, object metav1.Object) error {
	instance := &workloadsv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      instanceName,
		},
	}
	return r.client.Delete(ctx, instance)
}

func (r *realInstanceControl) PatchInstance(ctx context.Context, namespace, instanceName string, data []byte, object metav1.Object) error {
	instance := &workloadsv1alpha1.Instance{}
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: instanceName}, instance); err != nil {
		return err
	}
	return r.client.Patch(ctx, instance, sigsclient.RawPatch(types.StrategicMergePatchType, data))
}

// InstanceControllerRefManager is used to manage controllerRef of Instances
type InstanceControllerRefManager struct {
	instanceControl InstanceControlInterface
	controller      metav1.Object
	selector        labels.Selector
	controllerKind  schema.GroupVersionKind
	canAdopt        func(ctx context.Context) error
}

// NewInstanceControllerRefManager returns a new *InstanceControllerRefManager
func NewInstanceControllerRefManager(
	instanceControl InstanceControlInterface,
	controller metav1.Object,
	selector labels.Selector,
	controllerKind schema.GroupVersionKind,
	canAdopt func(ctx context.Context) error,
) *InstanceControllerRefManager {
	return &InstanceControllerRefManager{
		instanceControl: instanceControl,
		controller:      controller,
		selector:        selector,
		controllerKind:  controllerKind,
		canAdopt:        canAdopt,
	}
}

// ClaimInstances tries to take ownership of a list of Instances.
//
// It will reconcile the following:
//   - Adopt orphans if the selector matches.
//   - Release owned objects if the selector no longer matches.
//
// A non-nil error is returned if some form of reconciliation was attempted and
// failed. Usually, controllers should try again later in case reconciliation
// is still needed.
//
// If the error is nil, either the reconciliation succeeded, or no
// reconciliation was necessary. The list of Instances that you now own is returned.
func (m *InstanceControllerRefManager) ClaimInstances(ctx context.Context, instances []*workloadsv1alpha1.Instance, filters ...func(*workloadsv1alpha1.Instance) bool) ([]*workloadsv1alpha1.Instance, error) {
	var claimed []*workloadsv1alpha1.Instance
	var errlist []error

	match := func(obj *workloadsv1alpha1.Instance) bool {
		// Check selector
		if !m.selector.Matches(labels.Set(obj.Labels)) {
			return false
		}
		// Check filters
		for _, filter := range filters {
			if !filter(obj) {
				return false
			}
		}
		return true
	}

	adopt := func(obj *workloadsv1alpha1.Instance) bool {
		if obj.DeletionTimestamp != nil {
			return false
		}
		// Check if already owned by this controller
		controllerRef := metav1.GetControllerOf(obj)
		if controllerRef != nil {
			if controllerRef.UID == m.controller.GetUID() {
				// Already owned
				return true
			}
			// Owned by someone else
			return false
		}

		// Orphan, try to adopt if it matches
		if !match(obj) {
			return false
		}

		// Check if we can adopt
		if err := m.canAdopt(ctx); err != nil {
			errlist = append(errlist, fmt.Errorf("can't adopt instance %s/%s: %v", obj.Namespace, obj.Name, err))
			return false
		}

		// Try to adopt by setting owner reference
		obj.OwnerReferences = append(obj.OwnerReferences, *metav1.NewControllerRef(m.controller, m.controllerKind))
		ownerRefsJSON, _ := json.Marshal(obj.OwnerReferences)
		if err := m.instanceControl.PatchInstance(ctx, obj.Namespace, obj.Name, []byte(fmt.Sprintf(`{"metadata":{"ownerReferences":%s}}`, ownerRefsJSON)), m.controller); err != nil {
			errlist = append(errlist, err)
			return false
		}
		return true
	}

	release := func(obj *workloadsv1alpha1.Instance) bool {
		controllerRef := metav1.GetControllerOf(obj)
		if controllerRef == nil || controllerRef.UID != m.controller.GetUID() {
			return false
		}

		// Should we release?
		if match(obj) {
			return false
		}

		// Release by removing owner reference
		newRefs := []metav1.OwnerReference{}
		for _, ref := range obj.OwnerReferences {
			if ref.UID != m.controller.GetUID() {
				newRefs = append(newRefs, ref)
			}
		}
		obj.OwnerReferences = newRefs
		newRefsJSON, _ := json.Marshal(newRefs)
		if err := m.instanceControl.PatchInstance(ctx, obj.Namespace, obj.Name, []byte(fmt.Sprintf(`{"metadata":{"ownerReferences":%s}}`, newRefsJSON)), m.controller); err != nil {
			errlist = append(errlist, err)
			return false
		}
		return true
	}

	for _, instance := range instances {
		controllerRef := metav1.GetControllerOf(instance)
		if controllerRef != nil && controllerRef.UID == m.controller.GetUID() {
			// Already owned
			if !match(instance) {
				// Need to release
				release(instance)
			} else {
				claimed = append(claimed, instance)
			}
		} else {
			// Not owned, try to adopt if it matches
			if adopt(instance) {
				claimed = append(claimed, instance)
			}
		}
	}

	return claimed, utilerrors.NewAggregate(errlist)
}
