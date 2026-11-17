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

package roleinstance

import (
	"context"
	"fmt"
	"slices"
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
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	"k8s.io/kubernetes/pkg/controller/history"
	"k8s.io/utils/lru"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	inplaceupdatepod "sigs.k8s.io/rbgs/pkg/inplace/pod"
	revisioncontrol "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/revision"
	synccontrol "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/sync"
	instanceutil "sigs.k8s.io/rbgs/pkg/reconciler/roleinstance/utils"
	pkgutils "sigs.k8s.io/rbgs/pkg/utils"
	utilclient "sigs.k8s.io/rbgs/pkg/utils/client"
	"sigs.k8s.io/rbgs/pkg/utils/fieldindex"
)

func NewReconciler(mgr ctrl.Manager, bindings *synccontrol.NodeBindingStore) reconcile.Reconciler {
	recorder := mgr.GetEventRecorderFor("instance-controller")
	c := utilclient.NewClientWithUserAgent(mgr, "roleinstance")
	return &reconciler{
		Client:                c,
		scheme:                mgr.GetScheme(),
		recorder:              recorder,
		controllerHistory:     historyutil.NewHistory(c),
		statusUpdater:         newStatusUpdater(c, mgr.GetAPIReader()),
		revisionControl:       revisioncontrol.NewRevisionControl(),
		syncControl:           synccontrol.New(c, mgr.GetAPIReader(), recorder, bindings),
		revisionEqualityCache: lru.New(pkgutils.MaxRevisionEqualityCacheEntries),
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
	// revisionEqualityCache caches semantic revision equality results to avoid
	// expensive ApplyRevision + buildPatch reconstruction on every reconcile.
	revisionEqualityCache *lru.Cache
}

func (r *reconciler) Reconcile(ctx context.Context, request reconcile.Request) (result reconcile.Result, retErr error) {
	logger := log.FromContext(ctx)

	defer handleCrash(ctx, func(p interface{}) {
		logger.Error(fmt.Errorf("%v", p), "Fail to sync Instance: Observed a panic")
		result.RequeueAfter = 1 * time.Minute
	})

	instance := new(workloadsv1alpha2.RoleInstance)
	if err := r.Get(ctx, request.NamespacedName, instance); err != nil {
		if apierrors.IsNotFound(err) {
			r.syncControl.ClearRestarting(&workloadsv1alpha2.RoleInstance{
				ObjectMeta: metav1.ObjectMeta{Namespace: request.Namespace, Name: request.Name},
			})
			logger.Info("Instance has been deleted")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// Skip reconcile if the instance is being deleted to avoid StorageError
	// caused by precondition failures when the object's UID becomes empty during foreground deletion.
	if instance.DeletionTimestamp != nil {
		r.syncControl.ClearRestarting(instance)
		return reconcile.Result{}, r.syncControl.CleanupRestartBudget(ctx, instance)
	}

	// TODO(@yangsoon) add a gc manager to handle inActivePods
	filteredPods, inactivePods, err := r.getOwnedPods(ctx, instance)
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

	newStatus := workloadsv1alpha2.RoleInstanceStatus{
		ObservedGeneration:              instance.Generation,
		CurrentRevision:                 currentRevision.Name,
		UpdateRevision:                  updateRevision.Name,
		CollisionCount:                  new(int32),
		LabelSelector:                   selector.String(),
		InPlaceUpdateContainerBaselines: deepCopyBaselines(instance.Status.InPlaceUpdateContainerBaselines),
		RestartCount:                    instance.Status.RestartCount,
		LastRestartTime:                 instance.Status.LastRestartTime,
		LastReadyGeneration:             instance.Status.LastReadyGeneration,
		LastPhaseCompleteGeneration:     lastConfirmedPhaseGeneration(instance),
	}
	*newStatus.CollisionCount = collisionCount

	res := r.syncInstance(ctx, instance, &newStatus, currentRevision, updateRevision, revisions, filteredPods, inactivePods)

	phaseFacts := r.phaseFacts(instance, &newStatus, filteredPods)
	newStatus.Phase = nextRoleInstancePhase(instance.Status.Phase, phaseFacts)
	if phaseCompletionSatisfied(phaseFacts, &newStatus) {
		// Latch phase completion separately from serving readiness: external
		// readiness gates must not keep spec-driven updates classified as
		// active after the controller-observed work has converged.
		newStatus.LastPhaseCompleteGeneration = instance.Generation
	}

	if err = r.statusUpdater.UpdateInstanceStatus(ctx, instance, &newStatus, filteredPods); err != nil {
		logger.Error(err, "Failed to update instance status")
		return reconcile.Result{}, err
	}

	// Clear the in-memory restarting cache when the instance becomes Ready, but
	// only when no restart wave is in progress: a trigger can fire while the pod
	// snapshot is still Ready (for example a container restart), and clearing the
	// marker on that snapshot would disarm the wave that just started.
	restartingInProgress := false
	for _, cond := range newStatus.Conditions {
		if cond.Type == workloadsv1alpha2.RoleInstanceRestarting && cond.Status == v1.ConditionTrue {
			restartingInProgress = true
			break
		}
	}
	if !restartingInProgress {
		for _, cond := range newStatus.Conditions {
			if cond.Type == workloadsv1alpha2.RoleInstanceReady && cond.Status == v1.ConditionTrue {
				r.syncControl.ClearRestarting(instance)
				break
			}
		}
	}

	if err = r.truncateHistory(filteredPods, revisions, currentRevision, updateRevision); err != nil {
		logger.Error(err, "Failed to truncate history for Instance")
	}
	return reconcile.Result{RequeueAfter: res.requeue}, res.err
}

func (r *reconciler) syncInstance(ctx context.Context, instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus,
	currentRevision, updateRevision *apps.ControllerRevision, revisions []*apps.ControllerRevision,
	filteredPods []*v1.Pod, inactivePods []*v1.Pod) syncResult {
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

	var scaleRequeue time.Duration
	scaling, scaleRequeue, podsScaleErr = r.syncControl.Scale(ctx, updateInstance, currentRevision, updateRevision, revisions, filteredPods, inactivePods)
	// Remove a stale Restarting condition from the informer copy when the
	// revision-patched copy (synced with the fresh API state) holds none:
	// setInstanceConditions carries conditions not present in newStatus forward
	// from this object, so a stale entry here could resurrect a Restarting state
	// the API has already left. Value changes are deliberately NOT mirrored: the
	// propagation loop below writes the authoritative value into newStatus, and
	// leaving the old value on this copy is what lets inconsistentStatus detect
	// and persist the completion transition.
	removeStaleRestartingCondition(instance, updateInstance)
	if podsScaleErr != nil {
		// KEP-502: single operation attempts are Warning Events; the current blocker
		// fact is carried by the Progressing condition. FailedScale/FailedUpdate are
		// no longer written, and stale legacy entries are dropped by setInstanceConditions.
		scaleReason := classifyPodOperationError("Create", podsScaleErr)
		// The event carries the classification of the error that actually happened;
		// only the condition retains a previously confirmed blocker family.
		r.emitOperationFailureEvent(instance, scaleReason, podsScaleErr)
		newStatus.Conditions = append(newStatus.Conditions, workloadsv1alpha2.RoleInstanceCondition{
			Type:               workloadsv1alpha2.RoleInstanceProgressing,
			Status:             v1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             r.stableBlockerReason(instance, scaleReason),
			Message:            podsScaleErr.Error(),
		})
	}
	// Propagate the Restarting condition mutated during Scale: True while a
	// recreation wave is in progress, and False (RestartCompleted) once every
	// pre-restart Pod is gone. Propagate it regardless of the scaling state so a
	// completed wave persists even when pod creation has already finished; it does
	// not wait for the instance to become Ready. setInstanceConditions carries it
	// only when absent here, and drops it once the instance is Ready.
	for _, cond := range updateInstance.Status.Conditions {
		if cond.Type == workloadsv1alpha2.RoleInstanceRestarting {
			newStatus.Conditions = append(newStatus.Conditions, cond)
			break
		}
	}
	if scaling {
		// Propagate restart tracking only when updateRestartTracking modified
		// the values during Scale (i.e., a restart-policy-triggered deletion
		// was triggered). ApplyRevision produces zero-value status fields, so
		// unconditional propagation would clobber the correct values from the
		// live object during the pod creation phase.
		if updateInstance.Status.RestartCount > 0 {
			newStatus.RestartCount = updateInstance.Status.RestartCount
		}
		if updateInstance.Status.LastRestartTime != nil {
			newStatus.LastRestartTime = updateInstance.Status.LastRestartTime
		}
		return syncResult{err: podsScaleErr, requeue: scaleRequeue}
	}
	// Restart backoff: delay not elapsed, skip Update and just requeue.
	// The Restarting condition was already propagated above, regardless of the
	// scaling state: backoff only applies after a wave completed, so the condition
	// is False (RestartCompleted) here; setInstanceConditions drops it when the
	// instance becomes Ready.
	if scaleRequeue > 0 {
		return syncResult{requeue: scaleRequeue}
	}

	requeueDuration, podsUpdateErr = r.syncControl.Update(ctx, instance, newStatus, currentRevision, updateRevision, revisions, filteredPods)
	if podsUpdateErr != nil {
		updateReason := classifyPodOperationError("Update", podsUpdateErr)
		r.emitOperationFailureEvent(instance, updateReason, podsUpdateErr)
		newStatus.Conditions = append(newStatus.Conditions, workloadsv1alpha2.RoleInstanceCondition{
			Type:               workloadsv1alpha2.RoleInstanceProgressing,
			Status:             v1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             r.stableBlockerReason(instance, updateReason),
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

// removeStaleRestartingCondition removes the Restarting condition from dst when
// src holds none. src is the authoritative in-reconcile view (synced from a
// fresh API read); when it has no condition, the informer copy must not
// resurrect one through setInstanceConditions' carry-forward. When src does
// hold a condition, its value is written to newStatus by the propagation loop
// and must stay unmirrored on dst so inconsistentStatus detects the change.
func removeStaleRestartingCondition(dst, src *workloadsv1alpha2.RoleInstance) {
	var srcCond *workloadsv1alpha2.RoleInstanceCondition
	for i := range src.Status.Conditions {
		if src.Status.Conditions[i].Type == workloadsv1alpha2.RoleInstanceRestarting {
			srcCond = &src.Status.Conditions[i]
			break
		}
	}
	if srcCond != nil {
		return
	}
	filtered := make([]workloadsv1alpha2.RoleInstanceCondition, 0, len(dst.Status.Conditions))
	for _, cond := range dst.Status.Conditions {
		if cond.Type != workloadsv1alpha2.RoleInstanceRestarting {
			filtered = append(filtered, cond)
		}
	}
	dst.Status.Conditions = filtered
}

func (r *reconciler) getOwnedPods(ctx context.Context, instance *workloadsv1alpha2.RoleInstance) ([]*v1.Pod, []*v1.Pod, error) {
	opts := &client.ListOptions{
		Namespace:     instance.Namespace,
		FieldSelector: fields.SelectorFromSet(fields.Set{fieldindex.IndexNameForOwnerRefUID: string(instance.UID)}),
	}
	active, inactive, err := instanceutil.GetActiveAndInactivePods(ctx, r.Client, opts)
	if err != nil {
		return nil, nil, err
	}
	// The owner UID index includes non-controller references, which are not members.
	foreign := func(pod *v1.Pod) bool { return !metav1.IsControlledBy(pod, instance) }
	return slices.DeleteFunc(active, foreign), slices.DeleteFunc(inactive, foreign), nil
}

func (r *reconciler) getActiveRevisions(instance *workloadsv1alpha2.RoleInstance, revisions []*apps.ControllerRevision) (
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
	} else if revisionCount > 0 && r.revisionControl.SetMatchesRevision(instance, updateRevision, revisions[revisionCount-1], r.revisionEqualityCache) {
		// if there is no equivalent revision we check semantic equality before creating a new one.
		// This handles serialization drift across client-go versions (e.g.,
		// "creationTimestamp": null vs omitted) that causes EqualRevision to
		// wrongly return false.
		updateRevision = revisions[revisionCount-1]
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

// phaseFacts derives the controller-observed facts for the RI phase machine. External
// readiness gates are intentionally excluded: they gate externally observed readiness
// and parent availability, not phase completion.
func (r *reconciler) phaseFacts(instance *workloadsv1alpha2.RoleInstance, newStatus *workloadsv1alpha2.RoleInstanceStatus, pods []*v1.Pod) roleInstancePhaseFacts {
	podsPresent := podsMatchComponentTopology(instance, pods)

	// Revision inequality does not cover component size changes: size is excluded
	// from revision patches, so a previously ready instance whose current
	// generation has not yet been confirmed ready is also executing a
	// spec-driven update. Independent Pod loss keeps the generation equal to the
	// last confirmed-ready generation and stays recovery.
	revisionUpdate := newStatus.UpdateRevision != "" && newStatus.UpdateRevision != newStatus.CurrentRevision
	confirmedGeneration := lastConfirmedPhaseGeneration(instance)
	specDrivenUpdate := confirmedGeneration != 0 && instance.Generation > confirmedGeneration
	updateActive := revisionUpdate || specDrivenUpdate

	readySatisfied := true
	if policy := instance.Spec.ReadyPolicy; policy == "" || policy == workloadsv1alpha2.RoleInstanceReadyOnAllPodReady {
		for _, pod := range pods {
			// Phase completion uses runtime readiness plus the controller-owned
			// in-place update gate, not the Pod Ready condition: externally
			// owned readiness gates gate serving readiness and the parent's
			// availability, not the lifecycle phase.
			if !podPhaseRuntimeReady(pod) {
				readySatisfied = false
				break
			}
		}
	} else {
		// readyPolicy None skips pod readiness for phase completion, but the
		// controller-owned in-place update readiness gate still gates completion;
		// external gates remain excluded.
		for _, pod := range pods {
			if condition := inplaceupdatepod.GetInPlaceCondition(pod); condition != nil && condition.Status != v1.ConditionTrue {
				readySatisfied = false
				break
			}
		}
	}
	return roleInstancePhaseFacts{
		updateActive:          updateActive,
		podsPresent:           podsPresent,
		readySatisfied:        readySatisfied,
		previouslyEstablished: confirmedGeneration != 0,
	}
}

// podPhaseRuntimeReady reports controller-observed runtime readiness: the Pod
// is Running, its containers are Ready, and the controller-owned in-place
// update gate is not holding it. The externally observed Pod Ready condition
// and CustomReady gates are deliberately excluded.
func podPhaseRuntimeReady(pod *v1.Pod) bool {
	if pod.DeletionTimestamp != nil || pod.Status.Phase != v1.PodRunning {
		return false
	}
	_, containersReady := podutil.GetPodCondition(&pod.Status, v1.ContainersReady)
	if containersReady == nil || containersReady.Status != v1.ConditionTrue {
		return false
	}
	if condition := inplaceupdatepod.GetInPlaceCondition(pod); condition != nil && condition.Status != v1.ConditionTrue {
		return false
	}
	return true
}

// lastConfirmedPhaseGeneration returns the generation at which the phase machine
// last confirmed completion of a target. Objects written before
// lastPhaseCompleteGeneration existed fall back to the full-readiness latch, so
// upgrade behavior is unchanged until the next completion.
func lastConfirmedPhaseGeneration(instance *workloadsv1alpha2.RoleInstance) int64 {
	confirmed := instance.Status.LastPhaseCompleteGeneration
	if confirmed == 0 {
		return instance.Status.LastReadyGeneration
	}
	return confirmed
}

// phaseCompletionSatisfied reports whether the current target has converged by
// the phase machine's controller-observed criteria: the required Pod structure
// exists, runtime readiness (plus the controller-owned in-place gate) is
// satisfied, and the revisions have converged.
func phaseCompletionSatisfied(facts roleInstancePhaseFacts, newStatus *workloadsv1alpha2.RoleInstanceStatus) bool {
	return facts.podsPresent && facts.readySatisfied &&
		newStatus.UpdateRevision != "" && newStatus.UpdateRevision == newStatus.CurrentRevision
}

// podsMatchComponentTopology reports whether the observed Pods exactly occupy
// the component topology of the current target: every required component slot
// has a Pod, and no Pod occupies a slot the target does not declare.
// Component resizing can keep the total Pod count unchanged (A=2/B=1 to
// A=1/B=2), so counting Pods would let a superseded surplus Pod hide a missing
// required Pod and let creation or update complete against the old structure.
func podsMatchComponentTopology(instance *workloadsv1alpha2.RoleInstance, pods []*v1.Pod) bool {
	present := map[string]map[int32]bool{}
	for _, pod := range pods {
		componentName := instanceutil.GetPodComponentName(pod)
		componentID := instanceutil.GetPodComponentID(pod)
		matched := false
		for i := range instance.Spec.Components {
			component := &instance.Spec.Components[i]
			if component.Name != componentName || component.Size == nil {
				continue
			}
			if componentID >= 0 && componentID < *component.Size {
				matched = true
				if present[component.Name] == nil {
					present[component.Name] = map[int32]bool{}
				}
				present[component.Name][componentID] = true
			}
		}
		if !matched {
			// A Pod outside the current topology: superseded surplus, foreign
			// member, or malformed identity.
			return false
		}
	}
	for i := range instance.Spec.Components {
		component := &instance.Spec.Components[i]
		if component.Size == nil {
			continue
		}
		ids := present[component.Name]
		for id := int32(0); id < *component.Size; id++ {
			if !ids[id] {
				return false
			}
		}
	}
	return true
}

// emitOperationFailureEvent emits an edge-triggered Warning Event for a failed pod
// operation. Events are diagnostic history: the current blocker fact stays in the
// Progressing condition. The event is emitted only when the failure message changes,
// so repeated identical failures do not drain the object's event budget.
func (r *reconciler) emitOperationFailureEvent(instance *workloadsv1alpha2.RoleInstance, reason string, err error) {
	for _, condition := range instance.Status.Conditions {
		if condition.Type == workloadsv1alpha2.RoleInstanceProgressing && condition.Message == err.Error() {
			return
		}
	}
	r.recorder.Eventf(instance, v1.EventTypeWarning, reason, "%v", err)
}

// stableBlockerReason keeps a previously confirmed blocker family when a new error
// cannot be classified: an unclassifiable error does not establish that the confirmed
// blocker no longer holds, so the family is retained until a fresh observation clears
// or replaces it.
func (r *reconciler) stableBlockerReason(instance *workloadsv1alpha2.RoleInstance, reason string) string {
	if reason != workloadsv1alpha2.ReasonPodOperationUnclassified {
		return reason
	}
	for _, condition := range instance.Status.Conditions {
		if condition.Type != workloadsv1alpha2.RoleInstanceProgressing || condition.Status != v1.ConditionTrue {
			continue
		}
		// Retention applies only to a confirmed blocker family observed for the
		// current target. Activity and child-readiness reasons are not blockers, a
		// stale condition does not describe the current target, and an unclassified
		// reason is not a confirmed family.
		if condition.ObservedGeneration == instance.Generation && isInstanceBlockerReason(condition.Reason) {
			return condition.Reason
		}
	}
	return reason
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

// deepCopyBaselines returns a deep copy of the baselines map to prevent aliasing
// between instance.Status and newStatus.
func deepCopyBaselines(src map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline) map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline {
	if src == nil {
		return nil
	}
	dst := make(map[string]map[string]workloadsv1alpha2.ContainerUpdateBaseline, len(src))
	for podName, containers := range src {
		dstContainers := make(map[string]workloadsv1alpha2.ContainerUpdateBaseline, len(containers))
		for cName, baseline := range containers {
			dstContainers[cName] = baseline
		}
		dst[podName] = dstContainers
	}
	return dst
}
