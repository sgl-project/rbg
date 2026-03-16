package utils

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/openkruise/kruise/pkg/util/expectations"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/sets"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	kubecontroller "k8s.io/kubernetes/pkg/controller"
	"k8s.io/utils/integer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

var (
	ControllerKind              = workloadsv1alpha2.SchemeGroupVersion.WithKind("RoleInstance")
	RevisionAdapterImpl         = &revisionAdapterImpl{}
	EqualToRevisionHash         = RevisionAdapterImpl.EqualToRevisionHash
	WriteRevisionHash           = RevisionAdapterImpl.WriteRevisionHash
	ResourceVersionExpectations = expectations.NewResourceVersionExpectation()
)

type revisionAdapterImpl struct {
}

func (r *revisionAdapterImpl) EqualToRevisionHash(_ string, obj metav1.Object, hash string) bool {
	objHash := obj.GetLabels()[apps.ControllerRevisionHashLabelKey]
	if objHash == hash {
		return true
	}
	return GetShortHash(hash) == GetShortHash(objHash)
}

func (r *revisionAdapterImpl) WriteRevisionHash(obj metav1.Object, hash string) {
	if obj.GetLabels() == nil {
		obj.SetLabels(make(map[string]string, 1))
	}
	shortHash := GetShortHash(hash)
	obj.GetLabels()[apps.ControllerRevisionHashLabelKey] = shortHash
}

func GetShortHash(hash string) string {
	// This makes sure the real hash must be the last '-' substring of revision name
	// vendor/k8s.io/kubernetes/pkg/controller/history/controller_history.go#82
	list := strings.Split(hash, "-")
	return list[len(list)-1]
}

func FormatComponentPodName(instanceName, componentName string, id int32, roleTemplateType constants.RoleTemplateType) string {
	switch roleTemplateType {
	case constants.LeaderWorkerSetTemplateType:
		podIndex := id
		if componentName == "worker" {
			podIndex++
		}
		return fmt.Sprintf("%s-%d", instanceName, podIndex)
	case constants.ComponentsTemplateType:
		return fmt.Sprintf("%s-%s-%d", instanceName, componentName, id)
	default:
		return instanceName
	}
}

func InitComponentPodLabels(instanceName, componentName string, id int32, roleTemplateType constants.RoleTemplateType) map[string]string {
	l := GetSelectorMatchLabels(instanceName)
	l[constants.ComponentNameLabelKey] = componentName
	l[constants.ComponentIDLabelKey] = fmt.Sprintf("%d", id)
	if roleTemplateType == constants.LeaderWorkerSetTemplateType {
		l[constants.ComponentIndexLabelKey] = fmt.Sprintf("%d", id)
		// when roleTemplateType is LWS, component name will be controlled by RBG-controller
		if componentName == "worker" {
			l[constants.ComponentIndexLabelKey] = fmt.Sprintf("%d", id+1)
		}
	}

	return l
}

// IsRunningAndAvailable returns true if pod is in the PodRunning Phase, if it is available.
func IsRunningAndAvailable(pod *v1.Pod, minReadySeconds int32) bool {
	return pod.Status.Phase == v1.PodRunning && podutil.IsPodAvailable(pod, minReadySeconds, metav1.Now())
}

func GetSelectorMatchLabels(instanceName string) map[string]string {
	return map[string]string{
		constants.RoleInstanceNameLabelKey: instanceName,
	}
}

func GetSelector(instance *workloadsv1alpha2.RoleInstance) labels.Selector {
	matchLabels := GetSelectorMatchLabels(instance.Name)
	selector := labels.NewSelector()
	for k, v := range matchLabels {
		requirement, _ := labels.NewRequirement(
			k, selection.Equals, []string{v},
		)
		selector.Add(*requirement)
	}

	return selector
}

// GetActiveAndInactivePods get activePods and inactivePods
func GetActiveAndInactivePods(ctx context.Context, reader client.Reader, opts *client.ListOptions) ([]*v1.Pod, []*v1.Pod, error) {
	podList := &v1.PodList{}
	if err := reader.List(ctx, podList, opts); err != nil {
		return nil, nil, err
	}
	var activePods, inactivePods []*v1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		if kubecontroller.IsPodActive(pod) {
			activePods = append(activePods, pod)
		} else {
			inactivePods = append(inactivePods, pod)
		}
	}
	return activePods, inactivePods, nil
}

// NextRevision finds the next valid revision number based on revisions. If the length of revisions
// is 0 this is 1. Otherwise, it is 1 greater than the largest revision's Revision. This method
// assumes that revisions has been sorted by Revision.
func NextRevision(revisions []*apps.ControllerRevision) int64 {
	count := len(revisions)
	if count <= 0 {
		return 1
	}
	return revisions[count-1].Revision + 1
}

func GetPodComponentName(pod *v1.Pod) string {
	componentName := pod.Labels[constants.ComponentNameLabelKey]
	if len(componentName) != 0 {
		return componentName
	}
	list := strings.Split(pod.Name, "-")
	if len(list) < 2 {
		return ""
	}
	return list[len(list)-2]
}

func GetPodComponentID(pod *v1.Pod) int32 {
	componentId := pod.Labels[constants.ComponentIDLabelKey]
	if len(componentId) != 0 {
		id, _ := strconv.Atoi(componentId)
		return int32(id)
	}
	list := strings.Split(pod.Name, "-")
	componentId = list[len(list)-1]
	id, _ := strconv.Atoi(componentId)
	return int32(id)
}

func GroupPodsByComponentName(pods []*v1.Pod) map[string][]*v1.Pod {
	group := make(map[string][]*v1.Pod)
	for i := range pods {
		roleType := GetPodComponentName(pods[i])
		group[roleType] = append(group[roleType], pods[i])
	}
	return group
}

// DoItSlowly tries to call the provided function a total of 'count' times,
// starting slow to check for errors, then speeding up if calls succeed.
//
// It groups the calls into batches, starting with a group of initialBatchSize.
// Within each batch, it may call the function multiple times concurrently.
//
// If a whole batch succeeds, the next batch may get exponentially larger.
// If there are any failures in a batch, all remaining batches are skipped
// after waiting for the current batch to complete.
//
// It returns the number of successful calls to the function.
func DoItSlowly(count int, initialBatchSize int, fn func() error) (int, error) {
	remaining := count
	successes := 0
	for batchSize := integer.IntMin(remaining, initialBatchSize); batchSize > 0; batchSize = integer.IntMin(2*batchSize, remaining) {
		errCh := make(chan error, batchSize)
		var wg sync.WaitGroup
		wg.Add(batchSize)
		for i := 0; i < batchSize; i++ {
			go func() {
				defer wg.Done()
				if err := fn(); err != nil {
					errCh <- err
				}
			}()
		}
		wg.Wait()
		curSuccesses := batchSize - len(errCh)
		successes += curSuccesses
		if len(errCh) > 0 {
			return successes, <-errCh
		}
		remaining -= batchSize
	}
	return successes, nil
}

func IsPodScheduled(pod *v1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == v1.PodScheduled && condition.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

func ContainsReadinessGate(instance *workloadsv1alpha2.RoleInstance, gate workloadsv1alpha2.RoleInstanceConditionType) bool {
	for _, readinessGate := range instance.Spec.ReadinessGates {
		if readinessGate.ConditionType == gate {
			return true
		}
	}
	return false
}

func PodContainsReadinessGate(pod *v1.Pod, gate v1.PodConditionType) bool {
	for _, readinessGate := range pod.Spec.ReadinessGates {
		if readinessGate.ConditionType == gate {
			return true
		}
	}
	return false
}

var runtimePodConditions = sets.New[v1.PodConditionType](
	v1.ContainersReady,
	constants.InPlaceUpdateReady,
)

func IsPodRuntimeReady(pod *v1.Pod, minReadySeconds int32) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}
	if pod.Status.Phase != v1.PodRunning {
		return false
	}
	for conditionType := range runtimePodConditions {
		_, condition := podutil.GetPodCondition(&pod.Status, conditionType)
		if condition == nil {
			return false
		}
		if condition.Status != v1.ConditionTrue {
			return false
		}
	}
	return true
}
