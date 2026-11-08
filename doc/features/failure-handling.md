# Failure Handling

RBG supports multiple failure handling policies: `None` and `RecreateRoleInstanceOnPodRestart`.

![failure-handling](../img/failure-handling.png)

## Restart Policy Types

| Policy | Description |
|--------|-------------|
| `None` | No automatic restart action; rely on default pod restart behavior. Failed pods are replaced through normal reconciliation. |
| `RecreateRoleInstanceOnPodRestart` | Replace all member Pods of only the affected RoleInstance when a member fails, unexpectedly restarts a container, or is observed terminating. The RoleInstance object is retained. Default for `leaderWorkerPattern` and `customComponentsPattern`. |

## Configuration

Set the `restartPolicyConfig` field on the pattern (`leaderWorkerPattern` or `customComponentsPattern`).
`standalonePattern` has no restart policy field — it is always `None` (single pod, recreating the instance is equivalent to normal pod replacement).

`restartPolicyConfig` is an object with the following fields:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | `RecreateRoleInstanceOnPodRestart` | Restart policy type: `None` or `RecreateRoleInstanceOnPodRestart`. |
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

A `Failed` member Pod, an unexpected ordinary/init-container restart, or an
observed `deletionTimestamp` can trigger replacement of all member Pods even
before the RoleInstance first becomes Ready. Policy, generation/revision,
in-place-update and effective Pod `Ignore` protections still apply. Ordinary
Pending, model loading, and NotReady alone do not trigger recovery.

> **Deleting a single member rebuilds the group.** With
> `RecreateRoleInstanceOnPodRestart`, a manual `kubectl delete pod` on one member
> recreates **all** member Pods of that RoleInstance, not just the deleted Pod.
> This matches LWS `RecreateGroupOnPodRestart`. Use
> `restart-trigger-policy: Ignore` for auxiliary members that should be replaced
> individually.

The implementation keeps the existing path: `calculateDiffsWithExpectation`
checks `shouldRecreateInstance` and returns the existing deletion/creation diff
for `Scale`. There is no separate recovery coordinator or implementation module.

The only new recovery state is `status.retiringPods`: the old member names and
UIDs selected for this deletion. It is persisted together with the restart count
and time before issuing Delete. Subsequent reconciles, including after a
controller restart, finish the same list without charging another attempt.
During this drain ordinary creation/update is blocked. Delete acceptance is not
completion: finalizer-held old UIDs still have to disappear from the API.
Same-name replacements are not deleted on behalf of a recorded old UID.

Once all recorded old UIDs are absent, the list is cleared. **The replacement
Pods need not be Ready to finish the previous deletion.** A new set of Pods can
therefore fail initialization and trigger another backoff-governed attempt.
`Restarting` is an informational condition, not an independent guard or a
Ready-based completion latch. When recording the deletion list, the retiring
members' existing in-place baselines are cleared so replacement Pods do not
inherit them; no additional PodUID baseline field is introduced.

There is no persistent membership history, missing-slot trigger, deletion-intent
log, or Pending task. If a deletion is not observed until the Pod is already gone
(including during backoff), ordinary reconciliation may replace only that member.
If the new member then reports Failed or an unexpected restart, the controller
recreates all current member Pods. This eventual recovery depends on the runtime
or liveness probe producing an explicit fault: a new member that only hangs or
stays NotReady will not cause another attempt. Tests simulate these fault signals;
real multi-machine framework startup/serving has not been validated here.

During backoff, visible faults are re-evaluated and their remaining interval is
returned without cleaning Failed Pods or individually replacing members. A policy
or real template revision change can release that eligibility; it does not
abandon a deletion list that has already started. Normal scale-in slots are no
longer desired; normal rolling updates retain generation/revision guards. `Ignore`
auxiliary members do not initiate recovery, though another member's fault can
still select all owned members for deletion.

Recovery does not redefine `startAfter` or custom RI readiness. Existing component
PodReady-based selection remains; mutually initializing ranks must not be ordered
behind each other's Ready. The original ImageID-based +1 in-place allowance is
retained, with init restarts excluded from that allowance; this change does not
add same-digest/ContainerID support. Create, RI status and Pod writes remain
separate API operations, so
external/racing creations are not a cross-resource atomic transaction.

For an unreachable node, eviction typically begins after the not-ready/unreachable
toleration (normally 300 seconds), but old Pods can remain Terminating indefinitely.
This controller does not force-delete or fence nodes. Old UID absence from the
API is not proof that a process on a partitioned node has exited; lower toleration
times shorten detection, not necessarily safe cleanup. There is no NotReady timeout.

## Exponential Backoff

When the resolved restart policy type is `RecreateRoleInstanceOnPodRestart`, subsequent crash-triggered recreations within a stability window are delayed by exponential backoff:

- **First eligible failure**: immediate recovery when no previous attempt time exists.
- **Subsequent eligible failures**: wait only for the remainder of the interval
  measured from the previous attempt's `lastRestartTime`, not from the new failure.
- Formula: `interval = min(baseDelaySeconds * 2^(restartCount-1), maxDelaySeconds)`;
  `remaining = max(0, lastRestartTime + interval - now)`.

For example, if the previous attempt began at t=0 and its count is 1, a new fault
at t=3 with base=10 waits 7 seconds. A fault at t=12 need not wait again: draining
and initialization have already consumed the interval.

When a new attempt starts more than `max(maxDelaySeconds * 2, 10 minutes)` after
the previous attempt began, the count is reset before charging the new attempt
(count becomes 1). This is not a requirement that the workload served Ready for
that duration; a long unsuccessful initialization can also outlast the interval.

### Default Behavior Change

> **Note**: Before the backoff feature, pod crashes always triggered immediate recreation. With default settings (`baseDelaySeconds: 30`, `maxDelaySeconds: 600`), the second and
> subsequent failures within the stability window receive 30s→600s attempt intervals,
> measured from the preceding attempt's start. All existing workloads using the default `RecreateRoleInstanceOnPodRestart` policy
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
120s (capped); the first attempt has no prior interval. Actual waiting after each
new failure is the interval minus elapsed time since the preceding attempt began.

> **Note**: `maxDelaySeconds` must be greater than or equal to `baseDelaySeconds` (enforced by CEL validation at admission). Because `maxDelaySeconds` defaults to 600, setting `baseDelaySeconds` above 600 without explicitly setting `maxDelaySeconds` will be rejected. If you need a base delay above 600s, set `maxDelaySeconds` to the same or higher value.

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

When this annotation is set to "Ignore" on a component's pod template:

- Pod failure, unexpected container restart, and member-loss events from this component will NOT trigger the role's restart policy
- The restart policy (RecreateRoleInstanceOnPodRestart) remains unaffected for other components
- Only pods from components WITHOUT this annotation (or with annotation set to "Inherit") will trigger the restart policy

## Use Cases

- **RecreateRoleInstanceOnPodRestart**: Coupled worker ranks where failure, unexpected restart, or member loss should replace all Pods of the affected instance, including retries before first Ready.
- **None**: Monitoring/logging sidecars that don't affect the main workload. Failed pods are replaced individually.
- **restart-trigger-policy: "Ignore"** (annotation): Auxiliary components within a multi-component instance whose pod failures should not trigger the role's restart policy for the entire instance.

## Examples

- [Restart Policy Examples](../../examples/basic/rbg/restart-policy/restart-policy.yaml)
- [CustomComponentsPattern with restart-trigger-policy annotation](../../examples/basic/rbg/patterns/custom-components-pattern.yaml)
