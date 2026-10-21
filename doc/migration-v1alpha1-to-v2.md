# Migrating from v1alpha1 to v1alpha2

This guide applies to RBG `0.9.0`. In this release, `v1alpha1` is still served so existing objects remain readable and writable, but every write returns an API warning and new uses of the deprecated workload types are rejected. `v1alpha1` will stop being served in `0.10.0`.

## What is being deprecated

There are two independent migration tracks:

1. **API encoding (v1alpha1 to v1alpha2).**  
   Objects created through `v1alpha1` and never updated may still be encoded as `v1alpha1` in etcd. The storage-version migration rewrites them through the `v1alpha2` endpoint. It is a no-op for object content.

2. **Deprecated workload types (Deployment, StatefulSet, LeaderWorkerSet).**  
   A role may carry `rbg.workloads.x-k8s.io/role-workload-type` to select one of these older workload kinds. The replacement is `RoleInstanceSet`, the `v1alpha2` default. Unlike storage migration, this changes the downstream workload `Kind` and recreates the role's Pods.

Do not combine the two checks: finishing one track does not imply the other is complete.

## Prerequisites

- Upgrade the RBG controller to `0.9.0` or later.
- Install a matching `kubectl-rbg` CLI from the same release.
- Plan a maintenance window for each role that still uses Deployment, StatefulSet, or LeaderWorkerSet.
- Keep a rollback manifest and revision history for the RoleBasedGroup before changing it.

`controller.deprecatedWorkloadTypes.enabled` defaults to `false`. Existing roles that already use a deprecated workload type are exempt and continue to reconcile. New roles, and roles that did not previously use a deprecated type, are rejected. RBAC, watches, and orphan cleanup remain active while the flag is false so migration can remove old workloads. Use `true` only as a temporary rollback window.

## 1. Review the impact

The `plan` command is read-only. It reports every role in RoleBasedGroups and RoleBasedGroupSets and classifies each annotation:

```bash
kubectl rbg migrate plan
```

Roles are classified as:

- `safe-cleanup`: the annotation is absent or equals `workloads.x-k8s.io/v1alpha2/RoleInstanceSet`. Removing it has no behavior change.
- `workload-type`: the annotation selects Deployment, StatefulSet, LeaderWorkerSet, or an unknown type. Migrating that role replaces the old workload with a RoleInstanceSet and recreates its Pods.

The report is an impact inventory, not a migration decision. It does not choose an outage window or claim that a role is safe to move.

## 2. Migrate workload types one role at a time

For each RoleBasedGroup role reported as `workload-type`, first identify the Pods that will be recreated:

```bash
kubectl rbg migrate workload-type \
  --rbg RBG_NAME \
  --role ROLE_NAME
```

Both flags are required. The command lists the affected Pods and asks for confirmation before it changes the object. In a non-interactive terminal, add `--yes` only after you have reviewed the Pod list.

The command removes `rbg.workloads.x-k8s.io/role-workload-type` from the selected role and materializes the corresponding Pattern:

- Deployment and StatefulSet use `standalonePattern`.
- LeaderWorkerSet uses `leaderWorkerPattern`.
- A redundant `RoleInstanceSet` annotation is removed without changing the existing Pattern.

After confirming, the controller treats the old Deployment, StatefulSet, or LeaderWorkerSet as an orphan, deletes it, creates the RoleInstanceSet, and recreates all Pods for that role.

Use a bounded rollout:

1. Choose one role.
2. Run the command for that role.
3. Wait until the RoleBasedGroup reports that role ready:

   ```bash
   kubectl get rolebasedgroup RBG_NAME -o wide
   kubectl get pods -l rbg.workloads.x-k8s.io/group-name=RBG_NAME,rbg.workloads.x-k8s.io/role-name=ROLE_NAME
   ```

4. Check controller events for the role:

   ```bash
   kubectl describe rolebasedgroup RBG_NAME
   kubectl get events --field-selector involvedObject.name=RBG_NAME
   ```

5. Repeat only after the previous role is ready.

If the migration affects a RoleBasedGroupSet, `migrate plan` reports its template roles. Inspect and update that template with the same per-role semantics before creating new RoleBasedGroups from it. Existing generated RoleBasedGroups still need to be migrated individually.

## Rollback

Before each role migration, save the object:

```bash
kubectl get rolebasedgroup RBG_NAME -o yaml > RBG_NAME.pre-migration.yaml
```

To roll back one role while `v1alpha1` is still served, restore the saved manifest through the same API version used to create it. Be aware that rollback recreates Pods again and returns the role to a deprecated workload type. Restoring a saved `v1alpha2` object with the original `role-workload-type` annotation is a temporary escape hatch, not a completed migration.

If the old workload was already removed, do not manually recreate it with the same name. Let the controller own the replacement workload and use the saved manifest only to restore role configuration after stopping traffic.

## 3. Rewrite objects in the v1alpha2 storage encoding

After every reported workload-type role has been migrated and verified ready, run:

```bash
kubectl rbg migrate storage-version
```

This lists and updates every RoleBasedGroup and RoleBasedGroupSet through their `v1alpha2` endpoints. The update is content-neutral and repeatable. It is required because storage-version conversion does not rewrite objects that are never updated.

Verify both conversion-enabled CRDs:

```bash
kubectl get customresourcedefinition \
  rolebasedgroups.workloads.x-k8s.io \
  rolebasedgroupsets.workloads.x-k8s.io \
  -o custom-columns=CRD:.metadata.name,STORED_VERSIONS:.status.storedVersions
```

Each value must contain only `v1alpha2`. If `v1alpha1` remains, rerun `migrate storage-version` and check the controller and conversion webhook health before proceeding.

## 4. Finish the migration window

1. Re-run `kubectl rbg migrate plan` and resolve every remaining `workload-type` item.
2. Confirm both `status.storedVersions` values contain only `v1alpha2`.
3. Leave `controller.deprecatedWorkloadTypes.enabled=false`.
4. Stop writing `v1alpha1` manifests. Move manifests and clients to `v1alpha2`.
5. Keep the migration guide and CLI available until all clusters have passed both checks.

## Risks and failure modes

- **Pod recreation:** changing the workload Kind is destructive for the selected role. All Pods for that role are recreated during migration.
- **Partial readiness:** migrate only one role at a time and verify readiness before continuing. Do not script all roles in one command.
- **Stale old workloads:** RBG `0.9.0` keeps orphan cleanup active even with deprecated types disabled. Do not manually delete the old workload unless the controller cannot; manual deletion can race with replacement creation.
- **Storage ghosts:** if `v1alpha1` remains in `status.storedVersions` for a conversion-enabled CRD, removing the API version in a later release can make those objects unreadable. Run the storage check before upgrading past `0.9.0`.
- **Silent type fallback:** deleting the workload-type annotation without completing the content migration changes the role to RoleInstanceSet. That is intended only through the migration command after confirmation.
