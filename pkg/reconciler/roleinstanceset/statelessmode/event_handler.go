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
	"sigs.k8s.io/rbgs/api/workloads/constants"
	"sigs.k8s.io/rbgs/pkg/utils/expectations"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statelessmode/core"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statelessmode/utils"
)

type roleInstanceEventHandler struct {
	client.Reader
}

func NewRoleInstanceEventHandler(c client.Client) handler.EventHandler {
	return &roleInstanceEventHandler{Reader: c}
}

var _ handler.EventHandler = &roleInstanceEventHandler{}

func (e *roleInstanceEventHandler) Create(ctx context.Context, evt event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	roleInstance := evt.Object.(*workloadsv1alpha2.RoleInstance)
	if roleInstance.DeletionTimestamp != nil {
		// on a restart of the controller manager, it's possible a new roleInstance shows up in a state that
		// is already pending deletion. Prevent the roleInstance from being a creation observation.
		e.Delete(ctx, event.DeleteEvent{Object: evt.Object}, q)
		return
	}

	// If it has a ControllerRef, that's all that matters.
	if controllerRef := metav1.GetControllerOf(roleInstance); controllerRef != nil {
		req := resolveControllerRef(roleInstance.Namespace, controllerRef)
		if req == nil {
			return
		}
		// Only update ScaleExpectations for Stateless pattern RoleInstanceSet
		if !e.isStatelessPattern(roleInstance.Namespace, controllerRef.Name) {
			return
		}
		klog.V(4).Infof("RoleInstance %s/%s created, owner: %s", roleInstance.Namespace, roleInstance.Name, req.Name)
		utils.ScaleExpectations.ObserveScale(req.String(), expectations.Create, roleInstance.Name)
		q.Add(*req)
		return
	}

	// Otherwise, it's an orphan. Get a list of all matching RoleInstanceSets and sync
	// them to see if anyone wants to adopt it.
	// DO NOT observe creation because no controller should be waiting for an
	// orphan.
	gsList := e.getRelatedRoleInstanceSets(roleInstance)
	if len(gsList) == 0 {
		return
	}
	klog.V(4).Infof("Orphan RoleInstance %s/%s created, matched owner: %s", roleInstance.Namespace, roleInstance.Name, e.joinRoleInstanceSetNames(gsList))
	for _, gs := range gsList {
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
			Name:      gs.GetName(),
			Namespace: gs.GetNamespace(),
		}})
	}
}

func (e *roleInstanceEventHandler) Update(ctx context.Context, evt event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldRoleInstance := evt.ObjectOld.(*workloadsv1alpha2.RoleInstance)
	curRoleInstance := evt.ObjectNew.(*workloadsv1alpha2.RoleInstance)
	if curRoleInstance.ResourceVersion == oldRoleInstance.ResourceVersion {
		// Periodic resync will send update events for all known roleInstances.
		// Two different versions of the same roleInstances will always have different RVs.
		return
	}

	labelChanged := !reflect.DeepEqual(curRoleInstance.Labels, oldRoleInstance.Labels)
	if curRoleInstance.DeletionTimestamp != nil {
		// when a roleInstance is deleted gracefully it's deletion timestamp is first modified to reflect a grace period,
		// and after such time has passed, the kubelet actually deletes it from the store. We receive an update
		// for modification of the deletion timestamp and expect an rs to create more replicas asap, not wait
		// until the controller actually deletes the roleInstance. This is different from the Phase of a roleInstance changing, because
		// an rs never initiates a phase change, and so is never asleep waiting for the same.
		e.Delete(ctx, event.DeleteEvent{Object: evt.ObjectNew}, q)
		if labelChanged {
			// we don't need to check the oldRoleInstance.DeletionTimestamp because DeletionTimestamp cannot be unset.
			e.Delete(ctx, event.DeleteEvent{Object: evt.ObjectOld}, q)
		}
		return
	}

	curControllerRef := metav1.GetControllerOf(curRoleInstance)
	oldControllerRef := metav1.GetControllerOf(oldRoleInstance)
	controllerRefChanged := !reflect.DeepEqual(curControllerRef, oldControllerRef)
	if controllerRefChanged && oldControllerRef != nil {
		// The ControllerRef was changed. Sync the old controller, if any.
		if req := resolveControllerRef(oldRoleInstance.Namespace, oldControllerRef); req != nil {
			q.Add(*req)
		}
	}

	// If it has a ControllerRef, that's all that matters.
	if curControllerRef != nil {
		req := resolveControllerRef(curRoleInstance.Namespace, curControllerRef)
		if req == nil {
			return
		}
		klog.V(4).Infof("RoleInstance %s/%s updated, owner: %s", curRoleInstance.Namespace, curRoleInstance.Name, req.Name)
		q.Add(*req)
		return
	}

	// Otherwise, it's an orphan. If anything changed, sync matching controllers
	// to see if anyone wants to adopt it now.
	if labelChanged || controllerRefChanged {
		gsList := e.getRelatedRoleInstanceSets(curRoleInstance)
		if len(gsList) == 0 {
			return
		}
		klog.V(4).Infof("Orphan RoleInstance %s/%s updated, matched owner: %s",
			curRoleInstance.Namespace, curRoleInstance.Name, e.joinRoleInstanceSetNames(gsList))
		for _, gs := range gsList {
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
				Name:      gs.GetName(),
				Namespace: gs.GetNamespace(),
			}})
		}
	}
}

func (e *roleInstanceEventHandler) Delete(ctx context.Context, evt event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	roleInstance, ok := evt.Object.(*workloadsv1alpha2.RoleInstance)
	if !ok {
		klog.Errorf("DeleteEvent parse roleInstance failed, DeleteStateUnknown: %#v, obj: %#v", evt.DeleteStateUnknown, evt.Object)
		return
	}
	utils.ResourceVersionExpectations.Delete(roleInstance)

	controllerRef := metav1.GetControllerOf(roleInstance)
	if controllerRef == nil {
		// No controller should care about orphans being deleted.
		return
	}
	req := resolveControllerRef(roleInstance.Namespace, controllerRef)
	if req == nil {
		return
	}
	// Only update ScaleExpectations for Stateless pattern RoleInstanceSet
	if !e.isStatelessPattern(roleInstance.Namespace, controllerRef.Name) {
		return
	}

	klog.V(4).Infof("RoleInstance %s/%s deleted, owner: %s", roleInstance.Namespace, roleInstance.Name, req.Name)
	utils.ScaleExpectations.ObserveScale(req.String(), expectations.Delete, roleInstance.Name)
	q.Add(*req)
}

func (e *roleInstanceEventHandler) Generic(ctx context.Context, evt event.TypedGenericEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {

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

func (e *roleInstanceEventHandler) getRelatedRoleInstanceSets(roleInstance *workloadsv1alpha2.RoleInstance) []workloadsv1alpha2.RoleInstanceSet {
	setList := workloadsv1alpha2.RoleInstanceSetList{}
	if err := e.List(context.TODO(), &setList, client.InNamespace(roleInstance.Namespace)); err != nil {
		return nil
	}

	setMatched := make([]workloadsv1alpha2.RoleInstanceSet, 0, 1)
	for _, gs := range setList.Items {
		coreControl := core.New(&gs)
		if !coreControl.Selector().Matches(labels.Set(roleInstance.Labels)) {
			continue
		}

		setMatched = append(setMatched, gs)
	}

	if len(setMatched) > 1 {
		// ControllerRef will ensure we don't do anything crazy, but more than one
		// item in this list nevertheless constitutes user error.
		klog.Warningf("Error! More than one RoleInstanceSet is selecting RoleInstance %s/%s : %s",
			roleInstance.Namespace, roleInstance.Name, e.joinRoleInstanceSetNames(setMatched))
	}
	return setMatched
}

func (e *roleInstanceEventHandler) joinRoleInstanceSetNames(setList []workloadsv1alpha2.RoleInstanceSet) string {
	names := make([]string, 0, len(setList))
	for _, set := range setList {
		names = append(names, set.Name)
	}
	return strings.Join(names, ",")
}

// isStatelessPattern checks if the RoleInstanceSet has Stateless pattern
func (e *roleInstanceEventHandler) isStatelessPattern(namespace, name string) bool {
	set := &workloadsv1alpha2.RoleInstanceSet{}
	if err := e.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, set); err != nil {
		return false
	}
	return set.Annotations[constants.RoleInstancePatternKey] == string(constants.StatelessPattern)
}
