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

package v1alpha1

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/rbgs/api/workloads/constants"
)

const v1alpha1DeprecationWarning = "workloads.x-k8s.io/v1alpha1 is deprecated and will be removed in 0.10.0; migrate to v1alpha2 using the migration guide"

// DeprecationWarningValidator returns an Admission Warning for every write to a
// v1alpha1 resource. The API server formats these strings as `Warning: 299 - ...`.
//
// +kubebuilder:object:generate=false
type DeprecationWarningValidator struct{}

func (DeprecationWarningValidator) ValidateCreate(
	_ context.Context, obj runtime.Object,
) (admission.Warnings, error) {
	return warnV1alpha1(obj), nil
}

func (DeprecationWarningValidator) ValidateUpdate(
	_ context.Context, _, newObj runtime.Object,
) (admission.Warnings, error) {
	return warnV1alpha1(newObj), nil
}

func (DeprecationWarningValidator) ValidateDelete(
	_ context.Context, obj runtime.Object,
) (admission.Warnings, error) {
	return warnV1alpha1(obj), nil
}

func warnV1alpha1(obj runtime.Object) admission.Warnings {
	gvk := obj.GetObjectKind().GroupVersionKind()
	kind := gvk.Kind
	if meta, ok := obj.(interface{ GetName() string }); ok && meta.GetName() != "" {
		kind = fmt.Sprintf("%s %s", kind, meta.GetName())
	}
	klog.V(4).InfoS(v1alpha1DeprecationWarning, "kind", kind)
	return admission.Warnings{v1alpha1DeprecationWarning}
}

// deprecatedWorkloadValidator warns on every write. While deprecated workload
// compatibility is enabled, existing roles are exempt but new uses are rejected.
// When compatibility is disabled, every remaining use is rejected.
type deprecatedWorkloadValidator struct {
	EnableDeprecatedWorkloadTypes bool
}

func (v *deprecatedWorkloadValidator) ValidateCreate(
	_ context.Context, obj runtime.Object,
) (admission.Warnings, error) {
	warnings := warnV1alpha1(obj)
	roles, fieldPath, err := deprecatedWorkloadRoles(obj)
	if err != nil {
		return warnings, err
	}
	return warnings, validateV1alpha1DeprecatedRoles(fieldPath, roles, nil, v.EnableDeprecatedWorkloadTypes)
}

func (v *deprecatedWorkloadValidator) ValidateUpdate(
	_ context.Context, oldObj, newObj runtime.Object,
) (admission.Warnings, error) {
	warnings := warnV1alpha1(newObj)
	newRoles, fieldPath, err := deprecatedWorkloadRoles(newObj)
	if err != nil {
		return warnings, err
	}

	// oldObj is the authoritative persisted object captured by the API server and
	// already carries v1alpha1 schema defaults. Re-reading it from the (eventually
	// consistent) cache can neither improve the decision nor fail open: a stale
	// read can wrongly reject a grandfathered update or wrongly extend the
	// exemption, and any read error would fail-close an otherwise valid write.
	oldRoles, _, err := deprecatedWorkloadRoles(oldObj)
	if err != nil {
		return warnings, err
	}

	return warnings, validateV1alpha1DeprecatedRoles(fieldPath, newRoles, oldRoles, v.EnableDeprecatedWorkloadTypes)
}

func (v *deprecatedWorkloadValidator) ValidateDelete(
	_ context.Context, obj runtime.Object,
) (admission.Warnings, error) {
	return warnV1alpha1(obj), nil
}

func deprecatedWorkloadRoles(obj runtime.Object) (roles []RoleSpec, fieldPath string, err error) {
	switch typed := obj.(type) {
	case *RoleBasedGroup:
		return typed.Spec.Roles, "spec.roles", nil
	case *RoleBasedGroupSet:
		return typed.Spec.Template.Roles, "spec.template.roles", nil
	default:
		return nil, "", fmt.Errorf("expected RoleBasedGroup or RoleBasedGroupSet but got %T", obj)
	}
}

func validateV1alpha1DeprecatedRoles(
	fieldPath string,
	newRoles, oldRoles []RoleSpec,
	allowExisting bool,
) error {
	oldWorkloadTypes := make(map[string]string, len(oldRoles))
	for i := range oldRoles {
		oldWorkloadTypes[oldRoles[i].Name] = workloadType(oldRoles[i].Workload)
	}

	var errs []error
	for i := range newRoles {
		role := &newRoles[i]
		workloadType := workloadType(role.Workload)
		if !isDeprecatedV1alpha1WorkloadType(workloadType) {
			continue
		}
		if allowExisting {
			// Type-exact grandfathering, matching the v1alpha2 validator: an
			// existing role keeps its own deprecated type, but switching between
			// deprecated types provisions a new deprecated workload and counts
			// as a new use.
			if oldType, existed := oldWorkloadTypes[role.Name]; existed && oldType == workloadType {
				continue
			}
			errs = append(errs, fmt.Errorf(
				"%s[%d] (role %q): workload type %q is deprecated; new uses are not accepted, migrate the role to RoleInstanceSet",
				fieldPath, i, role.Name, workloadType,
			))
			continue
		}
		errs = append(errs, fmt.Errorf(
			"%s[%d] (role %q): workload type %q is deprecated and not enabled on this cluster",
			fieldPath, i, role.Name, workloadType,
		))
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

func workloadType(spec WorkloadSpec) string {
	if spec.APIVersion == "" && spec.Kind == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s", spec.APIVersion, spec.Kind)
}

func isDeprecatedV1alpha1WorkloadType(workloadType string) bool {
	switch workloadType {
	case constants.DeploymentWorkloadType, constants.StatefulSetWorkloadType, constants.LeaderWorkerSetWorkloadType:
		return true
	default:
		return false
	}
}

// SetupDeprecationWebhookWithManager registers the warning and new-use guard for
// the v1alpha1 RoleBasedGroup endpoint.
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha1-rolebasedgroup,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=rolebasedgroups,verbs=create;update,versions=v1alpha1,name=vrolebasedgroup-v1alpha1-deprecation.kb.io,admissionReviewVersions=v1,matchPolicy=Exact

func (r *RoleBasedGroup) SetupDeprecationWebhookWithManager(mgr ctrl.Manager, enableDeprecatedWorkloadTypes bool) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(&deprecatedWorkloadValidator{
			EnableDeprecatedWorkloadTypes: enableDeprecatedWorkloadTypes,
		}).
		Complete()
}

// SetupDeprecationWebhookWithManager registers the warning and new-use guard for
// the v1alpha1 RoleBasedGroupSet endpoint.
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha1-rolebasedgroupset,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=rolebasedgroupsets,verbs=create;update,versions=v1alpha1,name=vrolebasedgroupset-v1alpha1-deprecation.kb.io,admissionReviewVersions=v1,matchPolicy=Exact

func (r *RoleBasedGroupSet) SetupDeprecationWebhookWithManager(mgr ctrl.Manager, enableDeprecatedWorkloadTypes bool) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(&deprecatedWorkloadValidator{
			EnableDeprecatedWorkloadTypes: enableDeprecatedWorkloadTypes,
		}).
		Complete()
}

// SetupV1alpha1WarningWebhook registers one warning-only endpoint covering the
// four v1alpha1 kinds that carry no enforcing gate: InstanceSet, Instance,
// RoleBasedGroupScalingAdapter, and ClusterEngineRuntimeProfile. All four get
// the identical deprecation warning, so a single handler answers every kind and
// the marker lists all four resources.
//
// The endpoint is registered with failurePolicy=Ignore: the handler can never
// reject, so failing closed would block writes to these still-supported kinds
// during a webhook outage for no enforcement value.
//
// +kubebuilder:webhook:path=/validate-workloads-v1alpha1-deprecation,mutating=false,failurePolicy=ignore,sideEffects=None,groups=workloads.x-k8s.io,resources=instancesets;instances;rolebasedgroupscalingadapters;clusterengineruntimeprofiles,verbs=create;update,versions=v1alpha1,name=vv1alpha1-warning-deprecation.kb.io,admissionReviewVersions=v1,matchPolicy=Exact

func SetupV1alpha1WarningWebhook(mgr ctrl.Manager) error {
	mgr.GetWebhookServer().Register("/validate-workloads-v1alpha1-deprecation", &admission.Webhook{
		Handler: v1alpha1WarningHandler{},
	})
	return nil
}

// v1alpha1WarningHandler answers every warning-only v1alpha1 kind with the
// deprecation warning. The response never depends on the object, so no decoding
// is needed.
type v1alpha1WarningHandler struct{}

var _ admission.Handler = v1alpha1WarningHandler{}

func (v1alpha1WarningHandler) Handle(_ context.Context, _ admission.Request) admission.Response {
	return admission.Allowed("").WithWarnings(v1alpha1DeprecationWarning)
}
