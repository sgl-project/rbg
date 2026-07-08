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
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
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
)

// RoleBasedGroupWarmupReconciler reconciles a RoleBasedGroupWarmup object
type RoleBasedGroupWarmupReconciler struct {
	client.Client

	apiReader client.Reader
	Scheme    *runtime.Scheme
	Recorder  record.EventRecorder
}

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

	desiredNodes, err := r.getDesiredNodesToWarmup(ctx, *warmup)
	if err != nil {
		logger.Error(err, "failed to get desired nodes to warm up")
		return ctrl.Result{}, err
	}

	// Check global timeout
	if warmup.Spec.Policies != nil && warmup.Spec.Policies.GlobalTimeoutSeconds != nil && !warmup.Status.StartTime.IsZero() {
		timeout := time.Duration(*warmup.Spec.Policies.GlobalTimeoutSeconds) * time.Second
		if time.Since(warmup.Status.StartTime.Time) >= timeout {
			logger.Info("Global timeout exceeded, failing warmup job",
				"timeout", timeout, "elapsed", time.Since(warmup.Status.StartTime.Time))
			return ctrl.Result{}, r.failWarmupJob(ctx, warmup, activePods, succeededPods, failedPods, desiredNodes,
				"GlobalTimeoutExceeded",
				fmt.Sprintf("Warmup job timed out after %d seconds", *warmup.Spec.Policies.GlobalTimeoutSeconds))
		}
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

	pendingNodes := collectPendingNodes(desiredNodes, activePods, succeededPods, permanentlyFailedNodes)
	activePods, err = r.createPodsForNodes(ctx, warmup, pendingNodes, desiredNodes, activePods)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.updateStatus(ctx, warmup, activePods, succeededPods, failedPods, desiredNodes, permanentlyFailedNodes); err != nil {
		logger.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	return r.requeueForTimeout(warmup), nil
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
	activePods []*corev1.Pod,
) ([]*corev1.Pod, error) {
	logger := log.FromContext(ctx)

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
			continue
		}
		if err := r.Create(ctx, pod); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				logger.Error(err, "Failed to create warmup Pod", "node", nodeName)
				r.Recorder.Eventf(warmup, corev1.EventTypeWarning, "FailedCreatePod",
					"Failed to create warmup Pod for node %s: %v", nodeName, err)
			}
			continue
		}
		activePods = append(activePods, pod)
		r.Recorder.Eventf(warmup, corev1.EventTypeNormal, "CreatedPod",
			"Created warmup Pod for node %s", nodeName)
	}
	return activePods, nil
}

// requeueForTimeout returns a Result with RequeueAfter set if global timeout is configured and the job is still running.
func (r *RoleBasedGroupWarmupReconciler) requeueForTimeout(warmup *workloadsv1alpha2.RoleBasedGroupWarmup) ctrl.Result {
	if warmup.Spec.Policies == nil || warmup.Spec.Policies.GlobalTimeoutSeconds == nil ||
		warmup.Status.Phase == workloadsv1alpha2.WarmupJobPhaseCompleted ||
		warmup.Status.Phase == workloadsv1alpha2.WarmupJobPhaseFailed ||
		warmup.Status.StartTime.IsZero() {
		return ctrl.Result{}
	}
	timeout := time.Duration(*warmup.Spec.Policies.GlobalTimeoutSeconds) * time.Second
	remaining := timeout - time.Since(warmup.Status.StartTime.Time)
	if remaining <= 0 {
		return ctrl.Result{}
	}
	requeueAfter := remaining
	if requeueAfter > MaxRequeueDelay {
		requeueAfter = MaxRequeueDelay
	}
	return ctrl.Result{RequeueAfter: requeueAfter}
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

	apimeta.SetStatusCondition(&warmup.Status.Conditions, metav1.Condition{
		Type:               "Failed",
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: warmup.Generation,
	})

	r.Recorder.Eventf(warmup, corev1.EventTypeWarning, reason, message)
	logger.Info("Warmup job failed", "reason", reason, "succeeded", succeeded, "failed", failed)
	return r.Status().Update(ctx, warmup)
}

func (r *RoleBasedGroupWarmupReconciler) getDesiredNodesToWarmup(ctx context.Context, warmup workloadsv1alpha2.RoleBasedGroupWarmup) (desiredNodesWithActions map[string][]workloadsv1alpha2.WarmupActions, err error) {
	logger := log.FromContext(ctx)
	desiredNodesWithActions = make(map[string][]workloadsv1alpha2.WarmupActions)

	// Handle TargetNodes configuration
	if warmup.Spec.TargetNodes != nil {
		if warmup.Spec.TargetNodes.NodeSelector != nil {
			nodeList := &corev1.NodeList{}
			if err := r.Client.List(ctx, nodeList, client.MatchingLabels(warmup.Spec.TargetNodes.NodeSelector)); err != nil {
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

	// Handle TargetRoleBasedGroup configuration
	if warmup.Spec.TargetRoleBasedGroup != nil {
		rbgTarget := warmup.Spec.TargetRoleBasedGroup

		// Fetch the RoleBasedGroup resource
		rbg := &workloadsv1alpha2.RoleBasedGroup{}
		rbgKey := client.ObjectKey{
			Name:      rbgTarget.Name,
			Namespace: warmup.Namespace,
		}
		if err := r.Get(ctx, rbgKey, rbg); err != nil {
			logger.Error(err, "Failed to get RoleBasedGroup", "name", rbgTarget.Name, "namespace", warmup.Namespace)
			return nil, fmt.Errorf("failed to get RoleBasedGroup %s/%s: %w", warmup.Namespace, rbgTarget.Name, err)
		}

		// List all Pods belonging to this RoleBasedGroup
		podList := &corev1.PodList{}
		if err := r.List(ctx, podList,
			client.InNamespace(warmup.Namespace),
			client.MatchingLabels{constants.GroupNameLabelKey: rbgTarget.Name}); err != nil {
			logger.Error(err, "Failed to list Pods for RoleBasedGroup", "name", rbgTarget.Name)
			return nil, fmt.Errorf("failed to list Pods for RoleBasedGroup %s/%s: %w", warmup.Namespace, rbgTarget.Name, err)
		}

		logger.Info("Found Pods for RoleBasedGroup", "count", len(podList.Items))

		// Group Pods by role and extract node names
		roleToNodes := make(map[string][]string)
		for _, pod := range podList.Items {
			if pod.Spec.NodeName == "" || pod.DeletionTimestamp != nil {
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
	ctrHashSet := map[string]bool{}
	existingVols := map[string]corev1.Volume{}
	for _, action := range actions {
		if action.CustomizedAction == nil {
			continue
		}
		for _, ctr := range action.CustomizedAction.Containers {
			hashCtr := ctr
			hashCtr.Name = ""
			h := fmt.Sprintf("%v", utils.HashContainer(&hashCtr))
			if ctrHashSet[h] {
				continue
			}
			ctrHashSet[h] = true
			ctr.Name = fmt.Sprintf("custom-%d", len(containers))
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
			Containers:       containers,
			Volumes:          volumes,
			ImagePullSecrets: imagePullSecrets,
			RestartPolicy:    corev1.RestartPolicyNever,
			Tolerations:      warmup.Spec.Tolerations,
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": nodeName,
			},
		},
	}

	return pod, hasVolumeConflict
}

// updateStatus calculates and updates the status based on observed Pod states.
// Counts are at node level, not Pod level: a node is "succeeded" if any of its
// Pods succeeded, "failed" if it exhausted its backoffLimit, and "active" if it
// currently has a running Pod.
func (r *RoleBasedGroupWarmupReconciler) updateStatus(ctx context.Context, warmup *workloadsv1alpha2.RoleBasedGroupWarmup,
	activePods []*corev1.Pod, succeededPods []*corev1.Pod, failedPods []*corev1.Pod,
	desiredNodes map[string][]workloadsv1alpha2.WarmupActions, permanentlyFailedNodes map[string]bool) error {
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

	// Set startTime if not already set and Pods exist
	if newStatus.StartTime.IsZero() && (len(activePods) > 0 || len(succeededPods) > 0 || len(failedPods) > 0) {
		now := metav1.Now()
		newStatus.StartTime = &now
		logger.Info("Setting start time", "time", now)
	}

	// Determine maxFailedNodes tolerance
	var maxFailedNodes *int32
	if warmup.Spec.Policies != nil {
		maxFailedNodes = warmup.Spec.Policies.MaxFailedNodes
	}

	// Calculate phase
	if desired == 0 {
		newStatus.Phase = workloadsv1alpha2.WarmupJobPhaseCompleted
		logger.Info("No nodes matched, completing immediately")
		r.Recorder.Event(warmup, corev1.EventTypeNormal, "WarmupCompleted",
			"No nodes matched the target configuration")
		apimeta.SetStatusCondition(&newStatus.Conditions, metav1.Condition{
			Type:               "Complete",
			Status:             metav1.ConditionTrue,
			Reason:             "NoNodesMatched",
			Message:            "No nodes matched the target configuration",
			ObservedGeneration: warmup.Generation,
		})
		now := metav1.Now()
		newStatus.StartTime = &now
		newStatus.CompletionTime = &now
	} else if terminalCount := succeeded + failed; terminalCount >= desired && active == 0 {
		if failed > 0 && maxFailedNodes != nil && failed > *maxFailedNodes {
			// Permanently failed nodes exceed tolerance
			newStatus.Phase = workloadsv1alpha2.WarmupJobPhaseFailed
			logger.Info("Warmup job failed", "succeeded", succeeded, "failed", failed)
			r.Recorder.Eventf(warmup, corev1.EventTypeWarning, "WarmupFailed",
				"Warmup job failed: %d succeeded, %d permanently failed (max tolerated: %d)",
				succeeded, failed, *maxFailedNodes)
			apimeta.SetStatusCondition(&newStatus.Conditions, metav1.Condition{
				Type:               "Failed",
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
				Type:               "Complete",
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

	// Only update if status has changed
	if !apiequality.Semantic.DeepEqual(warmup.Status, *newStatus) {
		warmup.Status = *newStatus
		if err := r.Status().Update(ctx, warmup); err != nil {
			return err
		}
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

// SetupWithManager sets up the controller with the Manager.
func (r *RoleBasedGroupWarmupReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&workloadsv1alpha2.RoleBasedGroupWarmup{}).
		Owns(&corev1.Pod{}).
		Named("rbgwarmup-controller").
		Complete(r)
}
