/*
Copyright 2026 The RBG Authors.
Copyright 2019 The Kruise Authors.
Copyright 2016 The Kubernetes Authors.

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

package statefulmode

import (
	"context"
	"flag"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	kubecontroller "k8s.io/kubernetes/pkg/controller"
	sigsclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/pkg/reconciler/utils/revisionadapter"
	utilclient "sigs.k8s.io/rbgs/pkg/utils/client"
	"sigs.k8s.io/rbgs/pkg/utils/expectations"
	utilhistory "sigs.k8s.io/rbgs/pkg/utils/history"
	"sigs.k8s.io/rbgs/pkg/utils/requeueduration"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	instancelisters "sigs.k8s.io/rbgs/client-go/listers/workloads/v1alpha1"
	instanceinplace "sigs.k8s.io/rbgs/pkg/inplace/instance/inplaceupdate"
	instancelifecycle "sigs.k8s.io/rbgs/pkg/inplace/instance/lifecycle"
)

func init() {
	flag.IntVar(&concurrentReconciles, "stateful-instanceset-workers", concurrentReconciles, "Max concurrent workers for StatefulInstanceSet controller.")
}

var (
	// controllerKind contains the schema.GroupVersionKind for this controller type.
	controllerKind       = workloadsv1alpha1.SchemeGroupVersion.WithKind("InstanceSet")
	concurrentReconciles = 3

	updateExpectations = expectations.NewUpdateExpectations(revisionadapter.NewDefaultImpl())
	// this is a short cut for any sub-functions to notify the reconcile how long to wait to requeue
	durationStore = requeueduration.DurationStore{}

	// global client
	sigsruntimeClient sigsclient.Client
)

// NewReconciler creates a new reconcile.Reconciler for external usage
func NewReconciler(mgr manager.Manager) reconcile.Reconciler {
	return newReconciler(mgr)
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	cacher := mgr.GetCache()
	instanceSetInformer, err := cacher.GetInformerForKind(context.TODO(), controllerKind)
	if err != nil {
		klog.ErrorS(err, "Failed to get informer for InstanceSet")
		return nil
	}
	instanceInformer, err := cacher.GetInformerForKind(context.TODO(), workloadsv1alpha1.SchemeGroupVersion.WithKind("Instance"))
	if err != nil {
		klog.ErrorS(err, "Failed to get informer for Instance")
		return nil
	}

	instanceSetLister := instancelisters.NewInstanceSetLister(instanceSetInformer.(toolscache.SharedIndexInformer).GetIndexer())
	instanceLister := instancelisters.NewInstanceLister(instanceInformer.(toolscache.SharedIndexInformer).GetIndexer())

	// Use manager's event recorder
	recorder := mgr.GetEventRecorderFor("stateful-instanceset-controller")

	// new a client
	sigsruntimeClient = utilclient.NewClientFromManager(mgr, "stateful-instanceset-controller")

	return &ReconcileStatefulInstanceSet{
		control: NewDefaultStatefulInstanceSetControl(
			NewStatefulInstanceControl(
				sigsruntimeClient,
				instanceLister,
				recorder),
			instanceinplace.New(utilclient.NewClientFromManager(mgr, "stateful-instanceset-controller"), revisionadapter.NewDefaultImpl()),
			instancelifecycle.New(utilclient.NewClientFromManager(mgr, "stateful-instanceset-controller")),
			NewRealStatefulInstanceSetStatusUpdater(sigsruntimeClient, instanceSetLister),
			utilhistory.NewHistory(sigsruntimeClient),
			recorder,
		),
		instanceControl: &realInstanceControl{sigsruntimeClient, recorder},
		instanceLister:  instanceLister,
		setLister:       instanceSetLister,
	}
}

var _ reconcile.Reconciler = &ReconcileStatefulInstanceSet{}

// ReconcileStatefulInstanceSet reconciles a InstanceSet object managing Instance resources
type ReconcileStatefulInstanceSet struct {
	// control returns an interface capable of syncing a stateful set.
	// Abstracted out for testing.
	control StatefulInstanceSetControlInterface
	// instanceControl is used for managing instances.
	instanceControl InstanceControlInterface
	// instanceLister is able to list/get instances from a shared informer's store
	instanceLister instancelisters.InstanceLister
	// setLister is able to list/get instance sets from a shared informer's store
	setLister instancelisters.InstanceSetLister
}

// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=controllerrevisions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instancesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instancesets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instancesets/finalizers,verbs=update

// Reconcile reads that state of the cluster for a InstanceSet object and makes changes based on the state read
// and what is in the InstanceSet.Spec
func (ssc *ReconcileStatefulInstanceSet) Reconcile(ctx context.Context, request reconcile.Request) (res reconcile.Result, retErr error) {
	key := request.NamespacedName.String()
	namespace := request.Namespace
	name := request.Name

	startTime := time.Now()
	defer func() {
		if retErr == nil {
			if res.RequeueAfter > 0 {
				klog.InfoS("Finished syncing InstanceSet", "instanceSet", request, "elapsedTime", time.Since(startTime), "result", res)
			} else {
				klog.InfoS("Finished syncing InstanceSet", "instanceSet", request, "elapsedTime", time.Since(startTime))
			}
		} else {
			klog.ErrorS(retErr, "Finished syncing InstanceSet error", "instanceSet", request, "elapsedTime", time.Since(startTime))
		}
	}()

	set, err := ssc.setLister.InstanceSets(namespace).Get(name)
	if errors.IsNotFound(err) {
		klog.InfoS("InstanceSet deleted", "instanceSet", key)
		updateExpectations.DeleteExpectations(key)
		return reconcile.Result{}, nil
	}
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("unable to retrieve InstanceSet %v from store: %v", key, err))
		return reconcile.Result{}, err
	}

	selector, err := metav1.LabelSelectorAsSelector(set.Spec.Selector)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("error converting InstanceSet %v selector: %v", key, err))
		// This is a non-transient error, so don't retry.
		return reconcile.Result{}, nil
	}

	if err := ssc.adoptOrphanRevisions(ctx, set); err != nil {
		return reconcile.Result{}, err
	}

	instances, err := ssc.getInstancesForInstanceSet(ctx, set, selector)
	if err != nil {
		return reconcile.Result{}, err
	}

	err = ssc.syncStatefulInstanceSet(ctx, set, instances)
	return reconcile.Result{RequeueAfter: durationStore.Pop(getInstanceSetKey(set))}, err
}

// adoptOrphanRevisions adopts any orphaned ControllerRevisions matched by set's Selector.
func (ssc *ReconcileStatefulInstanceSet) adoptOrphanRevisions(ctx context.Context, set *workloadsv1alpha1.InstanceSet) error {
	revisions, err := ssc.control.ListRevisions(set)
	if err != nil {
		return err
	}
	orphanRevisions := make([]*appsv1.ControllerRevision, 0)
	for i := range revisions {
		if metav1.GetControllerOf(revisions[i]) == nil {
			orphanRevisions = append(orphanRevisions, revisions[i])
		}
	}
	if len(orphanRevisions) > 0 {
		fresh := &workloadsv1alpha1.InstanceSet{}
		err := sigsruntimeClient.Get(ctx, sigsclient.ObjectKey{Namespace: set.Namespace, Name: set.Name}, fresh)
		if err != nil {
			return err
		}
		if fresh.UID != set.UID {
			return fmt.Errorf("original InstanceSet %v/%v is gone: got uid %v, wanted %v", set.Namespace, set.Name, fresh.UID, set.UID)
		}
		return ssc.control.AdoptOrphanRevisions(set, orphanRevisions)
	}
	return nil
}

// getInstancesForInstanceSet returns the Instances that a given InstanceSet should manage.
// It also reconciles ControllerRef by adopting/orphaning.
//
// NOTE: Returned Instances are pointers to objects from the cache.
//
//	If you need to modify one, you need to copy it first.
func (ssc *ReconcileStatefulInstanceSet) getInstancesForInstanceSet(ctx context.Context, set *workloadsv1alpha1.InstanceSet, selector labels.Selector) ([]*workloadsv1alpha1.Instance, error) {
	// List all instances to include the instances that don't match the selector anymore but
	// has a ControllerRef pointing to this InstanceSet.
	instances, err := ssc.instanceLister.Instances(set.Namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}

	filter := func(instance *workloadsv1alpha1.Instance) bool {
		// Only claim if it matches our InstanceSet name. Otherwise release/ignore.
		return isMemberOf(set, instance)
	}

	// If any adoptions are attempted, we should first recheck for deletion with
	// an uncached quorum read sometime after listing Instances (see #42639).
	canAdoptFunc := kubecontroller.RecheckDeletionTimestamp(func(ctx context.Context) (metav1.Object, error) {
		fresh := &workloadsv1alpha1.InstanceSet{}
		err := sigsruntimeClient.Get(ctx, sigsclient.ObjectKey{Namespace: set.Namespace, Name: set.Name}, fresh)
		if err != nil {
			return nil, err
		}
		if fresh.UID != set.UID {
			return nil, fmt.Errorf("original InstanceSet %v/%v is gone: got uid %v, wanted %v", set.Namespace, set.Name, fresh.UID, set.UID)
		}
		return fresh, nil
	})

	cm := NewInstanceControllerRefManager(ssc.instanceControl, set, selector, controllerKind, canAdoptFunc)
	return cm.ClaimInstances(ctx, instances, filter)
}

// syncStatefulInstanceSet syncs a tuple of (instanceset, []*Instance).
func (ssc *ReconcileStatefulInstanceSet) syncStatefulInstanceSet(ctx context.Context, set *workloadsv1alpha1.InstanceSet, instances []*workloadsv1alpha1.Instance) error {
	klog.V(4).InfoS("Syncing InstanceSet with instances", "instanceSet", klog.KObj(set), "instanceCount", len(instances))
	// TODO: investigate where we mutate the set during the update as it is not obvious.
	if err := ssc.control.UpdateStatefulInstanceSet(ctx, set.DeepCopy(), instances); err != nil {
		return err
	}
	klog.V(4).InfoS("Successfully synced InstanceSet", "instanceSet", klog.KObj(set))
	return nil
}
