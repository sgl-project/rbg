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
