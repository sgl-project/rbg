package statelessmode

import (
	"context"
	"reflect"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/pkg/utils/expectations"

	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset/statelessmode/core"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset/statelessmode/utils"
)

type instanceEventHandler struct {
	client.Reader
}

func NewInstanceEventHandler(c client.Client) handler.EventHandler {
	return &instanceEventHandler{Reader: c}
}

var _ handler.EventHandler = &instanceEventHandler{}

func (e *instanceEventHandler) Create(ctx context.Context, evt event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	instance := evt.Object.(*v1alpha1.Instance)
	if instance.DeletionTimestamp != nil {
		// on a restart of the controller manager, it's possible a new instance shows up in a state that
		// is already pending deletion. Prevent the instance from being a creation observation.
		e.Delete(ctx, event.DeleteEvent{Object: evt.Object}, q)
		return
	}

	// If it has a ControllerRef, that's all that matters.
	if controllerRef := metav1.GetControllerOf(instance); controllerRef != nil {
		req := resolveControllerRef(instance.Namespace, controllerRef)
		if req == nil {
			return
		}
		klog.V(4).Infof("Instance %s/%s created, owner: %s", instance.Namespace, instance.Name, req.Name)
		utils.ScaleExpectations.ObserveScale(req.String(), expectations.Create, instance.Name)
		q.Add(*req)
		return
	}

	// Otherwise, it's an orphan. Get a list of all matching InstanceSets and sync
	// them to see if anyone wants to adopt it.
	// DO NOT observe creation because no controller should be waiting for an
	// orphan.
	gsList := e.getRelatedInstanceSets(instance)
	if len(gsList) == 0 {
		return
	}
	klog.V(4).Infof("Orphan Instance %s/%s created, matched owner: %s", instance.Namespace, instance.Name, e.joinInstanceSetNames(gsList))
	for _, gs := range gsList {
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
			Name:      gs.GetName(),
			Namespace: gs.GetNamespace(),
		}})
	}
}

func (e *instanceEventHandler) Update(ctx context.Context, evt event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldInstance := evt.ObjectOld.(*v1alpha1.Instance)
	curInstance := evt.ObjectNew.(*v1alpha1.Instance)
	if curInstance.ResourceVersion == oldInstance.ResourceVersion {
		// Periodic resync will send update events for all known instances.
		// Two different versions of the same instances will always have different RVs.
		return
	}

	labelChanged := !reflect.DeepEqual(curInstance.Labels, oldInstance.Labels)
	if curInstance.DeletionTimestamp != nil {
		// when a instances is deleted gracefully it's deletion timestamp is first modified to reflect a grace period,
		// and after such time has passed, the kubelet actually deletes it from the store. We receive an update
		// for modification of the deletion timestamp and expect an rs to create more replicas asap, not wait
		// until the controller actually deletes the instances. This is different from the Phase of a instances changing, because
		// an rs never initiates a phase change, and so is never asleep waiting for the same.
		e.Delete(ctx, event.DeleteEvent{Object: evt.ObjectNew}, q)
		if labelChanged {
			// we don't need to check the oldInstance.DeletionTimestamp because DeletionTimestamp cannot be unset.
			e.Delete(ctx, event.DeleteEvent{Object: evt.ObjectOld}, q)
		}
		return
	}

	curControllerRef := metav1.GetControllerOf(curInstance)
	oldControllerRef := metav1.GetControllerOf(oldInstance)
	controllerRefChanged := !reflect.DeepEqual(curControllerRef, oldControllerRef)
	if controllerRefChanged && oldControllerRef != nil {
		// The ControllerRef was changed. Sync the old controller, if any.
		if req := resolveControllerRef(oldInstance.Namespace, oldControllerRef); req != nil {
			q.Add(*req)
		}
	}

	// If it has a ControllerRef, that's all that matters.
	if curControllerRef != nil {
		req := resolveControllerRef(curInstance.Namespace, curControllerRef)
		if req == nil {
			return
		}
		klog.V(4).Infof("Instance %s/%s updated, owner: %s", curInstance.Namespace, curInstance.Name, req.Name)
		q.Add(*req)
		return
	}

	// Otherwise, it's an orphan. If anything changed, sync matching controllers
	// to see if anyone wants to adopt it now.
	if labelChanged || controllerRefChanged {
		gsList := e.getRelatedInstanceSets(curInstance)
		if len(gsList) == 0 {
			return
		}
		klog.V(4).Infof("Orphan Instance %s/%s updated, matched owner: %s",
			curInstance.Namespace, curInstance.Name, e.joinInstanceSetNames(gsList))
		for _, gs := range gsList {
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
				Name:      gs.GetName(),
				Namespace: gs.GetNamespace(),
			}})
		}
	}
}

func (e *instanceEventHandler) Delete(ctx context.Context, evt event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	instance, ok := evt.Object.(*v1alpha1.Instance)
	if !ok {
		klog.Errorf("DeleteEvent parse instance failed, DeleteStateUnknown: %#v, obj: %#v", evt.DeleteStateUnknown, evt.Object)
		return
	}
	utils.ResourceVersionExpectations.Delete(instance)

	controllerRef := metav1.GetControllerOf(instance)
	if controllerRef == nil {
		// No controller should care about orphans being deleted.
		return
	}
	req := resolveControllerRef(instance.Namespace, controllerRef)
	if req == nil {
		return
	}

	klog.V(4).Infof("Instance %s/%s deleted, owner: %s", instance.Namespace, instance.Name, req.Name)
	utils.ScaleExpectations.ObserveScale(req.String(), expectations.Delete, instance.Name)
	q.Add(*req)
}

func (e *instanceEventHandler) Generic(ctx context.Context, evt event.TypedGenericEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {

}

func resolveControllerRef(namespace string, controllerRef *metav1.OwnerReference) *reconcile.Request {
	// Parse the Group out of the OwnerReference to compare it to what was parsed out of the requested OwnerType
	refGV, err := schema.ParseGroupVersion(controllerRef.APIVersion)
	if err != nil {
		klog.Errorf("Could not parse OwnerReference %v APIVersion: %v", controllerRef, err)
		return nil
	}

	// Compare the OwnerReference Instance and Kind against the OwnerType Group and Kind specified by the user.
	// If the two match, create a Request for the objected referred to by
	// the OwnerReference.  Use the Name from the OwnerReference and the Namespace from the
	// object in the event.
	if controllerRef.Kind == utils.ControllerKind.Kind && refGV.Group == utils.ControllerKind.Group {
		// Match found - add a Request for the object referred to in the OwnerReference
		req := reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: namespace,
			Name:      controllerRef.Name,
		}}
		return &req
	}
	return nil
}

func (e *instanceEventHandler) getRelatedInstanceSets(instance *v1alpha1.Instance) []v1alpha1.InstanceSet {
	setList := v1alpha1.InstanceSetList{}
	if err := e.List(context.TODO(), &setList, client.InNamespace(instance.Namespace)); err != nil {
		return nil
	}

	setMatched := make([]v1alpha1.InstanceSet, 0, 1)
	for _, gs := range setList.Items {
		coreControl := core.New(&gs)
		if !coreControl.Selector().Matches(labels.Set(instance.Labels)) {
			continue
		}

		setMatched = append(setMatched, gs)
	}

	if len(setMatched) > 1 {
		// ControllerRef will ensure we don't do anything crazy, but more than one
		// item in this list nevertheless constitutes user error.
		klog.Warningf("Error! More than one InstanceSet is selecting Instance %s/%s : %s",
			instance.Namespace, instance.Name, e.joinInstanceSetNames(setMatched))
	}
	return setMatched
}

func (e *instanceEventHandler) joinInstanceSetNames(setList []v1alpha1.InstanceSet) string {
	names := make([]string, 0, len(setList))
	for _, set := range setList {
		names = append(names, set.Name)
	}
	return strings.Join(names, ",")
}
