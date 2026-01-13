/*
Copyright 2025.

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

package workloadsxk8sio

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
)

const (
	// Label keys for warmup Pods
	LabelWarmUpName = "workloads.x-k8s.io/warmup-name"
	LabelWarmUpUID  = "workloads.x-k8s.io/warmup-uid"
	LabelNodeName   = "workloads.x-k8s.io/node-name"
)

// RoleBasedGroupWarmUpReconciler reconciles a RoleBasedGroupWarmUp object
type RoleBasedGroupWarmUpReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=workloads.x-k8s.io.x-k8s.io,resources=rolebasedgroupwarmups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io.x-k8s.io,resources=rolebasedgroupwarmups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io.x-k8s.io,resources=rolebasedgroupwarmups/finalizers,verbs=update
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroups,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *RoleBasedGroupWarmUpReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the RoleBasedGroupWarmUp instance
	warmup := &workloadsv1alpha1.RoleBasedGroupWarmUp{}
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

	switch warmup.Status.Phase {
	case workloadsv1alpha1.WarmUpJobPhaseFailed, workloadsv1alpha1.WarmUpJobPhaseCompleted:
		return r.reconcileFinished(ctx, warmup)
	case workloadsv1alpha1.WarmUpJobPhaseRunning, workloadsv1alpha1.WarmUpJobPhaseNone:
		return r.reconcileUnfinished(ctx, warmup)
	}

	return ctrl.Result{}, nil
}

func (r *RoleBasedGroupWarmUpReconciler) reconcileFinished(ctx context.Context, warmup *workloadsv1alpha1.RoleBasedGroupWarmUp) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func (r *RoleBasedGroupWarmUpReconciler) reconcileUnfinished(ctx context.Context, warmup *workloadsv1alpha1.RoleBasedGroupWarmUp) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	if err := r.validate(ctx, *warmup); err != nil {
		logger.Error(err, "Validation failed")
		return ctrl.Result{}, err
	}

	// 1. GetExistingPods
	// 2. filter pods -> (active pods, succeeded pods, failed pods, )
	activePods, succeededPods, failedPods, err := r.listWarmUpPods(ctx, *warmup)
	if err != nil {
		logger.Error(err, "failed to list warmup pods")
		return ctrl.Result{}, err
	}
	// 3. sort desired nodes

	// List all existing warmup Pods owned by this resource
	allPods := append([]*corev1.Pod{}, activePods...)
	allPods = append(allPods, succeededPods...)
	allPods = append(allPods, failedPods...)

	nodeToPodMap, err := r.getExistingNodeToPodMap(ctx, allPods)
	if err != nil {
		logger.Error(err, "failed to get existing node to pod map")
		return ctrl.Result{}, err
	}

	desiredNodes, err := r.getDesiredNodesToWarmUp(ctx, *warmup)
	if err != nil {
		logger.Error(err, "failed to get desired nodes to warm up")
		return ctrl.Result{}, err
	}

	for nodeName, actions := range desiredNodes {
		if _, exists := nodeToPodMap[nodeName]; !exists {
			logger.Info("Creating warmup Pod", "node", nodeName)
			pod := r.buildWarmUpPod(warmup, nodeName, actions)
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
	}

	// Calculate and update the status based on observed Pod states
	if err := r.updateStatus(ctx, warmup, activePods, succeededPods, failedPods, desiredNodes); err != nil {
		logger.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *RoleBasedGroupWarmUpReconciler) validate(ctx context.Context, warmup workloadsv1alpha1.RoleBasedGroupWarmUp) error {
	logger := log.FromContext(ctx)

	// Validate that either targetNodes or targetRoleBasedGroup is set
	if warmup.Spec.TargetNodes == nil && warmup.Spec.TargetRoleBasedGroup == nil {
		return fmt.Errorf("either targetNodes or targetRoleBasedGroup must be specified")
	}

	// Validate TargetNodes if specified
	if warmup.Spec.TargetNodes != nil {
		if warmup.Spec.TargetNodes.WarmUpActions.ImagePreload == nil || len(warmup.Spec.TargetNodes.WarmUpActions.ImagePreload.Items) == 0 {
			return fmt.Errorf("targetNodes.imagePreload.items must contain at least one image")
		}
	}

	// Validate TargetRoleBasedGroup if specified
	if warmup.Spec.TargetRoleBasedGroup != nil {
		if warmup.Spec.TargetRoleBasedGroup.Name == "" {
			return fmt.Errorf("targetRoleBasedGroup.name must be specified")
		}
		if len(warmup.Spec.TargetRoleBasedGroup.Roles) == 0 {
			return fmt.Errorf("targetRoleBasedGroup.roles must contain at least one role")
		}

		// Validate each role's warmup actions
		for roleName, actions := range warmup.Spec.TargetRoleBasedGroup.Roles {
			if actions.ImagePreload == nil || len(actions.ImagePreload.Items) == 0 {
				logger.Info("Role has no image preload actions", "role", roleName)
				// This is a warning, not an error - allow roles without actions
			}
		}
	}

	return nil
}

func (r *RoleBasedGroupWarmUpReconciler) getDesiredNodesToWarmUp(ctx context.Context, warmup workloadsv1alpha1.RoleBasedGroupWarmUp) (desiredNodesWithActions map[string][]workloadsv1alpha1.WarmUpActions, err error) {
	logger := log.FromContext(ctx)
	desiredNodesWithActions = make(map[string][]workloadsv1alpha1.WarmUpActions)

	// Handle TargetNodes configuration
	if warmup.Spec.TargetNodes != nil {
		if warmup.Spec.TargetNodes.NodeSelector != nil {
			nodeList := &corev1.NodeList{}
			if err := r.Client.List(ctx, nodeList, client.MatchingLabels(warmup.Spec.TargetNodes.NodeSelector)); err != nil {
				return nil, err
			}

			for _, node := range nodeList.Items {
				desiredNodesWithActions[node.Name] = append(desiredNodesWithActions[node.Name], warmup.Spec.TargetNodes.WarmUpActions)
			}
		}

		if warmup.Spec.TargetNodes.NodeNames != nil {
			for _, nodeName := range warmup.Spec.TargetNodes.NodeNames {
				desiredNodesWithActions[nodeName] = append(desiredNodesWithActions[nodeName], warmup.Spec.TargetNodes.WarmUpActions)
			}
		}
	}

	// Handle TargetRoleBasedGroup configuration
	if warmup.Spec.TargetRoleBasedGroup != nil {
		rbgTarget := warmup.Spec.TargetRoleBasedGroup

		// Fetch the RoleBasedGroup resource
		rbg := &workloadsv1alpha1.RoleBasedGroup{}
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
			client.MatchingLabels{workloadsv1alpha1.SetNameLabelKey: rbgTarget.Name}); err != nil {
			logger.Error(err, "Failed to list Pods for RoleBasedGroup", "name", rbgTarget.Name)
			return nil, fmt.Errorf("failed to list Pods for RoleBasedGroup %s/%s: %w", warmup.Namespace, rbgTarget.Name, err)
		}

		logger.Info("Found Pods for RoleBasedGroup", "count", len(podList.Items))

		// Group Pods by role and extract node names
		roleToNodes := make(map[string][]string)
		for _, pod := range podList.Items {
			// Skip pods that are not scheduled yet
			if pod.Spec.NodeName == "" {
				continue
			}

			// Extract role from pod labels
			roleName := pod.Labels[workloadsv1alpha1.SetRoleLabelKey]
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

func (r *RoleBasedGroupWarmUpReconciler) listWarmUpPods(ctx context.Context, warmup workloadsv1alpha1.RoleBasedGroupWarmUp) (activePods []*corev1.Pod, succeededPods []*corev1.Pod, failedPods []*corev1.Pod, err error) {
	activePods = make([]*corev1.Pod, 0)
	succeededPods = make([]*corev1.Pod, 0)
	failedPods = make([]*corev1.Pod, 0)
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(warmup.Namespace), client.MatchingLabels{
		LabelWarmUpName: warmup.Name,
		LabelWarmUpUID:  string(warmup.UID),
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

func (r *RoleBasedGroupWarmUpReconciler) getExistingNodeToPodMap(ctx context.Context, pods []*corev1.Pod) (nodeToPodMap map[string]*corev1.Pod, error error) {
	nodeToPodMap = make(map[string]*corev1.Pod)

	for _, p := range pods {
		if nodeName, ok := p.Labels[LabelNodeName]; ok {
			nodeToPodMap[nodeName] = p
		}
	}

	return nodeToPodMap, nil
}

// buildWarmUpPod constructs a Pod specification for warming up images on a specific node
func (r *RoleBasedGroupWarmUpReconciler) buildWarmUpPod(warmup *workloadsv1alpha1.RoleBasedGroupWarmUp, nodeName string, actions []workloadsv1alpha1.WarmUpActions) *corev1.Pod {
	// handle image preload actions
	containers := []corev1.Container{}
	imageToPreloadSet := map[string]bool{}
	for _, action := range actions {
		for _, image := range action.ImagePreload.Items {
			if !imageToPreloadSet[image] {
				imageToPreloadSet[image] = true
				containers = append(containers, corev1.Container{
					Name:            fmt.Sprintf("image-warmup-%d", len(containers)),
					Image:           image,
					Command:         []string{"sh", "-c", "exit 0"},
					ImagePullPolicy: corev1.PullIfNotPresent,
				})
			}
		}
	}

	imagePullSecrets := []corev1.LocalObjectReference{}
	imagePullSecretsSet := map[string]bool{}
	for _, action := range actions {
		for _, pullSecret := range action.ImagePreload.PullSecrets {
			if !imagePullSecretsSet[pullSecret.Name] {
				imagePullSecretsSet[pullSecret.Name] = true
				imagePullSecrets = append(imagePullSecrets, pullSecret)
			}
		}
	}

	// handle customized actions
	customizedActionContainerSet := map[string]bool{}
	for _, action := range actions {
		for _, ctr := range action.CustomizedAction.Containers {
			ctrHash := fmt.Sprintf("%v", utils.HashContainer(&ctr))
			if !customizedActionContainerSet[ctrHash] {
				customizedActionContainerSet[ctrHash] = true
				ctr.Name = fmt.Sprintf("customized-%s", ctr.Name)
				containers = append(containers, ctr)
			}
		}
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-", warmup.Name),
			Namespace:    warmup.Namespace,
			Labels: map[string]string{
				LabelWarmUpName: warmup.Name,
				LabelWarmUpUID:  string(warmup.UID),
				LabelNodeName:   nodeName,
			},
		},
		Spec: corev1.PodSpec{
			ImagePullSecrets: imagePullSecrets,
			Containers:       containers,
			RestartPolicy:    corev1.RestartPolicyNever,
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": nodeName,
			},
		},
	}

	return pod
}

// 	// Add image pull secrets if specified
// 	if len(warmup.Spec.TargetNodes.WarmUpActions.ImagePreload.PullSecrets) > 0 {
// 		pod.Spec.ImagePullSecrets = warmup.Spec.TargetNodes.WarmUpActions.ImagePreload.PullSecrets
// 	}

// 	return pod
// }

// updateStatus calculates and updates the status based on observed Pod states
func (r *RoleBasedGroupWarmUpReconciler) updateStatus(ctx context.Context, warmup *workloadsv1alpha1.RoleBasedGroupWarmUp, activePods []*corev1.Pod, succeededPods []*corev1.Pod, failedPods []*corev1.Pod, desiredNodes map[string][]workloadsv1alpha1.WarmUpActions) error {
	var (
		active    int32
		succeeded int32
		failed    int32
	)

	logger := log.FromContext(ctx)

	active = int32(len(activePods))
	succeeded = int32(len(succeededPods))
	failed = int32(len(failedPods))

	desired := int32(len(desiredNodes))

	// Update status fields
	newStatus := warmup.Status.DeepCopy()
	newStatus.Desired = desired
	newStatus.Active = active
	// newStatus.Ready = ready
	newStatus.Succeeded = succeeded
	newStatus.Failed = failed

	// Set startTime if not already set and Pods exist
	if newStatus.StartTime.IsZero() && (len(activePods) > 0 || len(succeededPods) > 0 || len(failedPods) > 0) {
		now := metav1.Now()
		newStatus.StartTime = now
		logger.Info("Setting start time", "time", now)
	}

	// Calculate phase
	terminalCount := succeeded + failed
	if terminalCount >= desired && desired > 0 {
		// All Pods have reached terminal state
		if failed > 0 {
			newStatus.Phase = workloadsv1alpha1.WarmUpJobPhaseFailed
			logger.Info("Warmup job failed", "succeeded", succeeded, "failed", failed)
			r.Recorder.Eventf(warmup, corev1.EventTypeWarning, "WarmUpFailed",
				"Warmup job failed: %d succeeded, %d failed", succeeded, failed)
		} else {
			newStatus.Phase = workloadsv1alpha1.WarmUpJobPhaseCompleted
			logger.Info("Warmup job completed successfully", "succeeded", succeeded)
			r.Recorder.Event(warmup, corev1.EventTypeNormal, "WarmUpCompleted",
				"Warmup job completed successfully")
		}

		// Set completion time if not already set
		if newStatus.CompletionTime.IsZero() {
			now := metav1.Now()
			newStatus.CompletionTime = now
			logger.Info("Setting completion time", "time", now)
		}
	} else if desired > 0 && (active > 0 || succeeded > 0 || failed > 0) {
		// Pods are in progress
		if newStatus.Phase != workloadsv1alpha1.WarmUpJobPhaseRunning {
			newStatus.Phase = workloadsv1alpha1.WarmUpJobPhaseRunning
			logger.Info("Warmup job running")
			if warmup.Status.Phase == workloadsv1alpha1.WarmUpJobPhaseNone {
				r.Recorder.Event(warmup, corev1.EventTypeNormal, "WarmUpStarted",
					"Warmup job started")
			}
		}
	}

	// Only update if status has changed
	if !statusEqual(warmup.Status, *newStatus) {
		warmup.Status = *newStatus
		if err := r.Status().Update(ctx, warmup); err != nil {
			return err
		}
		logger.Info("Updated status", "phase", newStatus.Phase,
			"desired", newStatus.Desired, "active", newStatus.Active,
			"ready", newStatus.Ready, "succeeded", newStatus.Succeeded,
			"failed", newStatus.Failed)
	}

	return nil
}

// isPodReady checks if all containers in a Pod are ready
func isPodReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// statusEqual compares two status objects for equality
func statusEqual(a, b workloadsv1alpha1.RoleBasedGroupWarmUpStatus) bool {
	return a.Desired == b.Desired &&
		a.Active == b.Active &&
		a.Ready == b.Ready &&
		a.Succeeded == b.Succeeded &&
		a.Failed == b.Failed &&
		a.Phase == b.Phase &&
		a.StartTime.Equal(&b.StartTime) &&
		a.CompletionTime.Equal(&b.CompletionTime)
}

// SetupWithManager sets up the controller with the Manager.
func (r *RoleBasedGroupWarmUpReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("RoleBasedGroupWarmUp")
	return ctrl.NewControllerManagedBy(mgr).
		For(&workloadsv1alpha1.RoleBasedGroupWarmUp{}).
		Owns(&corev1.Pod{}).
		Named("workloads.x-k8s.io-rolebasedgroupwarmup").
		Complete(r)
}
