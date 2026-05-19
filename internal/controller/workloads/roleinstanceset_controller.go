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
	"regexp"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
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
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// Clean up RoleInstances that belong to a different pattern than the current one.
	// This handles the case where the pattern annotation is changed (stateful ↔ stateless),
	// leaving behind instances from the old pattern that the new reconciler won't manage.
	if requeue, err := r.cleanupStaleInstances(ctx, set); err != nil || requeue {
		return reconcile.Result{Requeue: requeue}, err
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

// cleanupStaleInstances lists all RoleInstances owned by the set and deletes those
// that don't belong to the current pattern. Returns true if any instance was deleted
// (caller should requeue to wait for deletion and let the new reconciler create fresh instances).
func (r *RoleInstanceSetReconciler) cleanupStaleInstances(ctx context.Context, set *v1alpha2.RoleInstanceSet) (bool, error) {
	selector, err := metav1.LabelSelectorAsSelector(set.Spec.Selector)
	if err != nil {
		return false, err
	}

	instanceList := &v1alpha2.RoleInstanceList{}
	if err := r.client.List(ctx, instanceList,
		client.InNamespace(set.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		return false, err
	}

	pattern := constants.InstancePatternType(set.Annotations[constants.RoleInstancePatternKey])
	var deleted bool
	for i := range instanceList.Items {
		instance := &instanceList.Items[i]
		if !metav1.IsControlledBy(instance, set) {
			continue
		}
		if instanceBelongsToPattern(instance, set, pattern) {
			continue
		}
		klog.InfoS("Deleting stale RoleInstance from different pattern", "instanceSet", klog.KObj(set), "instance", klog.KObj(instance), "currentPattern", pattern)
		if err := r.client.Delete(ctx, instance); err != nil && !errors.IsNotFound(err) {
			return false, err
		}
		deleted = true
	}
	return deleted, nil
}

// statefulInstanceNameRegex extracts the parent name and ordinal from a stateful instance name.
var statefulInstanceNameRegex = regexp.MustCompile(`^(.*)-(\d+)$`)

// instanceBelongsToPattern returns true if the instance was created by the given pattern.
func instanceBelongsToPattern(instance *v1alpha2.RoleInstance, set *v1alpha2.RoleInstanceSet, pattern constants.InstancePatternType) bool {
	switch pattern {
	case constants.StatelessPattern:
		return instance.Labels[constants.RoleInstanceOwnerLabelKey] == string(set.UID)
	case constants.StatefulPattern, "":
		// Stateful instances follow the ordinal naming pattern: {setName}-{ordinal}
		subMatch := statefulInstanceNameRegex.FindStringSubmatch(instance.Name)
		return len(subMatch) == 3 && subMatch[1] == set.Name
	default:
		return true
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
