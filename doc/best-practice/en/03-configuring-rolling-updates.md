# Configuring Rolling Update Strategies

## Overview

RBG supports configuring rolling update strategies via `rolloutStrategy` to control how, at what rate, and to what extent role instances are updated. For multi-role inference services (such as PD-disaggregated architecture), you can also use `CoordinatedPolicy` to achieve cross-role coordinated upgrades, ensuring consistent update progress across roles.

> **Note**: This document does not cover the detailed mechanisms of in-place update (In-Place Update). In-place update will be introduced in a separate document.
>

## Prerequisites

+ Kubernetes cluster version >= 1.24
+ RBG Controller installed (see [Installation Guide](https://github.com/sgl-project/rbg))

---

## Rolling Update Basic Configuration

Each role can configure an independent rolling update strategy via the `rolloutStrategy` field. When a role's `template` changes (e.g., updating the image version), RBG Controller gradually updates instances according to the configured strategy.

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: rolling-update-demo
spec:
  roles:
    - name: backend
      replicas: 4
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 1
          maxSurge: 0
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
```

### Parameter Description

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `rolloutStrategy.type` | string | No | `RollingUpdate` | Update strategy type, currently only supports `RollingUpdate` |
| `rolloutStrategy.rollingUpdate.maxUnavailable` | intOrString | No | `1` | Maximum number of unavailable instances during update. Can be an absolute value (e.g., `1`) or a percentage (e.g., `"25%"`) |
| `rolloutStrategy.rollingUpdate.maxSurge` | intOrString | No | `0` | Maximum number of instances allowed beyond the desired replica count during update. Can be an absolute value or a percentage |

#### How maxUnavailable and maxSurge Work

+ **maxUnavailable**: Controls the upper limit of "unavailable instances" during the update process. Higher values mean faster updates but lower service availability.
+ **maxSurge**: Controls whether additional instances can be created to speed up updates. Set to `0` means delete old instances first then create new ones; set to a positive number means create new instances first then delete old ones.

**Example combinations**:

| maxUnavailable | maxSurge | Behavior | Applicable Scenario |
| --- | --- | --- | --- |
| `1` | `0` | One-by-one replacement, no extra instances created | Resource-constrained environments |
| `1` | `1` | Create 1 new instance first, then delete 1 old instance | Balance availability and update speed |
| `"25%"` | `"25%"` | Allow 25% instances unavailable, create 25% extra instances simultaneously | Fast updates, lower availability requirements |

---

## Canary Release: Partition Control

The `partition` parameter allows you to control update progress, enabling canary releases. Only instances with ordinal **greater than or equal to** `partition` are updated; instances with ordinal less than `partition` remain on the old version.

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: canary-deployment
spec:
  roles:
    - name: backend
      replicas: 4
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 1
          partition: 2  # Only update instances with ordinal >= 2 (i.e., instances 2 and 3)
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
```

### Parameter Description (Canary Release)

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `rolloutStrategy.rollingUpdate.partition` | int | No | `0` | Partition value. Only instances with ordinal >= partition are updated |

### Canary Release Flow

Assuming `replicas: 4`, the update flow is as follows:

1. **Set** `partition: 4`: All instances remain on the old version, no update starts
2. **Set** `partition: 3`: Only update instance 3 (the last one), observe the new version's behavior
3. **Set** `partition: 2`: Update instances 2 and 3, continue observing
4. **Set** `partition: 0`: Update all instances, complete the release

> **Note**: Instance ordinals start from 0. `partition: 0` (default value) means update all instances.
>

---

## Pause and Resume Updates

The `paused` parameter can pause rolling updates, allowing you to manually control the timing of updates.

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: paused-update
spec:
  roles:
    - name: backend
      replicas: 4
      rolloutStrategy:
        type: RollingUpdate
        rollingUpdate:
          maxUnavailable: 1
          paused: true  # Pause updates
      standalonePattern:
        template:
          spec:
            containers:
              - name: engine
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000
```

### Parameter Description (Pause and Resume)

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `rolloutStrategy.rollingUpdate.paused` | bool | No | `false` | Whether to pause rolling updates |

### Use Cases

+ **Manual approval**: Pause after updating the template, confirm configuration is correct before resuming updates
+ **Phased release**: First update some instances (with `partition`), observe for a while before continuing
+ **Emergency rollback**: Immediately pause updates when issues are discovered, preventing further impact

To resume updates, set `paused` to `false` (or remove the field), and the update will continue.

---

## Advanced Configuration: Multi-Role Coordinated Upgrade

### Why Coordinated Upgrades Are Needed

In PD-disaggregated architecture, Prefill and Decode are two independent roles, each with its own independent rolling update strategy. If updates are triggered simultaneously, the two roles may update at different speeds, leading to the following issues:

1. **Version mismatch**: Prefill has been updated to the new version, but Decode is still using the old version — the protocol or data structures between them may be incompatible
2. **Service unavailability**: If Prefill updates too fast, many instances become unavailable while Decode hasn't prepared to receive traffic, potentially causing request failures
3. **Resource contention**: Both roles updating simultaneously may cause cluster resource (e.g., GPU, network bandwidth) contention, slowing down updates

**Coordinated upgrades** ensure that Prefill and Decode's update progress remains consistent, avoiding the above issues.

### Configuring CoordinatedPolicy

`CoordinatedPolicy` is an independent CRD used to define cross-role coordination policies. It controls the update progress difference between roles via the `maxSkew` parameter.

> **Important**: The binding between a `CoordinatedPolicy` and a `RoleBasedGroup` is based on **same-name matching within the same namespace** — that is, a `CoordinatedPolicy` only takes effect for the RBG that has the **same namespace and the same name**. Therefore, the `CoordinatedPolicy`'s `metadata.name` and `metadata.namespace` must exactly match those of the target RBG. Each RBG can be bound to at most one CoordinatedPolicy.

```yaml
---
# RBG definition
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-rollout-demo
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
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000

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
                image: lmsysorg/sglang:v0.5.9
                command: ["sleep", "3600"]
                ports:
                  - containerPort: 8000


---
# CoordinatedPolicy defines coordinated upgrade
apiVersion: workloads.x-k8s.io/v1alpha2
kind: CoordinatedPolicy
metadata:
  name: pd-rollout-demo
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
| `spec.policies[].name` | string | Yes | - | Policy name, unique within the CoordinatedPolicy |
| `spec.policies[].roles` | []string | Yes | - | List of role names that need coordination |
| `spec.policies[].strategy.rollingUpdate.maxSkew` | string | No | `"100%"` | Maximum allowed progress deviation (percentage). Lower values mean tighter coordination |
| `spec.policies[].strategy.rollingUpdate.maxUnavailable` | string | No | - | Maximum proportion of unavailable instances allowed during coordinated update |

### How Coordinated Upgrades Work

Assume Prefill has 7 instances, Decode has 3 instances, `maxSkew: "10%"`:

1. **Calculate update progress**: Each role's update progress = updated instances / total instances
2. **Control progress difference**: Controller ensures the update progress difference between Prefill and Decode does not exceed 10%
3. **Coordinate update order**: If Prefill updates too fast, Controller pauses Prefill's update, waiting for Decode to catch up; and vice versa

**Example**:

+ Prefill updated 4/7 = 57% of instances
+ Decode updated 1/3 = 33% of instances
+ Progress difference = 57% - 33% = 24% > 10% (exceeds maxSkew)
+ Controller pauses Prefill's update, prioritizes updating Decode, until progress difference <= 10%

### Choosing maxSkew

| maxSkew Value | Behavior | Applicable Scenario |
| --- | --- | --- |
| `"1%"` | Near-synchronous update, minimal progress difference | Scenarios requiring strict version consistency |
| `"10%"` | Allows small progress difference | Most production environments |
| `"50%"` | Allows larger progress difference | Higher update speed requirements, lower consistency requirements |
| `"100%"` | No progress difference limit (default) | No coordination needed, each role updates independently |

> **Note**: The lower the `maxSkew`, the slower the update speed, but the higher the version consistency across roles. It is recommended to choose an appropriate value based on business requirements and cluster scale.
>

---

## Verify Update Status

```bash
# Check RBG status
kubectl get rbg

# Check role instance update status
kubectl get roleinstances -l rbg-name=<rbg-name>

# Check CoordinatedPolicy status
kubectl get coordinatedpolicy
```

## Related Documents

+ [Deploying Inference Services with RBG](01-deploy-inference-service.md)
+ [Using RoleTemplates to Reduce Configuration Duplication](02-using-role-templates.md)
+ In-Place Update (In-Place Update)
+ [Configuring HPA Autoscaling](08-configuring-autoscaling.md)
