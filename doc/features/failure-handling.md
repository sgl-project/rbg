# Failure Handling

RBG supports `None` and `RecreateRoleInstanceOnPodRestart`. For the recreate policy,
`restartPolicyConfig.activateAfter` decides when its triggers are armed.

![failure-handling](../img/failure-handling.png)

## Restart Policy Types

| Policy | Description |
|--------|-------------|
| `None` | No automatic restart action; rely on default pod restart behavior. Failed pods are replaced through normal reconciliation. |
| `RecreateRoleInstanceOnPodRestart` | Recreate the whole role instance when a member fails, restarts unexpectedly, or is lost while terminating. `activateAfter` decides whether that starts before or after the instance is first Ready. |

### Triggers

`RecreateRoleInstanceOnPodRestart` recreates the instance on any of:

- a desired member Pod that is **Terminating**,
- a Pod in **Failed** phase,
- a **container restart** outside the Pod's in-place-update exemption and recorded restart allowance.

**Succeeded phase alone does not trigger recreation.** A desired member observed with a
`deletionTimestamp` can trigger it regardless of phase, including Succeeded. A member removed
by scale-in (ordinal outside the desired component size) is not a desired member.

A Terminating member is the earliest signal after an eviction: with an unreachable node the
Pod stays Terminating until the node comes back, the Node object is deleted, an
out-of-service taint is applied, or it is force-deleted, and no same-name replacement
exists before that. A member that
disappears without ever being observed as Terminating (force delete, PodGC, controller
downtime) is not covered by this trigger; missing/unobserved termination handling is deferred
to [#562](https://github.com/sgl-project/rbg/issues/562).

The rollout guard (`currentRevision` not yet equal to `updateRevision`) blocks recreation,
and a generation not yet observed by the controller also blocks it. With
`activateAfter: Ready`, a spec change invalidates the previous generation's readiness latch. Pending or
NotReady alone is not a trigger; a NotReady timeout is deferred to
[#380](https://github.com/sgl-project/rbg/issues/380). Init-container and native-sidecar
restarts do not count. A Pod with its `InPlaceUpdateReady` condition False is exempt from
container-restart detection for that Pod, not just from restarts proven expected by
baselines. Otherwise, recorded in-place-update baselines allow expected restarts. Failed
phase and desired-member deletion are checked **before** this exemption and remain triggers.

### Arming: `activateAfter`

| Value | Meaning |
|-------|---------|
| `Ready` (default) | Triggers are armed once the instance has been Ready at its current generation. Creation and scale-up keep their protection. |
| `Created` | Triggers are armed as soon as the instance exists, which also covers failures before the instance first becomes Ready. |

The stability fact is persisted as `status.lastReadyGeneration`. Gating on the live
`RoleInstanceReady` condition instead would be unsound: status is written after scaling, so
a fault flips the condition to False before the restart signal becomes visible, and the
policy would then never fire again. The `Ready` gate also retains the existing current-Ready
and recent-restart bypasses, subject to the generation and revision guards.

**A replacement that also fails before Ready is recovered again, with either activation
mode.** The `Restarting` guard is scoped to one recreation wave: it blocks only while the
pre-restart Pods are still being deleted. Once they are all gone the wave completes, the
guard is released and the next failure starts another whole-instance recreation, bounded
by the exponential backoff ([#563](https://github.com/sgl-project/rbg/issues/563)). With
the default `Ready` activation this applies through the latched `lastReadyGeneration`
and the recent-restart bypass; with `Created` it applies from instance creation. The
exact release point holds for the in-memory wave marker; after a controller restart
mid-wave, the persisted fallback uses creation timestamps with second granularity and
may release the guard early for a same-second pre-restart Pod, allowing at most one
extra backoff-bounded recreation.

`activateAfter` is supported by native RoleInstanceSet workloads. LeaderWorkerSet-backed
roles keep the policy they had; their behavior is already ungated (LWS recreates the group on
`ContainerRestarted` or `PodDeleted` with no start or Ready gate) and the vendored LWS v0.7
adapter exposes no field to express the distinction. StatefulSet and Deployment roles have no
instance-level recreate path.

**Deleting a single member now rebuilds the whole RoleInstance.** That is the intended
semantics for this policy — the members of a multi-rank workload agree on a one-shot
membership at startup, so replacing one rank in place does not recover it — and it matches
LeaderWorkerSet. Use the `restart-trigger-policy: Ignore` annotation on auxiliary components
whose failures should not affect the main workload.

Both served RBG APIs accept the existing restart policy, but **only v1alpha2 expresses
`activateAfter`**, in the pattern's `restartPolicyConfig`. v1alpha1 has only
`spec.roles[].restartPolicy`. A GET through v1alpha1 omits `activateAfter` from the response;
it neither drops the persisted configuration nor changes running behavior. A
read-modify-write through v1alpha1 drops the setting and resolves to the default `Ready`,
as it already does for the backoff delays. Use v1alpha2 to preserve these settings.

## Configuration

Set the `restartPolicyConfig` field on the pattern (`leaderWorkerPattern` or `customComponentsPattern`).
`standalonePattern` has no restart policy field — it is always `None` (single pod, recreating the instance is equivalent to normal pod replacement).

`restartPolicyConfig` is an object with the following fields:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | `RecreateRoleInstanceOnPodRestart` | Restart policy type: `None` or `RecreateRoleInstanceOnPodRestart`. |
| `activateAfter` | string | `Ready` | When the recreate triggers are armed: `Ready` or `Created`. Omit it to keep the default. |
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
- **Serialized-template compatibility**: `activateAfter` is omitted unless explicitly set; its `Ready` default is resolved at runtime, not by a CRD default. The status-only readiness latch is not hashed. This avoids an upgrade-only template change/default materialization and rollout, **not** runtime behavior changes: the default recreate policy intentionally gains the readiness latch and Terminating-member trigger.
- **Setting or changing `activateAfter`**: changes the serialized RoleInstance template and can roll the role. With `RecreatePod`, this can delete and recreate every RoleInstance and rebuild all its Pods, even when explicitly setting `Ready` where the field was previously omitted. With `InPlaceIfPossible`, a policy-only change updates RoleInstance specs in place without restarting Pods.
- **Terminating member at upgrade**: a Ready instance that already has a Terminating desired member when the new controller starts (for example during a node drain) can be rebuilt on the first reconcile. Existing instances stuck NotReady without a readiness latch are not rescued automatically: manually delete the **RoleInstance**, not just one Pod, to recover. `status.lastReadyGeneration` only starts latching after Ready is observed.
- **Older writers and controller rollback**: a v1alpha1 read-modify-write drops `activateAfter` (as it drops the backoff delays today). A rolled-back controller or another older writer may also drop it when writing the object/template. Merely rolling back the binary does not itself rewrite persisted objects, but losing the serialized field on a subsequent write changes the template and can roll the role again, rebuilding all Pods under `RecreatePod`.

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

## Exponential Backoff

When the resolved restart policy type is `RecreateRoleInstanceOnPodRestart`, subsequent crash-triggered recreations within a stability window are delayed by exponential backoff:

- **First crash**: immediate recreation (no delay).
- **Second crash** (within the stability window): delayed by `baseDelaySeconds`.
- **Each subsequent crash**: delay doubles, capped at `maxDelaySeconds`.
- Formula: `delay = min(baseDelaySeconds * 2^(restartCount-1), maxDelaySeconds)`.

The stability window is `max(maxDelaySeconds * 2, 10 minutes)`. If the instance remains stable (no crashes) for longer than this window, `restartCount` resets to 0 and the next crash starts with immediate recreation again.

Backoff does not override activation. The `Restarting` guard only absorbs the triggers
produced while the pre-restart Pods of the in-flight recreation are deleted; once that wave
completes, backoff bounds any further recreations, including pre-Ready failures with
`Created` ([#563](https://github.com/sgl-project/rbg/issues/563)).

### Default Behavior Change

> **Note**: Before the backoff feature, pod crashes always triggered immediate recreation. With default settings (`baseDelaySeconds: 30`, `maxDelaySeconds: 600`), the second and
> subsequent crashes within the stability window now receive 30s→600s exponential delays. All existing workloads using the default `RecreateRoleInstanceOnPodRestart` policy
> automatically inherit this behavior.

### Disabling Backoff

To disable backoff delays for eligible recreations, set both `baseDelaySeconds` and `maxDelaySeconds` to `0`. Activation arming still applies, but nothing delays recreations: a rebuilt group that keeps failing (before or after Ready) is recreated repeatedly, paced only by each wave's Pod deletion and creation cycle, and `RestartCount` grows unboundedly. Keep the defaults enabled if the workload can enter a crash-looping state:

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

With the above configuration: first crash is immediate, second crash delayed 10s, third 20s, fourth 40s, fifth 80s, sixth and beyond capped at 120s.

> **Note**: `maxDelaySeconds` must be greater than or equal to `baseDelaySeconds` (enforced by CEL validation at admission). Because `maxDelaySeconds` defaults to 600, setting `baseDelaySeconds` above 600 without explicitly setting `maxDelaySeconds` will be rejected. If you need a base delay above 600s, set `maxDelaySeconds` to the same or higher value.

## Bounded Group Recovery

Upgrade the CRDs, controller, and admission webhook together before configuring a
budget. The existing v1alpha2 admission webhook is the only admission layer that
enforces the native RoleInstanceSet backend constraint; CRD CEL validates the
explicit policy type and non-negative limit. An older or disabled webhook can
admit a budget on an unsupported backend, which then ignores the budget.

`maxGroupRestarts` is supported only by v1alpha2, like `activateAfter` and the
backoff delays. Configure the budget through v1alpha2. A GET through v1alpha1
omits these fields but does not change the persisted object. A v1alpha1
read-modify-write of the spec drops them: an omitted budget means unlimited
recovery, not the previous limit. Do not use v1alpha1 spec writes to manage a
budgeted workload. This compatibility boundary is covered by conversion tests
and the real API-server conversion path.

For native RoleInstanceSet workloads, set `restartPolicyConfig.maxGroupRestarts` on
`leaderWorkerPattern` or `customComponentsPattern` (or on a directly managed
RoleInstance's `spec.restartPolicyConfig`):

```yaml
restartPolicyConfig:
  type: RecreateRoleInstanceOnPodRestart
  activateAfter: Created
  maxGroupRestarts: 2
```

The optional non-negative limit applies to the native
`RecreateRoleInstanceOnPodRestart` policy with either activation setting.
Specify `type` explicitly when setting a budget. `None` and backing
LeaderWorkerSet, Deployment, and StatefulSet workloads do not support a budget.
An unset limit preserves unbounded automatic recovery. `0` permits healthy initial
creation, but stops recovery on the first eligible fault; `2` permits exactly two
accepted whole-group rebuilds, then stops on the next eligible fault. Ignored
components cannot trigger exhaustion, but are terminated with the complete group
if another member triggers it.

### Durable accounting and exhaustion

The budget is per RoleInstance UID and component topology/Pod-template revision. The RI status carries
`groupRestartCount`, `groupRestartRevision`, and `restartBudgetExhausted`. Accepted
rebuilds are recorded under an optimistic status lock **before** any Pod deletion;
repeated or suppressed attempts do not consume budget. The durable count is separate
from the quiet-period-resetting `restartCount` used for backoff. Ready transitions,
quiet periods, policy edits, and limit edits/removal do not reset it within the
same revision. When no budget remains, exhaustion takes precedence over restart
backoff.

### Configuration updates use the existing rollout path

Changes to `maxGroupRestarts`, `baseDelaySeconds`, and `maxDelaySeconds` remain
part of the RoleInstanceSet template and follow its existing revision/update
strategy, just like other template changes. There is no separate synchronization
to existing RoleInstances and no bypass of pause, partition, availability, or
lifecycle controls. Applying a revision uses the recovery configuration stored
in that revision.

A rollout may replace an RI, giving the replacement its own budget. When the RI
is updated in place, changes to policy, budget, or backoff alone do not reset its
current topology budget or clear exhaustion.

### Size changes start a fresh budget, as in LWS

Component sizes and their derived Pod-template labels participate in the budget
revision. Changing group `size` creates a new topology/revision and follows the
existing RoleInstanceSet rollout path, with a fresh budget for each new group.
This also permits a new size revision to replace a budget-exhausted old group;
it does not reset the old revision's accounting in place. No special resize
identity-preservation path, member stamp or temporary-surge protocol is used.
Existing pause, partition, availability, lifecycle and update-strategy controls
continue to govern the rollout.

Changing the number of groups (`replicas`) is different: surviving instances keep
their UID, revision and consumed budget, while newly created groups start at zero.
A size/configuration rollout itself is not charged as a failure-recovery attempt.

On exhaustion, the RI reports `Degraded=True`, reason
`GroupRestartBudgetExceeded`, and `RoleInstanceReady=False`. The controller stops
replacement creation and in-place updates, adds the internal
`rbg.workloads.x-k8s.io/group-restart-budget-cleanup` finalizer to **all owned Pods
that can still receive it**, then requests deletion of the complete group. Pods
already deleting cannot acquire a new finalizer. Suppression is persisted in RI
status and survives controller restarts, even if all retained Pods disappear.
Changing or removing the limit or tuning backoff on the same RI does **not**
unfreeze it. Configuration changes through a RoleInstanceSet follow its normal
rollout rules and may replace the RI, as noted above.

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

In `spec.roles[].annotations` the annotation does not request recovery, and the
controller does not copy it from there to the role's workload. It is still part
of the role like any other role annotation, so adding or removing it there
creates a new RoleBasedGroup revision.

Only the exact value `true` requests recovery. An annotation that predates
exhaustion is removed when exhaustion is recorded: recovery must be requested
afterward. Recovery first retries any unfinished termination, then releases only
the group's budget finalizers. Suppression, counters and the recovery annotation
remain in place until an authoritative API read finds no old owned Pod objects.
A successful Delete request or an elapsed grace period alone is not completion.
Other finalizers can hold recovery indefinitely and are never removed by RBG.
Only after that barrier does the controller clear suppression and the durable
count, consume the annotation, and allow a fresh group. If the status reset is
committed but annotation cleanup fails or the controller crashes, the next
reconcile removes the now-stale `recover=true` annotation without resetting the
budget again. This prevents the old group's Terminating members from immediately
charging or exhausting the new budget. Recovery is independent of Ready; an
exhausted group need not and cannot be required to become Ready before recovery
is allowed.

A new topology/Pod-template revision (including a size change) has the same
old-group exit barrier when updating an exhausted RI directly. RoleInstanceSet
rollout replacement instead follows teardown cleanup below. Limit or policy
edits alone do not change the budget revision.

Deleting an RI, selecting it for rollout replacement, or scaling it down is
**teardown**, not recovery. An RI cleanup finalizer keeps cleanup retryable until
all of its owned Pods have had budget finalizers removed. Teardown never charges
another restart, clears the counter as a recovery side effect, or creates a new
group. Other RI replicas remain independent and continue normal reconciliation.

Finalizer acquisition is limited to Pods still controlled by the original RI and
matching its current member selector. If an operator changes Pod labels or removes
its owner reference, RBG no longer manages that Pod; external cleanup is the
operator's responsibility. The budget finalizer never grants authority over a
foreign or same-name replacement Pod.

Budgeted accepted rebuilds emit `ReCreateInstance` with the policy, triggering
Pod and live-validated reason, only after the accounting status transaction
succeeds. Deferred, rejected and duplicate budget attempts do not emit another
accepted-rebuild event. Unbudgeted event behavior is unchanged.

### Controller downgrade prerequisites

Do not roll back to a controller that lacks restart-budget support while any
RoleInstance or Pod still carries
`rbg.workloads.x-k8s.io/group-restart-budget-cleanup`. The older controller cannot
release this finalizer, so deletion and scale-down can remain stuck. Healthy,
recovered, and previously budgeted RIs may still carry the RI finalizer;
unsetting `maxGroupRestarts` or requesting recovery does not remove it.

Before downgrading:

1. Keep the budget-aware controller and current CRDs running.
2. Scale the affected parent workloads to zero or remove them so they cannot
   recreate budgeted RIs. Normally delete any directly managed RIs carrying the
   budget finalizer, and let the current controller finish teardown cleanup.
3. Verify that the budget finalizer is absent from both RIs and Pods in every
   affected namespace. Pods manually relabeled or detached from their owner
   require operator cleanup as described above.
4. Remove budget settings from the source configuration before restoring
   replicas under the older controller, then downgrade only after cleanup has
   completed.

This procedure tears down the affected workloads and is not a workload-preserving
downgrade. Do not blindly remove all finalizers; unrelated finalizers remain
under their respective owners' control.

### Remaining recovery limitations

The budget does not add missing-member detection or retain a transient deletion
signal through backoff; those are tracked in [#562](https://github.com/sgl-project/rbg/issues/562)
and [#566](https://github.com/sgl-project/rbg/issues/566). Budgeted restarts reuse
the wave guard and deletion retry helpers from [#573](https://github.com/sgl-project/rbg/pull/573):
the guard ends when the original Pod UIDs disappear, without waiting for Ready.
No additional Pod ledger is introduced.
A NotReady timeout remains out of scope ([#380](https://github.com/sgl-project/rbg/issues/380)).

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

When this annotation has the exact value `"Ignore"` on a component's pod template:

- Failure, deletion and container restarts of that component's Pods do not **initiate** instance recreation.
- Other components can still trigger the policy, and the resulting whole-instance recreation also deletes and rebuilds the ignored component's Pods.
- Omitted values, `"Inherit"`, and any value other than the exact `"Ignore"` do not suppress initiation.

## Use Cases

- **`RecreateRoleInstanceOnPodRestart`**: tightly coupled workloads needing whole-instance recovery from unexpected ordinary-container restarts, Failed Pods, or a desired member observed Terminating; armed after Ready by default.
- **`activateAfter: Created`**: instances that also need a recovery attempt before first Ready, such as a role whose startup can fail and then hang in a rendezvous. Repeated pre-Ready failures trigger repeated whole-instance recreations, bounded by the exponential backoff ([#563](https://github.com/sgl-project/rbg/issues/563)).
- **None**: Monitoring/logging sidecars that don't affect the main workload. Failed pods are replaced individually.
- **restart-trigger-policy: "Ignore"** (annotation): Auxiliary components within a multi-component instance whose pod failures should not trigger the role's restart policy for the entire instance.

## Examples

- [Restart Policy Examples](../../examples/basic/rbg/restart-policy/restart-policy.yaml)
- [CustomComponentsPattern with restart-trigger-policy annotation](../../examples/basic/rbg/patterns/custom-components-pattern.yaml)
