# Manually migrating from v1alpha1 to v1alpha2

This guide applies to RBG `0.9.0`. In this release, `v1alpha1` is still served so existing objects remain readable and writable, but every write returns an API warning and new uses of the deprecated workload types are rejected. `v1alpha1` will stop being served in `0.10.0`.

## Scope and responsibility

RBG intentionally does **not** provide an executable migration command or script. A migration changes running workloads and may recreate Pods, so the operator must review each role, decide the rollout order, pause when business conditions require it, and submit changes through the organization's normal change-management process.

The Kubernetes examples in this document are limited to inspection or explicit object writes that an operator performs one object at a time. They are not an automated migration. If your organization uses GitOps, a policy engine, or another deployment workflow, use that workflow instead of the examples.
Workload-type content migration must use the `v1alpha2` representation and write path. `v1alpha1` remains served for compatibility, but its `spec.roles[].workload` field is authoritative and conversion recreates the compatibility annotation. Do not perform the annotation-only migration edit through `v1alpha1`.

## What is being migrated

There are two independent tracks:

1. **API encoding (v1alpha1 to v1alpha2).**  
   Objects created through `v1alpha1` and never updated may still be encoded as `v1alpha1` in etcd. The storage-version step rewrites them through the `v1alpha2` endpoint. It does not change object content.

2. **Deprecated workload types (Deployment, StatefulSet, LeaderWorkerSet).**  
   A role may carry `rbg.workloads.x-k8s.io/role-workload-type` to select one of these older workload kinds. The replacement is `RoleInstanceSet`, the `v1alpha2` default. Changing the workload Kind recreates the role's Pods.

Finishing one track does not imply that the other is complete. Check both before upgrading beyond `0.9.0`.

## Prerequisites

- Upgrade the RBG controller to `0.9.0` or later and confirm the controller and webhooks are healthy.
- Confirm the roles being migrated are currently ready and have no unrelated rollout in progress.
- Confirm each role has an existing Pattern and a resolvable `template` or `templateRef`. A role without these is already invalid for reconciliation; repair or remove it through a separate change before migration.
- Coordinate with autoscaling, traffic management, scheduling, and any business process that can change the role while it is being migrated.
- Freeze external writes to the object during the change window.
- Save the current object and record its revision before editing it.

`controller.deprecatedWorkloadTypes.enabled` defaults to `true` during the migration window. Existing roles that already use a deprecated workload type continue to reconcile, while new roles and roles that did not previously use a deprecated type are rejected. RBAC, watches, and orphan cleanup remain active in this mode so operators can migrate roles and remove old workloads. Set the value to `false` only after every role has migrated; that is the final cutoff, and all remaining uses are rejected.

## 1. Inventory the affected roles

Review the current objects and their role annotations:

```bash
kubectl get rolebasedgroups -A -o yaml
kubectl get rolebasedgroupsets -A -o yaml
```

The role locations are:

- `RoleBasedGroup`: `spec.roles`
- `v1alpha2 RoleBasedGroupSet`: `spec.groupTemplate.spec.roles`
- `v1alpha1 RoleBasedGroupSet`: `spec.template.roles`

For every role, inspect `rbg.workloads.x-k8s.io/role-workload-type`:

| Annotation | Meaning | Action |
| --- | --- | --- |
| Missing | The role uses the default `RoleInstanceSet`. | No workload migration is required. |
| `workloads.x-k8s.io/v1alpha2/RoleInstanceSet` | Equivalent to the default. | Optionally remove the redundant annotation. Preserve the existing Pattern. |
| `apps/v1/Deployment` | The role still depends on a Deployment. | Manual workload migration is required. |
| `apps/v1/StatefulSet` | The role still depends on a StatefulSet. | Manual workload migration is required. |
| `leaderworkerset.x-k8s.io/v1/LeaderWorkerSet` | The role still depends on a LeaderWorkerSet. | Manual workload migration is required. |
| Any other value | Unknown compatibility value. | Stop and review the object with its owner; do not guess. |

This inventory is an impact report, not a rollout decision. The operator decides which role to migrate first and when to pause.

## 2. Migrate one role at a time

For each role that uses a deprecated workload type:

1. Save the current `v1alpha2` representation for audit and review. For a plain Kubernetes workflow:

   ```bash
   kubectl get rolebasedgroups.v1alpha2.workloads.x-k8s.io RBG_NAME -o yaml > RBG_NAME.pre-migration.yaml
   ```

2. Identify the Pods that the downstream workload currently owns. They will be recreated when the workload Kind changes. For example:

   ```bash
   kubectl get pods -A \
     -l rbg.workloads.x-k8s.io/group-name=RBG_NAME,rbg.workloads.x-k8s.io/role-name=ROLE_NAME \
     -o wide
   ```

3. Review the role's existing `Pattern`:
   - Deployment and StatefulSet roles use `standalonePattern`.
   - LeaderWorkerSet roles use `leaderWorkerPattern`.
   - Preserve the existing template source, replicas, size, patches, rollout strategy, and restart-policy configuration.
   - A missing Pattern, or a Pattern with no resolvable template source, is a pre-existing object-health problem, not a migration step. Stop and repair or remove that role through a separate change.
   - If the existing Pattern conflicts with the old workload type, stop and design the change with the workload owner. Do not remove the annotation merely to make the object pass validation.

4. Edit only the selected role in the `v1alpha2` representation:
   - Remove `rbg.workloads.x-k8s.io/role-workload-type` from that role's annotations.
   - Leave the annotations and specs of all other roles unchanged.
   - Preserve the correct Pattern described above.
   - For a redundant `RoleInstanceSet` annotation, remove only the annotation and leave the Pattern unchanged.

5. Submit the single-role `v1alpha2` change through your normal deployment workflow. If the source of truth is still a v1alpha1 manifest, first review and convert that source to v1alpha2. Do not batch multiple roles in one change.

6. Observe the replacement:
   - The controller treats the old Deployment, StatefulSet, or LeaderWorkerSet as an orphan and deletes it.
   - The controller creates a RoleInstanceSet.
   - All Pods for that role are recreated.

7. Verify the role before continuing:

   ```bash
   kubectl get rolebasedgroup RBG_NAME -o wide
   kubectl describe rolebasedgroup RBG_NAME
   kubectl get events --field-selector involvedObject.name=RBG_NAME
   ```

   Also inspect the Pods and any traffic, readiness, cache-warmup, or business-specific health signal that matters to the service.

8. Stop, pause, or adjust the rollout whenever business conditions require it. Do not continue to the next role merely because the object is writable.

A `RoleBasedGroupSet` is not an independent per-child staging mechanism. Its controller compares generated children with `groupTemplate` and copies the template roles back into them. Migrating a child while the set template still carries the legacy workload type is therefore unstable: the controller may try to restore the legacy role, and the admission guard rejects that new use.

Updating the set template can propagate the migration to all remaining children, so it is a set-wide rollout decision rather than a one-role change. If per-child staging is required, use an explicit, separately reviewed plan that coordinates or isolates the child from set reconciliation. Do not assume a child can be permanently migrated while its parent template remains unchanged.

## Rollback

Before each role migration, retain an immutable copy of the object, its resource version, and any revision history used by your deployment process. These records remain useful for audit and for restoring non-workload fields, but they do not make the workload-type change reversible.

Once a role has switched to `RoleInstanceSet`, restoring the saved deprecated workload type is a new deprecated use. The v1alpha2 and v1alpha1 validators reject it even while `controller.deprecatedWorkloadTypes.enabled=true`; that compatibility mode preserves existing roles, but does not authorize reintroducing a deprecated type after migration. Do not begin migration until the business owner has approved a recovery plan for a failed RoleInstanceSet rollout.

If the old workload was already removed, do not manually recreate it with the same name. Let the controller own the replacement workload. A recovery after submission must use the pre-agreed emergency plan rather than a simple restore of the deprecated workload type.

## 3. Rewrite objects in the v1alpha2 storage encoding

Only begin this track after every identified deprecated workload type has been migrated and verified ready.

For every RoleBasedGroup and RoleBasedGroupSet:

1. Read the object through the `v1alpha2` endpoint.
2. Save and review the returned object.
3. Write the same object back through the `v1alpha2` endpoint. A normal update with the current resourceVersion is sufficient; the spec and status should not change.
4. Confirm the write succeeded, then continue to the next object.

One explicit, non-batched Kubernetes workflow is:

```bash
kubectl get rolebasedgroups.v1alpha2.workloads.x-k8s.io RBG_NAME -o yaml > RBG_NAME.storage-rewrite.yaml
# Review RBG_NAME.storage-rewrite.yaml, then write it back:
kubectl replace -f RBG_NAME.storage-rewrite.yaml
```

Use the equivalent fully qualified `v1alpha2` resource for RoleBasedGroupSets. If the write conflicts because the object changed, fetch a fresh copy, review it, and decide whether to retry.

Do not round-trip the object through `v1alpha1`: that path can reintroduce `role-workload-type` compatibility annotations and create noise in later checks.

After every object has been rewritten, independently confirm from the migration inventory that the rewrite set was complete. `status.storedVersions` is a CRD-level historical record, not a per-object inventory, and it does not automatically discard `v1alpha1` after objects are rewritten. Repeating object writes cannot remove an old entry from this list.

Only after the complete rewrite has been confirmed, update each CRD status through an explicitly reviewed change:

```bash
kubectl patch customresourcedefinition rolebasedgroups.workloads.x-k8s.io \
  --subresource=status --type=merge \
  -p '{"status":{"storedVersions":["v1alpha2"]}}'

kubectl patch customresourcedefinition rolebasedgroupsets.workloads.x-k8s.io \
  --subresource=status --type=merge \
  -p '{"status":{"storedVersions":["v1alpha2"]}}'
```

Then verify both conversion-enabled CRDs:

```bash
kubectl get customresourcedefinition \
  rolebasedgroups.workloads.x-k8s.io \
  rolebasedgroupsets.workloads.x-k8s.io \
  -o custom-columns=CRD:.metadata.name,STORED_VERSIONS:.status.storedVersions
```

Each value must contain only `v1alpha2`. If `v1alpha1` remains before the status update, do not infer which objects are still old from that list; reconfirm the rewrite inventory first. Never remove the historical entry while objects encoded as `v1alpha1` remain.

## 4. Complete the migration window

1. Repeat the role inventory and resolve every remaining deprecated workload type.
2. Confirm from the migration inventory that every object was rewritten, then update and verify both `status.storedVersions` values as described above.
3. Only after every role has migrated, set `controller.deprecatedWorkloadTypes.enabled=false` and verify that no write is rejected.
4. Move manifests, clients, and automation to `v1alpha2`.
5. Retire the saved migration artifacts only after the rollback window agreed with the business owner has elapsed.

## Risks and failure modes

- **Pod recreation:** changing the workload Kind is destructive for the selected role. All Pods for that role can be recreated during migration.
- **Partial readiness:** migrate one role at a time. Do not automate the full set of roles.
- **Business state:** readiness alone may not be sufficient. Databases, caches, session state, traffic draining, and scheduler behavior must be reviewed by the service owner.
- **Stale old workloads:** While `controller.deprecatedWorkloadTypes.enabled=true`, orphan cleanup can remove the old workload after a role switches to `RoleInstanceSet`. Do not manually delete it unless the controller cannot, because manual deletion can race with replacement creation. Keep the compatibility mode enabled until every role has migrated; setting it to `false` prematurely disables legacy RBAC, watches, and orphan cleanup and can leave old workloads behind.
- **Storage ghosts:** `status.storedVersions` records storage versions that have been used. Confirm a complete object rewrite before removing `v1alpha1` from that list; if old-encoded objects remain when the API version is removed, they can become unreadable. Complete the storage inventory, status update, and verification before upgrading beyond `0.9.0`.
- **Silent type fallback:** deleting the workload-type annotation without completing the content migration changes the role to RoleInstanceSet. Make that change only after the Pattern and business impact have been reviewed.
