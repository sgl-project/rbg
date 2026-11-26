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

package workloads

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

const (
	// ReasonEngineRuntimeProfileMutatedWhileReferenced is the warning event
	// the controller emits on a referenced profile whose live generation no
	// longer matches status.referencedGeneration — a spec edit the
	// immutability webhook did not block (absent, misconfigured, or
	// uninstalled). The event fires once per drift, on the Frozen condition's
	// reason flip, not on every reconcile.
	ReasonEngineRuntimeProfileMutatedWhileReferenced = "EngineRuntimeProfileMutatedWhileReferenced"
	ReasonEngineRuntimeLabelBackfillSkipped          = "EngineRuntimeLabelBackfillSkipped"
	ReasonEngineRuntimeLabelBackfillFailed           = "EngineRuntimeLabelBackfillFailed"

	engineRuntimeLabelBackfillRetryLimit = 3
	engineRuntimeLabelBackfillRetryAfter = 30 * time.Second
)

// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=clusterengineruntimeprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=clusterengineruntimeprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroups,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// ClusterEngineRuntimeProfileReconciler maintains the KEP-531 reference
// tracking: status.referenceCount, the referencedGeneration baseline and
// the Frozen condition, plus the webhook-bypass detection event. Enforcement
// is the admission webhook's; status is display and detection surface only —
// rendering never consults it, and it is never authoritative.
type ClusterEngineRuntimeProfileReconciler struct {
	client.Client
	Recorder                          record.EventRecorder
	warnedEngineRuntimeLabelBackfill  sync.Map
	engineRuntimeLabelBackfillFailure sync.Map
	engineRuntimeLabelBackfillFlight  singleflight.Group
}

type engineRuntimeLabelBackfillKey struct {
	Kind      string
	Namespace string
	Name      string
	UID       string
}

type engineRuntimeLabelBackfillFailureState struct {
	mu         sync.Mutex
	generation int64
	attempts   int
	retryAt    time.Time
	warned     bool
}

func NewClusterEngineRuntimeProfileReconciler(mgr ctrl.Manager) *ClusterEngineRuntimeProfileReconciler {
	return &ClusterEngineRuntimeProfileReconciler{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorderFor("clusterengineruntimeprofile-controller"),
	}
}

// Reconcile recomputes the profile's live reference set and writes status
// only on change. The referencedGeneration baseline records the profile's
// metadata.generation on the none-to-referenced transition, never refreshes
// while referenced, and clears when the last reference leaves; a live
// generation that drifts from it is precise evidence of a spec edit made
// while referenced, surfacing as the Frozen condition's drifted reason and a
// one-shot warning event.
func (r *ClusterEngineRuntimeProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{}
	if err := r.Get(ctx, req.NamespacedName, profile); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// A profile mid-deletion is going away; tracking on a terminating
	// object only races the delete.
	if !profile.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx).WithValues("clusterengineruntimeprofile", profile.Name)
	ctx = ctrl.LoggerInto(ctx, logger)

	// The controller is the label's authority during convergence: it scans
	// through the informer cache (no apiserver List cost) and uses the
	// label-independent variant, so reference counting stays correct while
	// pre-label objects are being backfilled.
	refs, err := workloadsv1alpha2.ListAllEngineRuntimeProfileReferences(ctx, r.Client, profile.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing engine runtime profile references: %w", err)
	}

	desired := *profile.Status.DeepCopy()
	changed := false

	desired.ReferenceCount = int32(len(refs))
	if desired.ReferenceCount != profile.Status.ReferenceCount {
		changed = true
	}

	mutatedWhileReferenced := false
	if len(refs) > 0 {
		if desired.ReferencedGeneration == 0 {
			desired.ReferencedGeneration = profile.Generation
			changed = true
		}
		reason := workloadsv1alpha2.ReasonReferenced
		message := fmt.Sprintf(
			"%d live reference(s); the profile's spec is frozen while referenced", len(refs))
		if profile.Generation != desired.ReferencedGeneration {
			reason = workloadsv1alpha2.ReasonSpecChangedWhileReferenced
			message = fmt.Sprintf(
				"the spec changed since the baseline was recorded: metadata.generation %d no longer matches the "+
					"referencedGeneration baseline %d — a write the webhook let through, or a legal edit in a "+
					"zero-reference window the controller never observed",
				profile.Generation, desired.ReferencedGeneration)
			// Bypass detection: emit the warning once, on the reason flip from
			// Referenced (or from no condition) into the drifted reason. The
			// signal fires on the spec-change paths the webhook did not block,
			// evidence of the webhook's absence rather than silence; a
			// same-name recreate (a CREATE, not a spec change) is the watch's
			// one blind spot among them.
			previous := apimeta.FindStatusCondition(profile.Status.Conditions, workloadsv1alpha2.FrozenConditionType)
			mutatedWhileReferenced = previous == nil || previous.Reason != workloadsv1alpha2.ReasonSpecChangedWhileReferenced
		}
		if apimeta.SetStatusCondition(&desired.Conditions, metav1.Condition{
			Type:               workloadsv1alpha2.FrozenConditionType,
			Status:             metav1.ConditionTrue,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: profile.Generation,
		}) {
			changed = true
		}
	} else {
		if desired.ReferencedGeneration != 0 {
			desired.ReferencedGeneration = 0
			changed = true
		}
		if apimeta.RemoveStatusCondition(&desired.Conditions, workloadsv1alpha2.FrozenConditionType) {
			changed = true
		}
	}

	if changed {
		profile.Status = desired
		if err := r.Status().Update(ctx, profile); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating engine runtime profile status: %w", err)
		}
	}

	if mutatedWhileReferenced {
		r.Recorder.Event(profile, corev1.EventTypeWarning, ReasonEngineRuntimeProfileMutatedWhileReferenced,
			fmt.Sprintf(
				"the spec of referenced engine runtime profile %q changed since the baseline was recorded (generation %d, baseline %d) — a write the webhook did not block or a legal edit in a zero-reference window never observed; check the validating webhook configuration; renders read the drifted spec live",
				profile.Name, profile.Generation, desired.ReferencedGeneration))
	}

	// Backfill after the status write and after the warning: a failed label
	// patch must not block status tracking or lose the one-shot bypass event.
	// It still returns an error so the reconcile retries and the admission
	// webhook keeps its unscoped fallback until the scan has completed cleanly.
	result, err := r.backfillHasEngineRuntimesLabels(ctx)
	if err == nil && result.RequeueAfter > 0 {
		logger.Info("scheduled engine runtime label backfill retry",
			"requeueAfter", result.RequeueAfter)
	}
	return result, err
}

// backfillHasEngineRuntimesLabels converges the has-engine-runtimes label
// onto RoleBasedGroups and RoleBasedGroupSets whose writes predate the
// label (the upgrade path), or bypassed the admission defaulters
// (--enable-webhooks=none). The scan runs on the manager's informer cache;
// only objects whose label state disagrees with their spec are patched, so
// steady state costs one cached List and no writes. Label-only changes to
// has-engine-runtimes deliberately requeue the referenced profiles so a
// bypassed write is repaired.
func (r *ClusterEngineRuntimeProfileReconciler) backfillHasEngineRuntimesLabels(ctx context.Context) (ctrl.Result, error) {
	value, err, _ := r.engineRuntimeLabelBackfillFlight.Do(
		"engine-runtime-label-backfill",
		func() (interface{}, error) {
			return r.runEngineRuntimeLabelBackfill(ctx)
		},
	)
	result, ok := value.(ctrl.Result)
	if !ok {
		result = ctrl.Result{}
	}
	return result, err
}

func (r *ClusterEngineRuntimeProfileReconciler) runEngineRuntimeLabelBackfill(ctx context.Context) (ctrl.Result, error) {
	if workloadsv1alpha2.EngineRuntimeLabelConverged() {
		// Every admitted write maintains the label, so a converged process
		// only re-scans on restart. Objects written with webhooks disabled
		// (--enable-webhooks=none) can drift; that is a debugging mode and
		// the admission fallback is irrelevant without webhooks anyway.
		return ctrl.Result{}, nil
	}
	epoch := workloadsv1alpha2.BeginEngineRuntimeLabelScan()
	var allErrs []error
	stuck := false
	var requeueAfter time.Duration
	rbgList := &workloadsv1alpha2.RoleBasedGroupList{}
	if err := r.List(ctx, rbgList); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing rolebasedgroups for engine runtime label backfill: %w", err)
	}
	for i := range rbgList.Items {
		rbg := &rbgList.Items[i]
		outcome, err := r.backfillEngineRuntimeLabel(ctx, rbg)
		if err != nil {
			allErrs = append(allErrs, err)
		}
		stuck = stuck || outcome.stuck
		requeueAfter = engineRuntimeLabelBackfillNextRetry(requeueAfter, outcome.requeueAfter)
	}

	rbgsList := &workloadsv1alpha2.RoleBasedGroupSetList{}
	if err := r.List(ctx, rbgsList); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing rolebasedgroupsets for engine runtime label backfill: %w", err)
	}
	for i := range rbgsList.Items {
		rbgs := &rbgsList.Items[i]
		outcome, err := r.backfillEngineRuntimeLabel(ctx, rbgs)
		if err != nil {
			allErrs = append(allErrs, err)
		}
		stuck = stuck || outcome.stuck
		requeueAfter = engineRuntimeLabelBackfillNextRetry(requeueAfter, outcome.requeueAfter)
	}
	if len(allErrs) > 0 {
		return ctrl.Result{}, utilerrors.NewAggregate(allErrs)
	}
	if requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	if stuck {
		return ctrl.Result{}, nil
	}
	// A clean full scan with no writes and no concurrent drift: the scoped
	// admission lookup can take over from the unscoped fallback.
	workloadsv1alpha2.CompleteEngineRuntimeLabelScan(epoch)
	return ctrl.Result{}, nil
}

type engineRuntimeLabelBackfillOutcome struct {
	stuck        bool
	requeueAfter time.Duration
}

func (r *ClusterEngineRuntimeProfileReconciler) backfillEngineRuntimeLabel(
	ctx context.Context, obj client.Object,
) (engineRuntimeLabelBackfillOutcome, error) {
	key := engineRuntimeLabelBackfillKeyForObject(obj)
	if engineRuntimeLabelMatches(obj.GetLabels(), engineRuntimeLabelObjectReferences(obj)) {
		r.engineRuntimeLabelBackfillFailure.Delete(key)
		r.warnedEngineRuntimeLabelBackfill.Delete(key)
		return engineRuntimeLabelBackfillOutcome{}, nil
	}
	if engineRuntimeBackfillWouldChangeSpec(ctx, obj) {
		r.warnEngineRuntimeLabelBackfillSkipped(obj)
		return engineRuntimeLabelBackfillOutcome{stuck: true}, nil
	}

	attempt, retryAfter := r.shouldAttemptEngineRuntimeLabelBackfill(obj, time.Now())
	if !attempt {
		return engineRuntimeLabelBackfillOutcome{stuck: true, requeueAfter: retryAfter}, nil
	}

	base := obj.DeepCopyObject().(client.Object)
	setEngineRuntimeLabelObject(obj)
	if err := r.Patch(ctx, obj, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		retry, scheduledRetry := r.recordEngineRuntimeLabelBackfillFailure(obj, err)
		outcome := engineRuntimeLabelBackfillOutcome{
			stuck:        !apierrors.IsNotFound(err),
			requeueAfter: scheduledRetry,
		}
		if !retry {
			return outcome, nil
		}
		return outcome, fmt.Errorf(
			"backfilling engine runtime label on %s %s/%s: %w",
			engineRuntimeLabelObjectKind(obj), obj.GetNamespace(), obj.GetName(), err)
	}

	r.engineRuntimeLabelBackfillFailure.Delete(key)
	return engineRuntimeLabelBackfillOutcome{}, nil
}

func engineRuntimeLabelBackfillNextRetry(current, candidate time.Duration) time.Duration {
	if candidate > 0 && (current == 0 || candidate < current) {
		return candidate
	}
	return current
}

func (r *ClusterEngineRuntimeProfileReconciler) recordEngineRuntimeLabelBackfillFailure(
	obj client.Object, err error,
) (bool, time.Duration) {
	if apierrors.IsNotFound(err) {
		r.engineRuntimeLabelBackfillFailure.Delete(engineRuntimeLabelBackfillKeyForObject(obj))
		return false, 0
	}

	key := engineRuntimeLabelBackfillKeyForObject(obj)
	value, _ := r.engineRuntimeLabelBackfillFailure.LoadOrStore(key, &engineRuntimeLabelBackfillFailureState{
		generation: obj.GetGeneration(),
	})
	state, ok := value.(*engineRuntimeLabelBackfillFailureState)
	if !ok {
		return true, 0
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.generation != obj.GetGeneration() {
		state.generation = obj.GetGeneration()
		state.attempts = 0
		state.retryAt = time.Time{}
		state.warned = false
	}
	state.attempts++
	if state.attempts < engineRuntimeLabelBackfillRetryLimit {
		return true, 0
	}
	if state.warned {
		return false, 0
	}
	state.warned = true
	state.retryAt = time.Now().Add(engineRuntimeLabelBackfillRetryAfter)
	if r.Recorder != nil {
		r.Recorder.Event(obj, corev1.EventTypeWarning, ReasonEngineRuntimeLabelBackfillFailed,
			fmt.Sprintf("the engine runtime label backfill failed %d times: %v; the object stays unlabeled and admission keeps the unscoped lookup",
				state.attempts, err))
	}
	return false, engineRuntimeLabelBackfillRetryAfter
}

func (r *ClusterEngineRuntimeProfileReconciler) shouldAttemptEngineRuntimeLabelBackfill(
	obj client.Object, now time.Time,
) (bool, time.Duration) {
	key := engineRuntimeLabelBackfillKeyForObject(obj)
	value, loaded := r.engineRuntimeLabelBackfillFailure.Load(key)
	if !loaded {
		return true, 0
	}
	state, ok := value.(*engineRuntimeLabelBackfillFailureState)
	if !ok {
		return true, 0
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.generation != obj.GetGeneration() {
		state.generation = obj.GetGeneration()
		state.attempts = 0
		state.retryAt = time.Time{}
		state.warned = false
		return true, 0
	}
	if state.attempts < engineRuntimeLabelBackfillRetryLimit {
		return true, 0
	}
	if state.attempts == engineRuntimeLabelBackfillRetryLimit {
		if now.Before(state.retryAt) {
			return false, time.Until(state.retryAt)
		}
		return true, 0
	}
	return false, 0
}

func engineRuntimeLabelBackfillKeyForObject(obj client.Object) engineRuntimeLabelBackfillKey {
	key := engineRuntimeLabelBackfillKey{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
		UID:       string(obj.GetUID()),
	}
	key.Kind = engineRuntimeLabelObjectKind(obj)
	return key
}

func engineRuntimeLabelObjectKind(obj client.Object) string {
	switch obj.(type) {
	case *workloadsv1alpha2.RoleBasedGroup:
		return "RoleBasedGroup"
	case *workloadsv1alpha2.RoleBasedGroupSet:
		return "RoleBasedGroupSet"
	default:
		return ""
	}
}

func engineRuntimeLabelObjectReferences(obj client.Object) bool {
	switch typed := obj.(type) {
	case *workloadsv1alpha2.RoleBasedGroup:
		return roleSpecHasEngineRuntimes(&typed.Spec)
	case *workloadsv1alpha2.RoleBasedGroupSet:
		return roleSpecHasEngineRuntimes(&typed.Spec.GroupTemplate.Spec)
	default:
		return false
	}
}

func setEngineRuntimeLabelObject(obj client.Object) {
	switch typed := obj.(type) {
	case *workloadsv1alpha2.RoleBasedGroup:
		workloadsv1alpha2.SetHasEngineRuntimesLabel(typed)
	case *workloadsv1alpha2.RoleBasedGroupSet:
		workloadsv1alpha2.SetRoleBasedGroupSetHasEngineRuntimesLabel(typed)
	}
}

func engineRuntimeBackfillWouldChangeSpec(ctx context.Context, obj client.Object) bool {
	switch typed := obj.(type) {
	case *workloadsv1alpha2.RoleBasedGroup:
		candidate := typed.DeepCopy()
		if err := (&workloadsv1alpha2.RoleBasedGroupDefaulter{}).Default(ctx, candidate); err != nil {
			return true
		}
		return !apiequality.Semantic.DeepEqual(typed.Spec, candidate.Spec)
	case *workloadsv1alpha2.RoleBasedGroupSet:
		candidate := typed.DeepCopy()
		if err := (&workloadsv1alpha2.RoleBasedGroupSetDefaulter{}).Default(ctx, candidate); err != nil {
			return true
		}
		return !apiequality.Semantic.DeepEqual(typed.Spec, candidate.Spec)
	default:
		return true
	}
}

func (r *ClusterEngineRuntimeProfileReconciler) warnEngineRuntimeLabelBackfillSkipped(obj client.Object) {
	if r.Recorder == nil {
		return
	}
	key := engineRuntimeLabelBackfillKeyForObject(obj)
	if _, warned := r.warnedEngineRuntimeLabelBackfill.LoadOrStore(key, struct{}{}); warned {
		return
	}
	r.Recorder.Event(obj, corev1.EventTypeWarning, ReasonEngineRuntimeLabelBackfillSkipped,
		"the engine runtime label backfill skipped this object because the mutating webhook would also normalize its stored spec; touch the object to let the webhook label it")
}

func engineRuntimeLabelMatches(labels map[string]string, references bool) bool {
	if references {
		return labels[constants.HasEngineRuntimesLabelKey] == constants.HasEngineRuntimesLabelValue
	}
	_, present := labels[constants.HasEngineRuntimesLabelKey]
	return !present
}

// SetupWithManager registers the controller. The CERP watch carries the
// tracking; the RBG and RBGSet watches enqueue the profiles each object's
// roles reference. Spec-generation changes recompute the count, while
// engine-runtime label changes requeue the backfill after a bypassed write.
func (r *ClusterEngineRuntimeProfileReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&workloadsv1alpha2.ClusterEngineRuntimeProfile{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&workloadsv1alpha2.RoleBasedGroup{},
			r.engineRuntimeProfileRequestsForRBG(),
			builder.WithPredicates(engineRuntimeLabelOrGenerationChanged())).
		Watches(&workloadsv1alpha2.RoleBasedGroupSet{},
			r.engineRuntimeProfileRequestsForRBGSet(),
			builder.WithPredicates(engineRuntimeLabelOrGenerationChanged())).
		Named("clusterengineruntimeprofile-controller").
		Complete(r)
}

func (r *ClusterEngineRuntimeProfileReconciler) engineRuntimeProfileRequestsForRBG() handler.EventHandler {
	return handler.Funcs{
		CreateFunc: func(_ context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			rbg, ok := e.Object.(*workloadsv1alpha2.RoleBasedGroup)
			if !ok {
				return
			}
			markEngineRuntimeLabelDrift(rbg.Labels, roleSpecHasEngineRuntimes(&rbg.Spec))
			addProfileRequests(q, profilesReferencedBy(&rbg.Spec))
		},
		UpdateFunc: func(_ context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			oldRBG, oldOK := e.ObjectOld.(*workloadsv1alpha2.RoleBasedGroup)
			newRBG, newOK := e.ObjectNew.(*workloadsv1alpha2.RoleBasedGroup)
			if !oldOK || !newOK {
				return
			}
			markEngineRuntimeLabelDrift(newRBG.Labels, roleSpecHasEngineRuntimes(&newRBG.Spec))
			// Enqueue the union so removing the last reference still repairs a
			// stale label left behind by a bypassed write.
			addProfileRequests(q,
				profilesReferencedBy(&oldRBG.Spec),
				profilesReferencedBy(&newRBG.Spec))
		},
		DeleteFunc: func(_ context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			rbg, ok := e.Object.(*workloadsv1alpha2.RoleBasedGroup)
			if !ok {
				return
			}
			r.engineRuntimeLabelBackfillFailure.Delete(engineRuntimeLabelBackfillKeyForObject(rbg))
			r.warnedEngineRuntimeLabelBackfill.Delete(engineRuntimeLabelBackfillKeyForObject(rbg))
			addProfileRequests(q, profilesReferencedBy(&rbg.Spec))
		},
	}
}

func (r *ClusterEngineRuntimeProfileReconciler) engineRuntimeProfileRequestsForRBGSet() handler.EventHandler {
	return handler.Funcs{
		CreateFunc: func(_ context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			rbgs, ok := e.Object.(*workloadsv1alpha2.RoleBasedGroupSet)
			if !ok {
				return
			}
			markEngineRuntimeLabelDrift(rbgs.Labels, roleSpecHasEngineRuntimes(&rbgs.Spec.GroupTemplate.Spec))
			addProfileRequests(q, profilesReferencedBy(&rbgs.Spec.GroupTemplate.Spec))
		},
		UpdateFunc: func(_ context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			oldRBGS, oldOK := e.ObjectOld.(*workloadsv1alpha2.RoleBasedGroupSet)
			newRBGS, newOK := e.ObjectNew.(*workloadsv1alpha2.RoleBasedGroupSet)
			if !oldOK || !newOK {
				return
			}
			markEngineRuntimeLabelDrift(newRBGS.Labels, roleSpecHasEngineRuntimes(&newRBGS.Spec.GroupTemplate.Spec))
			addProfileRequests(q,
				profilesReferencedBy(&oldRBGS.Spec.GroupTemplate.Spec),
				profilesReferencedBy(&newRBGS.Spec.GroupTemplate.Spec))
		},
		DeleteFunc: func(_ context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			rbgs, ok := e.Object.(*workloadsv1alpha2.RoleBasedGroupSet)
			if !ok {
				return
			}
			r.engineRuntimeLabelBackfillFailure.Delete(engineRuntimeLabelBackfillKeyForObject(rbgs))
			r.warnedEngineRuntimeLabelBackfill.Delete(engineRuntimeLabelBackfillKeyForObject(rbgs))
			addProfileRequests(q, profilesReferencedBy(&rbgs.Spec.GroupTemplate.Spec))
		},
	}
}

func addProfileRequests(
	q workqueue.TypedRateLimitingInterface[reconcile.Request],
	requests ...[]reconcile.Request,
) {
	seen := make(map[reconcile.Request]struct{})
	for _, group := range requests {
		for _, request := range group {
			if _, ok := seen[request]; ok {
				continue
			}
			seen[request] = struct{}{}
			q.Add(request)
		}
	}
}

// profilesReferencedByRBG maps a RoleBasedGroup to the cluster-scoped
// profiles its roles' engineRuntimes entries name.
func profilesReferencedByRBG(_ context.Context, obj client.Object) []reconcile.Request {
	rbg, ok := obj.(*workloadsv1alpha2.RoleBasedGroup)
	if !ok {
		return nil
	}
	markEngineRuntimeLabelDrift(rbg.Labels, roleSpecHasEngineRuntimes(&rbg.Spec))
	return profilesReferencedBy(&rbg.Spec)
}

// roleSpecHasEngineRuntimes reports whether any role of the spec references
// an engine runtime profile.
func roleSpecHasEngineRuntimes(spec *workloadsv1alpha2.RoleBasedGroupSpec) bool {
	for i := range spec.Roles {
		if len(spec.Roles[i].EngineRuntimes) > 0 {
			return true
		}
	}
	return false
}

// profilesReferencedByRBGSet maps a RoleBasedGroupSet to the profiles named
// by its group template's roles — a set's group template counts as a live
// reference even at zero replicas, no child RoleBasedGroup required.
func profilesReferencedByRBGSet(_ context.Context, obj client.Object) []reconcile.Request {
	rbgs, ok := obj.(*workloadsv1alpha2.RoleBasedGroupSet)
	if !ok {
		return nil
	}
	markEngineRuntimeLabelDrift(rbgs.Labels, roleSpecHasEngineRuntimes(&rbgs.Spec.GroupTemplate.Spec))
	return profilesReferencedBy(&rbgs.Spec.GroupTemplate.Spec)
}

// markEngineRuntimeLabelDrift un-converges the gate before the queued
// profile reconcile runs, so a bypassed label-only write cannot hide behind
// an already-converged gate.
func markEngineRuntimeLabelDrift(labels map[string]string, references bool) {
	if references && labels[constants.HasEngineRuntimesLabelKey] != constants.HasEngineRuntimesLabelValue {
		workloadsv1alpha2.MarkEngineRuntimeLabelDrift()
	}
}

func engineRuntimeLabelOrGenerationChanged() predicate.Predicate {
	return predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				return e.ObjectOld.GetLabels()[constants.HasEngineRuntimesLabelKey] !=
					e.ObjectNew.GetLabels()[constants.HasEngineRuntimesLabelKey]
			},
		},
	)
}

func profilesReferencedBy(spec *workloadsv1alpha2.RoleBasedGroupSpec) []reconcile.Request {
	var requests []reconcile.Request
	seen := make(map[string]struct{})
	for i := range spec.Roles {
		for _, er := range spec.Roles[i].EngineRuntimes {
			if er.ProfileName == "" {
				continue
			}
			if _, dup := seen[er.ProfileName]; dup {
				continue
			}
			seen[er.ProfileName] = struct{}{}
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Name: er.ProfileName}})
		}
	}
	return requests
}
