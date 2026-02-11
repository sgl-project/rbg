/*
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
	"fmt"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	sigsclient "sigs.k8s.io/controller-runtime/pkg/client"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	instancelisters "sigs.k8s.io/rbgs/client-go/listers/workloads/v1alpha1"
)

// StatefulInstanceControlObjectManager abstracts the manipulation of Instances.
type StatefulInstanceControlObjectManager interface {
	CreateInstance(ctx context.Context, instance *workloadsv1alpha1.Instance) error
	GetInstance(namespace, instanceName string) (*workloadsv1alpha1.Instance, error)
	UpdateInstance(instance *workloadsv1alpha1.Instance) error
	DeleteInstance(instance *workloadsv1alpha1.Instance) error
}

// StatefulInstanceControl defines the interface that InstanceSetController uses to create, update, and delete Instances,
// Manipulation of objects is provided through objectMgr, which allows the k8s API to be mocked out for testing.
type StatefulInstanceControl struct {
	objectMgr StatefulInstanceControlObjectManager
	recorder  record.EventRecorder
}

// NewStatefulInstanceControl constructs a StatefulInstanceControl using a realStatefulInstanceControlObjectManager
func NewStatefulInstanceControl(
	client sigsclient.Client,
	instanceLister instancelisters.InstanceLister,
	recorder record.EventRecorder,
) *StatefulInstanceControl {
	return &StatefulInstanceControl{&realStatefulInstanceControlObjectManager{client, instanceLister}, recorder}
}

// NewStatefulInstanceControlFromManager creates a StatefulInstanceControl using the given StatefulInstanceControlObjectManager and recorder.
func NewStatefulInstanceControlFromManager(om StatefulInstanceControlObjectManager, recorder record.EventRecorder) *StatefulInstanceControl {
	return &StatefulInstanceControl{om, recorder}
}

// realStatefulInstanceControlObjectManager uses a sigsclient.Client.
type realStatefulInstanceControlObjectManager struct {
	client         sigsclient.Client
	instanceLister instancelisters.InstanceLister
}

func (om *realStatefulInstanceControlObjectManager) CreateInstance(ctx context.Context, instance *workloadsv1alpha1.Instance) error {
	return om.client.Create(ctx, instance)
}

func (om *realStatefulInstanceControlObjectManager) GetInstance(namespace, instanceName string) (*workloadsv1alpha1.Instance, error) {
	return om.instanceLister.Instances(namespace).Get(instanceName)
}

func (om *realStatefulInstanceControlObjectManager) UpdateInstance(instance *workloadsv1alpha1.Instance) error {
	return om.client.Update(context.TODO(), instance)
}

func (om *realStatefulInstanceControlObjectManager) DeleteInstance(instance *workloadsv1alpha1.Instance) error {
	return om.client.Delete(context.TODO(), instance)
}

func (sic *StatefulInstanceControl) CreateStatefulInstance(ctx context.Context, set *workloadsv1alpha1.InstanceSet, instance *workloadsv1alpha1.Instance) error {
	// Attempt to create the Instance
	err := sic.objectMgr.CreateInstance(ctx, instance)
	// sink already exists errors
	if apierrors.IsAlreadyExists(err) {
		return err
	}
	sic.recordInstanceEvent("create", set, instance, err)
	return err
}

func (sic *StatefulInstanceControl) UpdateStatefulInstance(set *workloadsv1alpha1.InstanceSet, instance *workloadsv1alpha1.Instance) error {
	attemptedUpdate := false
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// assume the Instance is consistent
		consistent := true
		// if the Instance does not conform to its identity, update the identity and dirty the Instance
		if !identityMatches(set, instance) {
			updateIdentity(set, instance)
			consistent = false
		}

		// if the Instance is not dirty, do nothing
		if consistent {
			return nil
		}

		attemptedUpdate = true
		// commit the update, retrying on conflicts

		updateErr := sic.objectMgr.UpdateInstance(instance)
		if updateErr == nil {
			return nil
		}

		if updated, err := sic.objectMgr.GetInstance(set.Namespace, instance.Name); err == nil {
			// make a copy so we don't mutate the shared cache
			instance = updated.DeepCopy()
		} else {
			utilruntime.HandleError(fmt.Errorf("error getting updated Instance %s/%s from lister: %v", set.Namespace, instance.Name, err))
		}

		return updateErr
	})
	if attemptedUpdate {
		sic.recordInstanceEvent("update", set, instance, err)
	}
	return err
}

func (sic *StatefulInstanceControl) DeleteStatefulInstance(set *workloadsv1alpha1.InstanceSet, instance *workloadsv1alpha1.Instance) error {
	err := sic.objectMgr.DeleteInstance(instance)
	sic.recordInstanceEvent("delete", set, instance, err)
	return err
}

// recordInstanceEvent records an event for verb applied to a Instance in a InstanceSet. If err is nil the generated event will
// have a reason of v1.EventTypeNormal. If err is not nil the generated event will have a reason of v1.EventTypeWarning.
func (sic *StatefulInstanceControl) recordInstanceEvent(verb string, set *workloadsv1alpha1.InstanceSet, instance *workloadsv1alpha1.Instance, err error) {
	if err == nil {
		reason := fmt.Sprintf("Successful%s", strings.Title(verb))
		message := fmt.Sprintf("%s Instance %s in InstanceSet %s successful",
			strings.ToLower(verb), instance.Name, set.Name)
		sic.recorder.Event(set, v1.EventTypeNormal, reason, message)
	} else {
		reason := fmt.Sprintf("Failed%s", cases.Title(language.English, cases.Compact).String(verb))
		message := fmt.Sprintf("%s Instance %s in InstanceSet %s failed error: %s",
			strings.ToLower(verb), instance.Name, set.Name, err)
		sic.recorder.Event(set, v1.EventTypeWarning, reason, message)
	}
}
