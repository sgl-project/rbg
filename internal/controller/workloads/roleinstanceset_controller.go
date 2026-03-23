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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/rbgs/api/workloads/constants"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha2"
	portallocator "sigs.k8s.io/rbgs/pkg/port-allocator"
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
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instancesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=instancesets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=controllerrevisions,verbs=get;list;watch;create;update;patch;delete

func (r *RoleInstanceSetReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	set := &v1alpha2.RoleInstanceSet{}
	if err := r.client.Get(ctx, request.NamespacedName, set); err != nil {
		if errors.IsNotFound(err) {
			if cleanupErr := r.cleanupSubresources(ctx, request.Namespace, request.Name); cleanupErr != nil {
				klog.ErrorS(cleanupErr, "Failed to cleanup subresources for deleted InstanceSet", "instanceSet", request.NamespacedName)
				return reconcile.Result{RequeueAfter: 10 * time.Second}, cleanupErr
			}
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

func (r *RoleInstanceSetReconciler) cleanupSubresources(ctx context.Context, namespace, name string) error {
	if err := portallocator.CleanupInstanceSetPorts(ctx, r.client, namespace, name); err != nil {
		return err
	}
	return nil
}
