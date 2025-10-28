package core

import (
	"encoding/json"
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	kubecontroller "k8s.io/kubernetes/pkg/controller"

	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	inplaceupdatepod "sigs.k8s.io/rbgs/pkg/inplace/pod"
	podinplaceupdate "sigs.k8s.io/rbgs/pkg/inplace/pod/inplaceupdate"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/instance/utils"
)

const (
	directiveMarker                = "$patch"
	replaceDirective               = "replace"
	setElementOrderDirectivePrefix = "$setElementOrder"
)

var (
	componentSpecIgnoreRevisionKeys = sets.NewString("size")
	componentSpecRetainRevisionKeys = sets.NewString("serviceName")
)

type Control interface {
	SetRevisionTemplate(revisionSpec map[string]interface{}, componentTemplates []interface{})
	ApplyRevisionPatch(patched []byte) (*v1alpha1.Instance, error)

	GetComponentsTopology(pods []*v1.Pod) (*ComponentsTopology, error)
	NewUpdatePods(updateVersion string, componentName string, availableIDs []int32) ([]*v1.Pod, error)

	GetUpdateOptions() *podinplaceupdate.UpdateOptions
	IsPodUpdateReady(pod *v1.Pod, minReadySeconds int32) bool
}

func New(instance *v1alpha1.Instance) Control {
	return &commonControl{
		Instance: instance,
	}
}

type commonControl struct {
	*v1alpha1.Instance
}

func (c *commonControl) SetRevisionTemplate(revisionSpec map[string]interface{}, componentTemplates []interface{}) {
	elementOrders := make([]map[string]interface{}, len(componentTemplates))
	for i := range componentTemplates {
		componentTemplate := componentTemplates[i].(map[string]interface{})
		elementOrders[i] = map[string]interface{}{
			"name": componentTemplate["name"],
		}
		for retainKey := range componentSpecRetainRevisionKeys {
			if _, ok := componentTemplate[retainKey]; !ok {
				componentTemplate[retainKey] = nil
			}
		}
		for ignoreKey := range componentSpecIgnoreRevisionKeys {
			delete(componentTemplate, ignoreKey)
		}
		template := componentTemplate["template"].(map[string]interface{})
		template[directiveMarker] = replaceDirective
	}
	revisionSpec[setElementOrderDirectivePrefix+"/"+"components"] = elementOrders
	revisionSpec["components"] = componentTemplates
}

func (c *commonControl) ApplyRevisionPatch(patched []byte) (*v1alpha1.Instance, error) {
	restoreInstance := new(v1alpha1.Instance)
	if err := json.Unmarshal(patched, restoreInstance); err != nil {
		return nil, err
	}
	return restoreInstance, nil
}

func (c *commonControl) GetComponentsTopology(pods []*v1.Pod) (*ComponentsTopology, error) {
	ct := &ComponentsTopology{
		Components: sets.New[string](),
	}
	componentGroup := instanceutil.GroupPodsByComponentName(pods)
	for i := range c.Instance.Spec.Components {
		component := c.Instance.Spec.Components[i]
		if component.Size == nil {
			return nil, fmt.Errorf("component %s'size is empty", component.Name)
		}
		ct.Components.Insert(component.Name)
		ct.Topologies = append(ct.Topologies, newComponentPodGroup(&component, componentGroup[component.Name]))
	}
	if ct.Components.Len() != len(ct.Topologies) {
		return nil, fmt.Errorf("the name of component must be unique")
	}
	return ct, nil
}

func (c *commonControl) NewUpdatePods(updateVersion string, componentName string, availableIDs []int32) ([]*v1.Pod, error) {
	var component *v1alpha1.InstanceComponent
	for i := range c.Instance.Spec.Components {
		if c.Spec.Components[i].Name == componentName {
			component = &c.Spec.Components[i]
			break
		}
	}
	instance := c.Instance
	newPods := make([]*v1.Pod, 0, len(availableIDs))
	for _, id := range availableIDs {
		pod, _ := kubecontroller.GetPodFromTemplate(&component.Template, instance,
			metav1.NewControllerRef(instance, instanceutil.ControllerKind))

		// 1. init pod's object key
		pod.Name = instanceutil.FormatComponentPodName(instance.Name, componentName, id)
		pod.Namespace = instance.Namespace

		// 2. init pod revision hash
		instanceutil.WriteRevisionHash(pod, updateVersion)

		// 3. init pod labels
		for key, value := range instanceutil.InitComponentPodLabels(instance.Name, componentName, id) {
			pod.Labels[key] = value
		}

		// 4. init pod identity for service discovery
		c.setComponentPodIdentity(component, pod)

		// 5. init pod readiness gates
		inplaceupdatepod.InjectInPlaceReadinessGate(pod)
		inplaceupdatepod.InjectInstancePodReadinessGate(pod)
		newPods = append(newPods, pod)
	}
	return newPods, nil
}

func (c *commonControl) setComponentPodIdentity(component *v1alpha1.InstanceComponent, pod *v1.Pod) {
	if len(component.ServiceName) == 0 {
		return
	}
	pod.Spec.Hostname = pod.Name
	pod.Spec.Subdomain = component.ServiceName
}

func (c *commonControl) GetUpdateOptions() *podinplaceupdate.UpdateOptions {
	opts := &podinplaceupdate.UpdateOptions{}
	podinplaceupdate.SetOptionsDefaults(opts)
	return opts
}

func (c *commonControl) IsPodUpdateReady(pod *v1.Pod, minReadySeconds int32) bool {
	if !instanceutil.IsRunningAndAvailable(pod, minReadySeconds) {
		return false
	}
	condition := inplaceupdatepod.GetInPlaceCondition(pod)
	if condition != nil && condition.Status != v1.ConditionTrue {
		return false
	}
	return true
}

type ComponentsTopology struct {
	Components sets.Set[string]
	Topologies []ComponentPodGroup
}

type ComponentPodGroup struct {
	*v1alpha1.InstanceComponent

	DesiredReplicas int32
	ExistIDs        sets.Set[int32]
	ToDeleteIDs     sets.Set[int32]
	ToScaleIDs      sets.Set[int32]

	ToDeletePod []*v1.Pod
	Pods        []*v1.Pod
}

func newComponentPodGroup(component *v1alpha1.InstanceComponent, pods []*v1.Pod) ComponentPodGroup {
	desiredReplicas := *component.Size
	var existIDs, toDeleteIDs, toScaleIDs = sets.New[int32](), sets.New[int32](), sets.New[int32]()
	for _, pod := range pods {
		existIDs.Insert(instanceutil.GetPodComponentID(pod))
	}
	for id := range existIDs {
		if id >= desiredReplicas {
			toDeleteIDs.Insert(id)
		}
	}
	var toDeletePods []*v1.Pod
	if toDeleteIDs.Len() > 0 {
		for _, pod := range pods {
			if toDeleteIDs.Has(instanceutil.GetPodComponentID(pod)) {
				toDeletePods = append(toDeletePods, pod)
			}
		}
	}
	for i := 0; i < int(desiredReplicas); i++ {
		if existIDs.Has(int32(i)) {
			continue
		}
		toScaleIDs.Insert(int32(i))
	}
	podTopology := ComponentPodGroup{
		InstanceComponent: component,
		DesiredReplicas:   desiredReplicas,
		Pods:              pods,
		ExistIDs:          existIDs,
		ToDeleteIDs:       toDeleteIDs,
		ToScaleIDs:        toScaleIDs,
		ToDeletePod:       toDeletePods,
	}
	return podTopology
}
