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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/rbgs/api/workloads/constants"
	workloadsv1alpha2 "sigs.k8s.io/rbgs/api/workloads/v1alpha2"
)

const (
	// ReasonEngineRuntimeProfileMutatedWhileReferenced is the warning event
	// the controller emits on a referenced profile whose live generation no
	// longer matches status.referencedGeneration — a spec edit the
	// immutability webhook did not block (absent, misconfigured, or
	// uninstalled). The event fires once per drift, on the Frozen condition's
	// reason flip, not on every reconcile.
	ReasonEngineRuntimeProfileMutatedWhileReferenced = "EngineRuntimeProfileMutatedWhileReferenced"
)

// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=clusterengineruntimeprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=clusterengineruntimeprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroups,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=workloads.x-k8s.io,resources=rolebasedgroupsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// ClusterEngineRuntimeProfileReconciler maintains the KEP-531 reference
// tracking: status.referenceCount, the referencedGeneration baseline and
// the Frozen condition, plus the webhook-bypass detection event. Enforcement
// is the admission webhook's; status is display and detection surface only —
// rendering never consults it, and it is never authoritative.
type ClusterEngineRuntimeProfileReconciler struct {
	client.Client
	Recorder record.EventRecorder
}

func NewClusterEngineRuntimeProfileReconciler(mgr ctrl.Manager) *ClusterEngineRuntimeProfileReconciler {
	return &ClusterEngineRuntimeProfileReconciler{
		Client:   mgr.GetClient(),
		Recorder: mgr.GetEventRecorderFor("clusterengineruntimeprofile-controller"),
	}
}

// Reconcile recomputes the profile's live reference set and writes status
// only on change. The referencedGeneration baseline records the profile's
// metadata.generation on the none-to-referenced transition, never refreshes
// while referenced, and clears when the last reference leaves; a live
// generation that drifts from it is precise evidence of a spec edit made
// while referenced, surfacing as the Frozen condition's drifted reason and a
// one-shot warning event.
func (r *ClusterEngineRuntimeProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	profile := &workloadsv1alpha2.ClusterEngineRuntimeProfile{}
	if err := r.Get(ctx, req.NamespacedName, profile); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// A profile mid-deletion is going away; tracking on a terminating
	// object only races the delete.
	if !profile.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx).WithValues("clusterengineruntimeprofile", profile.Name)
	ctx = ctrl.LoggerInto(ctx, logger)

	// The controller is the label's authority during convergence: it scans
	// through the informer cache (no apiserver List cost) and uses the
	// label-independent variant, so reference counting stays correct while
	// pre-label objects are being backfilled.
	refs, err := workloadsv1alpha2.ListAllEngineRuntimeProfileReferences(ctx, r.Client, profile.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing engine runtime profile references: %w", err)
	}

	desired := *profile.Status.DeepCopy()
	changed := false

	desired.ReferenceCount = int32(len(refs))
	if desired.ReferenceCount != profile.Status.ReferenceCount {
		changed = true
	}

	mutatedWhileReferenced := false
	if len(refs) > 0 {
		if desired.ReferencedGeneration == 0 {
			desired.ReferencedGeneration = profile.Generation
			changed = true
		}
		reason := workloadsv1alpha2.ReasonReferenced
		message := fmt.Sprintf(
			"%d live reference(s); the profile's spec is frozen while referenced", len(refs))
		if profile.Generation != desired.ReferencedGeneration {
			reason = workloadsv1alpha2.ReasonSpecChangedWhileReferenced
			message = fmt.Sprintf(
				"the spec changed since the baseline was recorded: metadata.generation %d no longer matches the "+
					"referencedGeneration baseline %d — a write the webhook let through, or a legal edit in a "+
					"zero-reference window the controller never observed",
				profile.Generation, desired.ReferencedGeneration)
			// Bypass detection: emit the warning once, on the reason flip from
			// Referenced (or from no condition) into the drifted reason. The
			// signal fires on the spec-change paths the webhook did not block,
			// evidence of the webhook's absence rather than silence; a
			// same-name recreate (a CREATE, not a spec change) is the watch's
			// one blind spot among them.
			previous := apimeta.FindStatusCondition(profile.Status.Conditions, workloadsv1alpha2.FrozenConditionType)
			mutatedWhileReferenced = previous == nil || previous.Reason != workloadsv1alpha2.ReasonSpecChangedWhileReferenced
		}
		if apimeta.SetStatusCondition(&desired.Conditions, metav1.Condition{
			Type:               workloadsv1alpha2.FrozenConditionType,
			Status:             metav1.ConditionTrue,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: profile.Generation,
		}) {
			changed = true
		}
	} else {
		if desired.ReferencedGeneration != 0 {
			desired.ReferencedGeneration = 0
			changed = true
		}
		if apimeta.RemoveStatusCondition(&desired.Conditions, workloadsv1alpha2.FrozenConditionType) {
			changed = true
		}
	}

	if changed {
		profile.Status = desired
		if err := r.Status().Update(ctx, profile); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating engine runtime profile status: %w", err)
		}
	}

	if mutatedWhileReferenced {
		r.Recorder.Event(profile, corev1.EventTypeWarning, ReasonEngineRuntimeProfileMutatedWhileReferenced,
			fmt.Sprintf(
				"the spec of referenced engine runtime profile %q changed since the baseline was recorded (generation %d, baseline %d) — a write the webhook did not block or a legal edit in a zero-reference window never observed; check the validating webhook configuration; renders read the drifted spec live",
				profile.Name, profile.Generation, desired.ReferencedGeneration))
	}

	// Backfill after the status write and after the warning: a failed label
	// patch must not block status tracking or lose the one-shot bypass event.
	// It still returns an error so the reconcile retries and the admission
	// webhook keeps its unscoped fallback until the scan has completed cleanly.
	if err := r.backfillHasEngineRuntimesLabels(ctx); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// backfillHasEngineRuntimesLabels converges the has-engine-runtimes label
// onto RoleBasedGroups and RoleBasedGroupSets whose writes predate the
// label (the upgrade path), or bypassed the admission defaulters
// (--enable-webhooks=none). The scan runs on the manager's informer cache;
// only objects whose label state disagrees with their spec are patched, so
// steady state costs one cached List and no writes. Label-only patches do
// not change metadata.generation, so they do not re-trigger this
// controller's watches; a mismatching object is patched only once.
func (r *ClusterEngineRuntimeProfileReconciler) backfillHasEngineRuntimesLabels(ctx context.Context) error {
	if workloadsv1alpha2.EngineRuntimeLabelConverged() {
		// Every admitted write maintains the label, so a converged process
		// only re-scans on restart. Objects written with webhooks disabled
		// (--enable-webhooks=none) can drift; that is a debugging mode and
		// the admission fallback is irrelevant without webhooks anyway.
		return nil
	}
	epoch := workloadsv1alpha2.BeginEngineRuntimeLabelScan()
	var allErrs []error
	rbgList := &workloadsv1alpha2.RoleBasedGroupList{}
	if err := r.List(ctx, rbgList); err != nil {
		return fmt.Errorf("listing rolebasedgroups for engine runtime label backfill: %w", err)
	}
	for i := range rbgList.Items {
		rbg := &rbgList.Items[i]
		if engineRuntimeLabelMatches(rbg.Labels, roleSpecHasEngineRuntimes(&rbg.Spec)) {
			continue
		}
		base := rbg.DeepCopy()
		workloadsv1alpha2.SetHasEngineRuntimesLabel(rbg)
		if err := r.Patch(ctx, rbg, client.MergeFrom(base)); err != nil {
			if !apierrors.IsNotFound(err) {
				allErrs = append(allErrs, fmt.Errorf("backfilling engine runtime label on rolebasedgroup %s/%s: %w", rbg.Namespace, rbg.Name, err))
			}
		}
	}

	rbgsList := &workloadsv1alpha2.RoleBasedGroupSetList{}
	if err := r.List(ctx, rbgsList); err != nil {
		return fmt.Errorf("listing rolebasedgroupsets for engine runtime label backfill: %w", err)
	}
	for i := range rbgsList.Items {
		rbgs := &rbgsList.Items[i]
		if engineRuntimeLabelMatches(rbgs.Labels, roleSpecHasEngineRuntimes(&rbgs.Spec.GroupTemplate.Spec)) {
			continue
		}
		base := rbgs.DeepCopy()
		workloadsv1alpha2.SetRoleBasedGroupSetHasEngineRuntimesLabel(rbgs)
		if err := r.Patch(ctx, rbgs, client.MergeFrom(base)); err != nil {
			if !apierrors.IsNotFound(err) {
				allErrs = append(allErrs, fmt.Errorf("backfilling engine runtime label on rolebasedgroupset %s/%s: %w", rbgs.Namespace, rbgs.Name, err))
			}
		}
	}
	if len(allErrs) > 0 {
		return utilerrors.NewAggregate(allErrs)
	}
	// A clean full scan with no writes and no concurrent drift: the scoped
	// admission lookup can take over from the unscoped fallback.
	workloadsv1alpha2.CompleteEngineRuntimeLabelScan(epoch)
	return nil
}

func engineRuntimeLabelMatches(labels map[string]string, references bool) bool {
	if references {
		return labels[constants.HasEngineRuntimesLabelKey] == constants.HasEngineRuntimesLabelValue
	}
	_, present := labels[constants.HasEngineRuntimesLabelKey]
	return !present
}

// SetupWithManager registers the controller. The CERP watch carries the
// tracking; the RBG and RBGSet watches (spec generation only, so status
// writes never loop) enqueue the profiles each object's roles reference,
// so a reference added or removed recomputes the count.
func (r *ClusterEngineRuntimeProfileReconciler) SetupWithManager(mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(&workloadsv1alpha2.ClusterEngineRuntimeProfile{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&workloadsv1alpha2.RoleBasedGroup{},
			handler.EnqueueRequestsFromMapFunc(profilesReferencedByRBG),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&workloadsv1alpha2.RoleBasedGroupSet{},
			handler.EnqueueRequestsFromMapFunc(profilesReferencedByRBGSet),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("clusterengineruntimeprofile-controller").
		Complete(r)
}

// profilesReferencedByRBG maps a RoleBasedGroup to the cluster-scoped
// profiles its roles' engineRuntimes entries name.
func profilesReferencedByRBG(_ context.Context, obj client.Object) []reconcile.Request {
	rbg, ok := obj.(*workloadsv1alpha2.RoleBasedGroup)
	if !ok {
		return nil
	}
	flagLabelDrift(rbg.Labels, roleSpecHasEngineRuntimes(&rbg.Spec))
	return profilesReferencedBy(&rbg.Spec)
}

// roleSpecHasEngineRuntimes reports whether any role of the spec references
// an engine runtime profile.
func roleSpecHasEngineRuntimes(spec *workloadsv1alpha2.RoleBasedGroupSpec) bool {
	for i := range spec.Roles {
		if len(spec.Roles[i].EngineRuntimes) > 0 {
			return true
		}
	}
	return false
}

// flagLabelDrift un-converges the label scan when a referencing object
// arrives without the has-engine-runtimes label — a write that bypassed the
// admission defaulters. The next profile reconcile then re-runs the
// backfill, so a converged process does not stay blind to bypassed writes.
func flagLabelDrift(labels map[string]string, references bool) {
	if references {
		if _, ok := labels[constants.HasEngineRuntimesLabelKey]; !ok {
			workloadsv1alpha2.MarkEngineRuntimeLabelDrift()
		}
	}
}

// profilesReferencedByRBGSet maps a RoleBasedGroupSet to the profiles named
// by its group template's roles — a set's group template counts as a live
// reference even at zero replicas, no child RoleBasedGroup required.
func profilesReferencedByRBGSet(_ context.Context, obj client.Object) []reconcile.Request {
	rbgs, ok := obj.(*workloadsv1alpha2.RoleBasedGroupSet)
	if !ok {
		return nil
	}
	flagLabelDrift(rbgs.Labels, roleSpecHasEngineRuntimes(&rbgs.Spec.GroupTemplate.Spec))
	return profilesReferencedBy(&rbgs.Spec.GroupTemplate.Spec)
}

func profilesReferencedBy(spec *workloadsv1alpha2.RoleBasedGroupSpec) []reconcile.Request {
	var requests []reconcile.Request
	seen := make(map[string]struct{})
	for i := range spec.Roles {
		for _, er := range spec.Roles[i].EngineRuntimes {
			if er.ProfileName == "" {
				continue
			}
			if _, dup := seen[er.ProfileName]; dup {
				continue
			}
			seen[er.ProfileName] = struct{}{}
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Name: er.ProfileName}})
		}
	}
	return requests
}
