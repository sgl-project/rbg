# Simplifying Configuration with RoleTemplates

## Overview

In RBG, multiple roles often share the same Pod configuration (such as image, environment variables, resource limits, etc.). `roleTemplates` allows you to define reusable Pod templates at the RBG level. Each role references a template via `templateRef` and can apply differential overrides via `patch`, avoiding the need to repeatedly write the same configuration.

## Prerequisites

+ Kubernetes cluster version >= 1.24
+ RBG Controller installed (see [Installation Guide](https://github.com/sgl-project/rbg))

> **Note**: The following examples use the SGLang engine (`lmsysorg/sglang:v0.5.9`) for demonstration. If using other inference engines, replace with the corresponding image and adjust startup parameters.
>

---

## The Problem Without RoleTemplates

In multi-role inference services, the engine image version, model volume configuration, etc. are the same for each role. For example, in PD-disaggregated deployment, Prefill and Decode roles typically use the same inference engine image, the same GPU resource requests, and the same shared memory mount, with only different startup parameters.

When these common configurations need to be updated (e.g., upgrading the engine image version), you must modify each role's `template` one by one — this is operationally complex and error-prone, as it's easy to miss a role and cause version inconsistency. `roleTemplates` centralizes these common configurations — when updating, you only need to modify the template definition, and all roles referencing that template take effect automatically.

---

## Using RoleTemplates

`roleTemplates` defines reusable Pod templates at the RBG's `spec` level. Roles reference them via `templateRef`.

### Define a Template and Reference It Directly

Define templates in `spec.roleTemplates`, and roles reference them via `templateRef.name`:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: rbg-with-templates
spec:
  # Define reusable templates
  roleTemplates:
    - name: engine-base
      template:
        spec:
          containers:
            - name: engine
              image: lmsysorg/sglang:v0.5.9
              command: ["sleep", "3600"]
              resources:
                requests:
                  cpu: "1"
                  memory: "1Gi"
                limits:
                  cpu: "1"
                  memory: "1Gi"
              volumeMounts:
                - name: dshm
                  mountPath: /dev/shm
          volumes:
            - name: dshm
              emptyDir:
                medium: Memory
                sizeLimit: 30Gi

  roles:
    # Reference the template directly, with no overrides
    - name: backend
      replicas: 1
      standalonePattern:
        templateRef:
          name: engine-base
          patch: {}
```

#### Parameter Description

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `spec.roleTemplates` | []object | No | - | List of reusable Pod templates |
| `spec.roleTemplates[].name` | string | Yes | - | Template name, unique within the RBG, conforming to DNS label format |
| `spec.roleTemplates[].template` | object | Yes | - | Pod template, follows standard Kubernetes `PodTemplateSpec` |
| `spec.roles[].standalonePattern.templateRef.name` | string | Yes | - | Name of the referenced template |

> **Note**: `templateRef` and `template` are mutually exclusive — a role either uses `templateRef` to reference a template, or uses `template` to define directly, but not both.
>

### Override Template via Patch

After referencing a template, roles can apply differential overrides via `templateRef.patch`. Patch uses Kubernetes standard **Strategic Merge Patch** semantics, which can add, modify, or override fields in the template.

Taking PD-disaggregated deployment as an example, Prefill and Decode share the same base configuration but have different startup parameters:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: pd-with-templates
spec:
  roleTemplates:
    - name: engine-base
      template:
        spec:
          containers:
            - name: engine
              image: lmsysorg/sglang:v0.5.9
              ports:
                - name: http
                  containerPort: 8000
              resources:
                requests:
                  nvidia.com/gpu: "1"
                limits:
                  nvidia.com/gpu: "1"
              volumeMounts:
                - name: dshm
                  mountPath: /dev/shm
          volumes:
            - name: dshm
              emptyDir:
                medium: Memory
                sizeLimit: 30Gi

  roles:
    # Prefill: Reference template, add startup parameters via patch
    - name: prefill
      replicas: 1
      standalonePattern:
        templateRef:
          name: engine-base
          patch:
            spec:
              containers:
                - name: engine
                  command:
                    - python3
                    - -m
                    - sglang.launch_server
                    - --model-path
                    - "Qwen/Qwen3-0.6B"
                    - --host
                    - "0.0.0.0"
                    - --port
                    - "8000"
                    - --disaggregation-mode
                    - "prefill"
                    - --tp-size
                    - "1"

    # Decode: Reference template, add different startup parameters via patch
    - name: decode
      replicas: 1
      standalonePattern:
        templateRef:
          name: engine-base
          patch:
            spec:
              containers:
                - name: engine
                  command:
                    - python3
                    - -m
                    - sglang.launch_server
                    - --model-path
                    - "Qwen/Qwen3-0.6B"
                    - --host
                    - "0.0.0.0"
                    - --port
                    - "8000"
                    - --disaggregation-mode
                    - "decode"
                    - --tp-size
                    - "1"
```

#### Parameter Description (templateRef + patch)

| Parameter | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `spec.roles[].standalonePattern.templateRef.name` | string | Yes | - | Name of the referenced template |
| `spec.roles[].standalonePattern.templateRef.patch` | object | No | - | Strategic Merge Patch, used to override fields in the template |

### Patch Merge Rules

Patch follows Kubernetes standard Strategic Merge Patch semantics:

| Field Type | Merge Behavior | Example |
| --- | --- | --- |
| Scalar fields (string, int, bool) | Override | `resources.requests.memory: "128Mi"` overrides the template's value |
| List fields (matched by name) | Merge by name | `containers` list matches by `name` field, merging corresponding container fields |
| Map fields | Recursive merge | `metadata.labels` merges rather than overrides |

Taking resource request override as an example:

```yaml
# Defined in template
containers:
  - name: engine
    resources:
      requests:
        memory: "8Gi"
        nvidia.com/gpu: "1"

# Patch overrides memory, gpu remains unchanged
patch:
  spec:
    containers:
      - name: engine          # Matches the container in template by name
        resources:
          requests:
            memory: "16Gi"    # Override memory
```

### Combining with leaderWorkerPattern

`templateRef` also works with `leaderWorkerPattern`. The template defines the base Pod configuration, while `leaderTemplatePatch` and `workerTemplatePatch` further differentiate Leader and Worker on top of that:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: agg-tp-with-templates
spec:
  roleTemplates:
    - name: engine-base
      template:
        spec:
          containers:
            - name: engine
              image: lmsysorg/sglang:v0.5.9
              resources:
                requests:
                  nvidia.com/gpu: "1"
                limits:
                  nvidia.com/gpu: "1"
              volumeMounts:
                - name: dshm
                  mountPath: /dev/shm
          volumes:
            - name: dshm
              emptyDir:
                medium: Memory
                sizeLimit: 30Gi

  roles:
    - name: backend
      replicas: 1
      leaderWorkerPattern:
        size: 2
        # Reference template as base configuration
        templateRef:
          name: engine-base
          patch:
            spec:
              containers:
                - name: engine
                  command:
                    - python3
                    - -m
                    - sglang.launch_server
                    - --model-path
                    - "Qwen/Qwen3-0.6B"
                    - --host
                    - "0.0.0.0"
                    - --port
                    - "8000"
                    - --tp-size
                    - "2"
                    - --dist-init-addr
                    - $(RBG_LWP_LEADER_ADDRESS):6379
                    - --nnodes
                    - $(RBG_LWP_GROUP_SIZE)
                    - --node-rank
                    - $(RBG_LWP_WORKER_INDEX)
        # On top of the template, add differentiated labels for Leader and Worker
        leaderTemplatePatch:
          metadata:
            labels:
              role: leader
        workerTemplatePatch:
          metadata:
            labels:
              role: worker
```

> **Note**: The configuration application order is `roleTemplates` → `templateRef.patch` → `leaderTemplatePatch` / `workerTemplatePatch`, with subsequent patches layered on top of previous results.
>

---

## Verify Deployment

```bash
# Check RBG status
kubectl get rbg

# Check Pod status
kubectl get pods

# Confirm template references are correctly resolved
kubectl get rbg <rbg-name> -o yaml | grep templateRef
```

## Related Documents

<!-- TODO: The following documents have not been created yet; links will be added once they are complete -->

+ Deploying Inference Services with RBG
+ Configuring HPA Autoscaling
+ Gang Scheduling Configuration
+ Rolling Updates and Canary Releases
