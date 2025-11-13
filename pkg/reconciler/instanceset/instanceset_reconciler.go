package instanceset

import (
	"context"
	"time"

	"github.com/openkruise/kruise/pkg/util/expectations"
	historyutil "github.com/openkruise/kruise/pkg/util/history"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/history"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset/core"
	revisioncontrol "sigs.k8s.io/rbgs/pkg/reconciler/instanceset/revision"
	synccontrol "sigs.k8s.io/rbgs/pkg/reconciler/instanceset/sync"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset/utils"
)

func NewReconciler(mgr ctrl.Manager) reconcile.Reconciler {
	recorder := mgr.GetEventRecorderFor("instanceset-controller")
	return &ReconcileInstanceSet{
		Client:            mgr.GetClient(),
		scheme:            mgr.GetScheme(),
		recorder:          recorder,
		controllerHistory: historyutil.NewHistory(mgr.GetClient()),
		statusUpdater:     newStatusUpdater(mgr.GetClient()),
		revisionControl:   revisioncontrol.NewRevisionControl(),
		syncControl:       synccontrol.New(mgr.GetClient(), recorder),
	}
}

// ReconcileInstanceSet reconciles a InstanceSet object
type ReconcileInstanceSet struct {
	client.Client
	scheme *runtime.Scheme

	recorder          record.EventRecorder
	controllerHistory history.Interface
	statusUpdater     StatusUpdater
	revisionControl   revisioncontrol.Interface
	syncControl       synccontrol.Interface
}

// Reconcile reads that state of the cluster for a InstanceSet object and makes changes based on the state read
// and what is in the InstanceSet.Spec
func (r *ReconcileInstanceSet) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	klog.Infof("Start syncing InstanceSet %s", request.String())
	return r.doReconcile(request)
}

func (r *ReconcileInstanceSet) doReconcile(request reconcile.Request) (res reconcile.Result, retErr error) {
	startTime := time.Now()
	defer func() {
		if retErr == nil {
			if res.RequeueAfter > 0 {
				klog.Infof("Finished syncing InstanceSet %s, cost %v, result: %v", request, time.Since(startTime), res)
			} else {
				klog.Infof("Finished syncing InstanceSet %s, cost %v", request, time.Since(startTime))
			}
		} else {
			klog.Errorf("Failed syncing InstanceSet %s: %v", request, retErr)
		}
		// clean the duration store
		_ = utils.DurationStore.Pop(request.String())
	}()

	// Fetch the requested InstanceSet
	set := &v1alpha1.InstanceSet{}
	err := r.Get(context.TODO(), request.NamespacedName, set)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			klog.V(3).Infof("InstanceSet %s has been deleted.", request)
			utils.ScaleExpectations.DeleteExpectations(request.String())
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	setControl := core.New(set)
	if setControl.IsInitializing() {
		klog.V(4).Infof("InstanceSet %s skip reconcile for initializing", request)
		return reconcile.Result{}, nil
	}

	// If scaling expectations have not satisfied yet, just skip this reconcile.
	if scaleSatisfied, unsatisfiedDuration, scaleDirtyData := utils.ScaleExpectations.SatisfiedExpectations(request.String()); !scaleSatisfied {
		if unsatisfiedDuration >= expectations.ExpectationTimeout {
			klog.Warningf("Expectation unsatisfied overtime for %v, scaleDirtyData=%v, overtime=%v", request.String(), scaleDirtyData, unsatisfiedDuration)
			return reconcile.Result{}, nil
		}
		klog.V(4).Infof("Not satisfied scale for %v, scaleDirtyData=%v", request.String(), scaleDirtyData)
		return reconcile.Result{RequeueAfter: expectations.ExpectationTimeout - unsatisfiedDuration}, nil
	}

	// list all active Instance belongs to the set
	selector := setControl.Selector()
	filteredInstances, err := r.getOwnedResource(set, selector)
	if err != nil {
		return reconcile.Result{}, err
	}

	// release the Instances ownerRef
	filteredInstances, err = r.claimInstances(set, filteredInstances, selector)
	if err != nil {
		return reconcile.Result{}, err
	}

	// list all revisions and sort them
	revisions, err := r.controllerHistory.ListControllerRevisions(set, selector)
	if err != nil {
		return reconcile.Result{}, err
	}
	history.SortControllerRevisions(revisions)

	// get the current, and update revisions
	currentRevision, updateRevision, collisionCount, err := r.getActiveRevisions(set, revisions)
	if err != nil {
		return reconcile.Result{}, err
	}

	// If resourceVersion expectations have not satisfied yet, just skip this reconcile
	utils.ResourceVersionExpectations.Observe(updateRevision)
	if isSatisfied, unsatisfiedDuration := utils.ResourceVersionExpectations.IsSatisfied(updateRevision); !isSatisfied {
		if unsatisfiedDuration < expectations.ExpectationTimeout {
			klog.V(4).Infof("Not satisfied resourceVersion for %v, wait for updateRevision %v updating", request.String(), updateRevision.Name)
			return reconcile.Result{RequeueAfter: expectations.ExpectationTimeout - unsatisfiedDuration}, nil
		}
		klog.Warningf("Expectation unsatisfied overtime for %v, wait for updateRevision %v updating, timeout=%v", request.String(), updateRevision.Name, unsatisfiedDuration)
		utils.ResourceVersionExpectations.Delete(updateRevision)
	}
	for _, instance := range filteredInstances {
		utils.ResourceVersionExpectations.Observe(instance)
		if isSatisfied, unsatisfiedDuration := utils.ResourceVersionExpectations.IsSatisfied(instance); !isSatisfied {
			if unsatisfiedDuration >= expectations.ExpectationTimeout {
				klog.Warningf("Expectation unsatisfied overtime for %v, wait for InstanceSet %v updating, timeout=%v", request.String(), instance.Name, unsatisfiedDuration)
				return reconcile.Result{}, nil
			}
			klog.V(4).Infof("Not satisfied resourceVersion for %v, wait for InstanceSet %v updating", request.String(), instance.Name)
			return reconcile.Result{RequeueAfter: expectations.ExpectationTimeout - unsatisfiedDuration}, nil
		}
	}

	newStatus := v1alpha1.InstanceSetStatus{
		ObservedGeneration: set.Generation,
		CurrentRevision:    currentRevision.Name,
		UpdateRevision:     updateRevision.Name,
		CollisionCount:     new(int32),
		LabelSelector:      selector.String(),
	}
	*newStatus.CollisionCount = collisionCount

	// scale and update
	syncErr := r.syncInstanceSet(set, &newStatus, currentRevision, updateRevision, revisions, filteredInstances)

	// update new status
	if err = r.statusUpdater.UpdateInstanceSetStatus(set, &newStatus, filteredInstances); err != nil {
		return reconcile.Result{}, err
	}

	if err = r.truncateInstancesToDelete(set, filteredInstances); err != nil {
		klog.Warningf("Failed to truncate instancesToDelete for %s: %v", request, err)
	}

	if err = r.truncateHistory(set, filteredInstances, revisions, currentRevision, updateRevision); err != nil {
		klog.Errorf("Failed to truncate history for %s: %v", request, err)
	}

	if syncErr == nil && set.Spec.MinReadySeconds > 0 && newStatus.AvailableReplicas != newStatus.ReadyReplicas {
		utils.DurationStore.Push(request.String(), time.Second*time.Duration(set.Spec.MinReadySeconds))
	}
	return reconcile.Result{RequeueAfter: utils.DurationStore.Pop(request.String())}, syncErr
}

func (r *ReconcileInstanceSet) syncInstanceSet(
	set *v1alpha1.InstanceSet, newStatus *v1alpha1.InstanceSetStatus,
	currentRevision, updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision,
	filteredInstances []*v1alpha1.Instance,
) error {
	if set.DeletionTimestamp != nil {
		return nil
	}

	// get the current and update revisions of the set.
	currentSet, err := r.revisionControl.ApplyRevision(set, currentRevision)
	if err != nil {
		return err
	}
	updateSet, err := r.revisionControl.ApplyRevision(set, updateRevision)
	if err != nil {
		return err
	}

	var scaling bool
	var scaleErr error
	var updateErr error

	scaling, scaleErr = r.syncControl.Scale(currentSet, updateSet, currentRevision.Name, updateRevision.Name, filteredInstances)
	if scaleErr != nil {
		newStatus.Conditions = append(newStatus.Conditions, v1alpha1.InstanceSetCondition{
			Type:               v1alpha1.InstanceSetConditionFailedScale,
			Status:             v1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Message:            scaleErr.Error(),
		})
		err = scaleErr
	}
	if scaling {
		return scaleErr
	}

	updateErr = r.syncControl.Update(updateSet, currentRevision, updateRevision, revisions, filteredInstances)
	if updateErr != nil {
		newStatus.Conditions = append(newStatus.Conditions, v1alpha1.InstanceSetCondition{
			Type:               v1alpha1.InstanceSetConditionFailedUpdate,
			Status:             v1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Message:            updateErr.Error(),
		})
		if err == nil {
			err = updateErr
		}
	}

	return err
}

func (r *ReconcileInstanceSet) getActiveRevisions(set *v1alpha1.InstanceSet, revisions []*apps.ControllerRevision) (
	*apps.ControllerRevision, *apps.ControllerRevision, int32, error,
) {
	var currentRevision, updateRevision *apps.ControllerRevision
	revisionCount := len(revisions)

	// Use a local copy of set.Status.CollisionCount to avoid modifying set.Status directly.
	// This copy is returned so the value gets carried over to set.Status in UpdateInstanceSetStatus.
	var collisionCount int32
	if set.Status.CollisionCount != nil {
		collisionCount = *set.Status.CollisionCount
	}

	// create a new revision from the current set
	updateRevision, err := r.revisionControl.NewRevision(set, utils.NextRevision(revisions), &collisionCount)
	if err != nil {
		return nil, nil, collisionCount, err
	}

	// find any equivalent revisions
	equalRevisions := history.FindEqualRevisions(revisions, updateRevision)
	equalCount := len(equalRevisions)

	if equalCount > 0 && history.EqualRevision(revisions[revisionCount-1], equalRevisions[equalCount-1]) {
		// if the equivalent revision is immediately prior the update revision has not changed
		updateRevision = revisions[revisionCount-1]
	} else if equalCount > 0 {
		// if the equivalent revision is not immediately prior we will roll back by incrementing the
		// Revision of the equivalent revision
		updateRevision, err = r.controllerHistory.UpdateControllerRevision(equalRevisions[equalCount-1], updateRevision.Revision)
		if err != nil {
			return nil, nil, collisionCount, err
		}
	} else {
		// if there is no equivalent revision we create a new one
		updateRevision, err = r.controllerHistory.CreateControllerRevision(set, updateRevision, &collisionCount)
		if err != nil {
			return nil, nil, collisionCount, err
		}
	}

	// attempt to find the revision that corresponds to the current revision
	for i := range revisions {
		if revisions[i].Name == set.Status.CurrentRevision {
			currentRevision = revisions[i]
			break
		}
	}

	// if the current revision is nil we initialize the history by setting it to the update revision
	if currentRevision == nil {
		currentRevision = updateRevision
	}

	return currentRevision, updateRevision, collisionCount, nil
}

func (r *ReconcileInstanceSet) getOwnedResource(set *v1alpha1.InstanceSet, selector labels.Selector) ([]*v1alpha1.Instance, error) {
	opts := &client.ListOptions{
		Namespace:     set.Namespace,
		LabelSelector: selector,
	}
	return utils.GetActiveInstance(r.Client, set, opts)
}

// truncateInstancesToDelete truncates any non-live instance names in spec.scaleStrategy.instanceToDelete.
func (r *ReconcileInstanceSet) truncateInstancesToDelete(set *v1alpha1.InstanceSet, instances []*v1alpha1.Instance) error {
	if len(set.Spec.ScaleStrategy.InstanceToDelete) == 0 {
		return nil
	}

	existingInstances := sets.NewString()
	for _, g := range instances {
		existingInstances.Insert(g.Name)
	}

	var newInstancesToDelete []string
	for _, name := range set.Spec.ScaleStrategy.InstanceToDelete {
		if existingInstances.Has(name) {
			newInstancesToDelete = append(newInstancesToDelete, name)
		}
	}

	if len(newInstancesToDelete) == len(set.Spec.ScaleStrategy.InstanceToDelete) {
		return nil
	}

	newSet := set.DeepCopy()
	newSet.Spec.ScaleStrategy.InstanceToDelete = newInstancesToDelete
	return r.Update(context.TODO(), newSet)
}

// truncateHistory truncates any non-live ControllerRevisions in revisions from set's history. The UpdateRevision and
// CurrentRevision in set's Status are considered to be live. Any revisions associated with the Instances in instance are also
// considered to be live. Non-live revisions are deleted, starting with the revision with the lowest Revision, until
// only RevisionHistoryLimit revisions remain. If the returned error is nil the operation was successful. This method
// expects that revisions is sorted when supplied.
func (r *ReconcileInstanceSet) truncateHistory(
	set *v1alpha1.InstanceSet,
	instances []*v1alpha1.Instance,
	revisions []*apps.ControllerRevision,
	current *apps.ControllerRevision,
	update *apps.ControllerRevision,
) error {
	noLiveRevisions := make([]*apps.ControllerRevision, 0, len(revisions))

	// collect live revisions and historic revisions
	for i := range revisions {
		if revisions[i].Name != current.Name && revisions[i].Name != update.Name {
			var found bool
			for _, instance := range instances {
				if utils.EqualToRevisionHash("", instance, revisions[i].Name) {
					found = true
					break
				}
			}
			if !found {
				noLiveRevisions = append(noLiveRevisions, revisions[i])
			}
		}
	}
	historyLen := len(noLiveRevisions)
	historyLimit := 10
	if set.Spec.RevisionHistoryLimit != nil {
		historyLimit = int(*set.Spec.RevisionHistoryLimit)
	}
	if historyLen <= historyLimit {
		return nil
	}
	// delete any non-live history to maintain the revision limit.
	noLiveRevisions = noLiveRevisions[:(historyLen - historyLimit)]
	for i := 0; i < len(noLiveRevisions); i++ {
		if err := r.controllerHistory.DeleteControllerRevision(noLiveRevisions[i]); err != nil {
			return err
		}
	}
	return nil
}

func (r *ReconcileInstanceSet) claimInstances(set *v1alpha1.InstanceSet, instances []*v1alpha1.Instance, selector labels.Selector) ([]*v1alpha1.Instance, error) {
	mgr, err := utils.NewRefManager(r, selector, set, r.scheme)
	if err != nil {
		return nil, err
	}

	selected := make([]metav1.Object, len(instances))
	for i, instance := range instances {
		selected[i] = instance
	}

	claimed, err := mgr.ClaimOwnedObjects(selected)
	if err != nil {
		return nil, err
	}

	claimedInstances := make([]*v1alpha1.Instance, len(claimed))
	for i, instance := range claimed {
		claimedInstances[i] = instance.(*v1alpha1.Instance)
	}

	return claimedInstances, nil
}
