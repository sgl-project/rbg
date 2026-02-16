package workloads

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset/statefulmode"
	"sigs.k8s.io/rbgs/pkg/reconciler/instanceset/statelessmode"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type InstanceSetReconciler struct {
	statelessMode reconcile.Reconciler
	statefulMode  reconcile.Reconciler
	client        client.Client
	apiReader     client.Reader
	recorder      record.EventRecorder
}

func NewInstanceSetReconciler(mgr ctrl.Manager) *InstanceSetReconciler {
	return &InstanceSetReconciler{
		statelessMode: statelessmode.NewReconciler(mgr),
		statefulMode:  statefulmode.NewReconciler(mgr),
		client:        mgr.GetClient(),
		apiReader:     mgr.GetAPIReader(),
		recorder:      mgr.GetEventRecorderFor("instanceset-controller"),
	}
}

// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instancesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instancesets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instancesets/finalizers,verbs=update
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=controllerrevisions,verbs=get;list;watch;create;update;patch;delete

func (r *InstanceSetReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	set := &v1alpha1.InstanceSet{}
	if err := r.client.Get(ctx, request.NamespacedName, set); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// Dispatch based on the instance pattern label
	pattern := v1alpha1.InstancePatternType(set.Labels[v1alpha1.RBGInstancePatternLabelKey])
	switch pattern {
	case v1alpha1.StatelessInstancePattern:
		return r.statelessMode.Reconcile(ctx, request)
	case v1alpha1.StatefulInstancePattern, "":
		// Empty pattern defaults to stateful mode for backward compatibility
		return r.statefulMode.Reconcile(ctx, request)
	default:
		err := fmt.Errorf("unknown instance pattern %q", pattern)
		r.recorder.Event(set, corev1.EventTypeWarning, "UnknownInstancePattern", err.Error())
		return reconcile.Result{}, err
	}
}

func (r *InstanceSetReconciler) CheckCrdExists() error {
	crds := []string{
		"instancesets.workloads.x-k8s.io",
	}

	for _, crd := range crds {
		if err := utils.CheckCrdExists(r.apiReader, crd); err != nil {
			return err
		}
	}
	return nil
}

func (r *InstanceSetReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&v1alpha1.InstanceSet{}).
		Watches(&v1alpha1.Instance{}, statelessmode.NewInstanceEventHandler(mgr.GetClient())).
		Complete(r)
}
