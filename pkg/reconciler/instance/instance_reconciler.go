package instance

import (
	"context"
	"fmt"
	"time"

	"github.com/openkruise/kruise/pkg/util/expectations"
	historyutil "github.com/openkruise/kruise/pkg/util/history"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/history"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	revisioncontrol "sigs.k8s.io/rbgs/pkg/reconciler/instance/revision"
	synccontrol "sigs.k8s.io/rbgs/pkg/reconciler/instance/sync"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/instance/utils"
	"sigs.k8s.io/rbgs/pkg/utils/fieldindex"
)

func NewReconciler(mgr ctrl.Manager) reconcile.Reconciler {
	recorder := mgr.GetEventRecorderFor("instance-controller")
	return &reconciler{
		Client:            mgr.GetClient(),
		scheme:            mgr.GetScheme(),
		recorder:          recorder,
		controllerHistory: historyutil.NewHistory(mgr.GetClient()),
		statusUpdater:     newStatusUpdater(mgr.GetClient()),
		revisionControl:   revisioncontrol.NewRevisionControl(),
		syncControl:       synccontrol.New(mgr.GetClient(), recorder),
	}
}

type reconciler struct {
	client.Client
	scheme            *runtime.Scheme
	controllerHistory history.Interface
	statusUpdater     StatusUpdater
	revisionControl   revisioncontrol.Interface
	syncControl       synccontrol.Interface
	recorder          record.EventRecorder
}

func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (result reconcile.Result, retErr error) {
	logger := log.FromContext(ctx)

	defer handleCrash(ctx, func(p interface{}) {
		logger.Error(fmt.Errorf("%v", p), "Fail to sync Instance: Observed a panic")
		result.RequeueAfter = 1 * time.Minute
	})

	instance := new(v1alpha1.Instance)
	if err := r.Get(ctx, request.NamespacedName, instance); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Instance has been deleted")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// TODO(@yangsoon) add a gc manager to handle inActivePods
	filteredPods, _, err := r.getOwnedPods(ctx, instance)
	if err != nil {
		return reconcile.Result{}, err
	}
	selector := instanceutil.GetSelector(instance)
	revisions, err := r.controllerHistory.ListControllerRevisions(instance, selector)
	if err != nil {
		return reconcile.Result{}, err
	}
	history.SortControllerRevisions(revisions)

	currentRevision, updateRevision, collisionCount, err := r.getActiveRevisions(instance, revisions)
	if err != nil {
		return reconcile.Result{}, err
	}

	for _, pod := range filteredPods {
		instanceutil.ResourceVersionExpectations.Observe(pod)
		if isSatisfied, unsatisfiedDuration := instanceutil.ResourceVersionExpectations.IsSatisfied(pod); !isSatisfied {
			if unsatisfiedDuration > expectations.ExpectationTimeout {
				logger.Info("Expectation unsatisfied overtime for Instance, wait for pod updating timeout",
					"pod", klog.KObj(pod), "timeout", unsatisfiedDuration)
				return reconcile.Result{}, nil
			}
		}
	}

	newStatus := v1alpha1.InstanceStatus{
		ObservedGeneration: instance.Generation,
		CurrentRevision:    currentRevision.Name,
		UpdateRevision:     updateRevision.Name,
		CollisionCount:     new(int32),
		LabelSelector:      selector.String(),
	}
	*newStatus.CollisionCount = collisionCount

	res := r.syncInstance(ctx, instance, &newStatus, currentRevision, updateRevision, revisions, filteredPods)

	if err = r.statusUpdater.UpdateInstanceStatus(ctx, instance, &newStatus, filteredPods); err != nil {
		logger.Error(err, "Failed to update instance status")
		return reconcile.Result{}, err
	}

	if err = r.truncateHistory(filteredPods, revisions, currentRevision, updateRevision); err != nil {
		logger.Error(err, "Failed to truncate history for Instance")
	}
	return reconcile.Result{RequeueAfter: res.requeue}, res.err
}

func (r *reconciler) syncInstance(ctx context.Context, instance *v1alpha1.Instance, newStatus *v1alpha1.InstanceStatus,
	currentRevision, updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision,
	filteredPods []*v1.Pod) syncResult {
	if instance.DeletionTimestamp != nil {
		return syncResult{}
	}
	updateInstance, err := r.revisionControl.ApplyRevision(instance, updateRevision)
	if err != nil {
		return syncResult{err: err}
	}
	var (
		scaling bool

		podsScaleErr  error
		podsUpdateErr error

		requeueDuration time.Duration
	)

	scaling, podsScaleErr = r.syncControl.Scale(ctx, updateInstance, currentRevision, updateRevision, revisions, filteredPods)
	if podsScaleErr != nil {
		newStatus.Conditions = append(newStatus.Conditions, v1alpha1.InstanceCondition{
			Type:               v1alpha1.InstanceFailedScale,
			Status:             v1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Message:            podsScaleErr.Error(),
		})
	}
	if scaling {
		return syncResult{err: podsScaleErr}
	}

	requeueDuration, podsUpdateErr = r.syncControl.Update(ctx, instance, currentRevision, updateRevision, revisions, filteredPods)
	if podsUpdateErr != nil {
		newStatus.Conditions = append(newStatus.Conditions, v1alpha1.InstanceCondition{
			Type:               v1alpha1.InstanceFailedUpdate,
			Status:             v1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Message:            podsUpdateErr.Error(),
		})
	}

	return syncResult{
		requeue: requeueDuration,
		err: utilerrors.NewAggregate([]error{
			podsScaleErr, podsUpdateErr,
		}),
	}
}

func (r *reconciler) getOwnedPods(ctx context.Context, instance *v1alpha1.Instance) ([]*v1.Pod, []*v1.Pod, error) {
	opts := &client.ListOptions{
		Namespace:     instance.Namespace,
		FieldSelector: fields.SelectorFromSet(fields.Set{fieldindex.IndexNameForOwnerRefUID: string(instance.UID)}),
	}
	return instanceutil.GetActiveAndInactivePods(ctx, r.Client, opts)
}

func (r *reconciler) getActiveRevisions(instance *v1alpha1.Instance, revisions []*apps.ControllerRevision) (
	*apps.ControllerRevision, *apps.ControllerRevision, int32, error,
) {
	var currentRevision, updateRevision *apps.ControllerRevision
	revisionCount := len(revisions)

	var collisionCount int32
	if instance.Status.CollisionCount != nil {
		collisionCount = *instance.Status.CollisionCount
	}

	updateRevision, err := r.revisionControl.NewRevision(instance, instanceutil.NextRevision(revisions), &collisionCount)
	if err != nil {
		return nil, nil, collisionCount, err
	}
	equalRevisions := history.FindEqualRevisions(revisions, updateRevision)
	equalCount := len(equalRevisions)
	if equalCount > 0 && history.EqualRevision(revisions[revisionCount-1], equalRevisions[equalCount-1]) {
		updateRevision = revisions[revisionCount-1]
	} else if equalCount > 0 {
		updateRevision, err = r.controllerHistory.UpdateControllerRevision(equalRevisions[equalCount-1], updateRevision.Revision)
		if err != nil {
			return nil, nil, collisionCount, err
		}
	} else {
		updateRevision, err = r.controllerHistory.CreateControllerRevision(instance, updateRevision, &collisionCount)
		if err != nil {
			return nil, nil, collisionCount, err
		}
	}
	for i := range revisions {
		if revisions[i].Name == instance.Status.CurrentRevision {
			currentRevision = revisions[i]
			break
		}
	}
	if currentRevision == nil {
		currentRevision = updateRevision
	}

	return currentRevision, updateRevision, collisionCount, nil
}

func (r *reconciler) truncateHistory(
	pods []*v1.Pod,
	revisions []*apps.ControllerRevision,
	current *apps.ControllerRevision,
	update *apps.ControllerRevision,
) error {
	noLiveRevisions := make([]*apps.ControllerRevision, 0, len(revisions))

	// collect live revisions and historic revisions
	for i := range revisions {
		if revisions[i].Name != current.Name && revisions[i].Name != update.Name {
			var found bool
			for _, pod := range pods {
				if instanceutil.EqualToRevisionHash("", pod, revisions[i].Name) {
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
	historyLimit := 5
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

func handleCrash(ctx context.Context, additionalHandlers ...func(interface{})) {
	if r := recover(); r != nil {
		for _, fn := range utilruntime.PanicHandlers {
			fn(ctx, r)
		}
		for _, fn := range additionalHandlers {
			fn(r)
		}
	}
}

type syncResult struct {
	requeue time.Duration
	err     error
}
