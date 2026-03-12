package core

import (
	"k8s.io/apimachinery/pkg/labels"
	v1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
)

type Control interface {
	// common
	IsInitializing() bool
	SetRevisionTemplate(revisionSpec map[string]interface{}, template map[string]interface{})
	ApplyRevisionPatch(patched []byte) (*v1alpha2.RoleInstanceSet, error)
	Selector() labels.Selector

	// scale
	IsReadyToScale() bool
	NewVersionedInstances(
		currentSet, updateSet *v1alpha2.RoleInstanceSet,
		currentRevision, updateRevision string,
		expectedCreations, expectedCurrentCreations int,
		availableIDs []string,
	) ([]*v1alpha2.RoleInstance, error)

	// update
	IsInstanceUpdatePaused(instance *v1alpha2.RoleInstance) bool
	IsInstanceUpdateReady(instance *v1alpha2.RoleInstance, minReadySeconds int32) bool
	GetInstancesSortFunc(instances []*v1alpha2.RoleInstance, waitUpdateIndexes []int) func(i, j int) bool
	GetUpdateOptions() *inplaceupdate.UpdateOptions
	ExtraStatusCalculation(status *v1alpha2.RoleInstanceSetStatus, instances []*v1alpha2.RoleInstance) error

	// validation
	ValidateInstanceSetUpdate(oldSet, newSet *v1alpha2.RoleInstanceSet) error
}

func New(set *v1alpha2.RoleInstanceSet) Control {
	return &commonControl{RoleInstanceSet: set}
}
