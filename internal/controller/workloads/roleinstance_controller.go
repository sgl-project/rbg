package workloads

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstance"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type RoleInstanceReconciler struct {
	reconcileFunc reconcile.Func
	apiReader     client.Reader
}

func NewRoleInstanceReconciler(mgr ctrl.Manager) *RoleInstanceReconciler {
	reconciler := roleinstance.NewReconciler(mgr)
	return &RoleInstanceReconciler{
		reconcileFunc: reconciler.Reconcile,
		apiReader:     mgr.GetAPIReader(),
	}
}

// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=roleinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=roleinstances/status,verbs=get;update;patch;create;delete;list;watch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=roleinstances/finalizers,verbs=update

func (r *RoleInstanceReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	return r.reconcileFunc(ctx, request)
}

func (r *RoleInstanceReconciler) CheckCrdExists() error {
	crds := []string{
		"roleinstances.workloads.x-k8s.io",
	}

	for _, crd := range crds {
		if err := utils.CheckCrdExists(r.apiReader, crd); err != nil {
			return err
		}
	}
	return nil
}

func (r *RoleInstanceReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&v1alpha2.RoleInstance{}).
		Watches(&corev1.Pod{}, roleinstance.NewPodEventHandler()).
		Complete(r)
}
