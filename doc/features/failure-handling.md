# Failure Handling

RBG supports multiple failure handling policies: `None`, `RecreateRBGOnPodRestart`, and `RecreateRoleInstanceOnPodRestart`.

![failure-handling](../img/failure-handling.png)

## Restart Policy Types

| Policy | Description |
|--------|-------------|
| `None` | No automatic restart action; rely on default pod restart behavior. |
| `RecreateRBGOnPodRestart` | Recreate the entire RoleBasedGroup when any pod in this role restarts. Useful for critical roles that require all pods to be healthy. |
| `RecreateRoleInstanceOnPodRestart` | Recreate only the affected role instance when a pod restarts. More granular control for less critical roles. **This is the default policy for all patterns** (standalonePattern, leaderWorkerPattern, customComponentsPattern). |

## Configuration

Set the `restartPolicy` field in each role's spec:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: restart-policy-demo
spec:
  roles:
    # Critical role - recreate entire RBG on restart
    - name: driver
      restartPolicy: RecreateRBGOnPodRestart
      replicas: 1
      standalonePattern:
        template:
          spec:
            containers:
              - name: driver
                image: nginx:latest

    # Worker role - recreate only this instance
    - name: worker
      restartPolicy: RecreateRoleInstanceOnPodRestart
      replicas: 3
      dependencies: ["driver"]
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

## Excluding Components from Restart Policy Trigger (CustomComponentsPattern)

When using `customComponentsPattern`, you can prevent specific components from triggering the role's restart policy by setting an annotation on the component's pod template. This is useful for auxiliary components (e.g., monitoring, logging sidecars) whose failures should not affect the main workload.

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
- Pod restart/delete events from this component will NOT trigger the role's restart policy
- The restart policy (RecreateRoleInstanceOnPodRestart or RecreateRBGOnPodRestart) remains unaffected for other components
- Only pods from components WITHOUT this annotation (or with annotation set to "Inherit") will trigger the restart policy

## Use Cases

- **RecreateRBGOnPodRestart**: Gateway/router roles that require all downstream services to be healthy.
- **RecreateRoleInstanceOnPodRestart**: Worker roles that can tolerate individual instance failures.
- **None**: Monitoring/logging sidecars that don't affect the main workload.
- **ignoreRestartPolicy**: Auxiliary components within customComponentsPattern that should be isolated from restart policy actions.

## Examples

- [Restart Policy Examples](../../examples/basic/rbg/restart-policy/restart-policy.yaml)
- [CustomComponentsPattern with ignoreRestartPolicy](../../examples/basic/rbg/patterns/custom-components-pattern.yaml)