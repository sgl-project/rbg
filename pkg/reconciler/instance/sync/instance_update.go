package sync

import (
	"context"
	"time"

	"github.com/openkruise/kruise/pkg/util/requeueduration"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	instancecore "sigs.k8s.io/rbgs/pkg/reconciler/instance/core"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/instance/utils"
)

func (c *realControl) Update(ctx context.Context, instance *v1alpha1.Instance, _, updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision, pods []*v1.Pod) (time.Duration, error) {
	logger := log.FromContext(ctx)
	requeueDuration := requeueduration.Duration{}
	coreControl := instancecore.New(instance)

	waitUpdateComponentPods := make(map[string][]*v1.Pod)
	for i := range pods {
		if res := c.inplaceControl.Refresh(ctx, pods[i], coreControl.GetUpdateOptions()); res.RefreshErr != nil {
			logger.Error(res.RefreshErr, "Instance failed to update pod condition for inplace", "pod", pods[i].Name)
			return requeueDuration.Get(), res.RefreshErr
		} else if res.DelayDuration > 0 {
			requeueDuration.Update(res.DelayDuration)
		}
		if !instanceutil.EqualToRevisionHash("", pods[i], updateRevision.Name) {
			waitUpdateComponentPods[instanceutil.GetPodComponentName(pods[i])] = append(waitUpdateComponentPods[instanceutil.GetPodComponentName(pods[i])], pods[i])
		}
	}
	for componentName := range waitUpdateComponentPods {
		componentPods := waitUpdateComponentPods[componentName]
		for i := range componentPods {
			if duration, err := c.updatePod(ctx, instance, updateRevision, revisions, componentPods[i]); err != nil {
				return requeueDuration.Get(), err
			} else if duration > 0 {
				requeueDuration.Update(duration)
			}
		}
	}
	return requeueDuration.Get(), nil
}

func (c *realControl) updatePod(ctx context.Context, instance *v1alpha1.Instance,
	updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision, pod *v1.Pod) (time.Duration, error) {
	logger := log.FromContext(ctx)
	var oldRevision *apps.ControllerRevision
	for _, r := range revisions {
		if instanceutil.EqualToRevisionHash("", pod, r.Name) {
			oldRevision = r
			break
		}
	}
	coreControl := instancecore.New(instance)
	res := c.inplaceControl.Update(ctx, pod, oldRevision, updateRevision, coreControl.GetUpdateOptions())
	if res.InPlaceUpdate {
		if res.UpdateErr == nil {
			c.recorder.Eventf(instance, v1.EventTypeNormal, "SuccessfulUpdatePodInPlace", "successfully update pod %s in-place", pod.Name)
			instanceutil.ResourceVersionExpectations.Expect(&metav1.ObjectMeta{UID: pod.UID, ResourceVersion: res.NewResourceVersion})
			return res.DelayDuration, nil
		}
		c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedUpdatePodInPlace", "failed to update pod %s in-place: %v", pod.Name, res.UpdateErr)
		return res.DelayDuration, res.UpdateErr
	}
	logger.Info("Instance can not update Pod in-place, so it will back off to ReCreate", "pod", klog.KObj(pod))
	if err := c.Delete(context.TODO(), pod); err != nil {
		c.recorder.Eventf(instance, v1.EventTypeWarning, "FailedUpdatePodReCreate", "failed to delete pod %s for update: %v", pod.Name, err)
		return 0, err
	}
	return 0, nil
}
