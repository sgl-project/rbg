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
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/scale"
	"sigs.k8s.io/rbgs/pkg/utils"
)

const (
	// Label keys for warmup Pods
	LabelWarmupName = "workloads.x-k8s.io/warmup-name"
	LabelWarmupUID  = "workloads.x-k8s.io/warmup-uid"
	LabelNodeName   = "workloads.x-k8s.io/node-name"

	// MaxRequeueDelay caps long requeue intervals to avoid workqueue scheduling drift.
	// Even for multi-day TTLs, the controller wakes up at most every 10 minutes to recheck.
	MaxRequeueDelay = 10 * time.Minute

	// TargetWaitRequeuePeriod is how often a Warmup re-checks a target that is not
	// ready yet (missing RoleBasedGroup, or target Pods not scheduled).
	TargetWaitRequeuePeriod = 10 * time.Second

	// ConditionTargetReady reports whether the target RoleBasedGroup is observable and
	// its Pods are scheduled, so warmup can proceed.
	ConditionTargetReady = "TargetReady"

	// ConditionComplete reports whether the Warmup reached a successful terminal state.
	ConditionComplete = "Complete"

	// ConditionFailed reports whether the Warmup reached an unsuccessful terminal state.
	ConditionFailed = "Failed"

	// ReasonInvalidTarget reports a target that remains absent after the wait timeout.
	ReasonInvalidTarget = "InvalidTarget"
)

// RoleBasedGroupWarmupReconciler reconciles a RoleBasedGroupWarmup object
type RoleBasedGroupWarmupReconciler struct {
	client.Client

	apiReader client.Reader
	Scheme    *runtime.Scheme
	Recorder  record.EventRecorder
}

var marshalCustomizedActionMappings = json.Marshal

func NewRoleBasedGroupWarmupReconciler(mgr ctrl.Manager) *RoleBasedGroupWarmupReconciler {
	return &RoleBasedGroupWarmupReconciler{
		Client:    mgr.GetClient(),
		apiReader: mgr.GetAPIReader(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorderFor("rbg-warmup-controller"),
	}
}

// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupwarmups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupwarmups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupwarmups/finalizers,verbs=update
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroups,verbs=get;list;watch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupscalingadapters,verbs=get
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *RoleBasedGroupWarmupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the RoleBasedGroupWarmup instance
	warmup := &workloadsv1alpha2.RoleBasedGroupWarmup{}
	if err := r.Get(ctx, req.NamespacedName, warmup); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Skip reconciliation if the resource is being deleted
	if !warmup.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx).WithValues("warmup", klog.KObj(warmup))
	ctx = ctrl.LoggerInto(ctx, logger)
	logger.Info("Start reconciling")
	start := time.Now()
	defer func() {
		logger.Info("Finished reconciling", "duration", time.Since(start))
	}()

	if warmup.Spec.Paused != nil && *warmup.Spec.Paused &&
		warmup.Status.Phase != workloadsv1alpha2.WarmupJobPhaseFailed &&
		warmup.Status.Phase != workloadsv1alpha2.WarmupJobPhaseCompleted {
		if warmup.Status.Phase != workloadsv1alpha2.WarmupJobPhasePaused {
			warmup.Status.Phase = workloadsv1alpha2.WarmupJobPhasePaused
			if err := r.Status().Update(ctx, warmup); err != nil {
				return ctrl.Result{}, err
			}
			r.Recorder.Event(warmup, corev1.EventTypeNormal, "Paused", "Warmup job paused")
			logger.Info("Warmup job paused")
		}
		return ctrl.Result{}, nil
	}

	switch warmup.Status.Phase {
	case workloadsv1alpha2.WarmupJobPhaseFailed, workloadsv1alpha2.WarmupJobPhaseCompleted:
		return r.reconcileFinished(ctx, warmup)
	case workloadsv1alpha2.WarmupJobPhasePaused:
		warmup.Status.Phase = workloadsv1alpha2.WarmupJobPhaseRunning
		if err := r.Status().Update(ctx, warmup); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Event(warmup, corev1.EventTypeNormal, "Resumed", "Warmup job resumed")
		logger.Info("Warmup job resumed")
		fallthrough
	case workloadsv1alpha2.WarmupJobPhaseRunning, workloadsv1alpha2.WarmupJobPhaseNone:
		return r.reconcileUnfinished(ctx, warmup)
	}

	return ctrl.Result{}, nil
}

func (r *RoleBasedGroupWarmupReconciler) reconcileFinished(ctx context.Context, warmup *workloadsv1alpha2.RoleBasedGroupWarmup) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if warmup.Spec.Policies == nil || warmup.Spec.Policies.TTLSecondsAfterFinished == nil {
		return ctrl.Result{}, nil
	}

	if warmup.Status.CompletionTime.IsZero() {
		return ctrl.Result{}, nil
	}

	ttl := time.Duration(*warmup.Spec.Policies.TTLSecondsAfterFinished) * time.Second
	elapsed := time.Since(warmup.Status.CompletionTime.Time)
	remaining := ttl - elapsed

	if remaining > 0 {
		requeueAfter := remaining
		if requeueAfter > MaxRequeueDelay {
			requeueAfter = MaxRequeueDelay
		}
		logger.V(1).Info("TTL not yet expired, requeueing", "remaining", remaining, "requeueAfter", requeueAfter)
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	logger.Info("TTL expired, deleting RoleBasedGroupWarmup")
	r.Recorder.Event(warmup, corev1.EventTypeNormal, "TTLExpired",
		"TTL expired, deleting RoleBasedGroupWarmup resource")
	if err := r.Delete(ctx, warmup); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

func (r *RoleBasedGroupWarmupReconciler) reconcileUnfinished(ctx context.Context, warmup *workloadsv1alpha2.RoleBasedGroupWarmup) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	activePods, succeededPods, failedPods, err := r.listWarmupPods(ctx, *warmup)
	if err != nil {
		logger.Error(err, "failed to list warmup pods")
		return ctrl.Result{}, err
	}
	warmupPodsExist := len(activePods) > 0 || len(succeededPods) > 0 || len(failedPods) > 0 ||
		!warmup.Status.StartTime.IsZero()
	target := targetObservation{observedStatus: warmup.Status.DeepCopy()}

	if err := validateWarmupSpec(warmup.Spec); err != nil {
		logger.Error(err, "invalid warmup specification")
		return ctrl.Result{}, r.failWarmupJob(ctx, warmup, activePods, succeededPods, failedPods, nil,
			"InvalidWarmupSpec", err.Error())
	}

	// Fetch the target RoleBasedGroup and its Pods once. Desired nodes and target
	// readiness are derived from the same snapshot, so a Pod that is created or
	// scheduled between two observations cannot make the Warmup falsely complete.
	var targetPods []corev1.Pod
	var targetRBG *workloadsv1alpha2.RoleBasedGroup
	if warmup.Spec.TargetRoleBasedGroup != nil {
		targetRBG, targetPods, err = r.getTargetRoleBasedGroupState(ctx, *warmup)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to get target RoleBasedGroup state")
				return ctrl.Result{}, err
			}
			// A missing target is recoverable before the Warmup has started. Once
			// warmup Pods exist, however, the target disappearing (teardown,
			// namespace cleanup) must not suppress terminal-state accounting: the
			// reconciler continues so updateStatus can complete a fully-warmed job.
			if !warmupPodsExist {
				if r.globalTimeoutExpired(warmup) {
					return ctrl.Result{}, r.failWarmupJob(ctx, warmup, activePods, succeededPods, failedPods, nil,
						ReasonInvalidTarget, err.Error())
				}
				return r.markTargetNotReady(ctx, warmup, "RoleBasedGroupNotFound", err.Error())
			}
			targetRBG = nil
			targetPods = nil
			target.deleted = true
			r.setTargetNotReady(warmup, "RoleBasedGroupNotFound", "target RoleBasedGroup no longer exists")
		}
	}

	desiredNodes, err := r.getDesiredNodesToWarmup(ctx, *warmup, targetPods)
	if err != nil {
		logger.Error(err, "failed to get desired nodes to warm up")
		return ctrl.Result{}, err
	}

	// Check every expected Pod in the selected roles before creating any warmup
	// Pods. The expected count comes from the target RBG's spec, so a newly created
	// RBG whose controller has not created Pods yet waits instead of completing as
	// NoNodesMatched. After the Warmup has started, a new pending target Pod (for
	// example from a scale-up) does not block completion of already-warmed nodes.
	if warmup.Spec.TargetRoleBasedGroup != nil && targetRBG != nil {
		// The RBG controller applies bound ScalingAdapter replicas only in memory,
		// so the persisted spec read here can be stale. Use the effective replicas
		// when deriving the expected Pod count, or a scaled-to-zero target would
		// wait forever and a scaled-up target could be partially warmed.
		effectiveTargetRBG, adapterErr := r.applyTargetScalingAdapterReplicas(ctx, targetRBG)
		if adapterErr != nil {
			logger.Error(adapterErr, "failed to resolve target role replicas from scaling adapters")
			return ctrl.Result{}, adapterErr
		}
		pending := countTargetPodsNotReady(*effectiveTargetRBG, targetPods, warmup.Spec.TargetRoleBasedGroup.Roles)
		target.pending = pending
		if pending > 0 {
			message := fmt.Sprintf("%d selected target RoleBasedGroup Pod(s) are missing or not scheduled yet", pending)
			if !warmupPodsExist {
				if r.globalTimeoutExpired(warmup) {
					return ctrl.Result{}, r.failWarmupJob(ctx, warmup, activePods, succeededPods, failedPods, desiredNodes,
						"GlobalTimeoutExceeded",
						fmt.Sprintf("Warmup job timed out waiting for %d selected target Pod(s) to be created or scheduled", pending))
				}
				return r.markTargetNotReady(ctx, warmup, "TargetPodsNotScheduled", message)
			}
			r.setTargetNotReady(warmup, "TargetPodsNotScheduled", message)
		} else {
			apimeta.SetStatusCondition(&warmup.Status.Conditions, metav1.Condition{
				Type:               ConditionTargetReady,
				Status:             metav1.ConditionTrue,
				Reason:             "RoleBasedGroupReady",
				Message:            "all expected selected target RoleBasedGroup Pods are created and scheduled",
				ObservedGeneration: warmup.Generation,
			})
		}
	}

	if failed, err := r.failCollapsedDesiredSet(ctx, warmup, activePods, succeededPods,
		failedPods, desiredNodes, target); failed {
		return ctrl.Result{}, err
	}

	var backoffLimit *int32
	if warmup.Spec.Policies != nil {
		backoffLimit = warmup.Spec.Policies.BackoffLimitPerNode
	}
	permanentlyFailedNodes := r.computePermanentlyFailedNodes(ctx, failedPods, backoffLimit)

	// Check maxFailedNodes — fail the entire job if permanently failed nodes exceed the tolerance
	if warmup.Spec.Policies != nil && warmup.Spec.Policies.MaxFailedNodes != nil && backoffLimit != nil {
		if int32(len(permanentlyFailedNodes)) > *warmup.Spec.Policies.MaxFailedNodes {
			logger.Info("MaxFailedNodes exceeded, failing warmup job",
				"permanentlyFailed", len(permanentlyFailedNodes),
				"maxFailedNodes", *warmup.Spec.Policies.MaxFailedNodes)
			return ctrl.Result{}, r.failWarmupJob(ctx, warmup, activePods, succeededPods, failedPods, desiredNodes,
				"MaxFailedNodesExceeded",
				fmt.Sprintf("Warmup job failed: %d nodes permanently failed (max tolerated: %d)",
					len(permanentlyFailedNodes), *warmup.Spec.Policies.MaxFailedNodes))
		}
	}

	// The global timeout is anchored at the Warmup's creation, which covers both the
	// wait phase and the run phase. A job whose nodes have already reached a terminal
	// state is allowed to complete even if the deadline passed before this reconcile.
	if warmupReachedTerminalState(desiredNodes, activePods, succeededPods, permanentlyFailedNodes, target.pending) {
		// updateStatus below will record the terminal phase and counters.
	} else if r.globalTimeoutExpired(warmup) {
		timeout := time.Duration(*warmup.Spec.Policies.GlobalTimeoutSeconds) * time.Second
		logger.Info("Global timeout exceeded, failing warmup job",
			"timeout", timeout, "elapsed", time.Since(warmup.CreationTimestamp.Time))
		return ctrl.Result{}, r.failWarmupJob(ctx, warmup, activePods, succeededPods, failedPods, desiredNodes,
			"GlobalTimeoutExceeded",
			fmt.Sprintf("Warmup job timed out after %d seconds", *warmup.Spec.Policies.GlobalTimeoutSeconds))
	}

	allPods := make([]*corev1.Pod, 0, len(activePods)+len(succeededPods)+len(failedPods))
	allPods = append(allPods, activePods...)
	allPods = append(allPods, succeededPods...)
	allPods = append(allPods, failedPods...)
	preCreateCustomizedActionResults := evaluateCustomizedActionResults(desiredNodes, allPods)

	pendingNodes := collectPendingNodes(desiredNodes, activePods, succeededPods, permanentlyFailedNodes)
	activePods, err = r.createPodsForNodes(ctx, warmup, pendingNodes, desiredNodes, activePods, failedPods)
	postCreatePods := make([]*corev1.Pod, 0, len(activePods)+len(succeededPods)+len(failedPods))
	postCreatePods = append(postCreatePods, activePods...)
	postCreatePods = append(postCreatePods, succeededPods...)
	postCreatePods = append(postCreatePods, failedPods...)
	recordDisplacedCustomizedActionFailures(
		r.Recorder,
		warmup,
		warmup.Status.CustomizedActionResults,
		preCreateCustomizedActionResults,
		evaluateCustomizedActionResults(desiredNodes, postCreatePods),
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.updateStatusWithTarget(ctx, warmup, activePods, succeededPods, failedPods,
		desiredNodes, permanentlyFailedNodes, target); err != nil {
		logger.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	return r.requeueAfterReconcile(warmup, target, len(desiredNodes)), nil
}

// targetObservation carries what reconcileUnfinished learned about the target into
// the status decision. observedStatus is the status as fetched, before in-memory
// condition updates, so condition-only transitions are still persisted.
type targetObservation struct {
	// deleted is true when the target RBG disappeared after the Warmup started.
	deleted bool
	// pending counts selected target Pods that are missing or not scheduled.
	pending int
	// observedStatus is the status at the start of this reconcile.
	observedStatus *workloadsv1alpha2.RoleBasedGroupWarmupStatus
}

// requeueAfterReconcile polls a Warmup whose desired set collapsed mid-run while
// the target still expects selected Pods. Target Pod scheduling is not watched by
// this controller, so an RBG event alone is not sufficient to observe replacement.
func (r *RoleBasedGroupWarmupReconciler) requeueAfterReconcile(warmup *workloadsv1alpha2.RoleBasedGroupWarmup,
	target targetObservation, desired int) ctrl.Result {
	if target.pending > 0 && desired == 0 && warmup.Status.Phase == workloadsv1alpha2.WarmupJobPhaseRunning {
		return ctrl.Result{RequeueAfter: targetWaitRequeueAfter(warmup)}
	}
	return r.requeueForTimeout(warmup)
}

// countUnsucceededNodes counts nodes that have a failed attempt and no successful
// attempt. A later success on the same node means the node ultimately succeeded.
func countUnsucceededNodes(succeededPods, failedPods []*corev1.Pod) int32 {
	succeededNodeSet := make(map[string]bool, len(succeededPods))
	for _, pod := range succeededPods {
		if nodeName, ok := pod.Labels[LabelNodeName]; ok {
			succeededNodeSet[nodeName] = true
		}
	}

	unsucceededNodeSet := make(map[string]bool, len(failedPods))
	for _, pod := range failedPods {
		if nodeName, ok := pod.Labels[LabelNodeName]; ok && !succeededNodeSet[nodeName] {
			unsucceededNodeSet[nodeName] = true
		}
	}
	return int32(len(unsucceededNodeSet))
}

// failCollapsedDesiredSet reports whether a Warmup whose desired set became empty
// must fail because attempted nodes that never succeeded exceed maxFailedNodes.
// Once the desired set is empty, retryable failures are final: there is no target
// node left on which to retry. Otherwise the same tolerance semantics as the
// non-empty desired-set path apply, so the outcome does not depend on whether the
// set collapsed before or after the settle reconcile.
func (r *RoleBasedGroupWarmupReconciler) failCollapsedDesiredSet(ctx context.Context,
	warmup *workloadsv1alpha2.RoleBasedGroupWarmup,
	activePods, succeededPods, failedPods []*corev1.Pod,
	desiredNodes map[string][]workloadsv1alpha2.WarmupActions, target targetObservation) (bool, error) {
	unsucceeded := countUnsucceededNodes(succeededPods, failedPods)
	if len(desiredNodes) != 0 || target.pending > 0 || len(activePods) != 0 || unsucceeded == 0 {
		return false, nil
	}

	var maxFailedNodes *int32
	if warmup.Spec.Policies != nil {
		maxFailedNodes = warmup.Spec.Policies.MaxFailedNodes
	}
	if maxFailedNodes == nil || unsucceeded <= *maxFailedNodes {
		return false, nil
	}

	targetState := "target no longer selects any node"
	if target.deleted {
		targetState = "target RoleBasedGroup was deleted"
	}
	return true, r.failWarmupJob(ctx, warmup, activePods, succeededPods, failedPods,
		desiredNodes, "MaxFailedNodesExceeded",
		fmt.Sprintf("%s with %d attempted node(s) not succeeded (max tolerated: %d)",
			targetState, unsucceeded, *maxFailedNodes))
}

// getTargetRoleBasedGroupState returns the target RoleBasedGroup and all Pods that
// belong to it from one read of the RBG and one Pod-list snapshot. The cached client
// is preferred; the API reader is only consulted when the RBG is absent from the
// cache, to distinguish informer lag from a target that really does not exist yet.
func (r *RoleBasedGroupWarmupReconciler) getTargetRoleBasedGroupState(ctx context.Context,
	warmup workloadsv1alpha2.RoleBasedGroupWarmup) (*workloadsv1alpha2.RoleBasedGroup, []corev1.Pod, error) {
	logger := log.FromContext(ctx)
	rbgTarget := warmup.Spec.TargetRoleBasedGroup
	if rbgTarget == nil {
		return nil, nil, nil
	}

	rbg := &workloadsv1alpha2.RoleBasedGroup{}
	rbgKey := client.ObjectKey{Name: rbgTarget.Name, Namespace: warmup.Namespace}
	reader := client.Reader(r.Client)
	if err := r.Get(ctx, rbgKey, rbg); err != nil {
		if !apierrors.IsNotFound(err) || r.apiReader == nil {
			logger.Error(err, "Failed to get RoleBasedGroup", "name", rbgTarget.Name, "namespace", warmup.Namespace)
			return nil, nil, fmt.Errorf("failed to get RoleBasedGroup %s/%s: %w", warmup.Namespace, rbgTarget.Name, err)
		}
		if fallbackErr := r.apiReader.Get(ctx, rbgKey, rbg); fallbackErr != nil {
			return nil, nil, fmt.Errorf("failed to get RoleBasedGroup %s/%s: %w", warmup.Namespace, rbgTarget.Name, fallbackErr)
		}
		reader = r.apiReader
	}

	podList := &corev1.PodList{}
	if err := reader.List(ctx, podList,
		client.InNamespace(warmup.Namespace),
		client.MatchingLabels{constants.GroupNameLabelKey: rbgTarget.Name}); err != nil {
		logger.Error(err, "Failed to list Pods for RoleBasedGroup", "name", rbgTarget.Name)
		return nil, nil, fmt.Errorf("failed to list Pods for RoleBasedGroup %s/%s: %w", warmup.Namespace, rbgTarget.Name, err)
	}
	return rbg, podList.Items, nil
}

// applyTargetScalingAdapterReplicas returns a copy of the target RoleBasedGroup
// with each enabled role's replicas replaced by the value of its bound
// RoleBasedGroupScalingAdapter, mirroring the RBG controller's in-memory override.
// Roles without an enabled, bound adapter (or whose adapter has no replicas) keep
// their persisted value.
func (r *RoleBasedGroupWarmupReconciler) applyTargetScalingAdapterReplicas(
	ctx context.Context, rbg *workloadsv1alpha2.RoleBasedGroup,
) (*workloadsv1alpha2.RoleBasedGroup, error) {
	effective := rbg.DeepCopy()
	for i := range effective.Spec.Roles {
		role := &effective.Spec.Roles[i]
		if !scale.IsScalingAdapterEnable(role) {
			continue
		}
		adapter := &workloadsv1alpha2.RoleBasedGroupScalingAdapter{}
		adapterName := scale.GenerateScalingAdapterName(rbg.Name, role.Name)
		if err := r.Get(ctx, types.NamespacedName{Namespace: rbg.Namespace, Name: adapterName}, adapter); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("get scaling adapter %s/%s: %w", rbg.Namespace, adapterName, err)
		}
		if !adapter.ContainsRBGOwner(rbg) || adapter.Status.Phase != constants.AdapterPhaseBound {
			continue
		}
		if adapter.Spec.Replicas == nil {
			continue
		}
		role.Replicas = ptr.To(*adapter.Spec.Replicas)
	}
	return effective, nil
}

// countTargetPodsNotReady returns how many selected-role target Pods are missing or
// unscheduled. Expected and observed Pods are counted per role, so surplus
// scheduled Pods in one role cannot mask a missing or Pending Pod in another role.
// Pods in unselected roles never affect readiness.
func countTargetPodsNotReady(rbg workloadsv1alpha2.RoleBasedGroup, pods []corev1.Pod, selectedRoles map[string]workloadsv1alpha2.WarmupActions) int {
	expectedByRole := make(map[string]int, len(rbg.Spec.Roles))
	for i := range rbg.Spec.Roles {
		role := &rbg.Spec.Roles[i]
		if _, selected := selectedRoles[role.Name]; !selected {
			continue
		}
		expectedByRole[role.Name] += int(workloadsv1alpha2.ComputeSubGroupSize(role)) *
			int(max(ptr.Deref(role.Replicas, 1), 0))
	}

	scheduledByRole := make(map[string]int, len(expectedByRole))
	for i := range pods {
		pod := &pods[i]
		if !isLiveScheduledTargetPod(pod) {
			continue
		}
		roleName, hasRole := pod.Labels[constants.RoleNameLabelKey]
		if !hasRole {
			continue
		}
		if _, selected := selectedRoles[roleName]; !selected {
			continue
		}
		scheduledByRole[roleName]++
	}

	pending := 0
	for roleName, expected := range expectedByRole {
		pending += max(expected-scheduledByRole[roleName], 0)
	}
	return pending
}

// isLiveScheduledTargetPod reports whether a target Pod still occupies a node: it
// is bound, not terminating, and not terminal. Deployment-backed roles can retain
// evicted Failed Pods with a node assignment while their replacements are Pending.
func isLiveScheduledTargetPod(pod *corev1.Pod) bool {
	return pod.DeletionTimestamp == nil && pod.Spec.NodeName != "" &&
		pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed
}

// setTargetNotReady records a TargetReady=False condition in the in-memory status
// without returning early. updateStatus persists it together with Pod counters.
func (r *RoleBasedGroupWarmupReconciler) setTargetNotReady(warmup *workloadsv1alpha2.RoleBasedGroupWarmup, reason, message string) {
	apimeta.SetStatusCondition(&warmup.Status.Conditions, metav1.Condition{
		Type:               ConditionTargetReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: warmup.Generation,
	})
}

// warmupReachedTerminalState reports whether every desired node has a terminal
// warmup Pod and no Pod is active. It uses the same node-level accounting as
// updateStatus, so a finished job is never mislabeled as timed out merely because
// a reconcile observes it after the global deadline.
//
// Accounting is deliberately conservative in both directions:
//   - terminal nodes are intersected with the current desired set, so a stale
//     terminal Pod for a replaced node cannot stand in for a node that was never
//     warmed (and thereby bypass an expired global timeout);
//   - any active Pod blocks the decision, including one on a node that left the
//     desired set. Otherwise the timeout safety net could be skipped while
//     updateStatus still refuses to complete, stranding the job in Running with
//     an orphan Pod that is never cleaned up.
func warmupReachedTerminalState(desiredNodes map[string][]workloadsv1alpha2.WarmupActions,
	activePods, succeededPods []*corev1.Pod, permanentlyFailedNodes map[string]bool, targetPending int) bool {
	if len(desiredNodes) == 0 {
		// An empty desired set is terminal only when no in-flight Pod remains and the
		// target expects no more selected Pods; otherwise the global-timeout safety
		// net must stay armed.
		return len(activePods) == 0 && targetPending == 0
	}
	if len(activePods) > 0 {
		return false
	}

	terminalNodes := make(map[string]bool, len(desiredNodes))
	for _, pod := range succeededPods {
		if nodeName, ok := pod.Labels[LabelNodeName]; ok {
			if _, desired := desiredNodes[nodeName]; desired {
				terminalNodes[nodeName] = true
			}
		}
	}
	for nodeName := range permanentlyFailedNodes {
		if _, desired := desiredNodes[nodeName]; desired {
			terminalNodes[nodeName] = true
		}
	}
	return len(terminalNodes) >= len(desiredNodes)
}

// validateWarmupActions is the only place a customized action container image is
// checked. It is intentionally not enforced by the CRD: a customized container is a
// bare corev1.Container embedded in the Warmup spec, so the Pod API's image
// validation never runs on it, and a CRD-level rule cannot be used safely. An
// item-scoped CEL rule is re-evaluated on every write, which on Kubernetes <= 1.32
// makes the status of an already-stored invalid object unwritable (the status
// strategy runs CEL without ratcheting there), and controller-gen cannot express
// "required, non-blank" for a slice item. See
// TestGeneratedWarmupCRDDoesNotValidateContainerImages and the KEP.
func validateWarmupActions(target string, action workloadsv1alpha2.WarmupActions) error {
	if action.ImagePreload != nil {
		if len(action.ImagePreload.Images) == 0 {
			return fmt.Errorf("%s.imagePreload.images must contain at least one image", target)
		}
		for i, image := range action.ImagePreload.Images {
			if strings.TrimSpace(image) == "" {
				return fmt.Errorf("%s.imagePreload.images[%d] must not be empty", target, i)
			}
		}
	}
	if action.CustomizedAction != nil {
		if len(action.CustomizedAction.Containers) == 0 {
			return fmt.Errorf("%s.customizedAction.containers must contain at least one container", target)
		}
		for i, container := range action.CustomizedAction.Containers {
			if strings.TrimSpace(container.Image) == "" {
				return fmt.Errorf("%s.customizedAction.containers[%d].image must not be empty", target, i)
			}
		}
		if action.CustomizedAction.TimeoutSeconds != nil && *action.CustomizedAction.TimeoutSeconds <= 0 {
			return fmt.Errorf("%s.customizedAction.timeoutSeconds must be greater than 0", target)
		}
		if action.CustomizedAction.CompletionPolicy != "" &&
			action.CustomizedAction.CompletionPolicy != workloadsv1alpha2.CustomizedActionCompletionPolicyAllSucceeded {
			return fmt.Errorf("%s.customizedAction has unsupported completionPolicy %q", target, action.CustomizedAction.CompletionPolicy)
		}
	}
	return nil
}

func validateWarmupSpec(spec workloadsv1alpha2.RoleBasedGroupWarmupSpec) error {
	if spec.TargetNodes != nil {
		if err := validateWarmupActions("spec.targetNodes", spec.TargetNodes.WarmupActions); err != nil {
			return err
		}
	}
	if spec.TargetRoleBasedGroup != nil {
		roleNames := make([]string, 0, len(spec.TargetRoleBasedGroup.Roles))
		for roleName := range spec.TargetRoleBasedGroup.Roles {
			roleNames = append(roleNames, roleName)
		}
		sort.Strings(roleNames)
		for _, roleName := range roleNames {
			target := fmt.Sprintf("spec.targetRoleBasedGroup.roles[%q]", roleName)
			if err := validateWarmupActions(target, spec.TargetRoleBasedGroup.Roles[roleName]); err != nil {
				return err
			}
		}
	}
	return nil
}

// computePermanentlyFailedNodes returns the set of nodes whose failure count exceeds the backoff limit.
func (r *RoleBasedGroupWarmupReconciler) computePermanentlyFailedNodes(ctx context.Context, failedPods []*corev1.Pod, backoffLimit *int32) map[string]bool {
	logger := log.FromContext(ctx)
	permanentlyFailedNodes := make(map[string]bool)
	if backoffLimit == nil {
		return permanentlyFailedNodes
	}

	nodeFailCount := make(map[string]int)
	for _, p := range failedPods {
		if nodeName, ok := p.Labels[LabelNodeName]; ok {
			nodeFailCount[nodeName]++
		}
	}

	for node, count := range nodeFailCount {
		if int32(count) > *backoffLimit {
			permanentlyFailedNodes[node] = true
			logger.Info("Node permanently failed",
				"node", node, "failCount", count, "backoffLimit", *backoffLimit)
		}
	}
	return permanentlyFailedNodes
}

// collectPendingNodes returns nodes that still need a warmup Pod, sorted for deterministic ordering.
func collectPendingNodes(
	desiredNodes map[string][]workloadsv1alpha2.WarmupActions,
	activePods []*corev1.Pod, succeededPods []*corev1.Pod,
	permanentlyFailedNodes map[string]bool,
) []string {
	occupiedNodes := make(map[string]bool, len(activePods)+len(succeededPods))
	for _, p := range activePods {
		if nodeName, ok := p.Labels[LabelNodeName]; ok {
			occupiedNodes[nodeName] = true
		}
	}
	for _, p := range succeededPods {
		if nodeName, ok := p.Labels[LabelNodeName]; ok {
			occupiedNodes[nodeName] = true
		}
	}

	pendingNodes := make([]string, 0, len(desiredNodes))
	for nodeName := range desiredNodes {
		if permanentlyFailedNodes[nodeName] || occupiedNodes[nodeName] {
			continue
		}
		pendingNodes = append(pendingNodes, nodeName)
	}
	sort.Strings(pendingNodes)
	return pendingNodes
}

// createPodsForNodes creates warmup Pods for pending nodes, respecting the parallelism limit.
func (r *RoleBasedGroupWarmupReconciler) createPodsForNodes(
	ctx context.Context, warmup *workloadsv1alpha2.RoleBasedGroupWarmup,
	pendingNodes []string, desiredNodes map[string][]workloadsv1alpha2.WarmupActions,
	activePods, failedPods []*corev1.Pod,
) ([]*corev1.Pod, error) {
	logger := log.FromContext(ctx)
	nextAttempts := nextWarmupAttempts(failedPods)

	createBudget := len(pendingNodes)
	if warmup.Spec.Policies != nil && warmup.Spec.Policies.Parallelism != nil {
		parallelism := int(*warmup.Spec.Policies.Parallelism)
		budget := parallelism - len(activePods)
		if budget <= 0 {
			logger.Info("Parallelism limit reached, waiting for running pods to complete",
				"parallelism", parallelism, "active", len(activePods))
			return activePods, nil
		}
		if budget < createBudget {
			createBudget = budget
		}
	}

	for i := 0; i < createBudget; i++ {
		nodeName := pendingNodes[i]
		actions := desiredNodes[nodeName]
		logger.Info("Creating warmup Pod", "node", nodeName)
		pod, volumeConflict := r.buildWarmupPod(warmup, nodeName, actions)
		attempt := nextAttempts[nodeName]
		if attempt == 0 {
			attempt = 1
		}
		pod.Labels[LabelWarmupAttempt] = strconv.FormatInt(attempt, 10)
		if volumeConflict {
			apimeta.SetStatusCondition(&warmup.Status.Conditions, metav1.Condition{
				Type:               "VolumeConflict",
				Status:             metav1.ConditionTrue,
				ObservedGeneration: warmup.Generation,
				Reason:             "ConflictingVolumeDefinitions",
				Message:            fmt.Sprintf("Volume definitions conflict across roles on node %s; first definition wins, which may cause unexpected behavior for containers expecting a different volume spec", nodeName),
			})
		}
		if err := controllerutil.SetControllerReference(warmup, pod, r.Scheme); err != nil {
			logger.Error(err, "Failed to set owner reference", "node", nodeName)
			return activePods, fmt.Errorf("set owner reference for warmup Pod on node %q: %w", nodeName, err)
		}
		if err := r.Create(ctx, pod); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				logger.Error(err, "Failed to create warmup Pod", "node", nodeName)
				r.Recorder.Eventf(warmup, corev1.EventTypeWarning, "FailedCreatePod",
					"Failed to create warmup Pod for node %s: %v", nodeName, err)
				return activePods, fmt.Errorf("create warmup Pod for node %q: %w", nodeName, err)
			}
			continue
		}
		activePods = append(activePods, pod)
		r.Recorder.Eventf(warmup, corev1.EventTypeNormal, "CreatedPod",
			"Created warmup Pod for node %s", nodeName)
	}
	return activePods, nil
}

func nextWarmupAttempts(failedPods []*corev1.Pod) map[string]int64 {
	failedCounts := make(map[string]int64)
	maxAttempts := make(map[string]int64)
	for _, pod := range failedPods {
		nodeName := pod.Labels[LabelNodeName]
		if nodeName == "" {
			continue
		}
		failedCounts[nodeName]++
		if attempt, ok := warmupPodAttempt(pod); ok && attempt > maxAttempts[nodeName] {
			maxAttempts[nodeName] = attempt
		}
	}
	for nodeName, count := range failedCounts {
		if count > maxAttempts[nodeName] {
			maxAttempts[nodeName] = count
		}
	}
	for nodeName := range maxAttempts {
		maxAttempts[nodeName]++
	}
	return maxAttempts
}

// globalTimeoutDeadline returns the configured deadline. The global timeout is
// anchored at the Warmup's creation time so it bounds the target wait and the Pod
// run phase as a single budget.
func globalTimeoutDeadline(warmup *workloadsv1alpha2.RoleBasedGroupWarmup) (time.Time, bool) {
	if warmup.Spec.Policies == nil || warmup.Spec.Policies.GlobalTimeoutSeconds == nil ||
		warmup.CreationTimestamp.IsZero() {
		return time.Time{}, false
	}
	return warmup.CreationTimestamp.Add(time.Duration(*warmup.Spec.Policies.GlobalTimeoutSeconds) * time.Second), true
}

// globalTimeoutExpired reports whether the creation-anchored global timeout has
// elapsed. When no timeout is configured, waiting is unbounded and the Warmup stays
// Running with a TargetReady=False condition.
func (r *RoleBasedGroupWarmupReconciler) globalTimeoutExpired(warmup *workloadsv1alpha2.RoleBasedGroupWarmup) bool {
	deadline, ok := globalTimeoutDeadline(warmup)
	return ok && !time.Now().Before(deadline)
}

// globalTimeoutRemaining returns the time left before the global deadline.
func globalTimeoutRemaining(warmup *workloadsv1alpha2.RoleBasedGroupWarmup) (time.Duration, bool) {
	deadline, ok := globalTimeoutDeadline(warmup)
	if !ok {
		return 0, false
	}
	return time.Until(deadline), true
}

// requeueForTimeout returns a Result with RequeueAfter set if global timeout is
// configured and the job is still running.
func (r *RoleBasedGroupWarmupReconciler) requeueForTimeout(warmup *workloadsv1alpha2.RoleBasedGroupWarmup) ctrl.Result {
	if warmup.Status.Phase == workloadsv1alpha2.WarmupJobPhaseCompleted ||
		warmup.Status.Phase == workloadsv1alpha2.WarmupJobPhaseFailed {
		return ctrl.Result{}
	}
	remaining, ok := globalTimeoutRemaining(warmup)
	if !ok || remaining <= 0 {
		return ctrl.Result{}
	}
	requeueAfter := remaining
	if requeueAfter > MaxRequeueDelay {
		requeueAfter = MaxRequeueDelay
	}
	return ctrl.Result{RequeueAfter: requeueAfter}
}

// targetWaitRequeueAfter wakes up at the earlier of the regular target poll and the
// global deadline. This matters for short timeouts: a one-second timeout must not be
// enforced only after the default ten-second poll.
func targetWaitRequeueAfter(warmup *workloadsv1alpha2.RoleBasedGroupWarmup) time.Duration {
	remaining, ok := globalTimeoutRemaining(warmup)
	if !ok {
		return TargetWaitRequeuePeriod
	}
	if remaining <= 0 {
		return 0
	}
	return min(TargetWaitRequeuePeriod, remaining)
}

// markTargetNotReady records a TargetReady=False condition, keeps the job in Running,
// and requeues without creating any Pods. Unlike a terminal failure this state is
// recoverable: once the target becomes ready the next reconcile proceeds normally.
func (r *RoleBasedGroupWarmupReconciler) markTargetNotReady(ctx context.Context,
	warmup *workloadsv1alpha2.RoleBasedGroupWarmup, reason, message string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	newStatus := warmup.Status.DeepCopy()
	newStatus.Phase = workloadsv1alpha2.WarmupJobPhaseRunning
	apimeta.SetStatusCondition(&newStatus.Conditions, metav1.Condition{
		Type:               ConditionTargetReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: warmup.Generation,
	})

	if !apiequality.Semantic.DeepEqual(warmup.Status, *newStatus) {
		warmup.Status = *newStatus
		if err := r.Status().Update(ctx, warmup); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("Target not ready, waiting", "reason", reason, "message", message)
		r.Recorder.Eventf(warmup, corev1.EventTypeNormal, reason, message)
	}

	return ctrl.Result{RequeueAfter: targetWaitRequeueAfter(warmup)}, nil
}

// failWarmupJob deletes all active Pods, sets the status to Failed with a condition, and records an event.
// Status counters are at node level: Active is set to 0 (pods deleted),
// Succeeded and Failed reflect unique node counts. The gap between Desired and (Succeeded + Failed)
// represents nodes whose pods were terminated early.
func (r *RoleBasedGroupWarmupReconciler) failWarmupJob(ctx context.Context, warmup *workloadsv1alpha2.RoleBasedGroupWarmup,
	activePods []*corev1.Pod, succeededPods []*corev1.Pod, failedPods []*corev1.Pod,
	desiredNodes map[string][]workloadsv1alpha2.WarmupActions,
	reason, message string) error {
	logger := log.FromContext(ctx)

	for _, pod := range activePods {
		if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "Failed to delete active pod on failure", "pod", klog.KObj(pod))
		}
	}

	succeededNodeSet := make(map[string]bool)
	for _, p := range succeededPods {
		if nodeName, ok := p.Labels[LabelNodeName]; ok {
			succeededNodeSet[nodeName] = true
		}
	}

	failedNodeSet := make(map[string]bool)
	for _, p := range failedPods {
		if nodeName, ok := p.Labels[LabelNodeName]; ok {
			failedNodeSet[nodeName] = true
		}
	}

	desired := int32(len(desiredNodes))
	succeeded := int32(len(succeededNodeSet))
	failed := int32(len(failedNodeSet))

	now := metav1.Now()
	warmup.Status.Phase = workloadsv1alpha2.WarmupJobPhaseFailed
	warmup.Status.CompletionTime = &now
	warmup.Status.Desired = desired
	warmup.Status.Active = 0
	warmup.Status.Succeeded = succeeded
	warmup.Status.Failed = failed
	allPods := make([]*corev1.Pod, 0, len(activePods)+len(succeededPods)+len(failedPods))
	allPods = append(allPods, activePods...)
	allPods = append(allPods, succeededPods...)
	allPods = append(allPods, failedPods...)
	oldCustomizedActionResults := append(
		[]workloadsv1alpha2.CustomizedActionResult(nil),
		warmup.Status.CustomizedActionResults...,
	)
	oldCustomizedActionResultsTruncated := warmup.Status.CustomizedActionResultsTruncated
	allCustomizedActionResults := warmup.Status.CustomizedActionResults
	preservedPrevious := false
	// A nil desiredNodes value means target resolution failed before the desired
	// state was available. Preserve the last observed diagnostics in that case.
	if desiredNodes != nil {
		observedCustomizedActionResults := evaluateCustomizedActionResults(desiredNodes, allPods)
		allCustomizedActionResults, preservedPrevious = preserveCustomizedActionResults(
			desiredNodes,
			warmup.Status.CustomizedActionResults,
			observedCustomizedActionResults,
			allPods,
		)
	}
	globallyTimedOut := reason == CustomizedActionReasonGlobalTimeout
	for i := range allCustomizedActionResults {
		result := &allCustomizedActionResults[i]
		if result.State == workloadsv1alpha2.CustomizedActionStateSucceeded {
			continue
		}
		result.State = workloadsv1alpha2.CustomizedActionStateFailed
		if result.Reason == "" {
			result.Reason = truncateCustomizedActionMessageTo(reason, customizedActionTerminationReasonLimit)
		}
		if result.Message == "" {
			result.Message = truncateCustomizedActionMessage(message)
		}
	}
	if desiredNodes != nil {
		warmup.Status.CustomizedActionResults, warmup.Status.CustomizedActionResultsTruncated =
			limitCustomizedActionResults(allCustomizedActionResults)
		if preservedPrevious && oldCustomizedActionResultsTruncated {
			warmup.Status.CustomizedActionResultsTruncated = true
		}
	}
	var backoffLimit *int32
	if warmup.Spec.Policies != nil {
		backoffLimit = warmup.Spec.Policies.BackoffLimitPerNode
	}
	permanentlyFailedNodes := r.computePermanentlyFailedNodes(ctx, failedPods, backoffLimit)
	if desiredNodes != nil {
		updateCustomizedActionConditions(
			&warmup.Status.Conditions,
			warmup.Generation,
			allCustomizedActionResults,
			permanentlyFailedNodes,
			globallyTimedOut,
			countDesiredCustomizedActionNodes(desiredNodes),
		)
	}

	apimeta.SetStatusCondition(&warmup.Status.Conditions, metav1.Condition{
		Type:               ConditionFailed,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: warmup.Generation,
	})

	r.Recorder.Eventf(warmup, corev1.EventTypeWarning, reason, message)
	logger.Info("Warmup job failed", "reason", reason, "succeeded", succeeded, "failed", failed)
	if err := r.Status().Update(ctx, warmup); err != nil {
		return err
	}
	recordCustomizedActionEvents(r.Recorder, warmup, oldCustomizedActionResults, warmup.Status.CustomizedActionResults)
	return nil
}

func (r *RoleBasedGroupWarmupReconciler) getDesiredNodesToWarmup(ctx context.Context, warmup workloadsv1alpha2.RoleBasedGroupWarmup,
	targetPods []corev1.Pod) (desiredNodesWithActions map[string][]workloadsv1alpha2.WarmupActions, err error) {
	logger := log.FromContext(ctx)
	desiredNodesWithActions = make(map[string][]workloadsv1alpha2.WarmupActions)

	// Handle TargetNodes configuration
	if warmup.Spec.TargetNodes != nil {
		if warmup.Spec.TargetNodes.NodeSelector != nil {
			nodeList := &corev1.NodeList{}
			if err := r.List(ctx, nodeList, client.MatchingLabels(warmup.Spec.TargetNodes.NodeSelector)); err != nil {
				return nil, err
			}

			for _, node := range nodeList.Items {
				desiredNodesWithActions[node.Name] = append(desiredNodesWithActions[node.Name], warmup.Spec.TargetNodes.WarmupActions)
			}
		}

		if warmup.Spec.TargetNodes.NodeNames != nil {
			for _, nodeName := range warmup.Spec.TargetNodes.NodeNames {
				desiredNodesWithActions[nodeName] = append(desiredNodesWithActions[nodeName], warmup.Spec.TargetNodes.WarmupActions)
			}
		}
	}

	// Handle TargetRoleBasedGroup configuration. The Pods were fetched by
	// getTargetRoleBasedGroupState; using that same snapshot here prevents desired
	// nodes and readiness from disagreeing when the target changes concurrently.
	if warmup.Spec.TargetRoleBasedGroup != nil {
		rbgTarget := warmup.Spec.TargetRoleBasedGroup

		logger.Info("Found Pods for RoleBasedGroup", "count", len(targetPods))

		// Group Pods by role and extract node names
		roleToNodes := make(map[string][]string)
		for _, pod := range targetPods {
			if !isLiveScheduledTargetPod(&pod) {
				continue
			}

			// Extract role from pod labels
			roleName := pod.Labels[constants.RoleNameLabelKey]
			if roleName == "" {
				logger.Info("Pod missing role label, skipping", "pod", klog.KObj(&pod))
				continue
			}

			// Add node to the role's node list
			roleToNodes[roleName] = append(roleToNodes[roleName], pod.Spec.NodeName)
		}

		// For each role, apply the corresponding warmup actions to its nodes
		for roleName, nodes := range roleToNodes {
			// Get warmup actions for this role
			actions, exists := rbgTarget.Roles[roleName]
			if !exists {
				logger.Info("No warmup actions defined for role, skipping", "role", roleName)
				continue
			}

			// Deduplicate nodes for this role
			nodeSet := make(map[string]bool)
			for _, nodeName := range nodes {
				nodeSet[nodeName] = true
			}

			// Add warmup actions for each unique node
			for nodeName := range nodeSet {
				desiredNodesWithActions[nodeName] = append(desiredNodesWithActions[nodeName], actions)
				logger.V(1).Info("Added warmup actions for node", "node", nodeName, "role", roleName)
			}
		}

		logger.Info("Processed RoleBasedGroup target", "rbg", rbgTarget.Name, "roles", len(roleToNodes), "nodes", len(desiredNodesWithActions))
	}

	return desiredNodesWithActions, nil
}

func (r *RoleBasedGroupWarmupReconciler) listWarmupPods(ctx context.Context, warmup workloadsv1alpha2.RoleBasedGroupWarmup) (activePods []*corev1.Pod, succeededPods []*corev1.Pod, failedPods []*corev1.Pod, err error) {
	activePods = make([]*corev1.Pod, 0)
	succeededPods = make([]*corev1.Pod, 0)
	failedPods = make([]*corev1.Pod, 0)
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(warmup.Namespace), client.MatchingLabels{
		LabelWarmupName: warmup.Name,
		LabelWarmupUID:  string(warmup.UID),
	}); err != nil {
		return nil, nil, nil, err
	}

	for _, p := range podList.Items {
		if p.Status.Phase == corev1.PodSucceeded {
			succeededPods = append(succeededPods, &p)
		} else if p.Status.Phase == corev1.PodFailed {
			failedPods = append(failedPods, &p)
		} else if p.DeletionTimestamp.IsZero() {
			activePods = append(activePods, &p)
		}
	}
	return activePods, succeededPods, failedPods, nil
}

// buildWarmupPod constructs a Pod specification for warming up images on a specific node.
// The Pod succeeds when all containers complete successfully.
func (r *RoleBasedGroupWarmupReconciler) buildWarmupPod(warmup *workloadsv1alpha2.RoleBasedGroupWarmup, nodeName string, actions []workloadsv1alpha2.WarmupActions) (*corev1.Pod, bool) {
	var containers []corev1.Container
	var volumes []corev1.Volume
	hasVolumeConflict := false

	// Collect image preload as containers
	imageSet := map[string]bool{}
	for _, action := range actions {
		if action.ImagePreload == nil {
			continue
		}
		for _, image := range action.ImagePreload.Images {
			if imageSet[image] {
				continue
			}
			imageSet[image] = true
			containers = append(containers, corev1.Container{
				Name:            fmt.Sprintf("image-preload-%d", len(containers)),
				Image:           image,
				Command:         []string{"sh", "-c", "exit 0"},
				ImagePullPolicy: corev1.PullIfNotPresent,
			})
		}
	}

	// Collect pull secrets (pod-level, deduplicated)
	var imagePullSecrets []corev1.LocalObjectReference
	secretSet := map[string]bool{}
	for _, action := range actions {
		if action.ImagePreload == nil {
			continue
		}
		for _, ps := range action.ImagePreload.PullSecrets {
			if secretSet[ps.Name] {
				continue
			}
			secretSet[ps.Name] = true
			imagePullSecrets = append(imagePullSecrets, ps)
		}
	}

	// Collect customized action containers and volumes
	ctrHashToMapping := map[string]int{}
	customizedMappings := make([]customizedActionContainerMapping, 0)
	existingVols := map[string]corev1.Volume{}
	for _, action := range actions {
		if action.CustomizedAction == nil {
			continue
		}
		for _, ctr := range action.CustomizedAction.Containers {
			hashCtr := ctr
			hashCtr.Name = ""
			h := fmt.Sprintf("%v", utils.HashContainer(&hashCtr))
			if mappingIndex, exists := ctrHashToMapping[h]; exists {
				mapping := &customizedMappings[mappingIndex]
				mapping.ContainerNames = append(mapping.ContainerNames, ctr.Name)
				continue
			}
			originalName := ctr.Name
			ctr.Name = fmt.Sprintf("custom-%d", len(containers))
			ctrHashToMapping[h] = len(customizedMappings)
			customizedMappings = append(customizedMappings, customizedActionContainerMapping{
				PodContainerName: ctr.Name,
				ContainerNames:   []string{originalName},
			})
			containers = append(containers, ctr)
		}
		for _, vol := range action.CustomizedAction.Volumes {
			if existing, ok := existingVols[vol.Name]; ok {
				if !apiequality.Semantic.DeepEqual(existing.VolumeSource, vol.VolumeSource) {
					hasVolumeConflict = true
					r.Recorder.Eventf(warmup, corev1.EventTypeWarning, "VolumeConflict",
						"Volume %q has conflicting definitions across roles on node %s, using first definition",
						vol.Name, nodeName)
				}
				continue
			}
			existingVols[vol.Name] = vol
			volumes = append(volumes, vol)
		}
	}
	for i := range customizedMappings {
		sort.Strings(customizedMappings[i].ContainerNames)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-", warmup.Name),
			Namespace:    warmup.Namespace,
			Labels: map[string]string{
				LabelWarmupName: warmup.Name,
				LabelWarmupUID:  string(warmup.UID),
				LabelNodeName:   nodeName,
			},
		},
		Spec: corev1.PodSpec{
			Containers:            containers,
			Volumes:               volumes,
			ImagePullSecrets:      imagePullSecrets,
			RestartPolicy:         corev1.RestartPolicyNever,
			Tolerations:           warmup.Spec.Tolerations,
			ActiveDeadlineSeconds: minimumCustomizedActionTimeout(actions),
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": nodeName,
			},
		},
	}
	if len(customizedMappings) > 0 {
		mappingJSON, err := marshalCustomizedActionMappings(customizedMappings)
		if err != nil {
			r.Recorder.Eventf(warmup, corev1.EventTypeWarning, "CustomizedActionMappingError",
				"Failed to record customized action container mappings for node %s: %v", nodeName, err)
		} else {
			pod.Annotations = map[string]string{
				AnnotationCustomizedActionContainers: string(mappingJSON),
			}
		}
	}

	return pod, hasVolumeConflict
}

// updateEmptyDesiredPhase decides the phase when the current desired node set is
// empty. NoNodesMatched is reserved for a Warmup that never had anything to do.
// A started Warmup waits while Pods are active or the target still expects selected
// Pods; otherwise it records final unsucceeded attempts and completes. Failure
// beyond maxFailedNodes is settled by failCollapsedDesiredSet before this method
// runs, so collapsed-set semantics have a single implementation.
func (r *RoleBasedGroupWarmupReconciler) updateEmptyDesiredPhase(ctx context.Context,
	warmup *workloadsv1alpha2.RoleBasedGroupWarmup, newStatus *workloadsv1alpha2.RoleBasedGroupWarmupStatus,
	active, unsucceeded int32, warmupPodsExist bool, target targetObservation) {
	logger := log.FromContext(ctx)
	now := metav1.Now()

	if active > 0 || target.pending > 0 {
		if newStatus.Phase != workloadsv1alpha2.WarmupJobPhaseRunning {
			newStatus.Phase = workloadsv1alpha2.WarmupJobPhaseRunning
		}
		logger.Info("No target nodes are observable, waiting",
			"active", active, "pendingTargetPods", target.pending)
		return
	}

	// With an empty desired set there is no node on which a retryable failure can be
	// retried, so unsucceeded attempts are final for status accounting. Whether they
	// exceed maxFailedNodes was already decided by failCollapsedDesiredSet.
	if warmupPodsExist && unsucceeded > newStatus.Failed {
		newStatus.Failed = unsucceeded
	}

	if !warmupPodsExist {
		newStatus.Phase = workloadsv1alpha2.WarmupJobPhaseCompleted
		logger.Info("No nodes matched, completing immediately")
		r.Recorder.Event(warmup, corev1.EventTypeNormal, "WarmupCompleted",
			"No nodes matched the target configuration")
		apimeta.SetStatusCondition(&newStatus.Conditions, metav1.Condition{
			Type:               ConditionComplete,
			Status:             metav1.ConditionTrue,
			Reason:             "NoNodesMatched",
			Message:            "No nodes matched the target configuration",
			ObservedGeneration: warmup.Generation,
		})
		if newStatus.StartTime.IsZero() {
			newStatus.StartTime = &now
		}
	} else {
		message := fmt.Sprintf("%d node(s) warmed up and %d node(s) failed within tolerance; the target no longer selects any node",
			newStatus.Succeeded, newStatus.Failed)
		newStatus.Phase = workloadsv1alpha2.WarmupJobPhaseCompleted
		logger.Info("Warmup completed after the target no longer selects any node",
			"succeeded", newStatus.Succeeded, "failed", newStatus.Failed)
		r.Recorder.Event(warmup, corev1.EventTypeNormal, "WarmupCompleted", message)
		apimeta.SetStatusCondition(&newStatus.Conditions, metav1.Condition{
			Type:               ConditionComplete,
			Status:             metav1.ConditionTrue,
			Reason:             "WarmupCompleted",
			Message:            message,
			ObservedGeneration: warmup.Generation,
		})
	}

	if newStatus.CompletionTime.IsZero() {
		newStatus.CompletionTime = &now
	}
}

// updateWarmupPhase decides the Warmup phase transition from node-level
// counters and records the corresponding events and conditions on newStatus.
//
// A zero desired set must not complete a Warmup that still has running Pods,
// still expects target Pods, or has an attempted node that never succeeded. The
// target may have been deleted, or all of its selected Pods may be in an
// eviction/drain/recreate gap mid-run. Completed is terminal and only routes
// to TTL cleanup, so completing here would misreport an in-flight job and
// could GC its still-running Pods.
func (r *RoleBasedGroupWarmupReconciler) updateWarmupPhase(
	ctx context.Context,
	warmup *workloadsv1alpha2.RoleBasedGroupWarmup,
	newStatus *workloadsv1alpha2.RoleBasedGroupWarmupStatus,
	desired, active, succeeded, failed, unsucceeded int32,
	warmupPodsExist bool, target targetObservation,
) {
	logger := log.FromContext(ctx)

	var maxFailedNodes *int32
	if warmup.Spec.Policies != nil {
		maxFailedNodes = warmup.Spec.Policies.MaxFailedNodes
	}
	if desired == 0 {
		r.updateEmptyDesiredPhase(ctx, warmup, newStatus, active, unsucceeded, warmupPodsExist, target)
		return
	}

	if terminalCount := succeeded + failed; terminalCount >= desired && active == 0 {
		if failed > 0 && maxFailedNodes != nil && failed > *maxFailedNodes {
			// Permanently failed nodes exceed tolerance
			newStatus.Phase = workloadsv1alpha2.WarmupJobPhaseFailed
			logger.Info("Warmup job failed", "succeeded", succeeded, "failed", failed)
			r.Recorder.Eventf(warmup, corev1.EventTypeWarning, "WarmupFailed",
				"Warmup job failed: %d succeeded, %d permanently failed (max tolerated: %d)",
				succeeded, failed, *maxFailedNodes)
			apimeta.SetStatusCondition(&newStatus.Conditions, metav1.Condition{
				Type:               ConditionFailed,
				Status:             metav1.ConditionTrue,
				Reason:             "MaxFailedNodesExceeded",
				Message:            fmt.Sprintf("%d nodes failed (max tolerated: %d)", failed, *maxFailedNodes),
				ObservedGeneration: warmup.Generation,
			})
		} else {
			// All succeeded, or failures within tolerance
			newStatus.Phase = workloadsv1alpha2.WarmupJobPhaseCompleted
			logger.Info("Warmup job completed", "succeeded", succeeded, "failed", failed)
			r.Recorder.Eventf(warmup, corev1.EventTypeNormal, "WarmupCompleted",
				"Warmup job completed: %d succeeded, %d failed (within tolerance)", succeeded, failed)
			apimeta.SetStatusCondition(&newStatus.Conditions, metav1.Condition{
				Type:               ConditionComplete,
				Status:             metav1.ConditionTrue,
				Reason:             "WarmupCompleted",
				Message:            fmt.Sprintf("%d/%d nodes warmed up successfully", succeeded, desired),
				ObservedGeneration: warmup.Generation,
			})
		}

		if newStatus.CompletionTime.IsZero() {
			now := metav1.Now()
			newStatus.CompletionTime = &now
		}
	} else if desired > 0 && (active > 0 || succeeded > 0 || failed > 0) {
		if newStatus.Phase != workloadsv1alpha2.WarmupJobPhaseRunning {
			newStatus.Phase = workloadsv1alpha2.WarmupJobPhaseRunning
			logger.Info("Warmup job running")
			if warmup.Status.Phase == workloadsv1alpha2.WarmupJobPhaseNone {
				r.Recorder.Event(warmup, corev1.EventTypeNormal, "WarmupStarted",
					"Warmup job started")
			}
		}
	}
}

// updateStatus calculates and updates the status based on observed Pod states.
// Counts are at node level, not Pod level: a node is "succeeded" if any of its
// Pods succeeded, "failed" if it exhausted its backoffLimit, and "active" if it
// currently has a running Pod.
func (r *RoleBasedGroupWarmupReconciler) updateStatus(ctx context.Context, warmup *workloadsv1alpha2.RoleBasedGroupWarmup,
	activePods []*corev1.Pod, succeededPods []*corev1.Pod, failedPods []*corev1.Pod,
	desiredNodes map[string][]workloadsv1alpha2.WarmupActions, permanentlyFailedNodes map[string]bool) error {
	return r.updateStatusWithTarget(ctx, warmup, activePods, succeededPods, failedPods,
		desiredNodes, permanentlyFailedNodes, targetObservation{
			observedStatus: warmup.Status.DeepCopy(),
		})
}

func (r *RoleBasedGroupWarmupReconciler) updateStatusWithTarget(ctx context.Context,
	warmup *workloadsv1alpha2.RoleBasedGroupWarmup,
	activePods []*corev1.Pod, succeededPods []*corev1.Pod, failedPods []*corev1.Pod,
	desiredNodes map[string][]workloadsv1alpha2.WarmupActions, permanentlyFailedNodes map[string]bool,
	target targetObservation) error {
	logger := log.FromContext(ctx)

	// Count nodes at each state (node-level, not pod-level)
	succeededNodeSet := make(map[string]bool)
	for _, p := range succeededPods {
		if nodeName, ok := p.Labels[LabelNodeName]; ok {
			succeededNodeSet[nodeName] = true
		}
	}

	activeNodeSet := make(map[string]bool)
	for _, p := range activePods {
		if nodeName, ok := p.Labels[LabelNodeName]; ok {
			if !succeededNodeSet[nodeName] && !permanentlyFailedNodes[nodeName] {
				activeNodeSet[nodeName] = true
			}
		}
	}

	desired := int32(len(desiredNodes))
	active := int32(len(activeNodeSet))
	succeeded := int32(len(succeededNodeSet))
	failed := int32(len(permanentlyFailedNodes))

	newStatus := warmup.Status.DeepCopy()
	newStatus.Desired = desired
	newStatus.Active = active
	newStatus.Succeeded = succeeded
	newStatus.Failed = failed
	allPods := make([]*corev1.Pod, 0, len(activePods)+len(succeededPods)+len(failedPods))
	allPods = append(allPods, activePods...)
	allPods = append(allPods, succeededPods...)
	allPods = append(allPods, failedPods...)
	observedCustomizedActionResults := evaluateCustomizedActionResults(desiredNodes, allPods)
	allCustomizedActionResults, preservedPrevious := preserveCustomizedActionResults(
		desiredNodes,
		warmup.Status.CustomizedActionResults,
		observedCustomizedActionResults,
		allPods,
	)
	newStatus.CustomizedActionResults, newStatus.CustomizedActionResultsTruncated =
		limitCustomizedActionResults(allCustomizedActionResults)
	if preservedPrevious && warmup.Status.CustomizedActionResultsTruncated {
		newStatus.CustomizedActionResultsTruncated = true
	}

	// Set startTime if not already set and Pods exist
	if newStatus.StartTime.IsZero() && (len(activePods) > 0 || len(succeededPods) > 0 || len(failedPods) > 0) {
		now := metav1.Now()
		newStatus.StartTime = &now
		logger.Info("Setting start time", "time", now)
	}
	warmupPodsExist := len(activePods) > 0 || len(succeededPods) > 0 || len(failedPods) > 0 ||
		!newStatus.StartTime.IsZero()

	// Calculate phase (events and conditions are recorded by updateWarmupPhase).
	r.updateWarmupPhase(ctx, warmup, newStatus, desired, active, succeeded, failed,
		countUnsucceededNodes(succeededPods, failedPods), warmupPodsExist, target)

	updateCustomizedActionConditions(
		&newStatus.Conditions,
		warmup.Generation,
		allCustomizedActionResults,
		permanentlyFailedNodes,
		false,
		countDesiredCustomizedActionNodes(desiredNodes),
	)

	// Compare with the status as fetched. reconcileUnfinished may have changed
	// TargetReady in memory before updateStatusWithTarget builds newStatus.
	baseline := warmup.Status
	if target.observedStatus != nil {
		baseline = *target.observedStatus
	}
	if !apiequality.Semantic.DeepEqual(baseline, *newStatus) {
		oldCustomizedActionResults := baseline.CustomizedActionResults
		warmup.Status = *newStatus
		if err := r.Status().Update(ctx, warmup); err != nil {
			return err
		}
		recordCustomizedActionEvents(r.Recorder, warmup, oldCustomizedActionResults, newStatus.CustomizedActionResults)
		logger.Info("Updated status", "phase", newStatus.Phase,
			"desired", newStatus.Desired, "active", newStatus.Active,
			"succeeded", newStatus.Succeeded, "failed", newStatus.Failed)
	}

	return nil
}

func (r *RoleBasedGroupWarmupReconciler) CheckCrdExists() error {
	crds := []string{
		"rolebasedgroupwarmups.workloads.x-k8s.io",
	}

	for _, crd := range crds {
		if err := utils.CheckCrdExists(r.apiReader, crd); err != nil {
			return err
		}
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager. Besides the Warmup and
// its owned Pods, the controller watches target RoleBasedGroups so a target created
// after its Warmup is picked up immediately instead of waiting for the next poll.
func (r *RoleBasedGroupWarmupReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&workloadsv1alpha2.RoleBasedGroupWarmup{}).
		Owns(&corev1.Pod{}).
		Watches(&workloadsv1alpha2.RoleBasedGroup{}, handler.EnqueueRequestsFromMapFunc(r.warmupsForTargetRBG)).
		Named("rbgwarmup-controller").
		Complete(r)
}

// warmupsForTargetRBG maps a target RoleBasedGroup event to the Warmups in the same
// namespace that reference it by name.
func (r *RoleBasedGroupWarmupReconciler) warmupsForTargetRBG(ctx context.Context, obj client.Object) []reconcile.Request {
	logger := log.FromContext(ctx)
	warmups := &workloadsv1alpha2.RoleBasedGroupWarmupList{}
	if err := r.List(ctx, warmups, client.InNamespace(obj.GetNamespace())); err != nil {
		logger.Error(err, "failed to list Warmups for target RoleBasedGroup", "roleBasedGroup", klog.KObj(obj))
		return nil
	}

	requests := make([]reconcile.Request, 0, len(warmups.Items))
	for i := range warmups.Items {
		warmup := &warmups.Items[i]
		if warmup.Spec.TargetRoleBasedGroup == nil || warmup.Spec.TargetRoleBasedGroup.Name != obj.GetName() {
			continue
		}
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(warmup)})
	}
	return requests
}
