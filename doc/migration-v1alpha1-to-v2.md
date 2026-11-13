# Manually migrating from v1alpha1 to v1alpha2

This guide applies to RBG `0.9.0`. In this release, `v1alpha1` is still served so existing objects remain readable and writable, every `v1alpha1` request returns a CRD deprecation warning, and new uses of the deprecated workload types are rejected. `v1alpha1` will stop being served in `0.10.0`.

The storage-version rewrite below applies to `RoleBasedGroup` and `RoleBasedGroupSet`, the two CRDs that use conversion webhooks. The workload migration also covers the special v1alpha1 `InstanceSet` role selector. For other v1alpha1 kinds, use the replacement named by the CRD deprecation warning and review the v1alpha2 schema before changing manifests or clients.

## Scope and responsibility

RBG intentionally does **not** provide an executable migration command or script. A migration changes running workloads and may recreate Pods, so the operator must review each role, choose the rollout order, pause when business conditions require it, and submit changes through the organization's normal change-management process.

The Kubernetes examples in this document are limited to inspection or explicit object writes performed one object at a time. They are not an automated migration. If your organization uses GitOps, a policy engine, or another deployment workflow, use that workflow instead of the examples.

Workload-type content migration must use the `v1alpha2` representation and write path. In `v1alpha1`, `spec.roles[].workload` is authoritative and conversion recreates the compatibility annotation from it. Do not perform the annotation-only migration edit through `v1alpha1`.

## What is being migrated

There are two independent tracks:

1. **API encoding (v1alpha1 to v1alpha2).** Objects created through `v1alpha1` and never updated may still be encoded as `v1alpha1` in etcd. The storage-version step rewrites them through the `v1alpha2` endpoint. It does not change object content.
2. **Legacy workload selectors.** `Deployment`, `StatefulSet`, and `LeaderWorkerSet` roles are compatibility-gated and must move to `RoleInstanceSet`. A v1alpha1 `InstanceSet` selector is different: the v0.7.0-and-later controller has no InstanceSet reconciler, so such roles do not reconcile in either compatibility mode.

Finishing one track does not imply that the other is complete. Check both before upgrading beyond `0.9.0`.

## Prerequisites

- Upgrade the RBG controller to `0.9.0` or later and confirm that the controller, conversion webhook, and admission webhook are healthy.
- Confirm the roles being migrated are currently ready and have no unrelated rollout in progress. An `InstanceSet` role is an exception: it has not reconciled since v0.7.0, and its unsupported workload type prevents the whole RoleBasedGroup from passing preflight reconciliation.
- Confirm each role has an existing Pattern and a resolvable `template` or `templateRef`. A role without these is already invalid for reconciliation; repair or remove it through a separate change before migration.
- Coordinate with autoscaling, traffic management, scheduling, and any business process that can change the role while it is being migrated.
- Freeze external writes to the object during the change window.
- Save the current object and record its revision before editing it.

`controller.deprecatedWorkloadTypes.enabled` defaults to `true` during the migration window. Existing `Deployment`, `StatefulSet`, and `LeaderWorkerSet` roles continue to reconcile, while new uses are rejected. RBAC, watches, and orphan cleanup remain active so operators can migrate roles and remove old workloads. Set the value to `false` only after the inventory is empty, reconciliation has completed, and the old RBG-owned workloads are gone.

Compatibility is type-exact. An existing role keeps its own deprecated workload type; renaming the role, adding a role, or switching a role from one deprecated type to another is a new use and is rejected. Migrate the role to `RoleInstanceSet` instead. The v1alpha1 schema also defaults an absent `spec.roles[].workload` to `apps/v1 StatefulSet`, so a new v1alpha1 role that appears not to name a workload is still a new StatefulSet use.

## 1. Inventory the affected roles

Run an inventory that handles both API shapes. The command must exit successfully; only then does empty output mean that no role still selects a legacy workload:

```bash
inventory="$(kubectl get rbg,rbgs -A -o json)" &&
printf '%s' "$inventory" | jq -r '.items[] | . as $o
  | (.spec.roles // .spec.groupTemplate.spec.roles // .spec.template.roles // [])[]
  | (if .workload.kind then "\(.workload.apiVersion)/\(.workload.kind)"
     else ((.annotations // {})["rbg.workloads.x-k8s.io/role-workload-type"] // "")
     end) as $wt
  | select(($wt | test("/(Deployment|StatefulSet|LeaderWorkerSet)$"))
           or $wt == "workloads.x-k8s.io/v1alpha1/InstanceSet")
  | "\($o.kind) \($o.metadata.namespace)/\($o.metadata.name) role=\(.name) \($wt)"'
```

For v1alpha1 roles, the direct `workload` field takes precedence over a stale annotation. For v1alpha2 roles, which have no `workload` field, the command reads `rbg.workloads.x-k8s.io/role-workload-type`. Role locations are:

- `RoleBasedGroup`: `spec.roles`
- `v1alpha2 RoleBasedGroupSet`: `spec.groupTemplate.spec.roles`
- `v1alpha1 RoleBasedGroupSet`: `spec.template.roles`

Interpret the inventory as follows:

| Workload selector | Meaning | Action |
| --- | --- | --- |
| Missing or `workloads.x-k8s.io/v1alpha2/RoleInstanceSet` | The role uses the v1alpha2 default. | No workload migration is required. |
| `apps/v1/Deployment` | The role still depends on a Deployment. | Migrate to `RoleInstanceSet`. |
| `apps/v1/StatefulSet` | The role still depends on a StatefulSet. | Migrate to `RoleInstanceSet`. |
| `leaderworkerset.x-k8s.io/v1/LeaderWorkerSet` | The role still depends on a LeaderWorkerSet. | Migrate to `RoleInstanceSet`. |
| `workloads.x-k8s.io/v1alpha1/InstanceSet` | The selector has not been reconciled since v0.7.0 and blocks the whole RBG. | Migrate to `RoleInstanceSet`; then delete the old InstanceSet manually. |
| Any other value | Unknown compatibility value. | Stop and review the object with its owner; do not guess. |

The inventory is an impact report, not a rollout decision. The operator decides which role to migrate first and when to pause.

## 2. Migrate one role at a time

For each `Deployment`, `StatefulSet`, or `LeaderWorkerSet` role:

1. Save the current `v1alpha2` representation for audit and review:

   ```bash
   kubectl get rolebasedgroups.v1alpha2.workloads.x-k8s.io RBG_NAME -o yaml > RBG_NAME.pre-migration.yaml
   ```

2. Identify the Pods owned by the current downstream workload. They will be recreated when the workload Kind changes:

   ```bash
   kubectl get pods -A \
     -l rbg.workloads.x-k8s.io/group-name=RBG_NAME,rbg.workloads.x-k8s.io/role-name=ROLE_NAME \
     -o wide
   ```

3. Review the role's existing Pattern:
   - Deployment and StatefulSet roles use `standalonePattern`.
   - LeaderWorkerSet roles use `leaderWorkerPattern`.
   - Preserve the existing template source, replicas, size, patches, rollout strategy, and restart-policy configuration.
   - A missing Pattern, or a Pattern without a resolvable template source, is a pre-existing object-health problem. Stop and repair or remove the role through a separate change.
   - If the existing Pattern conflicts with the old workload type, stop and design the change with the workload owner. Do not remove the workload selector merely to make the object pass validation.

4. Edit only the selected role in the `v1alpha2` representation:
   - Remove `rbg.workloads.x-k8s.io/role-workload-type` from that role's annotations.
   - Leave the annotations and specs of all other roles unchanged.
   - Preserve the correct Pattern.
   - For a redundant `RoleInstanceSet` annotation, remove only the annotation and leave the Pattern unchanged.

5. Submit the single-role `v1alpha2` change through your normal deployment workflow. If the source of truth is still a v1alpha1 manifest, first review and convert that source to v1alpha2. Do not batch multiple roles in one change.

6. Observe the replacement:
   - The controller creates a `RoleInstanceSet`.
   - The controller treats the old Deployment, StatefulSet, or LeaderWorkerSet as an orphan and deletes it.
   - All Pods for that role are recreated.

7. Verify the role before continuing:

   ```bash
   kubectl get rolebasedgroup RBG_NAME -o wide
   kubectl describe rolebasedgroup RBG_NAME
   kubectl get events --field-selector involvedObject.name=RBG_NAME
   ```

   Confirm that the old workload is gone, not merely that the replacement is ready. Also inspect the Pods and any traffic, readiness, cache-warmup, or business-specific health signal that matters to the service.

8. Stop, pause, or adjust the rollout whenever business conditions require it. Do not continue to the next role merely because the object is writable.

### v1alpha1 InstanceSet roles

A v1alpha1 `InstanceSet` selector is not covered by the compatibility toggle. It has not been reconciled since v0.7.0, and an unsupported workload type makes the whole RoleBasedGroup fail preflight reconciliation rather than isolating the failure to one role.

If the cluster is running v0.6.0 or earlier, `RoleInstanceSet` is not available before the upgrade. Upgrade to v0.7.0 or later first, then migrate the role to `RoleInstanceSet` as soon as possible. If the cluster is already on v0.7.0 or later, the role already does not reconcile.

After the replacement `RoleInstanceSet` is ready, delete the old `InstanceSet` explicitly. Orphan cleanup does not cover InstanceSet, so its Pods continue running next to the replacement until the old object is deleted. Review the deletion with the workload owner and include it in the recovery plan.

### RoleBasedGroupSet rollout order

A `RoleBasedGroupSet` is not an independent per-child staging mechanism. Its controller compares generated children with `groupTemplate` and copies the template roles back into them. Migrating a child while the set template still carries a legacy workload type is unstable: the controller may try to restore the legacy role, and the admission guard rejects that new use.

Migrate the set template first. That can propagate the change to all remaining children, so it is a set-wide rollout decision rather than a one-role change. If per-child staging is required, use an explicitly reviewed plan that coordinates or isolates the child from set reconciliation. Do not assume that a child can be permanently migrated while its parent template remains unchanged.

## Rollback

Before each role migration, retain an immutable copy of the object, its resource version, and any revision history used by your deployment process. These records remain useful for audit and for restoring non-workload fields, but they do not make the workload-type change reversible.

Once a role has switched to `RoleInstanceSet`, restoring the saved Deployment, StatefulSet, LeaderWorkerSet, or InstanceSet selector is a new deprecated use. The validator rejects it even while `controller.deprecatedWorkloadTypes.enabled=true`; compatibility preserves existing roles, but does not authorize reintroducing a deprecated type after migration. Do not begin migration until the business owner has approved a recovery plan for a failed `RoleInstanceSet` rollout.

If the old workload was already removed, do not manually recreate it with the same name. Let the controller own the replacement workload. Recovery after submission must use the pre-agreed emergency plan rather than a simple restore of the deprecated workload type.

## 3. Rewrite objects in the v1alpha2 storage encoding

Only begin this track after every identified legacy workload selector has been migrated, verified ready, and cleaned up.

For every RoleBasedGroup and RoleBasedGroupSet:

1. Read the object through the `v1alpha2` endpoint.
2. Save and review the returned object.
3. Write the same object back through the `v1alpha2` endpoint. A normal update with the current resourceVersion is sufficient; the spec and status should not change.
4. Confirm that the write succeeded, then continue to the next object.

One explicit, non-batched Kubernetes workflow is:

```bash
kubectl get rolebasedgroups.v1alpha2.workloads.x-k8s.io RBG_NAME -o yaml > RBG_NAME.storage-rewrite.yaml
# Review RBG_NAME.storage-rewrite.yaml, then write it back:
kubectl replace -f RBG_NAME.storage-rewrite.yaml
```

Use the equivalent fully qualified `v1alpha2` resource for RoleBasedGroupSets. If the write conflicts because the object changed, fetch a fresh copy, review it, and decide whether to retry.

Do not round-trip the object through `v1alpha1`: that path can reintroduce the `role-workload-type` compatibility annotation and create noise in later checks.

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

1. Repeat the role inventory. It must run successfully and produce no output.
2. Confirm that reconciliation is complete and that no RBG-owned Deployment, StatefulSet, or LeaderWorkerSet remains. Confirm that every old v1alpha1 `InstanceSet` has also been deleted.
3. Confirm from the migration inventory that every RoleBasedGroup and RoleBasedGroupSet was rewritten, then update and verify both `status.storedVersions` values as described above.
4. Set `controller.deprecatedWorkloadTypes.enabled=false`, then verify that the controller remains healthy and that a normal v1alpha2 update is not rejected by the deprecation gate.
5. Move manifests, clients, and automation to `v1alpha2`.
6. Retire the saved migration artifacts only after the rollback window agreed with the business owner has elapsed.

## Risks and failure modes

- **Pod recreation:** changing the workload Kind is destructive for the selected role. All Pods for that role can be recreated during migration.
- **Partial readiness:** migrate one role at a time. Do not automate the full set of roles.
- **Business state:** readiness alone may not be sufficient. Databases, caches, session state, traffic draining, and scheduler behavior must be reviewed by the service owner.
- **Stale old workloads:** while compatibility is enabled, orphan cleanup removes an old Deployment, StatefulSet, or LeaderWorkerSet after its role moves to `RoleInstanceSet`. Verify that cleanup completed before the final cutoff. Do not manually delete one unless the controller cannot, because manual deletion can race with replacement creation.
- **InstanceSet residue:** orphan cleanup does not remove old v1alpha1 InstanceSets. Their Pods continue running until the operator deletes the old object after the replacement is ready.
- **Storage ghosts:** `status.storedVersions` records storage versions that have been used. Confirm a complete object rewrite before removing `v1alpha1` from that list; if old-encoded objects remain when the API version is removed, they can become unreadable.
- **Silent type fallback:** deleting the workload-type annotation without completing the content migration changes the role to `RoleInstanceSet`. Make that change only after the Pattern and business impact have been reviewed.
- **Set-wide propagation:** updating a RoleBasedGroupSet template can migrate all generated children. Treat it as a set-wide rollout, not a way to stage one child indefinitely.
