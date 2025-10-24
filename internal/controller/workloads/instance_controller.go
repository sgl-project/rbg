package workloads

import (
	"context"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/reconciler/instance"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type InstanceReconciler struct {
	reconcileFunc reconcile.Func
	apiReader     client.Reader
}

func NewInstanceReconciler(mgr ctrl.Manager) *InstanceReconciler {
	reconciler := instance.NewReconciler(mgr)
	return &InstanceReconciler{
		reconcileFunc: reconciler.Reconcile,
		apiReader:     mgr.GetAPIReader(),
	}
}

func (i *InstanceReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	return i.reconcileFunc(ctx, request)
}

func (i *InstanceReconciler) CheckCrdExists() error {
	crds := []string{
		"instances.workloads.x-k8s.io",
	}

	for _, crd := range crds {
		if err := utils.CheckCrdExists(i.apiReader, crd); err != nil {
			return err
		}
	}
	return nil
}

func (i *InstanceReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&v1alpha1.Instance{}, builder.WithPredicates(predicate.Funcs{
			UpdateFunc: func(e event.TypedUpdateEvent[client.Object]) bool {
				oldInstance := e.ObjectOld.(*v1alpha1.Instance)
				newInstance := e.ObjectNew.(*v1alpha1.Instance)
				if oldInstance.GetGeneration() != newInstance.GetGeneration() {
					klog.InfoS("Observed updated Spec", "Instance", klog.KObj(newInstance))
					return true
				}
				return false
			},
		})).
		Watches(&v1.Pod{}, instance.NewPodEventHandler()).
		Complete(i)
}
