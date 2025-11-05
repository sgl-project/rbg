package lifecycle

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	appsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/clientdapter"
	"sigs.k8s.io/rbgs/pkg/inplace/instance/readiness"
)

const (
	// these keys for MarkNotReady Policy of group lifecycle
	preparingDeleteHookKey = "preDeleteHook"
	preparingUpdateHookKey = "preUpdateHook"
)

func newGroupReadinessControl(adp clientdapter.Adapter) readiness.Interface {
	return readiness.NewForAdapter(adp)
}

// Interface for managing pods lifecycle.
type Interface interface {
	UpdateInstanceLifecycle(instance *appsv1alpha1.Instance, state appsv1alpha1.LifecycleStateType, markPodNotReady bool) (bool, *appsv1alpha1.Instance, error)
	UpdateInstanceLifecycleWithHandler(instance *appsv1alpha1.Instance, state appsv1alpha1.LifecycleStateType, inPlaceUpdateHandler *appsv1alpha1.LifecycleHook) (bool, *appsv1alpha1.Instance, error)
}

type realControl struct {
	adp                   clientdapter.Adapter
	groupReadinessControl readiness.Interface
}

func New(c client.Client) Interface {
	adp := &clientdapter.AdapterRuntimeClient{Client: c}
	return &realControl{
		adp:                   adp,
		groupReadinessControl: newGroupReadinessControl(adp),
	}
}

func GetInstanceLifecycleState(group *appsv1alpha1.Instance) appsv1alpha1.LifecycleStateType {
	return appsv1alpha1.LifecycleStateType(group.Labels[appsv1alpha1.LifecycleStateKey])
}

func IsHookMarkGroupNotReady(lifecycleHook *appsv1alpha1.LifecycleHook) bool {
	if lifecycleHook == nil {
		return false
	}
	return lifecycleHook.MarkNotReady
}

func IsLifecycleMarkInstanceNotReady(lifecycle *appsv1alpha1.Lifecycle) bool {
	if lifecycle == nil {
		return false
	}
	return IsHookMarkGroupNotReady(lifecycle.PreDelete) || IsHookMarkGroupNotReady(lifecycle.InPlaceUpdate)
}

func SetInstanceLifecycle(state appsv1alpha1.LifecycleStateType) func(instance *appsv1alpha1.Instance) {
	return func(instance *appsv1alpha1.Instance) {
		if instance.Labels == nil {
			instance.Labels = make(map[string]string)
		}
		if instance.Annotations == nil {
			instance.Annotations = make(map[string]string)
		}
		instance.Labels[appsv1alpha1.LifecycleStateKey] = string(state)
		instance.Annotations[appsv1alpha1.LifecycleTimestampKey] = time.Now().Format(time.RFC3339)
	}
}

func (c *realControl) executeInstanceNotReadyPolicy(instance *appsv1alpha1.Instance, state appsv1alpha1.LifecycleStateType) (err error) {
	switch state {
	case appsv1alpha1.LifecycleStatePreparingDelete:
		err = c.groupReadinessControl.AddNotReadyKey(instance, getReadinessMessage(preparingDeleteHookKey))
	case appsv1alpha1.LifecycleStatePreparingUpdate:
		err = c.groupReadinessControl.AddNotReadyKey(instance, getReadinessMessage(preparingUpdateHookKey))
	case appsv1alpha1.LifecycleStateNormal:
		err = c.groupReadinessControl.RemoveNotReadyKey(instance, getReadinessMessage(preparingUpdateHookKey))
	}

	if err != nil {
		klog.Errorf("Failed to set instance(%v) Ready/NotReady at %s lifecycle state, error: %v", klog.KObj(instance), state, err)
	}
	return
}

func (c *realControl) UpdateInstanceLifecycle(instance *appsv1alpha1.Instance, state appsv1alpha1.LifecycleStateType, markPodNotReady bool) (updated bool, gotPod *appsv1alpha1.Instance, err error) {
	if markPodNotReady {
		if err = c.executeInstanceNotReadyPolicy(instance, state); err != nil {
			return false, nil, err
		}
	}

	if GetInstanceLifecycleState(instance) == state {
		return false, instance, nil
	}

	instance = instance.DeepCopy()
	if adp, ok := c.adp.(clientdapter.AdapterWithPatch); ok {
		body := fmt.Sprintf(
			`{"metadata":{"labels":{"%s":"%s"},"annotations":{"%s":"%s"}}}`,
			appsv1alpha1.LifecycleStateKey,
			string(state),
			appsv1alpha1.LifecycleTimestampKey,
			time.Now().Format(time.RFC3339),
		)
		gotPod, err = adp.PatchInstance(instance, client.RawPatch(types.MergePatchType, []byte(body)))
	} else {
		SetInstanceLifecycle(state)(instance)
		gotPod, err = c.adp.UpdateInstance(instance)
	}

	return true, gotPod, err
}

func (c *realControl) UpdateInstanceLifecycleWithHandler(instance *appsv1alpha1.Instance, state appsv1alpha1.LifecycleStateType, inPlaceUpdateHandler *appsv1alpha1.LifecycleHook) (updated bool, gotPod *appsv1alpha1.Instance, err error) {
	if inPlaceUpdateHandler == nil || instance == nil {
		return false, instance, nil
	}

	if inPlaceUpdateHandler.MarkNotReady {
		if err = c.executeInstanceNotReadyPolicy(instance, state); err != nil {
			return false, nil, err
		}
	}

	if GetInstanceLifecycleState(instance) == state {
		return false, instance, nil
	}

	instance = instance.DeepCopy()
	if adp, ok := c.adp.(clientdapter.AdapterWithPatch); ok {
		var labelsHandler, finalizersHandler string
		for k, v := range inPlaceUpdateHandler.LabelsHandler {
			labelsHandler = fmt.Sprintf(`%s,"%s":"%s"`, labelsHandler, k, v)
		}
		for _, v := range inPlaceUpdateHandler.FinalizersHandler {
			finalizersHandler = fmt.Sprintf(`%s,"%s"`, finalizersHandler, v)
		}
		finalizersHandler = fmt.Sprintf(`[%s]`, strings.TrimLeft(finalizersHandler, ","))

		body := fmt.Sprintf(
			`{"metadata":{"labels":{"%s":"%s"%s},"annotations":{"%s":"%s"},"finalizers":%s}}`,
			appsv1alpha1.LifecycleStateKey,
			string(state),
			labelsHandler,
			appsv1alpha1.LifecycleTimestampKey,
			time.Now().Format(time.RFC3339),
			finalizersHandler,
		)
		gotPod, err = adp.PatchInstance(instance, client.RawPatch(types.MergePatchType, []byte(body)))
	} else {
		if instance.Labels == nil {
			instance.Labels = make(map[string]string)
		}
		for k, v := range inPlaceUpdateHandler.LabelsHandler {
			instance.Labels[k] = v
		}
		instance.Finalizers = append(instance.Finalizers, inPlaceUpdateHandler.FinalizersHandler...)

		SetInstanceLifecycle(state)(instance)
		gotPod, err = c.adp.UpdateInstance(instance)
	}

	return true, gotPod, err
}

func IsInstanceHooked(hook *appsv1alpha1.LifecycleHook, instance *appsv1alpha1.Instance) bool {
	if hook == nil || instance == nil {
		return false
	}
	for _, f := range hook.FinalizersHandler {
		if controllerutil.ContainsFinalizer(instance, f) {
			return true
		}
	}
	for k, v := range hook.LabelsHandler {
		if instance.Labels[k] == v {
			return true
		}
	}
	return false
}

func IsInstanceAllHooked(hook *appsv1alpha1.LifecycleHook, instance *appsv1alpha1.Instance) bool {
	if hook == nil || instance == nil {
		return false
	}
	for _, f := range hook.FinalizersHandler {
		if !controllerutil.ContainsFinalizer(instance, f) {
			return false
		}
	}
	for k, v := range hook.LabelsHandler {
		if instance.Labels[k] != v {
			return false
		}
	}
	return true
}

func getReadinessMessage(key string) readiness.Message {
	return readiness.Message{UserAgent: "Lifecycle", Key: key}
}
