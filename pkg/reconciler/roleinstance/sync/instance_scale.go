package sync

import (
	"context"
	"fmt"
	"sync/atomic"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/rbgs/pkg/constants"
	"sigs.k8s.io/rbgs/pkg/utils"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	podinplace "sigs.k8s.io/rbgs/pkg/inplace/pod"
	instancecore "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/core"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
)

const (
	// When batching pod creates, initialBatchSize is the size of the initial batch.
	initialBatchSize = 1
)

func (c *realControl) Scale(ctx context.Context, updateInstance *workloadsv1alpha2.RoleInstance, currentRevision, updateRevision *apps.ControllerRevision,
	revisions []*apps.ControllerRevision, pods []*v1.Pod) (bool, error) {
	diffRes, err := c.calculateDiffsWithExpectation(ctx, updateInstance, currentRevision, updateRevision, revisions, pods)
	if err != nil {
		return true, err
	}
	if diffRes.toScaleNum > 0 {
		return c.createPods(ctx, updateInstance, diffRes.toScaleRoleIDS, updateRevision.Name)
	}
	if diffRes.toDeleteNum > 0 {
		return c.deletePods(ctx, updateInstance, diffRes.toDeletePod)
	}
	return false, nil
}

type expectationDiff struct {
	toDeleteNum int
	toDeletePod []*v1.Pod

	toScaleNum     int
	toScaleRoleIDS map[string]sets.Set[int32]
}

func (c *realControl) calculateDiffsWithExpectation(ctx context.Context, updateInstance *workloadsv1alpha2.RoleInstance,
	currentRevision, updateRevision *apps.ControllerRevision,
	revisions []*apps.ControllerRevision, pods []*v1.Pod) (*expectationDiff, error) {

	coreControl := instancecore.New(updateInstance)

	// Check RestartPolicy: if RecreateInstanceOnPodRestart and any pod is deleted or restarted,
	// recreate all pods of the instance
	if shouldRecreateInstance(updateInstance, pods) {
		c.recorder.Event(updateInstance, v1.EventTypeNormal, "ReCreateInstance",
			fmt.Sprintf("RestartPolicy is RecreateInstanceOnPodRestart, recreate all pods of instance: %v", klog.KObj(updateInstance)))
		return &expectationDiff{toDeleteNum: len(pods), toDeletePod: pods}, nil
	}

	if isGangSchedulingEnabled(updateInstance) {
		for i := range pods {
			oldRevision := currentRevision
			for _, r := range revisions {
				if instanceutil.EqualToRevisionHash("", pods[i], r.Name) {
					oldRevision = r
					break
				}
			}
			if !c.inplaceControl.CanUpdateInPlace(ctx, oldRevision, updateRevision, coreControl.GetUpdateOptions()) {
				c.recorder.Event(updateInstance, v1.EventTypeNormal, "ReCreateInstance", fmt.Sprintf("component %s can't inplace updated, "+
					"recreate all pods of instance: %v", instanceutil.GetPodComponentName(pods[i]), klog.KObj(updateInstance)))
				return &expectationDiff{toDeleteNum: len(pods), toDeletePod: pods}, nil
			}
		}
	}

	var (
		toDeleteNum  = 0
		toDeletePods []*v1.Pod

		toScaleNum     = 0
		toScaleRoleIDS = make(map[string]sets.Set[int32])
	)

	prt, err := coreControl.GetComponentsTopology(pods)
	if err != nil {
		return nil, err
	}
	for _, rg := range prt.Topologies {
		if rg.ToDeleteIDs.Len() > 0 {
			toDeleteNum += rg.ToDeleteIDs.Len()
			toDeletePods = append(toDeletePods, rg.ToDeletePod...)
		}
		if rg.ToScaleIDs.Len() > 0 {
			toScaleNum += rg.ToScaleIDs.Len()
			toScaleRoleIDS[rg.Name] = rg.ToScaleIDs
		}
	}

	return &expectationDiff{
		toDeleteNum:    toDeleteNum,
		toScaleNum:     toScaleNum,
		toScaleRoleIDS: toScaleRoleIDS,
		toDeletePod:    toDeletePods,
	}, nil
}

func (c *realControl) createPods(ctx context.Context, updateInstance *workloadsv1alpha2.RoleInstance, expectedCreations map[string]sets.Set[int32], updateRevision string) (bool, error) {
	coreControl := instancecore.New(updateInstance)
	var newPods []*v1.Pod
	for _, component := range updateInstance.Spec.Components {
		updatePods, err := coreControl.NewUpdatePods(updateRevision, component.Name, sets.List(expectedCreations[component.Name]))
		if err != nil {
			return false, err
		}
		newPods = append(newPods, updatePods...)
	}
	podsCreationChan := make(chan *v1.Pod, len(newPods))
	toCreatePodNum := 0
	for _, p := range newPods {
		if c.hasOrphanPod(p.Namespace, p.Name) {
			if isGangSchedulingEnabled(updateInstance) {
				return false, fmt.Errorf("orphan pod %v has not been gc, fail to create new pod", klog.KObj(p))
			}
			continue
		}
		toCreatePodNum++
		podsCreationChan <- p
	}
	var created int64
	_, err := instanceutil.DoItSlowly(toCreatePodNum, initialBatchSize, func() error {
		pod := <-podsCreationChan
		if createErr := c.createOnePod(ctx, updateInstance, pod); createErr != nil {
			return createErr
		}
		atomic.AddInt64(&created, 1)
		return nil
	})
	if created == 0 {
		return false, err
	}
	return true, err
}

func (c *realControl) deletePods(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, podsToDelete []*v1.Pod) (bool, error) {
	var modified bool
	for _, pod := range podsToDelete {
		if err := c.Delete(ctx, pod); err != nil {
			c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedDelete", "failed to delete pod %s: %v", pod.Name, err)
			return modified, err
		}
		modified = true
		c.recorder.Event(instance, v1.EventTypeNormal, "SuccessfulDelete", fmt.Sprintf("succeed to delete pod %s", pod.Name))
	}
	return modified, nil
}

func (c *realControl) hasOrphanPod(namespace, name string) bool {
	pod := new(v1.Pod)
	err := c.Get(context.TODO(), client.ObjectKey{Namespace: namespace, Name: name}, pod)
	return err == nil
}

func (c *realControl) createOnePod(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, pod *v1.Pod) error {
	if err := c.Create(ctx, pod); err != nil {
		c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedCreate", "failed to create pod: %v, pod: %v", err, podinplace.DumpJSON(pod))
		return err
	}
	c.recorder.Eventf(instance, v1.EventTypeNormal, "SuccessfulCreate", "succeed to create pod %s", pod.Name)
	return nil
}

// shouldRecreateInstance checks if the instance should be recreated based on RestartPolicy.
// When RestartPolicy is RecreateInstanceOnPodRestart, the instance should be recreated if:
// 1. Any container has restarted (RestartCount > 0)
// 2. Any pod has been deleted (only when Instance was previously Ready)
//
// Note: We use Instance's Ready condition to distinguish between initial creation/scaling
// and pod deletion. This avoids infinite recreation loops.
func shouldRecreateInstance(instance *workloadsv1alpha2.RoleInstance, pods []*v1.Pod) bool {
	// Only check when RestartPolicy is RecreateInstanceOnPodRestart
	if instance.Spec.RestartPolicy != workloadsv1alpha2.RoleInstanceRestartPolicyRecreateOnPodRestart {
		return false
	}

	// If no pods exist yet (initial creation), don't trigger recreate
	if len(pods) == 0 {
		return false
	}

	// Check each pod for container restart
	for _, pod := range pods {
		if utils.ContainerRestarted(pod) {
			return true
		}
	}

	// Check if any pod has been deleted:
	// Only trigger recreate if Instance was previously Ready (stable state)
	// and spec is not being changed (Generation == ObservedGeneration).
	// This avoids triggering recreate during initial creation or scaling up.
	if wasInstanceReady(instance) && instance.Generation == instance.Status.ObservedGeneration {
		expectedPodCount := getExpectedPodCount(instance)
		// Calculate active pods (exclude those being deleted).
		// Pods with DeletionTimestamp != nil are in Terminating state and should not
		// be counted as active, otherwise the controller may fail to detect pod deletion.
		activeCount := 0
		for _, p := range pods {
			if p.DeletionTimestamp == nil {
				activeCount++
			}
		}
		if activeCount < expectedPodCount {
			return true
		}
	}

	return false
}

// wasInstanceReady checks if the Instance was previously in Ready state
func wasInstanceReady(instance *workloadsv1alpha2.RoleInstance) bool {
	for _, cond := range instance.Status.Conditions {
		if cond.Type == workloadsv1alpha2.RoleInstanceReady && cond.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

// getExpectedPodCount calculates the expected total pod count from instance spec
func getExpectedPodCount(instance *workloadsv1alpha2.RoleInstance) int {
	expectedPodCount := 0
	for _, component := range instance.Spec.Components {
		if component.Size != nil {
			expectedPodCount += int(*component.Size)
		}
	}
	return expectedPodCount
}

// isGangSchedulingEnabled reports whether gang-scheduling constraints are active for the
// given RoleInstance. The annotation is derived from the parent RBG's gang-scheduling
// annotation during RoleInstanceSet reconciliation, or set directly via role.Annotations.
func isGangSchedulingEnabled(instance *workloadsv1alpha2.RoleInstance) bool {
	return instance.Annotations[constants.RoleInstanceGangSchedulingAnnotationKey] == "true"
}
