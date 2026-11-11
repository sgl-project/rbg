# Controller patterns

## Writes: one writer style per field set

- **The RBG controller renders workloads and Services with server-side apply,** through
  applyconfigurations (`utils.PatchObjectApplyConfiguration` in `pkg/utils/utils.go`, field
  manager `rbg`, `Force: true`).
- **Never write those same fields with a typed `Create`, `Update` or merge patch on another code
  path.** Doing so leaves a second field manager (`rbg`/Update) in `managedFields`, and any key the
  apply later stops sending is never removed. In #538, every RoleInstanceSet created after the
  upgrade kept labels that had been removed from the role.
  - Check with `kubectl get <obj> -o yaml --show-managed-fields`.
  - Add a regression test: remove a label from a role and assert that it disappears from a newly
    created RoleInstanceSet.
  - To fix, create through SSA, or migrate ownership with `csaupgrade.UpgradeManagedFieldsPatch`
    (`k8s.io/client-go/util/csaupgrade`).
- **Elsewhere, follow the object's existing writer.** For example, the RoleInstanceSet controllers
  manage RoleInstances with typed create/update and patches, and several status writers use
  `Status().Update`. Don't introduce a second style for the same fields.
- **Add every new status field to every writer of that status,** whether it is an SSA apply
  configuration or a `Status().Update`.
- **SSA rejects duplicate keys in list-maps,** such as two env vars with the same name.
  De-duplicate deterministically before applying (#548).

## Preconditions and conflicts

- **Don't check `resourceVersion` against an in-memory parent.** It goes stale as soon as the
  controller writes its own status, and every reconcile then conflicts with itself (#538). For spec
  intent, compare `UID` + `Generation`. Compare against freshly read status, not a copy you already
  mutated in memory (#478).
- **Persist intent under a precondition, then wait for the cache to observe it before acting again.**
  Use the expectations in `pkg/utils/expectations` (#508).
- **Label-only patches to Pods take a UID precondition only.** A `resourceVersion` precondition races
  with kubelet status writes (#530).

## Failure isolation

- **One role's or child's failure must not block the others,** or skip later steps such as scaling,
  cleanup of orphaned workloads, or later dependency tiers. Emit a Warning event, mark the affected
  role, and continue (#508, #538, #548, #510).
- **Know how `reconcileRoles` fails.** It walks roles in dependency tiers.
  - Within a tier, a failing `reconcileSingleRole` doesn't stop its peers; the errors are joined.
  - After that tier, the joined error returns, and later tiers are skipped.
  - A dependency-check error or an unmet prerequisite returns immediately.
  - Keep unrelated roles isolated without bypassing dependency order or cleanup (#553).

## Requeues and load

- **Don't add broad periodic requeues; rely on `Owns`/`Watches` events.** Requeuing whenever a
  rollout was in progress turned every non-ready RBG into a 2-second poll: 822 reconciles in 2
  minutes versus 0 on `main`, and new RBGs took 4–5× longer to become Ready (#474/#491).
- **Every requeue needs a logged reason and a bounded backoff** (#508).
- **Show that the load is unchanged versus `main`.** Compare reconcile counts and API calls at the
  defaults (`--kube-api-qps=20`, `--max-concurrent-reconciles=10`). Back scale claims with
  `test/stress`; the `stress-test` skill automates that.

## Events

- **Emit events on transitions, not on every reconcile.** client-go's event correlator rate-limits
  events per involved object, so a chatty Warning can starve important ones like `FailedCreate`
  (#528, #510).
- **Make events actionable:** say which object, for example which Pod, and why (#550).
- **Event reasons are API.** Don't rename reasons a KEP documents (#510).

## Ownership and adoption

- **Adopt an existing child after checking that it is claimable** (`checkWorkloadClaimable` in
  `pkg/reconciler/common.go`); don't delete and recreate it. Recreating loses state. In #500, a
  recreated ScalingAdapter lost the HPA-scaled replica count.
- **Verify identity before adopting.** Check a reference such as `ScaleTargetRef`, not just the name.
  Derived names can collide: RBG `a` + role `b-c` and RBG `a-b` + role `c` produce the same name.
- **Never delete or re-parent an object you haven't proven is yours.**

## Status writers and readers

- **Every status writer must preserve fields it doesn't own.** An older controller's whole-object
  `Status().Update` wipes newer fields after a downgrade, so prefer apply or patch for status
  (#491, #508).
- **When the computation of a status field changes, audit all its readers.** For example, check
  `RoleStatuses[].UpdatedReplicas` consumers (#533).

## Watches, predicates and deletion

- **Watch every input that affects rendering or status.** An unwatched input gets applied at some
  later, unrelated reconcile or controller restart (#548). Reuse the shared informers, keep
  predicates narrow, and map events back to RBGs through the `rbg.workloads.x-k8s.io/group-name`
  label.
- **`RBGPredicate` only enqueues an RBG update when its `spec` changes**
  (`internal/controller/workloads/rolebasedgroup_controller.go`). As a result:
  - Edits to RBG metadata such as annotations don't trigger a reconcile.
  - Setting `deletionTimestamp` doesn't trigger one either.
  - Adding a finalizer therefore also needs predicate changes and explicit deletion handling (#528).

## Cache

- **Don't create a cached child that the cache filter will hide.** `cacheOptions()` in
  `cmd/rbgs/main.go` caches Services, StatefulSets and Deployments only if they carry the
  `rbg.workloads.x-k8s.io/group-name` label. A child created without that label is invisible to the
  controller, which then keeps trying to create it.
- **If you add a filter for a new type (KEP-284 proposes one for Pods, #530):**
  - Audit every cached read, including `new(v1.Pod)`-style reads.
  - Don't treat a DELETE event from a filtered watch as proof of deletion; the object may only have
    left the filter.
  - Use a live read (API reader) before acting on absence.
