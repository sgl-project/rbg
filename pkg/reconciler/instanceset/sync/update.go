package sync

import (
	"fmt"
	"sort"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/specifieddelete"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset/core"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset/utils"
)

func (rc *realControl) Update(
	set *appsv1alpha1.InstanceSet,
	currentRevision *apps.ControllerRevision,
	updateRevision *apps.ControllerRevision,
	revisions []*apps.ControllerRevision,
	instances []*appsv1alpha1.Instance,
) error {
	key := utils.GetControllerKey(set)
	coreControl := core.New(set)

	// 1. refresh states for all instances
	var modified bool
	for _, instance := range instances {
		patchedState, duration, err := rc.refreshInstanceState(set, coreControl, instance)
		if err != nil {
			return err
		} else if duration > 0 {
			utils.DurationStore.Push(key, duration)
		}
		if patchedState {
			modified = true
		}
	}
	if modified {
		return nil
	}

	if set.Spec.UpdateStrategy.Paused {
		return nil
	}

	// 2. calculate update diff and the revision to update
	diffRes := calculateDiffsWithExpectation(set, instances, currentRevision.Name, updateRevision.Name)
	if diffRes.updateNum == 0 {
		return nil
	}

	// 3. find all matched instances can update
	targetRevision := updateRevision
	if diffRes.updateNum < 0 {
		targetRevision = currentRevision
	}
	var waitUpdateIndexes []int
	for i, instance := range instances {
		if coreControl.IsInstanceUpdatePaused(instance) {
			continue
		}

		var waitUpdate, canUpdate bool
		if diffRes.updateNum > 0 {
			waitUpdate = !utils.EqualToRevisionHash("", instance, updateRevision.Name)
		} else {
			waitUpdate = utils.EqualToRevisionHash("", instance, updateRevision.Name)
		}
		if waitUpdate {
			switch lifecycle.GetInstanceLifecycleState(instance) {
			case appsv1alpha1.LifecycleStatePreparingDelete:
				klog.V(3).Infof("InstanceSet %s/%s find instance %s in state %s, so skip to update it",
					set.Namespace, set.Name, instance.Name, lifecycle.GetInstanceLifecycleState(instance))
			case appsv1alpha1.LifecycleStateUpdated:
				klog.V(3).Infof("InstanceSet %s/%s find instance %s in state %s but not in updated revision",
					set.Namespace, set.Name, instance.Name, appsv1alpha1.LifecycleStateUpdated)
				canUpdate = true
			default:
				canUpdate = true
			}
		}
		if canUpdate {
			waitUpdateIndexes = append(waitUpdateIndexes, i)
		}
	}

	// 4. sort all instances waiting to update
	waitUpdateIndexes = SortUpdateIndexes(coreControl, set.Spec.UpdateStrategy, instances, waitUpdateIndexes)

	// 5. limit max count of instances can update
	waitUpdateIndexes = limitUpdateIndexes(coreControl, set.Spec.MinReadySeconds, diffRes, waitUpdateIndexes, instances, targetRevision.Name)

	// 6. update instances
	for _, idx := range waitUpdateIndexes {
		instance := instances[idx]
		duration, err := rc.updateInstance(set, coreControl, targetRevision, revisions, instance)
		if duration > 0 {
			utils.DurationStore.Push(key, duration)
		}
		if err != nil {
			return err
		}
	}

	return nil
}

func (rc *realControl) refreshInstanceState(gs *appsv1alpha1.InstanceSet, coreControl core.Control, instance *appsv1alpha1.Instance) (bool, time.Duration, error) {
	opts := coreControl.GetUpdateOptions()
	opts = inplaceupdate.SetOptionsDefaults(opts)

	res := rc.inplaceControl.Refresh(instance, opts)
	if res.RefreshErr != nil {
		klog.Errorf("InstanceSet %s/%s failed to update instance %s condition for inplace: %v",
			gs.Namespace, gs.Name, instance.Name, res.RefreshErr)
		return false, 0, res.RefreshErr
	}

	var state appsv1alpha1.LifecycleStateType
	switch lifecycle.GetInstanceLifecycleState(instance) {
	case appsv1alpha1.LifecycleStateUpdating:
		if opts.CheckInstanceUpdateCompleted(instance) == nil {
			if gs.Spec.Lifecycle != nil && !lifecycle.IsInstanceHooked(gs.Spec.Lifecycle.InPlaceUpdate, instance) {
				state = appsv1alpha1.LifecycleStateUpdated
			} else {
				state = appsv1alpha1.LifecycleStateNormal
			}
		}
	case appsv1alpha1.LifecycleStateUpdated:
		if gs.Spec.Lifecycle == nil ||
			gs.Spec.Lifecycle.InPlaceUpdate == nil ||
			lifecycle.IsInstanceAllHooked(gs.Spec.Lifecycle.InPlaceUpdate, instance) {
			state = appsv1alpha1.LifecycleStateNormal
		}
	}

	if state != "" {
		var markNotReady bool
		if gs.Spec.Lifecycle != nil && gs.Spec.Lifecycle.InPlaceUpdate != nil {
			markNotReady = gs.Spec.Lifecycle.InPlaceUpdate.MarkNotReady
		}
		if updated, gotInstance, err := rc.lifecycleControl.UpdateInstanceLifecycle(instance, state, markNotReady); err != nil {
			return false, 0, err
		} else if updated {
			utils.ResourceVersionExpectations.Expect(gotInstance)
			klog.V(3).Infof("InstanceSet %s update instance %s lifecycle to %s", utils.GetControllerKey(gs), instance.Name, state)
			return true, res.DelayDuration, nil
		}
	}

	return false, res.DelayDuration, nil
}

func (rc *realControl) updateInstance(set *appsv1alpha1.InstanceSet, coreControl core.Control,
	updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision,
	instance *appsv1alpha1.Instance,
) (time.Duration, error) {

	if set.Spec.UpdateStrategy.Type == appsv1alpha1.InPlaceIfPossibleInstanceSetUpdateStrategyType {
		var oldRevision *apps.ControllerRevision
		for _, r := range revisions {
			if utils.EqualToRevisionHash("", instance, r.Name) {
				oldRevision = r
				break
			}
		}

		if rc.inplaceControl.CanUpdateInPlace(oldRevision, updateRevision, coreControl.GetUpdateOptions()) {
			switch state := lifecycle.GetInstanceLifecycleState(instance); state {
			case "", appsv1alpha1.LifecycleStateNormal:
				var err error
				var updated bool
				var gotInstance *appsv1alpha1.Instance
				if set.Spec.Lifecycle != nil && lifecycle.IsInstanceHooked(set.Spec.Lifecycle.InPlaceUpdate, instance) {
					markNotReady := set.Spec.Lifecycle.InPlaceUpdate.MarkNotReady
					if updated, gotInstance, err = rc.lifecycleControl.UpdateInstanceLifecycle(instance, appsv1alpha1.LifecycleStatePreparingUpdate, markNotReady); err == nil && updated {
						utils.ResourceVersionExpectations.Expect(gotInstance)
						klog.V(3).Infof("InstanceSet %s update instance %s lifecycle to PreparingUpdate",
							utils.GetControllerKey(set), instance.Name)
					}
					return 0, err
				}
			case appsv1alpha1.LifecycleStateUpdated:
				var err error
				var updated bool
				var gotInstance *appsv1alpha1.Instance
				var inPlaceUpdateHandler *appsv1alpha1.LifecycleHook
				if set.Spec.Lifecycle != nil {
					inPlaceUpdateHandler = set.Spec.Lifecycle.InPlaceUpdate
				}
				if updated, gotInstance, err = rc.lifecycleControl.UpdateInstanceLifecycleWithHandler(instance, appsv1alpha1.LifecycleStatePreparingUpdate, inPlaceUpdateHandler); err == nil && updated {
					utils.ResourceVersionExpectations.Expect(gotInstance)
					klog.V(3).Infof("InstanceSet %s update instance %s lifecycle to PreparingUpdate",
						utils.GetControllerKey(set), instance.Name)
				}
				return 0, err
			case appsv1alpha1.LifecycleStatePreparingUpdate:
				if set.Spec.Lifecycle != nil && lifecycle.IsInstanceHooked(set.Spec.Lifecycle.InPlaceUpdate, instance) {
					return 0, nil
				}
			case appsv1alpha1.LifecycleStateUpdating:
			default:
				return 0, fmt.Errorf("not allowed to in-place update instance %s in state %s", instance.Name, state)
			}

			opts := coreControl.GetUpdateOptions()
			opts.AdditionalFuncs = append(opts.AdditionalFuncs, lifecycle.SetInstanceLifecycle(appsv1alpha1.LifecycleStateUpdating))
			res := rc.inplaceControl.Update(instance, oldRevision, updateRevision, opts)
			if res.InPlaceUpdate {
				if res.UpdateErr == nil {
					rc.recorder.Eventf(set, v1.EventTypeNormal, "SuccessfulUpdateInstanceInPlace", "successfully update instance %s in-place(revision %v)", instance.Name, updateRevision.Name)
					utils.ResourceVersionExpectations.Expect(&metav1.ObjectMeta{UID: instance.UID, ResourceVersion: res.NewResourceVersion})
					return res.DelayDuration, nil
				}

				rc.recorder.Eventf(set, v1.EventTypeWarning, "FailedUpdateInstanceInPlace", "failed to update instance %s in-place(revision %v): %v", instance.Name, updateRevision.Name, res.UpdateErr)
				return res.DelayDuration, res.UpdateErr
			}
		}
		klog.Warningf("InstanceSet %s/%s can not update instance %s in-place, so it will back off to ReCreate", set.Namespace, set.Name, instance.Name)
	}

	klog.V(2).Infof("InstanceSet %s/%s start to patch instance %s specified-delete for update %s", set.Namespace, set.Name, instance.Name, updateRevision.Name)

	if patched, err := specifieddelete.PatchInstanceSpecifiedDelete(rc.Client, instance, "true"); err != nil {
		rc.recorder.Eventf(set, v1.EventTypeWarning, "FailedUpdateInstanceReCreate",
			"failed to patch instance specified-delete %s for update(revision %s): %v", instance.Name, updateRevision.Name, err)
		return 0, err
	} else if patched {
		utils.ResourceVersionExpectations.Expect(instance)
	}

	rc.recorder.Eventf(set, v1.EventTypeNormal, "SuccessfulUpdateInstanceReCreate",
		"successfully patch instance %s specified-delete for update(revision %s)", instance.Name, updateRevision.Name)
	return 0, nil
}

// SortUpdateIndexes sorts the given oldRevisionIndexes of Instances to update according to the InstanceSet strategy.
func SortUpdateIndexes(coreControl core.Control, strategy appsv1alpha1.InstanceSetUpdateStrategy, instances []*appsv1alpha1.Instance, waitUpdateIndexes []int) []int {
	// Sort Instances with default sequence
	sort.Slice(waitUpdateIndexes, coreControl.GetInstancesSortFunc(instances, waitUpdateIndexes))

	// PreparingUpdate first
	sort.SliceStable(waitUpdateIndexes, func(i, j int) bool {
		preparingUpdateI := lifecycle.GetInstanceLifecycleState(instances[waitUpdateIndexes[i]]) == appsv1alpha1.LifecycleStatePreparingUpdate
		preparingUpdateJ := lifecycle.GetInstanceLifecycleState(instances[waitUpdateIndexes[j]]) == appsv1alpha1.LifecycleStatePreparingUpdate
		if preparingUpdateI != preparingUpdateJ {
			return preparingUpdateI
		}
		return false
	})
	return waitUpdateIndexes
}

// limitUpdateIndexes limits all instances waiting update by the maxUnavailable policy, and returns the indexes of instances that can finally update
func limitUpdateIndexes(coreControl core.Control, minReadySeconds int32, diffRes expectationDiffs, waitUpdateIndexes []int, instances []*appsv1alpha1.Instance, targetRevisionHash string) []int {
	updateDiff := IntAbs(diffRes.updateNum)
	if updateDiff < len(waitUpdateIndexes) {
		waitUpdateIndexes = waitUpdateIndexes[:updateDiff]
	}

	var unavailableCount, targetRevisionUnavailableCount, canUpdateCount int
	for _, p := range instances {
		if !IsInstanceAvailable(coreControl, p, minReadySeconds) {
			unavailableCount++
			if utils.EqualToRevisionHash("", p, targetRevisionHash) {
				targetRevisionUnavailableCount++
			}
		}
	}
	for _, i := range waitUpdateIndexes {
		// Make sure unavailable instances in target revision should not be more than maxUnavailable.
		if targetRevisionUnavailableCount+canUpdateCount >= diffRes.updateMaxUnavailable {
			break
		}

		// Make sure unavailable instances in all revisions should not be more than maxUnavailable.
		// Note that update an old instance that already be unavailable will not increase the unavailable number.
		if IsInstanceAvailable(coreControl, instances[i], minReadySeconds) {
			if unavailableCount >= diffRes.updateMaxUnavailable {
				break
			}
			unavailableCount++
		}
		canUpdateCount++
	}

	if canUpdateCount < len(waitUpdateIndexes) {
		waitUpdateIndexes = waitUpdateIndexes[:canUpdateCount]
	}
	return waitUpdateIndexes
}
