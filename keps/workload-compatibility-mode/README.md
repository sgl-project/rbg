# KEP: Remove Workload Field from v1alpha2 API

## Summary

This KEP proposes removing the deprecated `workload` field from v1alpha2 `RoleSpec` and replacing it with a role-level annotation mechanism. The workload type will now be specified via a role annotation `rbg.workloads.x-k8s.io/role-workload-type`, which is primarily used by the conversion webhook for v1alpha1 to v1alpha2 compatibility. New v1alpha2 RoleBasedGroups should not use this annotation as the default workload type (RoleInstanceSet) is appropriate for most use cases.

## Motivation

In v1alpha2 API, the `workload` field in `RoleSpec` is deprecated. However, keeping it in the schema creates confusion for users and doesn't provide a clear deprecation path. By removing the field entirely and using annotations for backward compatibility, we:

1. **Clean API Design**: Remove deprecated field from the schema, making the API cleaner
2. **Clear Migration Path**: v1alpha1 RoleBasedGroups converted to v1alpha2 will have workload type preserved via role annotations
3. **Prevent Misuse**: New v1alpha2 RBGs cannot accidentally use deprecated workload types through the schema
4. **Internal Use Only**: The annotation is primarily for conversion webhook compatibility, not for general user consumption

### Goals

- Remove `Workload` field and `WorkloadSpec` struct from v1alpha2 `RoleSpec`
- Add role annotation `rbg.workloads.x-k8s.io/role-workload-type` for specifying workload type
- Modify conversion webhook to set the annotation based on v1alpha1 workload field
- Modify controller to read workload type from annotation (default to RoleInstanceSet if not set)

### Non-Goals

- Change default workload behavior (RoleInstanceSet remains the default)
- Support user-specified annotation for new RBGs (this is for conversion compatibility only)
- Modify v1alpha1 API

## Proposal

### Design Overview

The implementation consists of three components:

1. **API Change**: Remove `Workload` field from v1alpha2 `RoleSpec`
2. **Annotation**: Add role-level annotation `rbg.workloads.x-k8s.io/role-workload-type`
3. **Conversion Logic**: Set annotation during v1alpha1 -> v1alpha2 conversion
4. **Controller Logic**: Read workload type from annotation instead of field

### User Stories

#### Story 1: v1alpha1 Migration

An existing v1alpha1 RoleBasedGroup with StatefulSet workload:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha1
kind: RoleBasedGroup
spec:
  roles:
  - name: worker
    workload:
      apiVersion: apps/v1
      kind: StatefulSet
```

After conversion to v1alpha2:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
spec:
  roles:
  - name: worker
    annotations:
      rbg.workloads.x-k8s.io/role-workload-type: "apps/v1/StatefulSet"
    # ... other role fields
```

The controller reads the annotation and creates StatefulSet as expected.

#### Story 2: New v1alpha2 RBG

A new v1alpha2 RoleBasedGroup is created without workload annotation:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
spec:
  roles:
  - name: worker
    # no workload annotation - defaults to RoleInstanceSet
```

The controller uses the default workload type `workloads.x-k8s.io/v1alpha2/RoleInstanceSet`.

## Design Details

### Implementation

#### 1. Add Annotation Constant

File: `api/workloads/constants/annotation.go`

```go
// Role level annotations
const (
    // RoleWorkloadTypeAnnotationKey specifies the workload type for a role.
    // This is primarily used by the conversion webhook when converting v1alpha1
    // RoleBasedGroups that had workload field set. New v1alpha2 RBGs should
    // not use this annotation as the default (RoleInstanceSet) is appropriate.
    // Format: "apiVersion/kind" e.g., "apps/v1/StatefulSet"
    RoleWorkloadTypeAnnotationKey = RBGPrefix + "role-workload-type"
)
```

#### 2. Remove Workload Field from v1alpha2

File: `api/workloads/v1alpha2/rolebasedgroup_types.go`

Remove from `RoleSpec`:
```go
// Remove this block:
// Workload type specification
// Deprecated: This field is deprecated and will be removed in future versions.
// The underlying workload will use InstanceSet.
// +kubebuilder:default={apiVersion:"workloads.x-k8s.io/v1alpha2", kind:"RoleInstanceSet"}
// +optional
Workload WorkloadSpec `json:"workload,omitempty"`
```

Also remove `WorkloadSpec` struct if no longer needed.

#### 3. Modify Conversion Webhook

File: `api/workloads/v1alpha1/rolebasedgroup_conversion.go`

In `convertRoleV1alpha1ToV2`:
```go
func convertRoleV1alpha1ToV2(src *RoleSpec, dst *v2.RoleSpec) error {
    dst.Name = src.Name
    dst.Labels = src.Labels
    dst.Annotations = src.Annotations
    
    // If v1alpha1 has workload set, preserve it via role annotation
    if src.Workload.APIVersion != "" && src.Workload.Kind != "" {
        if dst.Annotations == nil {
            dst.Annotations = make(map[string]string)
        }
        workloadType := fmt.Sprintf("%s/%s", src.Workload.APIVersion, src.Workload.Kind)
        dst.Annotations[constants.RoleWorkloadTypeAnnotationKey] = workloadType
    }
    
    // ... rest of conversion
}
```

In `convertRoleV2ToV1alpha1`:
```go
func convertRoleV2ToV1alpha1(src *v2.RoleSpec, dst *RoleSpec) error {
    dst.Name = src.Name
    dst.Labels = src.Labels
    dst.Annotations = src.Annotations
    
    // Restore workload from annotation
    if src.Annotations != nil {
        workloadType := src.Annotations[constants.RoleWorkloadTypeAnnotationKey]
        if workloadType != "" {
            parts := strings.Split(workloadType, "/")
            if len(parts) >= 2 {
                dst.Workload.APIVersion = strings.Join(parts[:len(parts)-1], "/")
                dst.Workload.Kind = parts[len(parts)-1]
            }
        }
    }
    
    // ... rest of conversion
}
```

#### 4. Add Helper Function in v1alpha2

File: `api/workloads/v1alpha2/rolebasedgroup_types.go`

```go
// GetWorkloadType returns the workload type for this role.
// It reads from the annotation if set, otherwise returns the default.
func (r *RoleSpec) GetWorkloadType() string {
    if r.Annotations != nil {
        if wt := r.Annotations[constants.RoleWorkloadTypeAnnotationKey]; wt != "" {
            return wt
        }
    }
    return constants.RoleInstanceSetWorkloadType // default
}

// GetWorkloadSpec returns WorkloadSpec based on annotation or default.
func (r *RoleSpec) GetWorkloadSpec() WorkloadSpec {
    wt := r.GetWorkloadType()
    parts := strings.Split(wt, "/")
    if len(parts) >= 2 {
        return WorkloadSpec{
            APIVersion: strings.Join(parts[:len(parts)-1], "/"),
            Kind:       parts[len(parts)-1],
        }
    }
    // Return default
    return WorkloadSpec{
        APIVersion: "workloads.x-k8s.io/v1alpha2",
        Kind:       "RoleInstanceSet",
    }
}
```

#### 5. Modify Controller

File: `internal/controller/workloads/rolebasedgroup_controller.go`

Replace `role.Workload` with `role.GetWorkloadSpec()` or `role.GetWorkloadType()`:

```go
// Before:
workloadReconciler, err := r.getOrCreateWorkloadReconciler(roleCtx, role.Workload)

// After:
workloadReconciler, err := r.getOrCreateWorkloadReconciler(roleCtx, role.GetWorkloadSpec())
```

### Test Plan

#### Unit Tests

- Test `GetWorkloadType()` returns annotation value when set
- Test `GetWorkloadType()` returns default when annotation not set
- Test conversion webhook sets annotation correctly
- Test conversion webhook restores workload from annotation

#### Integration Tests

- Test v1alpha1 -> v1alpha2 conversion preserves workload via annotation
- Test v1alpha2 -> v1alpha1 conversion restores workload from annotation

### Graduation Criteria

This feature is complete when:

1. Workload field removed from v1alpha2 schema
2. Annotation constant added
3. Conversion webhook updated
4. Controller updated to use annotation
5. All tests pass

### Upgrade / Downgrade Strategy

- **Upgrade**: v1alpha1 objects converted to v1alpha2 via webhook, annotation set automatically
- **Downgrade**: v1alpha2 objects converted back to v1alpha1, workload field restored from annotation

## Alternatives

### Alternative 1: Keep Workload Field (Rejected)

Keep the deprecated field in the schema. This was rejected because it doesn't provide a clear migration path and may confuse users.

### Alternative 2: Validating Admission Webhook

Reject workload field via webhook. This adds complexity and requires additional webhook infrastructure.

## Implementation History

- 2026-04-21: KEP created with new design (annotation-based approach)