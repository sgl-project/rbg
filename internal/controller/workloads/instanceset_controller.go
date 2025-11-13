package workloads

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type InstanceSetReconciler struct {
	reconcileFunc reconcile.Func
	apiReader     client.Reader
}

func NewInstanceSetReconciler(mgr ctrl.Manager) *InstanceSetReconciler {
	reconciler := instanceset.NewReconciler(mgr)
	return &InstanceSetReconciler{
		reconcileFunc: reconciler.Reconcile,
		apiReader:     mgr.GetAPIReader(),
	}
}

// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instancesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instancesets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instancesets/finalizers,verbs=update

func (i *InstanceSetReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	return i.reconcileFunc(ctx, request)
}

func (i *InstanceSetReconciler) CheckCrdExists() error {
	crds := []string{
		"instancesets.workloads.x-k8s.io",
	}

	for _, crd := range crds {
		if err := utils.CheckCrdExists(i.apiReader, crd); err != nil {
			return err
		}
	}
	return nil
}

func (i *InstanceSetReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&v1alpha1.InstanceSet{}).
		Watches(&v1alpha1.Instance{}, instanceset.NewInstanceEventHandler(mgr.GetClient())).
		Complete(i)
}
