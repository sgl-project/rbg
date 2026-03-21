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

package core

import (
	"k8s.io/apimachinery/pkg/labels"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
)

type Control interface {
	// common
	IsInitializing() bool
	SetRevisionTemplate(revisionSpec map[string]interface{}, template map[string]interface{})
	ApplyRevisionPatch(patched []byte) (*workloadsv1alpha2.RoleInstanceSet, error)
	Selector() labels.Selector

	// scale
	IsReadyToScale() bool
	NewVersionedInstances(
		currentSet, updateSet *workloadsv1alpha2.RoleInstanceSet,
		currentRevision, updateRevision string,
		expectedCreations, expectedCurrentCreations int,
		availableIDs []string,
	) ([]*workloadsv1alpha2.RoleInstance, error)

	// update
	IsInstanceUpdatePaused(instance *workloadsv1alpha2.RoleInstance) bool
	IsInstanceUpdateReady(instance *workloadsv1alpha2.RoleInstance, minReadySeconds int32) bool
	GetInstancesSortFunc(instances []*workloadsv1alpha2.RoleInstance, waitUpdateIndexes []int) func(i, j int) bool
	GetUpdateOptions() *inplaceupdate.UpdateOptions
	ExtraStatusCalculation(status *workloadsv1alpha2.RoleInstanceSetStatus, instances []*workloadsv1alpha2.RoleInstance) error

	// validation
	ValidateInstanceSetUpdate(oldSet, newSet *workloadsv1alpha2.RoleInstanceSet) error
}

func New(set *workloadsv1alpha2.RoleInstanceSet) Control {
	return &commonControl{RoleInstanceSet: set}
}
