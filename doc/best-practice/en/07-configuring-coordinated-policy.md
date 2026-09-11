# Configuring Coordinated Policy

## Overview

In multi-role inference services (such as PD-disaggregated architecture), each role operates independently, but their deployment and upgrades need to remain coordinated. If Prefill and Decode create or update at different rates, it may lead to version mismatches, resource contention, or service unavailability.

`CoordinatedPolicy` is a standalone CRD that defines cross-role coordination strategies. It supports two coordination scenarios:

+ **Coordinated scaling**: Controls creation progress synchronization across multiple roles during initial deployment, ensuring replica counts grow proportionally and progressively
+ **Coordinated upgrade**: Controls update progress synchronization across multiple roles during rolling updates, ensuring consistent update progress across roles

`CoordinatedPolicy` **applies to the same-named RBG in the current namespace**, associating roles by referencing role names in the RBG. It is decoupled from the RBG lifecycle — creating or deleting a CoordinatedPolicy does not affect the RBG itself.

## Prerequisites

+ Kubernetes cluster version >= 1.24
+ RBG Controller installed (see [Installation Guide](https://github.com/sgl-project/rbg))
+ A RoleBasedGroup with multiple roles deployed

---

## Background: Why Multi-Role Coordination Is Needed

In PD-disaggregated architecture, Prefill (prompt encoding) and Decode (token generation) are two independent roles, each with its own replica count and update strategy. When they are deployed or upgraded simultaneously, the following problems arise without coordination:

### Initial Deployment Scenario

#### Uncoordinated Initial Deployment

| Role | Deployment Progress | Description |
| --- | --- | --- |
| Prefill | `0 → 6` (quickly created) | All instances ready |
| Decode | `0 → 2` (only half created) | Instances clearly insufficient |

Problems:

- Prefill instances are ready but Decode is insufficient, unable to form an effective inference pipeline
- Prefill instances created earlier sit idle, wasting GPU resources

### Upgrade Scenario

#### Uncoordinated Upgrade

| Role | Update Progress | Description |
| --- | --- | --- |
| Prefill | Fully updated to new version | All new instances online |
| Decode | Only half updated | Old instances still serving |

Problems:

- New-version Prefill and old-version Decode may have protocol incompatibilities
- Prefill has many unavailable instances while Decode is still serving normally

`CoordinatedPolicy` uses the `maxSkew` parameter to control the progress difference between roles, ensuring they advance in lockstep.

---

## CoordinatedPolicy Basic Structure

`CoordinatedPolicy` defines a set of coordination rules via `spec.policies`. Each rule specifies a group of roles and the corresponding strategy:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: <policy-name>             # Same name as the RBG to bind
  namespace: <ns-name>            # Same namespace as the RBG to bind
spec:
  policies:
    - name: <rule-name>           # Rule name, unique within the CoordinatedPolicy
      roles:                       # List of role names to coordinate
        - <role-1>
        - <role-2>
      strategy:                    # Coordination strategy
        scaling:                   # Coordinated scaling (optional)
          ...
        rollingUpdate:             # Coordinated upgrade (optional)
          ...
```

### Parameter Description

| Parameter | Type | Required | Description |
| --- | --- | --- | --- |
| `spec.policies[].name` | string | Yes | Rule name, unique within the CoordinatedPolicy |
| `spec.policies[].roles` | []string | Yes | List of role names to coordinate, must match role names in the RBG |
| `spec.policies[].strategy.scaling` | object | No | Coordinated scaling strategy (applies to both initial deployment and subsequent scale-outs) |
| `spec.policies[].strategy.rollingUpdate` | object | No | Coordinated upgrade strategy |

> **Note**: `scaling` and `rollingUpdate` can be configured together or used independently. `scaling` takes effect for both initial deployment and subsequent scale-outs, continuously limiting the scale-out progress difference between roles; it does not affect scale-down behavior. A single CoordinatedPolicy can contain multiple rules, each applying to different role groups.
>

---

## Scenario 1: Coordinated Scaling (Initial Deployment)

During initial deployment of a PD-disaggregated architecture, Prefill and Decode replicas need to be created proportionally and progressively. Without coordination, one role may finish creating quickly while the other is still queuing, causing the ready role to sit idle. The `scaling` strategy of `CoordinatedPolicy` uses `maxSkew` to control the creation progress difference between roles and `progression` to control the creation pace, ensuring the multi-role replica ratio always meets expectations.

> **Note**: Coordinated scaling applies to both initial deployment and subsequent scale-outs: during scale-out, `maxSkew` likewise limits the progress difference between roles so that no single role gets too far ahead. The strategy remains in effect continuously and does not expire after initial deployment completes. Scale-down is not limited by this strategy.
>

### Configuration Example

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-inference
spec:
  roles:
    - name: prefill
      replicas: 2
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                ports:
                  - containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"

    - name: decode
      replicas: 4
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                ports:
                  - containerPort: 8000
                resources:
                  requests:
                    nvidia.com/gpu: "1"
                  limits:
                    nvidia.com/gpu: "1"

---
# Coordinated scaling strategy (initial deployment)
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: pd-inference
spec:
  policies:
    - name: prefill-decode-scaling
      roles:
        - prefill
        - decode
      strategy:
        scaling:
          maxSkew: "10%"
          progression: OrderScheduled
```

### Parameter Description (Coordinated Scaling)

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `strategy.scaling.maxSkew` | intOrString | No | `"100%"` | Allowed deployment progress deviation between roles. Only percentage format is supported (e.g., `"10%"`); an integer value (e.g., `2`) fails to parse and blocks creation of all roles |
| `strategy.scaling.progression` | string | No | None | Creation pace: `OrderScheduled` or `OrderReady`. When omitted, batch gating is not applied and replicas are created without waiting for Pods to be scheduled or ready, so setting it explicitly is recommended |

### How maxSkew Works

`maxSkew` controls the creation progress difference between roles. The Controller calculates each role's deployment progress (created replicas / target replicas) and ensures the progress difference between any two roles does not exceed `maxSkew`.

Assume Prefill has a target replica count of 5 and Decode has a target replica count of 10, with `maxSkew: "10%"`:

#### Coordinated Scaling Process (maxSkew: 10%)

| Batch | Prefill Progress | Decode Progress | Progress Difference | Explanation |
| --- | --- | --- | --- | --- |
| 1 | `0 → 1` (20%) | `0 → 1` (10%) | 10% | Both roles start creating proportionally; the difference reaches the maxSkew limit |
| 2 | Holds at 1 (20%) | `1 → 2` (20%) | 0% | The progress cap equals the slowest unfinished role's progress + maxSkew = 0.2; Prefill is already at the cap and is held while Decode catches up |
| 3 | `1 → 2` (40%) | `2 → 3` (30%) | 10% | The difference returns to the limit; Prefill resumes creation |

And so on — the two roles always maintain proportional progressive creation. If Prefill creates too fast and the progress difference exceeds 10%, the Controller temporarily holds Prefill creation and waits for Decode to catch up.

#### Choosing maxSkew

| maxSkew Value | Behavior | Applicable Scenario |
| --- | --- | --- |
| `"1%"` | Nearly synchronous creation, minimal progress difference | Scenarios requiring extremely high inter-role consistency |
| `"10%"` | Allows small progress differences | Most production environments |
| `"50%"` | Allows larger progress differences | Prioritizes deployment speed over consistency |
| `"100%"` | No progress difference restriction (default) | No coordination needed, roles create independently |

### progression Mode Comparison

| Mode | Behavior | Applicable Scenario |
| --- | --- | --- |
| `OrderScheduled` | A Pod is considered complete once scheduled (node assigned), then the next batch proceeds | Sufficient GPU resources, prioritizing deployment speed |
| `OrderReady` | All Pods must be fully Ready before proceeding to the next batch | Ensures new instances are available before continuing, safer |

> **Note**: In `OrderReady` mode, if a Pod takes too long to become ready due to model loading time, it blocks subsequent role creation. It is recommended to configure appropriate initial delays with `startupProbe` / `readinessProbe`.
>

---

## Scenario 2: Coordinated Upgrade

When multiple roles of an RBG trigger rolling updates simultaneously (e.g., updating the inference engine image version), the `rollingUpdate` strategy of `CoordinatedPolicy` ensures consistent update progress across roles, avoiding version mismatches.

### Configuration Example (Coordinated Upgrade)

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-inference
spec:
  roles:
    - name: prefill
      replicas: 7
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.6.0   # New version

    - name: decode
      replicas: 3
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.6.0   # New version

---
# Coordinated upgrade strategy
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: pd-inference
spec:
  policies:
    - name: prefill-decode-rollout
      roles:
        - prefill
        - decode
      strategy:
        rollingUpdate:
          maxSkew: "10%"
          maxUnavailable: "10%"
```

### Parameter Description (Coordinated Upgrade)

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `strategy.rollingUpdate.maxSkew` | intOrString | No | `"100%"` | Allowed update progress deviation between roles |
| `strategy.rollingUpdate.maxUnavailable` | intOrString | No | - | Maximum unavailable instances allowed per role during coordinated update. Percentages are scaled against each role's own replica count. This value overrides the role's own `rolloutStrategy.rollingUpdate.maxUnavailable` |
| `strategy.rollingUpdate.partition` | intOrString | No | - | Partition value for coordinated updates. Percentages are scaled against each role's own replica count (e.g., `"50%"` becomes 4 on a 7-replica role and 2 on a 3-replica one). It acts as a floor only: if `maxSkew` derives a larger partition, the latter wins |

### How Coordinated Upgrade Works

Assume Prefill has 7 instances and Decode has 3 instances, with `maxSkew: "10%"`:

1. **Calculate update progress**: Each role's update progress = updated instances / total instances
2. **Control progress difference**: The Controller ensures the update progress difference between Prefill and Decode does not exceed 10%
3. **Coordinate update order**: If Prefill updates too fast, the Controller temporarily holds Prefill's update and waits for Decode to catch up

#### Coordinated Upgrade Process (maxSkew: 10%)

| Role | Instances | Update Status | Update Progress |
| --- | --- | --- | --- |
| Prefill | 7 | 4/7 updated | 57% |
| Decode | 3 | 1/3 updated | 33% |

Progress difference: `57% - 33% = 24%`, exceeds `10%`. The Controller temporarily holds Prefill's update and prioritizes Decode's update.

### Choosing maxSkew (Coordinated Upgrade)

| maxSkew Value | Behavior | Applicable Scenario |
| --- | --- | --- |
| `"1%"` | Nearly synchronous update, minimal progress difference | Scenarios requiring extremely high version consistency |
| `"10%"` | Allows small progress differences | Most production environments |
| `"50%"` | Allows larger progress differences | Prioritizes update speed over consistency |
| `"100%"` | No progress difference restriction (default) | No coordination needed, roles update independently |

> **Note**: The smaller the `maxSkew`, the slower the update, but the higher the version consistency across roles. Choose an appropriate value based on business requirements and cluster size.
>

---

## Scenario 3: Configuring Both Scaling and Upgrade Coordination

`scaling` and `rollingUpdate` can be configured simultaneously in the same CoordinatedPolicy without conflict:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: pd-full-policy
spec:
  policies:
    - name: prefill-decode
      roles:
        - prefill
        - decode
      strategy:
        # Coordinated scaling (initial deployment)
        scaling:
          maxSkew: "10%"
          progression: OrderScheduled
        # Coordinated upgrade
        rollingUpdate:
          maxSkew: "10%"
          maxUnavailable: "10%"
```

> **Note**: The `maxSkew` for scaling and upgrade can be set to different values. For example, allow a larger deviation (`"50%"`) during initial deployment for speed, while requiring strict synchronization (`"1%"`) during upgrades to ensure version consistency.
>

---

## Multi-Rule Configuration

A single CoordinatedPolicy can contain multiple rules, each applying to different role groups. This is useful in complex inference services, such as a Router + Prefill + Decode three-role architecture:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: multi-rule-policy
spec:
  policies:
    # Rule 1: Prefill and Decode coordinated deployment
    - name: engine-scaling
      roles:
        - prefill
        - decode
      strategy:
        scaling:
          maxSkew: "10%"
          progression: OrderReady

    # Rule 2: Prefill and Decode coordinated upgrade
    - name: engine-rollout
      roles:
        - prefill
        - decode
      strategy:
        rollingUpdate:
          maxSkew: "5%"
```

---

## Verification

```bash
# Check CoordinatedPolicy status
kubectl get cpolicy

# View CoordinatedPolicy details
kubectl get cpolicy <policy-name> -o yaml

# View RBG role replica counts (deployment progress coordination)
kubectl get rbg <rbg-name> -o jsonpath='{range .spec.roles[*]}{.name}{"="}{.replicas}{"\n"}{end}'

# View role Pod update status (upgrade coordination)
kubectl get pods -l rbg.workloads.x-k8s.io/group-name=<rbg-name> -o wide
```

## Related Documents

+ [Deploying Inference Services with RBG](./01-deploy-inference-service.md)
+ [Configuring Rolling Update Strategies](./03-configuring-rolling-updates.md)
+ Configuring Autoscaling Policies for RBG Services
+ In-Place Update and In-Place Scheduling
