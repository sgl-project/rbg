# Failure Handling

RBG supports `None`, `RecreateRoleInstanceOnPodRestart`, and the opt-in `RecreateRoleInstanceOnPodRestartFromCreation` policy.

![failure-handling](../img/failure-handling.png)

## Restart Policy Types

| Policy | Description |
|--------|-------------|
| `None` | No automatic restart action; rely on default pod restart behavior. Failed pods are replaced through normal reconciliation. |
| `RecreateRoleInstanceOnPodRestart` | Preserve the existing behavior: recreate the affected role instance on its existing unexpected-restart and Failed-pod triggers. |
| `RecreateRoleInstanceOnPodRestartFromCreation` | Opt-in broader recovery: use the same ordinary-container unexpected-restart and Failed-pod triggers, and also recreate when a desired member pod is deleted. These triggers apply before the instance first becomes Ready and after observed generation matches desired generation. |

The new policy is supported only by native RoleInstanceSet workloads. LeaderWorkerSet, StatefulSet, and Deployment roles are rejected at admission when configured with `RecreateRoleInstanceOnPodRestartFromCreation`, rather than silently ignoring its triggers. Existing `RecreateRoleInstanceOnPodRestart` configuration and behavior are unchanged.

`FromCreation` does not treat Pending or NotReady alone as a failure, bypass generation/revision guards, or count init-container/native-sidecar restarts. An in-place update exempts its expected container restarts, but cannot mask a Failed Pod or an observed desired-member deletion under the new policy.

Both served RBG APIs accept the new policy. In v1alpha1 use `spec.roles[].restartPolicy`; in v1alpha2 use the pattern's `restartPolicyConfig.type`. Conversion preserves the selected policy in both directions.

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

When the resolved restart policy type is `RecreateRoleInstanceOnPodRestart` or `RecreateRoleInstanceOnPodRestartFromCreation`, subsequent crash-triggered recreations within a stability window are delayed by exponential backoff:

- **First crash**: immediate recreation (no delay).
- **Second crash** (within the stability window): delayed by `baseDelaySeconds`.
- **Each subsequent crash**: delay doubles, capped at `maxDelaySeconds`.
- Formula: `delay = min(baseDelaySeconds * 2^(restartCount-1), maxDelaySeconds)`.

The stability window is `max(maxDelaySeconds * 2, 10 minutes)`. If the instance remains stable (no crashes) for longer than this window, `restartCount` resets to 0 and the next crash starts with immediate recreation again.

### Default Behavior Change

> **Note**: Before the backoff feature, pod crashes always triggered immediate recreation. With default settings (`baseDelaySeconds: 30`, `maxDelaySeconds: 600`), the second and
> subsequent crashes within the stability window now receive 30s→600s exponential delays. All existing workloads using the default `RecreateRoleInstanceOnPodRestart` policy
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

With the above configuration: first crash is immediate, second crash delayed 10s, third 20s, fourth 40s, fifth 80s, sixth and beyond capped at 120s.

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

When this annotation is set to "Ignore" on a component's pod template:
- Pod failure and container restart events from this component will NOT trigger the role's restart policy
- The restart policy (RecreateRoleInstanceOnPodRestart) remains unaffected for other components
- Only pods from components WITHOUT this annotation (or with annotation set to "Inherit") will trigger the restart policy

## Use Cases

- **`RecreateRoleInstanceOnPodRestart`**: Existing workloads that depend on the original, readiness-gated unexpected-restart and Failed-pod triggers.
- **`RecreateRoleInstanceOnPodRestartFromCreation`**: RoleInstanceSet workloads that must recover from the same triggers before first readiness or after a desired member pod is deleted.
- **None**: Monitoring/logging sidecars that don't affect the main workload. Failed pods are replaced individually.
- **restart-trigger-policy: "Ignore"** (annotation): Auxiliary components within a multi-component instance whose pod failures should not trigger the role's restart policy for the entire instance.

## Examples

- [Restart Policy Examples](../../examples/basic/rbg/restart-policy/restart-policy.yaml)
- [CustomComponentsPattern with restart-trigger-policy annotation](../../examples/basic/rbg/patterns/custom-components-pattern.yaml)