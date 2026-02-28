package sync

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"sigs.k8s.io/rbgs/pkg/utils/expectations"

	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	instanceutil "sigs.k8s.io/rbgs/pkg/inplace/instance"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset/statelessmode/core"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset/statelessmode/utils"
)

const (
	// LengthOfInstanceID is the length of instanceutil-id
	LengthOfInstanceID = 5

	// When batching start, initialBatchSize is the size of the initial batch.
	initialBatchSize = 1
)

func (rc *realControl) Scale(
	currentSet *appsv1alpha1.InstanceSet,
	updateSet *appsv1alpha1.InstanceSet,
	currentRevision string,
	updateRevision string,
	instances []*appsv1alpha1.Instance,
) (bool, error) {
	if updateSet.Spec.Replicas == nil {
		return false, fmt.Errorf("spec.Replicas is nil")
	}

	controllerKey := utils.GetControllerKey(updateSet)
	coreControl := core.New(updateSet)
	if !coreControl.IsReadyToScale() {
		klog.Warningf("InstanceSet %s skip scaling for not ready to scale", controllerKey)
		return false, nil
	}

	// 1. manage instances to delete and in preDelete
	instanceSpecifiedToDelete, instanceInPreDelete, numToDelete := getPlannedDeletedInstances(updateSet, instances)
	if modified, err := rc.managePreparingDelete(updateSet, instances, instanceInPreDelete, numToDelete); err != nil || modified {
		return modified, err
	}

	// 2. calculate scale numbers
	diffRes := calculateDiffsWithExpectation(updateSet, instances, currentRevision, updateRevision)
	updatedInstance, notUpdatedInstance := utils.SplitInstancesByRevision(instances, updateRevision)

	if diffRes.scaleNum > 0 && diffRes.scaleNum > diffRes.scaleUpLimit {
		rc.recorder.Event(updateSet, v1.EventTypeWarning, "ScaleUpLimited", fmt.Sprintf("scaleUp is limited because of scaleStrategy.maxUnavailable, limit: %d", diffRes.scaleUpLimit))
	}

	// 3. scale out
	if diffRes.scaleNum > 0 && diffRes.scaleUpLimit > 0 {
		// total number of this creation
		expectedCreations := diffRes.scaleUpLimit
		// lack number of current version
		expectedCurrentCreations := 0
		if diffRes.scaleNumOldRevision > 0 {
			expectedCurrentCreations = diffRes.scaleNumOldRevision
		}

		klog.V(3).Infof("InstanceSet %s begin to scale out %d instances including %d (current rev)",
			controllerKey, expectedCreations, expectedCurrentCreations)

		// available instanceutil-id come from free pvc
		availableIDs := getOrGenAvailableIDs(expectedCreations, instances)
		return rc.createInstances(expectedCreations, expectedCurrentCreations,
			currentSet, updateSet, currentRevision, updateRevision, availableIDs.UnsortedList())
	}

	// 4. try to delete instances already in pre-delete
	if len(instanceInPreDelete) > 0 {
		klog.V(3).Infof("InstanceSet %s try to delete instances in preDelete: %v", controllerKey, instanceutil.GetInstanceName(instanceInPreDelete...).UnsortedList())
		if modified, err := rc.deleteInstances(updateSet, instanceInPreDelete); err != nil || modified {
			return modified, err
		}
	}

	// 5. specified delete
	if instancesToDelete := DiffInstanceGroups(instanceSpecifiedToDelete, instanceInPreDelete); len(instancesToDelete) > 0 {
		newInstancesToDelete, oldInstancesToDelete := utils.SplitInstancesByRevision(instancesToDelete, updateRevision)
		klog.V(3).Infof("InstanceSet %s try to delete instances specified. Delete ready limit: %d. instances: %v, %v.",
			controllerKey, diffRes.deleteReadyLimit, instanceutil.GetInstanceName(newInstancesToDelete...).UnsortedList(), instanceutil.GetInstanceName(oldInstancesToDelete...).UnsortedList())

		instancesToDelete = make([]*appsv1alpha1.Instance, 0, len(instancesToDelete))
		for _, instance := range newInstancesToDelete {
			if !IsInstanceReady(coreControl, instance) {
				instancesToDelete = append(instancesToDelete, instance)
			} else if diffRes.deleteReadyLimit > 0 {
				instancesToDelete = append(instancesToDelete, instance)
				diffRes.deleteReadyLimit--
			}
		}
		for _, instance := range oldInstancesToDelete {
			if !IsInstanceReady(coreControl, instance) {
				instancesToDelete = append(instancesToDelete, instance)
			} else if diffRes.deleteReadyLimit > 0 {
				instancesToDelete = append(instancesToDelete, instance)
				diffRes.deleteReadyLimit--
			}
		}

		if modified, err := rc.deleteInstances(updateSet, instancesToDelete); err != nil || modified {
			return modified, err
		}
	}

	// 6. scale in
	if diffRes.scaleNum < 0 {
		if numToDelete > 0 {
			klog.V(3).Infof("InstanceSet %s skip to scale in %d for %d to delete, including %d specified and %d preDelete",
				controllerKey, diffRes.scaleNum, numToDelete, len(instanceSpecifiedToDelete), len(instanceInPreDelete))
			return false, nil
		}

		klog.V(3).Infof("InstanceSet %s begin to scale in %d instances including %d (current rev), delete ready limit: %d",
			controllerKey, -diffRes.scaleNum, -diffRes.scaleNumOldRevision, diffRes.deleteReadyLimit)

		instancesPreparingToDelete := rc.chooseInstancesToDelete(updateSet, -diffRes.scaleNum, -diffRes.scaleNumOldRevision, notUpdatedInstance, updatedInstance)
		instancesToDelete := make([]*appsv1alpha1.Instance, 0, len(instancesPreparingToDelete))
		for _, instance := range instancesPreparingToDelete {
			if !IsInstanceReady(coreControl, instance) {
				instancesToDelete = append(instancesToDelete, instance)
			} else if diffRes.deleteReadyLimit > 0 {
				instancesToDelete = append(instancesToDelete, instance)
				diffRes.deleteReadyLimit--
			}
		}

		return rc.deleteInstances(updateSet, instancesToDelete)
	}

	return false, nil
}

func (rc *realControl) managePreparingDelete(set *appsv1alpha1.InstanceSet, instances, instancesInPreDelete []*appsv1alpha1.Instance, numToDelete int) (bool, error) {
	//  We do not allow regret once the instance enter PreparingDelete state if MarkNotReady is set.
	// Actually, there is a bug cased by this transformation from PreparingDelete to Normal,
	// i.e., Lifecycle Updated Hook may be lost if the instance was transformed from Updating state
	// to PreparingDelete.
	if lifecycle.IsLifecycleMarkInstanceNotReady(set.Spec.Lifecycle) {
		return false, nil
	}

	diff := int(*set.Spec.Replicas) - len(instances) + numToDelete
	var modified bool
	for _, instance := range instancesInPreDelete {
		if diff <= 0 {
			return modified, nil
		}
		if isSpecifiedDelete(set, instance) {
			continue
		}

		klog.V(3).Infof("InstanceSet %s patch instance %s lifecycle from PreparingDelete to Normal",
			utils.GetControllerKey(set), instance.Name)
		if updated, gotInstance, err := rc.lifecycleControl.UpdateInstanceLifecycle(instance, appsv1alpha1.LifecycleStateNormal, false); err != nil {
			return modified, err
		} else if updated {
			modified = true
			utils.ResourceVersionExpectations.Expect(gotInstance)
		}
		diff--
	}
	return modified, nil
}

func (rc *realControl) createInstances(
	expectedCreations, expectedCurrentCreations int,
	currentSet, updateSet *appsv1alpha1.InstanceSet,
	currentRevision, updateRevision string,
	availableIDs []string,
) (bool, error) {
	// new all instance need to create
	coreControl := core.New(updateSet)
	newInstances, err := coreControl.NewVersionedInstances(currentSet, updateSet, currentRevision, updateRevision,
		expectedCreations, expectedCurrentCreations, availableIDs)
	if err != nil {
		return false, err
	}

	instancesCreationChan := make(chan *appsv1alpha1.Instance, len(newInstances))
	for _, p := range newInstances {
		utils.ScaleExpectations.ExpectScale(utils.GetControllerKey(updateSet), expectations.Create, p.Name)
		instancesCreationChan <- p
	}

	var created int64
	successInstanceNames := sync.Map{}
	_, err = utils.DoItSlowly(len(newInstances), initialBatchSize, func() error {
		instance := <-instancesCreationChan

		gs := updateSet
		if utils.EqualToRevisionHash("", instance, currentRevision) {
			gs = currentSet
		}
		lifecycle.SetInstanceLifecycle(appsv1alpha1.LifecycleStateNormal)(instance)

		var createErr error
		if createErr = rc.createOneInstance(gs, instance); createErr != nil {
			return createErr
		}

		atomic.AddInt64(&created, 1)

		successInstanceNames.Store(instance.Name, struct{}{})
		return nil
	})

	// rollback to ignore failure instance because the informer won't observe these instances.
	for _, instance := range newInstances {
		if _, ok := successInstanceNames.Load(instance.Name); !ok {
			utils.ScaleExpectations.ObserveScale(utils.GetControllerKey(updateSet), expectations.Create, instance.Name)
		}
	}

	if created == 0 {
		return false, err
	}
	return true, err
}

func (rc *realControl) createOneInstance(set *appsv1alpha1.InstanceSet, instance *appsv1alpha1.Instance) error {
	if err := rc.Create(context.TODO(), instance); err != nil {
		rc.recorder.Eventf(set, v1.EventTypeWarning, "FailedCreate", "failed to create instance: %v, instance: %v", err, utils.DumpJSON(instance))
		return err
	}

	rc.recorder.Eventf(set, v1.EventTypeNormal, "SuccessfulCreate", "succeed to create instance %s", instance.Name)
	return nil
}

func (rc *realControl) deleteInstances(gs *appsv1alpha1.InstanceSet, instancesToDelete []*appsv1alpha1.Instance) (bool, error) {
	var modified bool
	for _, instance := range instancesToDelete {
		if gs.Spec.Lifecycle != nil && lifecycle.IsInstanceHooked(gs.Spec.Lifecycle.PreDelete, instance) {
			markNotReady := gs.Spec.Lifecycle.PreDelete.MarkNotReady
			if updated, gotInstance, err := rc.lifecycleControl.UpdateInstanceLifecycle(instance, appsv1alpha1.LifecycleStatePreparingDelete, markNotReady); err != nil {
				return false, err
			} else if updated {
				klog.V(3).Infof("InstanceSet %s scaling update instance %s lifecycle to PreparingDelete",
					utils.GetControllerKey(gs), instance.Name)
				modified = true
				utils.ResourceVersionExpectations.Expect(gotInstance)
			}
			continue
		}

		utils.ScaleExpectations.ExpectScale(utils.GetControllerKey(gs), expectations.Delete, instance.Name)
		if err := rc.Delete(context.TODO(), instance); err != nil {
			utils.ScaleExpectations.ObserveScale(utils.GetControllerKey(gs), expectations.Delete, instance.Name)
			rc.recorder.Eventf(gs, v1.EventTypeWarning, "FailedDelete", "failed to delete instance %s: %v", instance.Name, err)
			return modified, err
		}
		modified = true
		rc.recorder.Event(gs, v1.EventTypeNormal, "SuccessfulDelete", fmt.Sprintf("succeed to delete instance %s", instance.Name))
	}

	return modified, nil
}

func getPlannedDeletedInstances(gs *appsv1alpha1.InstanceSet, instances []*appsv1alpha1.Instance) ([]*appsv1alpha1.Instance, []*appsv1alpha1.Instance, int) {
	var specifiedToDelete []*appsv1alpha1.Instance
	var inPreDelete []*appsv1alpha1.Instance
	names := sets.NewString()
	for _, instance := range instances {
		if isSpecifiedDelete(gs, instance) {
			names.Insert(instance.Name)
			specifiedToDelete = append(specifiedToDelete, instance)
		}
		if lifecycle.GetInstanceLifecycleState(instance) == appsv1alpha1.LifecycleStatePreparingDelete {
			names.Insert(instance.Name)
			inPreDelete = append(inPreDelete, instance)
		}
	}
	return specifiedToDelete, inPreDelete, names.Len()
}

// Get available IDs, if the a PVC exists but the corresponding instance does not exist, then reusing the ID, i.e., reuse the pvc.
// If there is not enough existing available IDs, then generate ID using rand utility.
// More details: if template changes more than container image, controller will delete instances during update, and
// it will keep the pvc to reuse.
func getOrGenAvailableIDs(num int, instances []*appsv1alpha1.Instance) sets.Set[string] {
	existingIDs := sets.New[string]()
	availableIDs := sets.New[string]()

	for _, instance := range instances {
		if id := instance.Labels[appsv1alpha1.SetInstanceIDLabelKey]; len(id) > 0 {
			existingIDs.Insert(id)
			availableIDs.Delete(id)
		}
	}

	retIDs := sets.New[string]()
	for i := 0; i < num; i++ {
		id := getOrGenInstanceID(existingIDs, availableIDs)
		retIDs.Insert(id)
	}

	return retIDs
}

func getOrGenInstanceID(existingIDs, availableIDs sets.Set[string]) string {
	id, _ := availableIDs.PopAny()
	if len(id) == 0 {
		for {
			id = rand.String(LengthOfInstanceID)
			if !existingIDs.Has(id) {
				break
			}
		}
	}
	return id
}

func (rc *realControl) chooseInstancesToDelete(set *appsv1alpha1.InstanceSet, totalDiff int, currentRevDiff int, notUpdatedInstances, updatedInstances []*appsv1alpha1.Instance) []*appsv1alpha1.Instance {
	coreControl := core.New(set)
	choose := func(instances []*appsv1alpha1.Instance, diff int) []*appsv1alpha1.Instance {
		// No need to sort instances if we are about to delete all of them.
		if diff < len(instances) {
			sort.Sort(utils.ActiveInstancesAvailableRank{
				Instances: instances,
				AvailableFunc: func(instance *appsv1alpha1.Instance) bool {
					return IsInstanceAvailable(coreControl, instance, set.Spec.MinReadySeconds)
				},
			})
		} else if diff > len(instances) {
			klog.Warningf("InstanceSet %s Diff > len(instances) in chooseInstancesToDelete func which is not expected.", utils.GetControllerKey(set))
			return instances
		}
		return instances[:diff]
	}

	var instancesToDelete []*appsv1alpha1.Instance
	if currentRevDiff >= totalDiff {
		instancesToDelete = choose(notUpdatedInstances, totalDiff)
	} else if currentRevDiff > 0 {
		instancesToDelete = choose(notUpdatedInstances, currentRevDiff)
		instancesToDelete = append(instancesToDelete, choose(updatedInstances, totalDiff-currentRevDiff)...)
	} else {
		instancesToDelete = choose(updatedInstances, totalDiff)
	}

	return instancesToDelete
}
