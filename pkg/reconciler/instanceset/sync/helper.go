package sync

import (
	"math"
	"reflect"

	intstrutil "k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/utils/integer"

	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/specifieddelete"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset/core"
	instancesetutils "sigs.k8s.io/rbgs/pkg/reconciler/instanceset/utils"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type expectationDiffs struct {
	// scaleNum is the diff number that should scale
	// '0' means no need to scale
	// positive number means need to scale out
	// negative number means need to scale in
	scaleNum int
	// scaleNumOldRevision is part of the scaleNum number
	// it indicates the scale number of old revision Instances
	scaleNumOldRevision int
	// scaleUpLimit is the limit number of creating Instances when scaling up
	// it is limited by scaleStrategy.maxUnavailable
	scaleUpLimit int
	// deleteReadyLimit is the limit number of ready Instances that can be deleted
	// it is limited by UpdateStrategy.maxUnavailable
	deleteReadyLimit int
	// useSurge is the number that temporarily expect to be above the desired replicas
	useSurge int
	// useSurgeOldRevision is part of the useSurge number
	// it indicates the above number of old revision Instances
	useSurgeOldRevision int

	// updateNum is the diff number that should update
	// '0' means no need to update
	// positive number means need to update more Instances to updateRevision
	// negative number means need to update more Instances to currentRevision (rollback)
	updateNum int
	// updateMaxUnavailable is the maximum number of ready Instances that can be updating
	updateMaxUnavailable int
}

func (e expectationDiffs) isEmpty() bool {
	return reflect.DeepEqual(e, expectationDiffs{})
}

// This is the most important algorithm in instance-set-controller.
// It calculates the instance numbers to scaling and updating for current InstanceSet.
func calculateDiffsWithExpectation(set *appsv1alpha1.InstanceSet, instances []*appsv1alpha1.Instance, currentRevision, updateRevision string) (res expectationDiffs) {
	coreControl := core.New(set)
	replicas := getInstanceSetReplicas(set)
	var partition, maxSurge, maxUnavailable, scaleMaxUnavailable int
	if pValue, err := utils.CalculatePartitionReplicas(set.Spec.UpdateStrategy.Partition, set.Spec.Replicas); err != nil {
		// TODO: maybe, we should block instance update if partition settings is wrong
		klog.Errorf("InstanceSet %s/%s partition value is illegal", set.Namespace, set.Name)
	} else {
		partition = pValue
	}
	if set.Spec.UpdateStrategy.MaxSurge != nil {
		maxSurge, _ = intstrutil.GetScaledValueFromIntOrPercent(set.Spec.UpdateStrategy.MaxSurge, replicas, true)
	}
	maxUnavailable, _ = intstrutil.GetScaledValueFromIntOrPercent(
		intstrutil.ValueOrDefault(set.Spec.UpdateStrategy.MaxUnavailable, intstrutil.FromString(appsv1alpha1.DefaultInstanceSetMaxUnavailable)), replicas, maxSurge == 0)
	scaleMaxUnavailable, _ = intstrutil.GetScaledValueFromIntOrPercent(
		intstrutil.ValueOrDefault(set.Spec.ScaleStrategy.MaxUnavailable, intstrutil.FromInt32(math.MaxInt32)), replicas, true)

	var newRevisionCount, newRevisionActiveCount, oldRevisionCount, oldRevisionActiveCount int
	var unavailableNewRevisionCount, unavailableOldRevisionCount int
	var toDeleteNewRevisionCount, toDeleteOldRevisionCount, preDeletingCount int
	defer func() {
		if res.isEmpty() {
			return
		}
		klog.V(1).Infof("Calculate diffs for InstanceSet %s/%s, replicas=%d, partition=%d, maxSurge=%d, maxUnavailable=%d,"+
			" allInstances=%d, newRevisionInstances=%d, newRevisionActiveInstances=%d, oldRevisionInstances=%d, oldRevisionActiveInstances=%d,"+
			" unavailableNewRevisionCount=%d, unavailableOldRevisionCount=%d,"+
			" preDeletingCount=%d, toDeleteNewRevisionCount=%d, toDeleteOldRevisionCount=%d."+
			" Result: %+v",
			set.Namespace, set.Name, replicas, partition, maxSurge, maxUnavailable,
			len(instances), newRevisionCount, newRevisionActiveCount, oldRevisionCount, oldRevisionActiveCount,
			unavailableNewRevisionCount, unavailableOldRevisionCount,
			preDeletingCount, toDeleteNewRevisionCount, toDeleteOldRevisionCount,
			res)
	}()

	for _, instance := range instances {
		if instancesetutils.EqualToRevisionHash("", instance, updateRevision) {
			newRevisionCount++

			switch state := lifecycle.GetInstanceLifecycleState(instance); state {
			case appsv1alpha1.LifecycleStatePreparingDelete:
				preDeletingCount++
			default:
				newRevisionActiveCount++

				if isSpecifiedDelete(set, instance) {
					toDeleteNewRevisionCount++
				} else if !IsInstanceAvailable(coreControl, instance, set.Spec.MinReadySeconds) {
					unavailableNewRevisionCount++
				}
			}

		} else {
			oldRevisionCount++

			switch state := lifecycle.GetInstanceLifecycleState(instance); state {
			case appsv1alpha1.LifecycleStatePreparingDelete:
				preDeletingCount++
			default:
				oldRevisionActiveCount++

				if isSpecifiedDelete(set, instance) {
					toDeleteOldRevisionCount++
				} else if !IsInstanceAvailable(coreControl, instance, set.Spec.MinReadySeconds) {
					unavailableOldRevisionCount++
				}
			}
		}
	}

	updateOldDiff := oldRevisionActiveCount - partition
	updateNewDiff := newRevisionActiveCount - (replicas - partition)
	totalUnavailable := preDeletingCount + unavailableNewRevisionCount + unavailableOldRevisionCount
	// If the currentRevision and updateRevision are consistent, Instances can only update to this revision
	// Not that: Instances can only update to the new revision.
	if updateRevision == currentRevision {
		updateOldDiff = integer.IntMax(updateOldDiff, 0)
		updateNewDiff = integer.IntMin(updateNewDiff, 0)
	}

	// calculate the number of surge to use
	if maxSurge > 0 && !set.Spec.UpdateStrategy.Paused {

		// Use surge for maxUnavailable not satisfied before scaling
		var scaleSurge, scaleOldRevisionSurge int
		if toDeleteCount := toDeleteNewRevisionCount + toDeleteOldRevisionCount; toDeleteCount > 0 {
			scaleSurge = integer.IntMin(integer.IntMax((unavailableNewRevisionCount+unavailableOldRevisionCount+toDeleteCount+preDeletingCount)-maxUnavailable, 0), toDeleteCount)
			if scaleSurge > toDeleteNewRevisionCount {
				scaleOldRevisionSurge = scaleSurge - toDeleteNewRevisionCount
			}
		}

		// Use surge for old and new revision updating
		var updateSurge, updateOldRevisionSurge int
		if IsIntPlusAndMinus(updateOldDiff, updateNewDiff) {
			if IntAbs(updateOldDiff) <= IntAbs(updateNewDiff) {
				updateSurge = IntAbs(updateOldDiff)
				if updateOldDiff < 0 {
					updateOldRevisionSurge = updateSurge
				}
			} else {
				updateSurge = IntAbs(updateNewDiff)
				if updateNewDiff > 0 {
					updateOldRevisionSurge = updateSurge
				}
			}
		}

		// It is because the controller is designed not to do scale and update in once reconcile
		if scaleSurge >= updateSurge {
			res.useSurge = integer.IntMin(maxSurge, scaleSurge)
			res.useSurgeOldRevision = integer.IntMin(res.useSurge, scaleOldRevisionSurge)
		} else {
			res.useSurge = integer.IntMin(maxSurge, updateSurge)
			res.useSurgeOldRevision = integer.IntMin(res.useSurge, updateOldRevisionSurge)
		}
	}

	res.scaleNum = replicas + res.useSurge - len(instances)
	if res.scaleNum > 0 {
		res.scaleNumOldRevision = integer.IntMax(partition+res.useSurgeOldRevision-oldRevisionCount, 0)
	} else if res.scaleNum < 0 {
		res.scaleNumOldRevision = integer.IntMin(partition+res.useSurgeOldRevision-oldRevisionCount, 0)
	}

	if res.scaleNum > 0 {
		res.scaleUpLimit = integer.IntMax(scaleMaxUnavailable-totalUnavailable, 0)
		res.scaleUpLimit = integer.IntMin(res.scaleNum, res.scaleUpLimit)
	}

	if toDeleteNewRevisionCount > 0 || toDeleteOldRevisionCount > 0 || res.scaleNum < 0 {
		res.deleteReadyLimit = integer.IntMax(maxUnavailable+(len(instances)-replicas)-totalUnavailable, 0)
	}

	// The consistency between scale and update will be guaranteed by sync and expectations
	if IntAbs(updateOldDiff) <= IntAbs(updateNewDiff) {
		res.updateNum = updateOldDiff
	} else {
		res.updateNum = 0 - updateNewDiff
	}
	if res.updateNum != 0 {
		res.updateMaxUnavailable = maxUnavailable + len(instances) - replicas
	}

	return res
}

func getInstanceSetReplicas(set *appsv1alpha1.InstanceSet) int {
	if set.Spec.Replicas != nil {
		return int(*set.Spec.Replicas)
	}
	return 1
}

func isSpecifiedDelete(set *appsv1alpha1.InstanceSet, instance *appsv1alpha1.Instance) bool {
	if specifieddelete.IsSpecifiedDelete(instance) {
		return true
	}
	for _, name := range set.Spec.ScaleStrategy.InstanceToDelete {
		if name == instance.Name {
			return true
		}
	}
	return false
}

func IsInstanceReady(coreControl core.Control, instance *appsv1alpha1.Instance) bool {
	return IsInstanceAvailable(coreControl, instance, 0)
}

func IsInstanceAvailable(coreControl core.Control, instance *appsv1alpha1.Instance, minReadySeconds int32) bool {
	state := lifecycle.GetInstanceLifecycleState(instance)
	if state != "" && state != appsv1alpha1.LifecycleStateNormal {
		return false
	}
	return coreControl.IsInstanceUpdateReady(instance, minReadySeconds)
}

// DiffInstanceGroups returns instances in group1 but not in groups2
func DiffInstanceGroups(group1, group2 []*appsv1alpha1.Instance) (ret []*appsv1alpha1.Instance) {
	names2 := sets.NewString()
	for _, instance := range group2 {
		names2.Insert(instance.Name)
	}
	for _, instance := range group1 {
		if names2.Has(instance.Name) {
			continue
		}
		ret = append(ret, instance)
	}
	return
}

// IntAbs returns the abs number of the given int number
func IntAbs(i int) int {
	return int(math.Abs(float64(i)))
}

func IsIntPlusAndMinus(i, j int) bool {
	return (i < 0 && j > 0) || (i > 0 && j < 0)
}
