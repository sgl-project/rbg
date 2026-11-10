# Failure Handling

RBG supports `None`, `RecreateRoleInstanceOnPodRestart`, and the opt-in `RecreateRoleInstanceOnPodRestartFromCreation` policy.

![failure-handling](../img/failure-handling.png)

## Restart Policy Types

| Policy | Description |
|--------|-------------|
| `None` | No automatic restart action; rely on default pod restart behavior. Failed pods are replaced through normal reconciliation. |
| `RecreateRoleInstanceOnPodRestart` | Compatibility policy: replace the affected RoleInstance's member Pods on Failed-pod or unexpected ordinary-container restart triggers, retaining the existing Ready/recent-restart gate. Member deletion is not a trigger. |
| `RecreateRoleInstanceOnPodRestartFromCreation` | Opt-in broader recovery: the same explicit failure/restart triggers, including before first Ready, plus observed deletion of a desired member Pod. The RoleInstance object and UID are retained. |

The new policy is supported only by native RoleInstanceSet workloads. LeaderWorkerSet, StatefulSet, and Deployment roles are rejected at admission rather than silently ignoring its triggers. Existing `RecreateRoleInstanceOnPodRestart` configuration and trigger eligibility are unchanged; accepted RoleInstance rebuilds use the durable drain described below.

`FromCreation` does not treat Pending or NotReady alone as a failure, bypass generation/revision guards, or count init-container/native-sidecar restarts. In-place updating exempts expected container restarts, not a Failed Pod or observed desired-member deletion under the new policy.

Both served APIs accept the policy: v1alpha1 uses `spec.roles[].restartPolicy`; v1alpha2 uses the pattern's `restartPolicyConfig.type`. Conversion preserves the effective policy.

## Configuration

Set the `restartPolicyConfig` field on the pattern (`leaderWorkerPattern` or `customComponentsPattern`).
`standalonePattern` has no restart policy field — it is always `None` (single pod, recreating the instance is equivalent to normal pod replacement).

`restartPolicyConfig` is an object with the following fields:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | `RecreateRoleInstanceOnPodRestart` | Restart policy type: `None`, `RecreateRoleInstanceOnPodRestart`, or `RecreateRoleInstanceOnPodRestartFromCreation`. |
| `baseDelaySeconds` | int | `30` | Base delay (seconds) for exponential backoff between restart attempts. Set to `0` with `maxDelaySeconds: 0` to disable backoff entirely. |
| `maxDelaySeconds` | int | `600` | Cap (seconds) for the exponential backoff delay. |

### Deprecated `restartPolicy` field

The pattern also accepts a deprecated `restartPolicy` string field, which is the shape v0.7.0
used before the backoff configuration was introduced. It is still honoured so that workloads
written for v0.7.0 keep working:

1. If `restartPolicyConfig.type` is set, it is used.
2. Otherwise, if `restartPolicy` is set, its value is used.
3. Otherwise the pattern default (`RecreateRoleInstanceOnPodRestart`) applies.

The two fields are resolved at runtime — the controller and the conversion webhook always read them through the precedence above, and stored objects are not modified. New workloads should only set `restartPolicyConfig`.

### Upgrading

- **From v0.7.0**: no action required. The `restartPolicy` wire format is unchanged.
- **From v0.8.0-alpha.3 / v0.8.0-alpha.4**: the released images for these two pre-releases briefly defined `restartPolicy` as an *object* instead of a string. That shape is no longer decodable by v0.8.0+ controllers — leaving even one object in the old shape prevents the new controller from listing that resource type at all, not just the offending object. These two versions are unsupported and should not be used. If you deployed them, delete all affected objects (`RoleBasedGroup`, `RoleBasedGroupSet`, `RoleInstance`, `RoleInstanceSet`) and recreate them with v0.8.0+.

- **Legacy `Restarting=True` without `status.retiringPods`**: the condition is no longer a Ready-based suppression latch. Without a durable UID ledger there is no recorded drain to resume; the stale condition can clear, and a still-visible eligible fault can cause one extra whole-instance rebuild, subject to backoff and budget. This is a behavior change for such in-flight upgrades, not a one-release migration mechanism. Upgrade the CRDs with the controller so the durable ledger is retained.

Existing deployments do not switch policies on upgrade: the default remains
`RecreateRoleInstanceOnPodRestart`; startup and observed-deletion recovery require
explicit opt-in. Backing Deployment and StatefulSet behavior is unchanged.

### Configuration example

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: restart-policy-demo
spec:
  roles:
    # Worker role with leaderWorkerPattern - recreate instance on pod failure (default)
    - name: worker
      replicas: 3
      leaderWorkerPattern:
        size: 2
        restartPolicyConfig:
          type: RecreateRoleInstanceOnPodRestart
        template:
          spec:
            containers:
              - name: worker
                image: nginx:latest

    # Worker role with explicit None - no restart action
    - name: auxiliary
      replicas: 1
      leaderWorkerPattern:
        size: 2
        restartPolicyConfig:
          type: None
        template:
          spec:
            containers:
              - name: auxiliary
                image: nginx:latest

    # StandalonePattern - no restart policy field (always None)
    - name: monitor
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: monitor
                image: nginx:latest
```

## Member Failure and Startup Recovery

For RoleInstanceSet-backed roles, opt in on the pattern:

```yaml
restartPolicyConfig:
  type: RecreateRoleInstanceOnPodRestartFromCreation
```

A `Failed` member Pod, an unexpected ordinary-container restart, or an observed
`deletionTimestamp` on a desired member can trigger replacement of **all member
Pods of the affected RoleInstance**, even before it first becomes Ready. The
RoleInstance itself is not deleted: its UID and revision history are preserved.
Other RoleInstances are unaffected. Init-container and native-sidecar restarts
are not inspected, because `restart-trigger-policy` is per Pod and cannot exempt
only a sidecar. A sidecar declared as an ordinary container is still inspected.

The legacy `RecreateRoleInstanceOnPodRestart` policy retains its Ready or recent
restart eligibility gate, and does not gain a deletion trigger. Both policies
retain observed-generation, current/update-revision, in-place-update, and Pod
`Ignore` protections. The new policy excludes slots no longer desired after
scale-in. Pending, model loading, and NotReady alone are not faults. A live
`Succeeded` Pod is normal completion and is not a trigger; under the new policy,
observing that same Pod with a `deletionTimestamp` is a trigger regardless of its
phase (including `Succeeded`).

> **Observed deletion of one member rebuilds the group with the new policy.**
> A manual `kubectl delete pod` can therefore replace all member Pods, not just
> the deleted member. `restart-trigger-policy: "Ignore"` suppresses initiation
> by an auxiliary member, not its participation in another member's rebuild.

### Durable Drain Before Replacement

For either recreation policy, the controller persists `status.retiringPods` with
the selected old member names and UIDs, together with `restartCount` and
`lastRestartTime`, **before issuing any Pod Delete**. Subsequent reconciles,
including after a controller restart, resume the same recorded drain without
charging another attempt. While it is recorded, normal creation/update is
blocked. All recorded old UIDs must be absent from the API before **any**
replacement Pod is created; an accepted Delete is not completion. A finalizer
can hold this barrier indefinitely. After five minutes of an active drain, the
controller emits a Warning event with reason `RetirementStalled` on the
RoleInstance. Inspect the remaining old Pods, their finalizers, and node health;
the warning never authorizes force deletion or automatic finalizer removal.
Deletes are pinned to the recorded UID, so
a same-name Pod with a different UID is not deleted on behalf of an old member.

Once all recorded old UIDs are gone, the list is cleared. `Restarting` is an
informational condition projected from that list: it is true only during the
drain and clears without waiting for replacements to become Ready. During the
drain, `RoleInstanceReady` is false with reason `Restarting` and message
`Removing the previous member Pods`. Old in-place-update baselines are cleared
when recording the drain, so replacements do not inherit old restart allowances.

With `RecreateRoleInstanceOnPodRestartFromCreation`, replacement Pods that fail startup can
start another backoff-governed round after the previous drain completes, even if
the instance has never been Ready. This does not remove the legacy policy's
Ready/recent-restart gate. Changing policy or template does not abandon an
already-recorded drain.

### Observed Deletion Versus a Missed Event

The new policy's deletion trigger is best-effort: the controller must observe
the desired member while its `deletionTimestamp` is set, and that signal must
still be present when it freshly re-reads the owned member from the API before
accepting a rebuild. If an observed deletion disappears before this validation,
the controller emits a `RestartSignalDropped` Warning event on the RoleInstance
and ordinary reconciliation may repair only the missing member. The warning
reports a dropped observed signal, not a persistent recovery request.

There is no persistent membership history, missing-slot trigger, deletion-intent
log, or Pending task. Deletions missed during a remaining backoff interval, a
controller outage, or removal without an observable terminating window (for
example force deletion/PodGC) may likewise result only in ordinary member repair.
A deletion never observed cannot be expected to produce a warning. Missing-member
triggers and a NotReady timeout are deferred, not provided by this policy.

If the replacement then reports Failed or an unexpected ordinary-container
restart, the new policy can rebuild all current members. A distributed runtime's
failure timeout that exits the container, or a configured startup/liveness probe
that kills it, can provide that later signal. A member that only hangs or remains
NotReady does not itself trigger recovery; without such a timeout or probe there
may be no later signal. A readiness probe alone does not restart a container.

While an eligible fault remains visible during backoff, the controller
re-evaluates it and waits for the remaining interval without cleaning Failed
Pods or individually replacing members. A policy or actual template revision
change can release that eligibility; it cannot cancel a drain already recorded.
A generation-only spec edit that leaves the policy and Pod-template revision
unchanged can first be acknowledged (`observedGeneration` catches up), then still
wait for the remaining failure backoff. Such edits are not guaranteed to bypass
backoff immediately.

Recovery does not redefine `startAfter` or custom instance readiness. Component
dependencies still use PodReady, so mutually initializing ranks must not be
ordered behind each other's Ready. The existing ImageID-based +1 in-place
restart allowance applies only to ordinary containers; it does not provide
same-digest/ContainerID-based detection. Pod creation, instance status, and Pod
writes are separate API operations, not a cross-resource atomic transaction
with external or racing creators.

For unreachable nodes, eviction typically begins after the not-ready/unreachable
toleration (normally 300 seconds), but old Pods may remain Terminating
indefinitely. RBG does not force-delete Pods or fence nodes. Absence of an old
UID from the API is not proof that its process has exited on a partitioned node.
Lower tolerations shorten detection, not necessarily safe cleanup. There is no
NotReady timeout.

## Exponential Backoff

When the resolved restart policy type is `RecreateRoleInstanceOnPodRestart` or `RecreateRoleInstanceOnPodRestartFromCreation`, subsequent crash-triggered recreations within a stability window are delayed by exponential backoff:

- **First eligible failure**: immediate recovery when no previous attempt time exists.
- **Subsequent eligible failures**: wait only for the remainder of the interval
  measured from the previous attempt's `lastRestartTime`, not from the new failure.
- Formula: `interval = min(baseDelaySeconds * 2^(restartCount-1), maxDelaySeconds)`;
  `remaining = max(0, lastRestartTime + interval - now)`.

For example, with base=10 and a previous attempt at t=0 with count=1, a new fault
at t=3 waits 7 seconds; a fault at t=12 need not wait again. Draining and startup
consume the interval too.

When a new attempt starts more than `max(maxDelaySeconds * 2, 10 minutes)` after
the previous attempt began, the count is reset before charging that attempt
(the count becomes 1). This is elapsed time since the last attempt, not a
requirement to serve Ready for that duration. Long unsuccessful initialization
can also outlast the stability window.

### Default Behavior Change

> **Note**: Before the backoff feature, pod crashes always triggered immediate recreation. With default settings (`baseDelaySeconds: 30`, `maxDelaySeconds: 600`), the second and
> subsequent eligible failures within the stability window receive 30s→600s attempt intervals measured from the preceding attempt's start. Existing workloads using the default `RecreateRoleInstanceOnPodRestart` policy
> automatically inherit this behavior.

### Disabling Backoff

To disable backoff entirely and restore the pre-backoff behavior (immediate recreation on every crash), set both `baseDelaySeconds` and `maxDelaySeconds` to `0`:

```yaml
restartPolicyConfig:
  type: RecreateRoleInstanceOnPodRestart
  baseDelaySeconds: 0
  maxDelaySeconds: 0
```

### Custom Backoff Configuration

```yaml
restartPolicyConfig:
  type: RecreateRoleInstanceOnPodRestart
  baseDelaySeconds: 10
  maxDelaySeconds: 120
```

With the above configuration, attempt intervals are 10s, 20s, 40s, 80s, then
120s (capped); the first attempt has no prior interval. Actual waiting after a
new failure is the interval minus elapsed time since the preceding attempt began.

> **Note**: `maxDelaySeconds` must be greater than or equal to `baseDelaySeconds` (enforced by CEL validation at admission). Because `maxDelaySeconds` defaults to 600, setting `baseDelaySeconds` above 600 without explicitly setting `maxDelaySeconds` will be rejected. If you need a base delay above 600s, set `maxDelaySeconds` to the same or higher value.

## Bounded Group Recovery

Both served RBG APIs support the same budget: v1alpha1 configures it as `spec.roles[].maxGroupRestarts` alongside `spec.roles[].restartPolicy`; v1alpha2 sets it on the pattern's `restartPolicyConfig.maxGroupRestarts`. Conversion preserves the effective policy and `maxGroupRestarts`, including explicit zero. `baseDelaySeconds` and `maxDelaySeconds` are v1alpha2-only: v1alpha1 has no field for them, so they are not round-tripped through v1alpha1.

When writing through v1alpha1, note that v1alpha1 still requires a role-level template for a component-backed role. A component-backed role that was created through v1alpha2 has no such template, so its v1alpha1 write is rejected by that pre-existing v1alpha1 validation; the spec change has to go through v1alpha2 until the template requirement is relaxed. Leader-worker roles are unaffected because they carry a role-level template.


For native RoleInstanceSet workloads, set `restartPolicyConfig.maxGroupRestarts` on
`leaderWorkerPattern` or `customComponentsPattern` (or on a directly managed
RoleInstance's `spec.restartPolicyConfig`):

```yaml
restartPolicyConfig:
  type: RecreateRoleInstanceOnPodRestartFromCreation
  maxGroupRestarts: 2
```

The optional non-negative limit applies to **both** whole-RoleInstance recreate
policies. Specify `type` explicitly when setting a budget. `None` and backing
LeaderWorkerSet, Deployment, and StatefulSet workloads do not support a budget.
An unset limit preserves unbounded automatic recovery. `0` permits healthy initial
creation, but stops recovery on the first eligible fault; `2` permits exactly two
accepted whole-group rebuilds, then stops on the next eligible fault. Ignored
components cannot trigger exhaustion, but are terminated with the complete group
if another member triggers it.

### Durable accounting and exhaustion

The budget is per RoleInstance UID and Pod-template revision. The RI status carries
`groupRestartCount`, `groupRestartRevision`, and `restartBudgetExhausted`. Accepted
rebuilds are recorded under an optimistic status lock **before** any Pod deletion;
repeated or suppressed attempts do not consume budget. The durable count is separate
from the quiet-period-resetting `restartCount` used for backoff. Ready transitions,
quiet periods, component size changes, policy edits, and limit edits/removal do not
reset it. When no budget remains, exhaustion takes precedence over restart backoff.

On exhaustion, the RI reports `Degraded=True`, reason
`GroupRestartBudgetExceeded`, and `RoleInstanceReady=False`. The controller stops
replacement creation and in-place updates, adds the internal
`rbg.workloads.x-k8s.io/group-restart-budget-cleanup` finalizer to **all owned Pods
that can still receive it**, then requests deletion of the complete group. Pods
already deleting cannot acquire a new finalizer. Suppression is persisted in RI
status and survives controller restarts, even if all retained Pods disappear.
Changing or removing the limit or policy does **not** unfreeze an exhausted RI.

Finalizers retain Pod **API objects and status**, not running containers. Kubelet's
Kubernetes 1.27+ normal deletion flow transitions Pods to a terminal phase and
releases scheduled resources (including GPUs); the controller never synthesizes
Pod phases. Native kubelet/CRI logs are best effort and are **not** guaranteed to
remain available. Export diagnostics to an external logging system.

### Explicit recovery and lifecycle cleanup

After investigating and fixing the failure, annotate the **RoleInstance**, not an
RBG, RoleInstanceSet, or Pod:

```bash
kubectl annotate roleinstance <instance-name> -n <namespace> \
  rbg.workloads.x-k8s.io/recover=true --overwrite
```

Only the exact value `true` requests recovery. An annotation that predates
exhaustion is removed when exhaustion is recorded: recovery must be requested
afterward. Recovery releases the group's budget finalizers, clears suppression and
the durable count, and allows a fresh group. The controller consumes the annotation.
A new Pod-template revision also releases obsolete retained objects and gets a fresh
budget; a limit/policy/size edit alone is not a new Pod-template revision.

Deleting an RI, selecting it for rollout replacement, or scaling it down is
**teardown**, not recovery. An RI cleanup finalizer keeps cleanup retryable until
all of its owned Pods have had budget finalizers removed. Teardown never charges
another restart, clears the counter as a recovery side effect, or creates a new
group. Other RI replicas remain independent and continue normal reconciliation.

## Excluding Components from Restart Policy Trigger

You can prevent specific pods from triggering the role's restart policy by setting an annotation on the pod template. This is useful for auxiliary components (e.g., monitoring, logging sidecars) whose failures should not affect the main workload. The annotation works with any deployment pattern (standalonePattern, leaderWorkerPattern, or customComponentsPattern).

Set the annotation `rbg.workloads.x-k8s.io/restart-trigger-policy: "Ignore"` in the component's `template.metadata.annotations`:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inference-demo
spec:
  roles:
    - name: engine
      replicas: 2
      customComponentsPattern:
        restartPolicyConfig:
          type: RecreateRoleInstanceOnPodRestart
        components:
          # Main inference component - will trigger restart policy on pod failure
          - name: inference
            size: 1
            template:
              spec:
                containers:
                  - name: inference
                    image: inference:latest

          # Monitoring sidecar - will NOT trigger restart policy on pod failure
          - name: monitor
            size: 1
            template:
              metadata:
                annotations:
                  # This component's pod restart/delete will NOT trigger role/RBG recreation
                  rbg.workloads.x-k8s.io/restart-trigger-policy: "Ignore"
              spec:
                containers:
                  - name: monitor
                    image: monitoring:latest
```

When this annotation is set to `"Ignore"` on a component's pod template:

- Its Pod failures and unexpected ordinary-container restarts do not initiate a rebuild. Under `RecreateRoleInstanceOnPodRestartFromCreation`, its observed deletion also does not initiate one.
- Other components still follow the configured policy. If another member triggers recovery, **all** selected owned member Pods are retired, including `Ignore` members: this annotation is an initiation filter only.
- Only the exact value `"Ignore"` suppresses triggering. An absent annotation, `"Inherit"`, or any unrecognised value follows the role's restart policy.
- The annotation is per Pod, not per container. Use a separate auxiliary component Pod to opt out without also exempting an ordinary main container.

## Use Cases

- **`RecreateRoleInstanceOnPodRestart`**: Existing workloads that depend on the original Ready/recent-restart-gated unexpected-restart and Failed-pod triggers, without a deletion trigger.
- **`RecreateRoleInstanceOnPodRestartFromCreation`**: Coupled RoleInstanceSet worker ranks that need repeated startup recovery before first Ready and whole-group recovery on observed desired-member deletion.
- **None**: Monitoring/logging sidecars that don't affect the main workload. Failed pods are replaced individually.
- **restart-trigger-policy: "Ignore"** (annotation): Auxiliary components within a multi-component instance whose pod failures should not trigger the role's restart policy for the entire instance.

## Examples

- [Restart Policy Examples](../../examples/basic/rbg/restart-policy/restart-policy.yaml)
- [CustomComponentsPattern with restart-trigger-policy annotation](../../examples/basic/rbg/patterns/custom-components-pattern.yaml)
