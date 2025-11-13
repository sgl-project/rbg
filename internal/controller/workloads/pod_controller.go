package workloads

import (
	"errors"
	"fmt"

	"golang.org/x/net/context"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	applyconfiguration "sigs.k8s.io/rbgs/client-go/applyconfiguration/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/dependency"
	"sigs.k8s.io/rbgs/pkg/reconciler"
	"sigs.k8s.io/rbgs/pkg/utils"
)

// PodReconciler reconciles a Pod object owned by RBG
type PodReconciler struct {
	client client.Client
	scheme *runtime.Scheme
}

func NewPodReconciler(mgr ctrl.Manager) *PodReconciler {
	return &PodReconciler{
		client: mgr.GetClient(),
		scheme: mgr.GetScheme(),
	}
}

func (r *PodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var rbg workloadsv1alpha1.RoleBasedGroup
	if err := r.client.Get(
		ctx, types.NamespacedName{
			Name:      req.Name,
			Namespace: req.Namespace,
		}, &rbg,
	); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	logger := log.FromContext(ctx).WithValues("rbg", klog.KObj(&rbg))

	if err := r.restartRBG(ctx, &rbg); err != nil {
		logger.Error(err, fmt.Sprintf("restartRBG error, err: %+v", err))
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *PodReconciler) restartRBG(ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup) error {
	logger := log.FromContext(ctx)
	logger.Info("Recreating RoleBasedGroup")

	// 1. update rbg status
	if err := r.setRestartCondition(ctx, rbg, false); err != nil {
		return err
	}

	// 2. sort role
	dependencyManager := dependency.NewDefaultDependencyManager(r.scheme, r.client)
	sortedRoles, err := dependencyManager.SortRoles(ctx, rbg)
	if err != nil {
		return err
	}
	for _, roleList := range sortedRoles {
		var errs error

		for _, role := range roleList {
			recon, err := reconciler.NewWorkloadReconciler(role.Workload, r.scheme, r.client)
			if err != nil {
				errs = errors.Join(errs, err)
				continue
			}
			// 3. recreate role
			if err := recon.RecreateWorkload(ctx, rbg, role); err != nil {
				errs = errors.Join(errs, err)
				continue
			}
		}

		if errs != nil {
			return errs
		}
	}

	// 4. remove restart status
	if err := r.setRestartCondition(ctx, rbg, true); err != nil {
		return err
	}

	return nil
}

func (r *PodReconciler) setRestartCondition(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, restartCompleted bool,
) error {
	var restartCondition metav1.Condition
	if restartCompleted {
		restartCondition = metav1.Condition{
			Type:               string(workloadsv1alpha1.RoleBasedGroupRestartInProgress),
			Status:             metav1.ConditionStatus(corev1.ConditionFalse),
			LastTransitionTime: metav1.Now(),
			Reason:             "RBGRestartCompleted",
			Message:            "RBG Restart Completed",
		}
	} else {
		restartCondition = metav1.Condition{
			Type:               string(workloadsv1alpha1.RoleBasedGroupRestartInProgress),
			Status:             metav1.ConditionStatus(corev1.ConditionTrue),
			LastTransitionTime: metav1.Now(),
			Reason:             "RBGRestart",
			Message:            "RBG Restart in progress",
		}
	}

	setCondition(rbg, restartCondition)

	rbgApplyConfig := ToRBGApplyConfigurationForStatus(rbg)

	return utils.PatchObjectApplyConfiguration(ctx, r.client, rbgApplyConfig, utils.PatchStatus)
}

func restartConditionTrue(status workloadsv1alpha1.RoleBasedGroupStatus) bool {
	for _, cond := range status.Conditions {
		if cond.Type == string(workloadsv1alpha1.RoleBasedGroupRestartInProgress) {
			return cond.Status == metav1.ConditionStatus(corev1.ConditionTrue)
		}
	}
	return false
}

func setCondition(rbg *workloadsv1alpha1.RoleBasedGroup, newCondition metav1.Condition) {
	found := false
	for i, curCondition := range rbg.Status.Conditions {
		if newCondition.Type == curCondition.Type {
			found = true
			if newCondition.Status != curCondition.Status {
				rbg.Status.Conditions[i] = newCondition
			}
		}
	}
	if !found {
		rbg.Status.Conditions = append(rbg.Status.Conditions, newCondition)
	}
}

func ToRBGApplyConfigurationForStatus(rbg *workloadsv1alpha1.RoleBasedGroup) *applyconfiguration.RoleBasedGroupApplyConfiguration {
	if rbg == nil {
		return nil
	}
	gkv := utils.GetRbgGVK()
	rbgApplyConfig := applyconfiguration.RoleBasedGroup(rbg.Name, rbg.Namespace).
		WithKind(gkv.Kind).
		WithAPIVersion(gkv.GroupVersion().String()).
		WithStatus(applyconfiguration.RoleBasedGroupStatus().WithRoleStatuses(ToRoleStatusApplyConfiguration(rbg.Status.RoleStatuses)...).WithConditions(ToConditionApplyConfigurations(rbg.Status.Conditions)...))
	return rbgApplyConfig
}

func ToRoleStatusApplyConfiguration(roleStatus []workloadsv1alpha1.RoleStatus) []*applyconfiguration.RoleStatusApplyConfiguration {
	out := make([]*applyconfiguration.RoleStatusApplyConfiguration, 0, len(roleStatus))
	for _, rs := range roleStatus {
		out = append(out, applyconfiguration.RoleStatus().
			WithName(rs.Name).
			WithReplicas(rs.Replicas).
			WithReadyReplicas(rs.ReadyReplicas).
			WithUpdatedReplicas(rs.UpdatedReplicas))
	}
	return out
}

func ToConditionApplyConfigurations(conds []metav1.Condition) []*metav1ac.ConditionApplyConfiguration {
	out := make([]*metav1ac.ConditionApplyConfiguration, 0, len(conds))
	for _, c := range conds {
		out = append(out, metav1ac.Condition().
			WithType(c.Type).
			WithStatus(c.Status).
			WithReason(c.Reason).
			WithMessage(c.Message).
			WithLastTransitionTime(c.LastTransitionTime))
	}
	return out
}

func (r *PodReconciler) podToRBG(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return []reconcile.Request{}
	}

	rbgName := pod.Labels[workloadsv1alpha1.SetNameLabelKey]
	if rbgName == "" {
		return []reconcile.Request{}
	}

	if !utils.ContainerRestarted(pod) && !utils.PodDeleted(pod) {
		return []reconcile.Request{}
	}

	logger := log.FromContext(ctx).WithValues("Pod.Namespace", pod.Namespace, "Pod.Name", pod.Name)
	logger.V(1).Info("Processing Pod event for reconciliation")

	var rbg workloadsv1alpha1.RoleBasedGroup
	err := r.client.Get(ctx, types.NamespacedName{Name: rbgName, Namespace: pod.Namespace}, &rbg)
	if err != nil || rbg.DeletionTimestamp != nil {
		return []reconcile.Request{}
	}

	// if rbg is in restart status, it means that a pod has already been restarted and the rbg is in restarting process now.
	// So, skip to handle this pod restart event to avoid restarting rbg repeatedly.
	if restartConditionTrue(rbg.Status) {
		logger.V(1).Info("rbg is already in restart status, skip handle pod restart event")
		return []reconcile.Request{}
	}

	roleName := pod.Labels[workloadsv1alpha1.SetRoleLabelKey]
	if roleName == "" {
		return []reconcile.Request{}
	}

	curRole, err := rbg.GetRole(roleName)
	if err != nil {
		return []reconcile.Request{}
	}

	// 1. if RestartPolicy is None, do nothing
	// 2. if RestartPolicy is RecreateRoleInstanceOnPodRestart, the lws controller will recreate lws. RBG controller does nothing.
	if curRole.RestartPolicy != workloadsv1alpha1.RecreateRBGOnPodRestart {
		return []reconcile.Request{}
	}

	// restart rbg
	return []reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      rbgName,
				Namespace: rbg.Namespace,
			},
		},
	}
}

func (r *PodReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	podPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPod, ok1 := e.ObjectOld.(*corev1.Pod)
			newPod, ok2 := e.ObjectNew.(*corev1.Pod)
			if ok1 && ok2 {
				_, oldExist := oldPod.Labels[workloadsv1alpha1.SetNameLabelKey]
				_, newExist := newPod.Labels[workloadsv1alpha1.SetNameLabelKey]
				return oldExist && newExist

			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			if pod, ok := e.Object.(*corev1.Pod); ok {
				_, exist := pod.Labels[workloadsv1alpha1.SetNameLabelKey]
				return exist
			}
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		Named("pod-controller").
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.podToRBG), builder.WithPredicates(podPredicate)).
		Complete(r)
}
