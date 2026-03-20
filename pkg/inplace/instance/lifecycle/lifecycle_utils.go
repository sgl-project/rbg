/*
Copyright 2026 The RBG Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package lifecycle

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
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

// Interface for managing role instances lifecycle.
type Interface interface {
	UpdateRoleInstanceLifecycle(instance *workloadsv1alpha2.RoleInstance, state constants.RoleInstanceLifecycleStateType, markPodNotReady bool) (bool, *workloadsv1alpha2.RoleInstance, error)
	UpdateRoleInstanceLifecycleWithHandler(instance *workloadsv1alpha2.RoleInstance, state constants.RoleInstanceLifecycleStateType, inPlaceUpdateHandler *workloadsv1alpha2.RoleInstanceSetLifecycleHook) (bool, *workloadsv1alpha2.RoleInstance, error)
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

func GetRoleInstanceLifecycleState(instance *workloadsv1alpha2.RoleInstance) constants.RoleInstanceLifecycleStateType {
	return constants.RoleInstanceLifecycleStateType(instance.Labels[constants.RoleInstanceSetLifecycleStateKey])
}

func IsHookMarkGroupNotReady(lifecycleHook *workloadsv1alpha2.RoleInstanceSetLifecycleHook) bool {
	if lifecycleHook == nil {
		return false
	}
	return lifecycleHook.MarkNotReady
}

func IsLifecycleMarkRoleInstanceNotReady(lifecycle *workloadsv1alpha2.RoleInstanceSetLifecycle) bool {
	if lifecycle == nil {
		return false
	}
	return IsHookMarkGroupNotReady(lifecycle.PreDelete) || IsHookMarkGroupNotReady(lifecycle.InPlaceUpdate)
}

func SetRoleInstanceLifecycle(state constants.RoleInstanceLifecycleStateType) func(instance *workloadsv1alpha2.RoleInstance) {
	return func(instance *workloadsv1alpha2.RoleInstance) {
		if instance.Labels == nil {
			instance.Labels = make(map[string]string)
		}
		if instance.Annotations == nil {
			instance.Annotations = make(map[string]string)
		}
		instance.Labels[constants.RoleInstanceSetLifecycleStateKey] = string(state)
		instance.Annotations[constants.RoleInstanceSetLifecycleTimestampKey] = time.Now().Format(time.RFC3339)
	}
}

func (c *realControl) executeRoleInstanceNotReadyPolicy(instance *workloadsv1alpha2.RoleInstance, state constants.RoleInstanceLifecycleStateType) (err error) {
	switch state {
	case constants.RoleInstanceLifecycleStatePreparingDelete:
		err = c.groupReadinessControl.AddNotReadyKey(instance, getReadinessMessage(preparingDeleteHookKey))
	case constants.RoleInstanceLifecycleStatePreparingUpdate:
		err = c.groupReadinessControl.AddNotReadyKey(instance, getReadinessMessage(preparingUpdateHookKey))
	case constants.RoleInstanceLifecycleStateNormal:
		err = c.groupReadinessControl.RemoveNotReadyKey(instance, getReadinessMessage(preparingUpdateHookKey))
	}

	if err != nil {
		klog.Errorf("Failed to set role instance(%v) Ready/NotReady at %s lifecycle state, error: %v", klog.KObj(instance), state, err)
	}
	return
}

func (c *realControl) UpdateRoleInstanceLifecycle(instance *workloadsv1alpha2.RoleInstance, state constants.RoleInstanceLifecycleStateType, markPodNotReady bool) (updated bool, gotPod *workloadsv1alpha2.RoleInstance, err error) {
	if markPodNotReady {
		if err = c.executeRoleInstanceNotReadyPolicy(instance, state); err != nil {
			return false, nil, err
		}
	}

	if GetRoleInstanceLifecycleState(instance) == state {
		return false, instance, nil
	}

	instance = instance.DeepCopy()
	if adp, ok := c.adp.(clientdapter.AdapterWithPatch); ok {
		body := fmt.Sprintf(
			`{"metadata":{"labels":{"%s":"%s"},"annotations":{"%s":"%s"}}}`,
			constants.RoleInstanceSetLifecycleStateKey,
			string(state),
			constants.RoleInstanceSetLifecycleTimestampKey,
			time.Now().Format(time.RFC3339),
		)
		gotPod, err = adp.PatchRoleInstance(instance, client.RawPatch(types.MergePatchType, []byte(body)))
	} else {
		SetRoleInstanceLifecycle(state)(instance)
		gotPod, err = c.adp.UpdateRoleInstance(instance)
	}

	return true, gotPod, err
}

func (c *realControl) UpdateRoleInstanceLifecycleWithHandler(instance *workloadsv1alpha2.RoleInstance, state constants.RoleInstanceLifecycleStateType, inPlaceUpdateHandler *workloadsv1alpha2.RoleInstanceSetLifecycleHook) (updated bool, gotPod *workloadsv1alpha2.RoleInstance, err error) {
	if inPlaceUpdateHandler == nil || instance == nil {
		return false, instance, nil
	}

	if inPlaceUpdateHandler.MarkNotReady {
		if err = c.executeRoleInstanceNotReadyPolicy(instance, state); err != nil {
			return false, nil, err
		}
	}

	if GetRoleInstanceLifecycleState(instance) == state {
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
			constants.RoleInstanceSetLifecycleStateKey,
			string(state),
			labelsHandler,
			constants.RoleInstanceSetLifecycleTimestampKey,
			time.Now().Format(time.RFC3339),
			finalizersHandler,
		)
		gotPod, err = adp.PatchRoleInstance(instance, client.RawPatch(types.MergePatchType, []byte(body)))
	} else {
		if instance.Labels == nil {
			instance.Labels = make(map[string]string)
		}
		for k, v := range inPlaceUpdateHandler.LabelsHandler {
			instance.Labels[k] = v
		}
		instance.Finalizers = append(instance.Finalizers, inPlaceUpdateHandler.FinalizersHandler...)

		SetRoleInstanceLifecycle(state)(instance)
		gotPod, err = c.adp.UpdateRoleInstance(instance)
	}

	return true, gotPod, err
}

func IsRoleInstanceHooked(hook *workloadsv1alpha2.RoleInstanceSetLifecycleHook, instance *workloadsv1alpha2.RoleInstance) bool {
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

func IsRoleInstanceAllHooked(hook *workloadsv1alpha2.RoleInstanceSetLifecycleHook, instance *workloadsv1alpha2.RoleInstance) bool {
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
