# Failure Handling

RBG supports multiple failure handling policies: `None` and `RecreateRoleInstanceOnPodRestart`.

![failure-handling](../img/failure-handling.png)

## Restart Policy Types

| Policy | Description |
|--------|-------------|
| `None` | No automatic restart action; rely on default pod restart behavior. Failed pods are replaced through normal reconciliation. |
| `RecreateRoleInstanceOnPodRestart` | Recreate only the affected role instance when a pod fails or a container restarts. **This is the default policy for all patterns** (standalonePattern, leaderWorkerPattern, customComponentsPattern). |

## Configuration

Set the `restartPolicy` field in each role's spec:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: restart-policy-demo
spec:
  roles:
    # Worker role - recreate only this instance on pod failure
    - name: worker
      restartPolicy: RecreateRoleInstanceOnPodRestart
      replicas: 3
      standalonePattern:
        template:
          spec:
            containers:
              - name: worker
                image: nginx:latest

    # Monitor role - no restart action
    - name: monitor
      restartPolicy: None
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: monitor
                image: nginx:latest
```

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
      restartPolicy: RecreateRoleInstanceOnPodRestart
      customComponentsPattern:
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

- **RecreateRoleInstanceOnPodRestart**: Worker roles where pod failure or container restart should recreate the affected instance.
- **None**: Monitoring/logging sidecars that don't affect the main workload. Failed pods are replaced individually.
- **restart-trigger-policy: "Ignore"** (annotation): Auxiliary components within a multi-component instance whose pod failures should not trigger the role's restart policy for the entire instance.

## Examples

- [Restart Policy Examples](../../examples/basic/rbg/restart-policy/restart-policy.yaml)
- [CustomComponentsPattern with restart-trigger-policy annotation](../../examples/basic/rbg/patterns/custom-components-pattern.yaml)