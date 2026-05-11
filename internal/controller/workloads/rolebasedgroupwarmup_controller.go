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
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/utils"
)

const (
	// Label keys for warmup Pods
	LabelWarmupName = "workloads.x-k8s.io/warmup-name"
	LabelWarmupUID  = "workloads.x-k8s.io/warmup-uid"
	LabelNodeName   = "workloads.x-k8s.io/node-name"
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

	switch warmup.Status.Phase {
	case workloadsv1alpha2.WarmupJobPhaseFailed, workloadsv1alpha2.WarmupJobPhaseCompleted:
		return ctrl.Result{}, r.reconcileFinished(ctx, warmup)
	case workloadsv1alpha2.WarmupJobPhaseRunning, workloadsv1alpha2.WarmupJobPhaseNone:
		return ctrl.Result{}, r.reconcileUnfinished(ctx, warmup)
	}

	return ctrl.Result{}, nil
}

func (r *RoleBasedGroupWarmupReconciler) reconcileFinished(ctx context.Context, warmup *workloadsv1alpha2.RoleBasedGroupWarmup) error {
	_ = log.FromContext(ctx)
	_ = warmup
	return nil
}

func (r *RoleBasedGroupWarmupReconciler) reconcileUnfinished(ctx context.Context, warmup *workloadsv1alpha2.RoleBasedGroupWarmup) error {
	logger := log.FromContext(ctx)
	if err := r.validate(*warmup); err != nil {
		logger.Error(err, "Validation failed")
		return err
	}

	// 1. GetExistingPods
	// 2. filter pods -> (active pods, succeeded pods, failed pods, )
	activePods, succeededPods, failedPods, err := r.listWarmupPods(ctx, *warmup)
	if err != nil {
		logger.Error(err, "failed to list warmup pods")
		return err
	}
	// 3. sort desired nodes

	// List all existing warmup Pods owned by this resource
	allPods := append([]*corev1.Pod{}, activePods...)
	allPods = append(allPods, succeededPods...)
	allPods = append(allPods, failedPods...)

	nodeToPodMap := r.getExistingNodeToPodMap(allPods)
	desiredNodes, err := r.getDesiredNodesToWarmup(ctx, *warmup)
	if err != nil {
		logger.Error(err, "failed to get desired nodes to warm up")
		return err
	}

	for nodeName, actions := range desiredNodes {
		if _, exists := nodeToPodMap[nodeName]; !exists {
			logger.Info("Creating warmup Pod", "node", nodeName)
			pod := r.buildWarmupPod(warmup, nodeName, actions)
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
		return err
	}

	return nil
}

func (r *RoleBasedGroupWarmupReconciler) validate(warmup workloadsv1alpha2.RoleBasedGroupWarmup) error {

	targetValidationFunc := func(warmup workloadsv1alpha2.RoleBasedGroupWarmup) error {
		// Validate that either targetNodes or targetRoleBasedGroup is set
		if warmup.Spec.TargetNodes == nil && warmup.Spec.TargetRoleBasedGroup == nil {
			return fmt.Errorf("either targetNodes or targetRoleBasedGroup must be specified")
		}

		if warmup.Spec.TargetNodes != nil && warmup.Spec.TargetRoleBasedGroup != nil {
			return fmt.Errorf("only one of targetNodes or targetRoleBasedGroup may be specified")
		}

		return nil
	}

	targetNodesValidationFunc := func(warmup workloadsv1alpha2.RoleBasedGroupWarmup) error {
		if warmup.Spec.TargetNodes != nil {
			if len(warmup.Spec.TargetNodes.NodeSelector) == 0 && len(warmup.Spec.TargetNodes.NodeNames) == 0 {
				return fmt.Errorf("either spec.targetNodes.nodeSelector or spec.targetNodes.nodeNames must be specified")
			}
		}

		return nil
	}

	targetRoleBasedGroupValidationFunc := func(warmup workloadsv1alpha2.RoleBasedGroupWarmup) error {
		if warmup.Spec.TargetRoleBasedGroup != nil {
			if warmup.Spec.TargetRoleBasedGroup.Name == "" {
				return fmt.Errorf("spec.targetRoleBasedGroup.name must be specified")
			}

			if len(warmup.Spec.TargetRoleBasedGroup.Roles) == 0 {
				return fmt.Errorf("spec.targetRoleBasedGroup.roles must contain at least one role")
			}
		}

		return nil
	}

	validationFuncs := []func(warmup workloadsv1alpha2.RoleBasedGroupWarmup) error{
		targetValidationFunc,
		targetNodesValidationFunc,
		targetRoleBasedGroupValidationFunc,
	}

	for _, validateFn := range validationFuncs {
		if err := validateFn(warmup); err != nil {
			r.Recorder.Eventf(&warmup, corev1.EventTypeWarning, "ValidationFailed", err.Error())
			return err
		}
	}

	return nil
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

func (r *RoleBasedGroupWarmupReconciler) getExistingNodeToPodMap(pods []*corev1.Pod) (nodeToPodMap map[string]*corev1.Pod) {
	nodeToPodMap = make(map[string]*corev1.Pod)

	for _, p := range pods {
		if nodeName, ok := p.Labels[LabelNodeName]; ok {
			nodeToPodMap[nodeName] = p
		}
	}

	return nodeToPodMap
}

// buildWarmupPod constructs a Pod specification for warming up images on a specific node
func (r *RoleBasedGroupWarmupReconciler) buildWarmupPod(warmup *workloadsv1alpha2.RoleBasedGroupWarmup, nodeName string, actions []workloadsv1alpha2.WarmupActions) *corev1.Pod {
	// handle image preload actions
	containers := []corev1.Container{}
	imageToPreloadSet := map[string]bool{}
	for _, action := range actions {
		// Check if ImagePreload is not nil before accessing its fields
		if action.ImagePreload != nil {
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
	}

	imagePullSecrets := []corev1.LocalObjectReference{}
	imagePullSecretsSet := map[string]bool{}
	for _, action := range actions {
		// Check if ImagePreload is not nil before accessing its fields
		if action.ImagePreload != nil {
			for _, pullSecret := range action.ImagePreload.PullSecrets {
				if !imagePullSecretsSet[pullSecret.Name] {
					imagePullSecretsSet[pullSecret.Name] = true
					imagePullSecrets = append(imagePullSecrets, pullSecret)
				}
			}
		}
	}

	// handle customized actions
	customizedActionContainerSet := map[string]bool{}
	for _, action := range actions {
		// Check if CustomizedAction is not nil before accessing its fields
		if action.CustomizedAction != nil {
			for _, ctr := range action.CustomizedAction.Containers {
				ctrHash := fmt.Sprintf("%v", utils.HashContainer(&ctr))
				if !customizedActionContainerSet[ctrHash] {
					customizedActionContainerSet[ctrHash] = true
					ctr.Name = fmt.Sprintf("customized-%s", ctr.Name)
					containers = append(containers, ctr)
				}
			}
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
// 	if len(warmup.Spec.TargetNodes.WarmupActions.ImagePreload.PullSecrets) > 0 {
// 		pod.Spec.ImagePullSecrets = warmup.Spec.TargetNodes.WarmupActions.ImagePreload.PullSecrets
// 	}

// 	return pod
// }

// updateStatus calculates and updates the status based on observed Pod states
func (r *RoleBasedGroupWarmupReconciler) updateStatus(ctx context.Context, warmup *workloadsv1alpha2.RoleBasedGroupWarmup, activePods []*corev1.Pod, succeededPods []*corev1.Pod, failedPods []*corev1.Pod, desiredNodes map[string][]workloadsv1alpha2.WarmupActions) error {
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
			newStatus.Phase = workloadsv1alpha2.WarmupJobPhaseFailed
			logger.Info("Warmup job failed", "succeeded", succeeded, "failed", failed)
			r.Recorder.Eventf(warmup, corev1.EventTypeWarning, "WarmupFailed",
				"Warmup job failed: %d succeeded, %d failed", succeeded, failed)
		} else {
			newStatus.Phase = workloadsv1alpha2.WarmupJobPhaseCompleted
			logger.Info("Warmup job completed successfully", "succeeded", succeeded)
			r.Recorder.Event(warmup, corev1.EventTypeNormal, "WarmupCompleted",
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
func statusEqual(a, b workloadsv1alpha2.RoleBasedGroupWarmupStatus) bool {
	return a.Desired == b.Desired &&
		a.Active == b.Active &&
		a.Ready == b.Ready &&
		a.Succeeded == b.Succeeded &&
		a.Failed == b.Failed &&
		a.Phase == b.Phase &&
		a.StartTime.Equal(&b.StartTime) &&
		a.CompletionTime.Equal(&b.CompletionTime)
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
		For(&workloadsv1alpha2.RoleBasedGroupWarmup{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
