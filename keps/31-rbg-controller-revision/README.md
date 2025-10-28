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

- On upgrade, a ControllerRevision will be created for each existing RBG object.  
- We add revision-related labels to the Role workloads under an RBG. Under normal circumstances, this will not trigger a restart of the Pods in those workloads.

## Design Details

### API Changes
None

---

### ControllerRevision Creation

**Default Behavior**: Keep the latest 5 ControllerRevisions.

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

Example generated ControllerRevision:

```yaml
apiVersion: apps/v1
kind: ControllerRevision
metadata:
  labels:
    rolebasedgroup.workloads.x-k8s.io/controller-revision-hash: <rbg-hash>
    rolebasedgroup.workloads.x-k8s.io/name: nginx-cluster
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
    $path: replace
    roles:
    - ...
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  labels:
    rolebasedgroup.workloads.x-k8s.io/group-unique-key: 4c81010f509b0ea495e76e1d66ed42ae9b0dc5ef
    rolebasedgroup.workloads.x-k8s.io/name: nginx-cluster
    rolebasedgroup.workloads.x-k8s.io/role: leader
    rolebasedgroup.workloads.x-k8s.io/role-revision-hash-leader: <role-1-hash>
  name: nginx-cluster-leader
  namespace: default
  ownerReferences:
  - apiVersion: workloads.x-k8s.io/v1alpha1
    blockOwnerDeletion: true
    controller: true
    kind: RoleBasedGroup
    name: nginx-cluster
    uid: 8de247f1-a5aa-474d-90e9-1aacf3e50563
spec:
  ...
```

---

### Reconciliation Logic (RBG Controller)
```go
func Reconcile() {
    currentRevision, err := r.getCurrentRevision(ctx, rbg)
    expectedRevision, err := utils.NewRevision(ctx, r.client, rbg)
    if !utils.EqualRevision(currentRevision, expectedRevision) {
      err := r.client.Create(ctx, expectedRevision)
    }

    reconciler.Reconciler(roleCtx, rbg, role, roleRevisionKey);
    ...
    _, err := utils.CleanExpiredRevision(ctx, r.client, rbg)
}
```
We now use each role's own revision, instead of the RBG's revision, as the label for its **workload objects** (e.g., LWS/STS). This prevents scenarios where a change to role-1 in the RBG inadvertently triggers an update of role-2 within the same RBG.

---

### Update Strategies(Workload, eg. LWS)
```go
func (r *LeaderWorkerSetReconciler) Reconciler(
	ctx context.Context, rbg *workloadsv1alpha1.RoleBasedGroup, role *workloadsv1alpha1.RoleSpec,
	revisionKey string) error {
        lwsApplyConfig, err := r.constructLWSApplyConfiguration(ctx, rbg, role, revisionKey)
        semanticallyEqual, err := semanticallyEqualLeaderWorkerSet(oldLWS, newLWS, false)
        
        revisionHashEqual := newLWS.Labels[roleHashKey] == oldLWS.Labels[roleHashKey]

        if semanticallyEqual && revisionHashEqual {
            logger.Info("lws equal, skip reconcile")
            return nil
        }
    }
```

To ensure that changes to the fields of interest are detected in real time and applied to the cluster, and that modifications to the Spec of a specific Role under an RBG are promptly reflected in the cluster, we determine whether to trigger an update action using two validation checks, taking the union of their results:

- Semantic consistency check for specific fields.
- Revision change check to verify whether the Role’s revision has changed.

---

## Lifecycle Management
- Each ControllerRevision created by RBG will have an `OwnerReference` to its RBG, ensuring their lifecycles align.
- Historical versions exceeding 5 will be **automatically garbage-collected**.
