# KEP: ControllerRevision Support for RoleBasedGroup (RBG)

## Summary
This KEP proposes adding **ControllerRevision** support to the RoleBasedGroup (RBG) object to store its historical states.

## Motivation
Currently, RBG does not store any historical state. Even a cluster administrator cannot inspect the previous version of an RBG configuration. This enhancement introduces ControllerRevision to preserve historical versions and enables:

1. **Historical Tracking**  
   By storing RBG history via ControllerRevision, we prepare for fine-grained canary/gradual rollout capabilities in the future.
   
2. **Explicit Version Awareness**  
   Adding a version field to explicitly indicate whether the workloads managed by an RBG meet the version requirement.  
   Today, RBG decides whether an update is needed based solely on semantic equality of specific fields. This prevents users from triggering updates when changing other non-specific fields.

## Goals
- Store historical states of RBG objects to determine whether the RBG itself or its managed workloads have changed.
- Ensure backward compatibility with the existing semantic equality–based rolling update mechanism.

## Non-Goals
- **No change** to existing update strategies of RBG or its managed workloads (`MaxSurge`, `MaxUnavailable`).
- No direct introduction of *rollback* functionality (will consider in future design).
- No immediate implementation of fine-grained rollout (canary) for RBGs.

## Proposal
We propose adding **ControllerRevision** support in the RBG Operator to store the full historical configuration of each RBG for change detection and future rollout/rollback purposes.

### User Stories
**Story 1: Cluster Administrator**
> As a cluster administrator, I can inspect ControllerRevisions of an RBG to view its historical configurations.

**Story 2: Regular User**
> As a regular user, any change to the `Template` fields of the RBG will be reflected in the actual workloads. I can simply monitor the RBG’s status to understand the actual workload state in the cluster.

**Risks and Mitigations**
1. On upgrade, a ControllerRevision will be created for each existing RBG object.  
   - Since RBG manages workloads (not only PodTemplates), we will add a **Revision Hash Label** directly to workloads (e.g., LWS, StatefulSets).  
   - If a restart is triggered due to adding this label, it is considered **expected** behavior, as it indicates that previous unsynced changes are now being reconciled.

## Design Details

### API Changes
We extend the `RoleBasedGroup` API with a new optional field:

```go
type RoleSpec struct {
    // Indicates the number of historical revisions to keep.
    // If unspecified, defaults to 10.
    // +optional
    RevisionHistoryLimit *int32 `json:"revisionHistoryLimit,omitempty"`
}
```

**Default Behavior**: Keep the latest 10 ControllerRevisions if not specified.

The historical version of an RBG will **contain all Role definitions**(`RoleBasedGroupSpec.Roles`) it manages.

```go
type RoleBasedGroupSpec struct {
    // +kubebuilder:pruning:PreserveUnknownFields
    // +kubebuilder:validation:MinItems=1
    // +kubebuilder:validation:Required
    Roles []RoleSpec `json:"roles"`

    // Optional: PodGroup policy for gang scheduling.
    PodGroupPolicy *PodGroupPolicy `json:"podGroupPolicy,omitempty"`
}
```

We will store **all fields** in `RoleSpec` (not only `podTemplate`), to enable flexible future evolution.

---

### ControllerRevision Creation
Example generated ControllerRevision:

```yaml
apiVersion: apps/v1
kind: ControllerRevision
metadata:
  labels:
    rolebasedgroup.workloads.x-k8s.io/name: rbg-with-lws-workload
    rolebasedgroup.workloads.x-k8s.io/hash: <hash>
  name: rbg-with-lws-workload-hash-1
  namespace: default
  ownerReferences:
  - apiVersion: workloads.x-k8s.io/v1alpha1
    blockOwnerDeletion: true
    controller: true
    kind: RoleBasedGroup
    name: rbg-with-lws-workload
revision: 1
data:
  spec:
    roles:
      $patch: replace
      ...
```

---

### Reconciliation Logic (RBG Controller)
```go
func Reconcile() {
    revision, err := r.getOrCreateRevisionIfNonExist(rbg)
    updatedRevision := getUpdatedRevision(rbg, revision)
    lwsUpdated := updatedRevision != nil

    if lwsUpdated {
        revision := CreateRevision(updatedRevision)
    }

    SSAWithRoles(GetRevisionKey(revision))
    updateDone := updateStatus()
    if updateDone {
        TruncateRevisions(GetRevisionKey(revision))
    }
}
```

We apply the ControllerRevision’s hash to **workload objects** (e.g., LWS/STSs) instead of Pods, to avoid unrelated workload restarts when only a subset of roles changes.

---

### Update Strategies

#### Option 1 — Compare Hash Before Update
- If a role's ControllerRevision hash matches the RBG's latest hash, skip update for that role.
- **Risk**: Other cluster components modifying RBG-managed workload could cause drift, potentially leading to service issues.

```go
if semanticallyEqualLeaderWorkerSet(oldLWS, revisionKey) {
    logger.Info("Revision hash equal, skip reconcile")
    return nil
}
```

#### Option 2 — Always Apply (Server-Side Apply)
- Treat hash as metadata only; always apply the latest spec via SSA every reconcile loop.
- **Safer**, ensures workloads always match the RBG spec.
- But it will **cause many unnecessary apply requests**.

#### Option 3 - Compare Hash and semantically
- We check both semantic equivalence and whether the controllerRevision hash version matches the latest revision hash of the RBG to determine if an update is needed.

```go
if semanticallyEqualLeaderWorkerSet(oldLWS, newLWS, revisionKey) {
    logger.Info("LWS and revision hash equal, skip reconcile")
    return nil
}
```
- This approach ensures that the fields we care about in the workloads managed by the RBG are not modified by other components, while also guaranteeing that when the RBG itself changes, the full RBG information can be synchronized to the managed workloads.

---

## Lifecycle Management
- Each ControllerRevision created by RBG will have an `OwnerReference` to its RBG, ensuring their lifecycles align.
- Historical versions exceeding `revisionHistoryLimit` will be **automatically garbage-collected**.
