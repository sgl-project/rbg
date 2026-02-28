package core

import (
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
)

type Control interface {
	// common
	IsInitializing() bool
	SetRevisionTemplate(revisionSpec map[string]interface{}, template map[string]interface{})
	ApplyRevisionPatch(patched []byte) (*v1alpha1.InstanceSet, error)
	Selector() labels.Selector

	// scale
	IsReadyToScale() bool
	NewVersionedInstances(
		currentSet, updateSet *v1alpha1.InstanceSet,
		currentRevision, updateRevision string,
		expectedCreations, expectedCurrentCreations int,
		availableIDs []string,
	) ([]*v1alpha1.Instance, error)

	// update
	IsInstanceUpdatePaused(instance *v1alpha1.Instance) bool
	IsInstanceUpdateReady(instance *v1alpha1.Instance, minReadySeconds int32) bool
	GetInstancesSortFunc(instances []*v1alpha1.Instance, waitUpdateIndexes []int) func(i, j int) bool
	GetUpdateOptions() *inplaceupdate.UpdateOptions
	ExtraStatusCalculation(status *v1alpha1.InstanceSetStatus, instances []*v1alpha1.Instance) error

	// validation
	ValidateInstanceSetUpdate(oldSet, newSet *v1alpha1.InstanceSet) error
}

func New(set *v1alpha1.InstanceSet) Control {
	return &commonControl{InstanceSet: set}
}
