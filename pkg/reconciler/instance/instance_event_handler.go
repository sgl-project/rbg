package instance

import (
	"context"
	"reflect"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/instance/utils"
)

func NewPodEventHandler() handler.TypedEventHandler[client.Object, reconcile.Request] {
	return &podEventHandler{}
}

type podEventHandler struct{}

func (e *podEventHandler) Create(ctx context.Context, evt event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod := evt.Object.(*v1.Pod)
	if pod.DeletionTimestamp != nil {
		return
	}
	if controllerRef := metav1.GetControllerOf(pod); controllerRef != nil {
		req := resolveControllerRef(pod.Namespace, controllerRef)
		if req == nil {
			return
		}
		klog.V(4).Infof("Pod created, pod: %s/%s, owner: %v", pod.Namespace, pod.Name, req)
		q.Add(*req)
	}
}

func (e *podEventHandler) Update(ctx context.Context, evt event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	oldPod := evt.ObjectOld.(*v1.Pod)
	curPod := evt.ObjectNew.(*v1.Pod)
	labelChanged := !reflect.DeepEqual(curPod.Labels, oldPod.Labels)
	if curPod.DeletionTimestamp != nil {
		e.Delete(ctx, event.DeleteEvent{Object: evt.ObjectNew}, q)
		if labelChanged {
			e.Delete(ctx, event.DeleteEvent{Object: evt.ObjectOld}, q)
		}
		return
	}
	curControllerRef := metav1.GetControllerOf(curPod)
	oldControllerRef := metav1.GetControllerOf(oldPod)
	controllerRefChanged := !reflect.DeepEqual(curControllerRef, oldControllerRef)
	if controllerRefChanged && oldControllerRef != nil {
		if req := resolveControllerRef(oldPod.Namespace, oldControllerRef); req != nil {
			q.Add(*req)
		}
	}
	if curControllerRef != nil {
		req := resolveControllerRef(curPod.Namespace, curControllerRef)
		if req == nil {
			return
		}
		klog.V(4).Infof("Pod updated, pod: %s/%s, owner: %v", curPod.Namespace, curPod.Name, req)
		q.Add(*req)
	}
}

func (e *podEventHandler) Delete(_ context.Context, evt event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	pod, ok := evt.Object.(*v1.Pod)
	if !ok {
		klog.ErrorS(nil, "Skipped pod deletion event", "deleteStateUnknown", evt.DeleteStateUnknown, "obj", evt.Object)
		return
	}
	instanceutil.ResourceVersionExpectations.Delete(pod)
	controllerRef := metav1.GetControllerOf(pod)
	if controllerRef == nil {
		return
	}
	req := resolveControllerRef(pod.Namespace, controllerRef)
	if req == nil {
		return
	}
	klog.V(4).Infof("Pod deleted, pod: %s/%s, owner: %v", pod.Namespace, pod.Name, req)
	q.Add(*req)
}

func (e *podEventHandler) Generic(ctx context.Context, evt event.TypedGenericEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

var _ handler.TypedEventHandler[client.Object, reconcile.Request] = &podEventHandler{}

func resolveControllerRef(namespace string, controllerRef *metav1.OwnerReference) *reconcile.Request {
	refGV, err := schema.ParseGroupVersion(controllerRef.APIVersion)
	if err != nil {
		klog.ErrorS(err, "Could not parse APIVersion in OwnerReference", "ownerRef", controllerRef)
		return nil
	}
	if controllerRef.Kind == instanceutil.ControllerKind.Kind && refGV.Group == instanceutil.ControllerKind.Group {
		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: namespace,
				Name:      controllerRef.Name,
			},
		}
		return &req
	}
	return nil
}
