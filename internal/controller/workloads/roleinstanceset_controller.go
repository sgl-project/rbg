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
	"sigs.k8s.io/rbgs/api/workloads/constants"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statefulmode"
	"sigs.k8s.io/rbgs/pkg/reconciler/roleinstanceset/statelessmode"
	"sigs.k8s.io/rbgs/pkg/utils"
)

type RoleInstanceSetReconciler struct {
	statelessMode reconcile.Reconciler
	statefulMode  reconcile.Reconciler
	client        client.Client
	apiReader     client.Reader
	recorder      record.EventRecorder
}

func NewRoleInstanceSetReconciler(mgr ctrl.Manager) *RoleInstanceSetReconciler {
	return &RoleInstanceSetReconciler{
		statelessMode: statelessmode.NewReconciler(mgr),
		statefulMode:  statefulmode.NewReconciler(mgr),
		client:        mgr.GetClient(),
		apiReader:     mgr.GetAPIReader(),
		recorder:      mgr.GetEventRecorderFor("roleinstanceset-controller"),
	}
}

// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=roleinstancesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=roleinstancesets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=roleinstancesets/finalizers,verbs=update
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=roleinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=roleinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=controllerrevisions,verbs=get;list;watch;create;update;patch;delete

func (r *RoleInstanceSetReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	set := &v1alpha2.RoleInstanceSet{}
	if err := r.client.Get(ctx, request.NamespacedName, set); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// Dispatch based on the role instance pattern annotation
	pattern := constants.InstancePatternType(set.Annotations[constants.RoleInstancePatternKey])
	switch pattern {
	case constants.StatelessPattern:
		return r.statelessMode.Reconcile(ctx, request)
	case constants.StatefulPattern, "":
		// Empty pattern defaults to stateful mode as the default behavior
		return r.statefulMode.Reconcile(ctx, request)
	default:
		err := fmt.Errorf("unknown role instance pattern %q", pattern)
		r.recorder.Event(set, corev1.EventTypeWarning, "UnknownRoleInstancePattern", err.Error())
		return reconcile.Result{}, err
	}
}

func (r *RoleInstanceSetReconciler) CheckCrdExists() error {
	crds := []string{
		"roleinstancesets.workloads.x-k8s.io",
	}

	for _, crd := range crds {
		if err := utils.CheckCrdExists(r.apiReader, crd); err != nil {
			return err
		}
	}
	return nil
}

func (r *RoleInstanceSetReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&v1alpha2.RoleInstanceSet{}).
		Owns(&v1alpha2.RoleInstance{}).
		Watches(&v1alpha2.RoleInstance{}, statelessmode.NewRoleInstanceEventHandler(mgr.GetClient())).
		Complete(r)
}
