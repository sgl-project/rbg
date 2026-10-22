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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	klog.Warningf("%s: %s", kind, v1alpha1DeprecationWarning)
	return admission.Warnings{v1alpha1DeprecationWarning}
}

// deprecatedWorkloadValidator warns on every write. While deprecated workload
// compatibility is enabled, existing roles are exempt but new uses are rejected.
// When compatibility is disabled, every remaining use is rejected.
type deprecatedWorkloadValidator struct {
	Client                        client.Client
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
	ctx context.Context, oldObj, newObj runtime.Object,
) (admission.Warnings, error) {
	warnings := warnV1alpha1(newObj)
	newRoles, fieldPath, err := deprecatedWorkloadRoles(newObj)
	if err != nil {
		return warnings, err
	}

	oldRoles, _, err := deprecatedWorkloadRoles(oldObj)
	if err != nil {
		return warnings, err
	}
	// Prefer the persisted object because oldObj is decoded from the request and
	// may not carry v1alpha1 schema defaults.
	if key, ok := objectKey(newObj); ok && v.Client != nil {
		persisted := newObj.DeepCopyObject()
		if persistedObject, canGet := persisted.(client.Object); canGet {
			if err := v.Client.Get(ctx, key, persistedObject); err == nil {
				if persistedRoles, _, roleErr := deprecatedWorkloadRoles(persistedObject); roleErr == nil {
					oldRoles = persistedRoles
				}
			} else if !apierrors.IsNotFound(err) {
				return warnings, err
			}
		}
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
			if oldType, existed := oldWorkloadTypes[role.Name]; existed && isDeprecatedV1alpha1WorkloadType(oldType) {
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

func objectKey(obj runtime.Object) (types.NamespacedName, bool) {
	meta, ok := obj.(client.Object)
	if !ok {
		return types.NamespacedName{}, false
	}
	return types.NamespacedName{Namespace: meta.GetNamespace(), Name: meta.GetName()}, true
}

// SetupDeprecationWebhookWithManager registers the warning and new-use guard for
// the v1alpha1 RoleBasedGroup endpoint.
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha1-rolebasedgroup,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=rolebasedgroups,verbs=create;update;delete,versions=v1alpha1,name=vrolebasedgroup-v1alpha1-deprecation.kb.io,admissionReviewVersions=v1,matchPolicy=Exact

func (r *RoleBasedGroup) SetupDeprecationWebhookWithManager(mgr ctrl.Manager, enableDeprecatedWorkloadTypes bool) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(&deprecatedWorkloadValidator{
			Client:                        mgr.GetClient(),
			EnableDeprecatedWorkloadTypes: enableDeprecatedWorkloadTypes,
		}).
		Complete()
}

// SetupDeprecationWebhookWithManager registers the warning and new-use guard for
// the v1alpha1 RoleBasedGroupSet endpoint.
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha1-rolebasedgroupset,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=rolebasedgroupsets,verbs=create;update;delete,versions=v1alpha1,name=vrolebasedgroupset-v1alpha1-deprecation.kb.io,admissionReviewVersions=v1,matchPolicy=Exact

func (r *RoleBasedGroupSet) SetupDeprecationWebhookWithManager(mgr ctrl.Manager, enableDeprecatedWorkloadTypes bool) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		WithValidator(&deprecatedWorkloadValidator{
			Client:                        mgr.GetClient(),
			EnableDeprecatedWorkloadTypes: enableDeprecatedWorkloadTypes,
		}).
		Complete()
}

// SetupDeprecationWebhookWithManager registers the warning-only v1alpha1 InstanceSet endpoint.
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha1-instanceset,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=instancesets,verbs=create;update;delete,versions=v1alpha1,name=vinstanceset-v1alpha1-deprecation.kb.io,admissionReviewVersions=v1,matchPolicy=Exact

func (r *InstanceSet) SetupDeprecationWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(r).WithValidator(&DeprecationWarningValidator{}).Complete()
}

// SetupDeprecationWebhookWithManager registers the warning-only v1alpha1 Instance endpoint.
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha1-instance,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=instances,verbs=create;update;delete,versions=v1alpha1,name=vinstance-v1alpha1-deprecation.kb.io,admissionReviewVersions=v1,matchPolicy=Exact

func (r *Instance) SetupDeprecationWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(r).WithValidator(&DeprecationWarningValidator{}).Complete()
}

// SetupDeprecationWebhookWithManager registers the warning-only v1alpha1 ScalingAdapter endpoint.
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha1-rolebasedgroupscalingadapter,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=rolebasedgroupscalingadapters,verbs=create;update;delete,versions=v1alpha1,name=vrolebasedgroupscalingadapter-v1alpha1-deprecation.kb.io,admissionReviewVersions=v1,matchPolicy=Exact

func (r *RoleBasedGroupScalingAdapter) SetupDeprecationWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(r).WithValidator(&DeprecationWarningValidator{}).Complete()
}

// SetupDeprecationWebhookWithManager registers the warning-only v1alpha1 ClusterEngineRuntimeProfile endpoint.
// +kubebuilder:webhook:path=/validate-workloads-x-k8s-io-v1alpha1-clusterengineruntimeprofile,mutating=false,failurePolicy=fail,sideEffects=None,groups=workloads.x-k8s.io,resources=clusterengineruntimeprofiles,verbs=create;update;delete,versions=v1alpha1,name=vclusterengineruntimeprofile-v1alpha1-deprecation.kb.io,admissionReviewVersions=v1,matchPolicy=Exact

func (r *ClusterEngineRuntimeProfile) SetupDeprecationWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(r).WithValidator(&DeprecationWarningValidator{}).Complete()
}
