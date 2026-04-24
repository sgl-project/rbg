# Failure Handling

RBG supports multiple failure handling policies: `None`, `RecreateRBGOnPodRestart`, and `RecreateRoleInstanceOnPodRestart`.

![failure-handling](../img/failure-handling.png)

## Restart Policy Types

| Policy | Description |
|--------|-------------|
| `None` | No automatic restart action; rely on default pod restart behavior. |
| `RecreateRBGOnPodRestart` | Recreate the entire RoleBasedGroup when any pod in this role restarts. Useful for critical roles that require all pods to be healthy. |
| `RecreateRoleInstanceOnPodRestart` | Recreate only the affected role instance when a pod restarts. More granular control for less critical roles. |

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

## Use Cases

- **RecreateRBGOnPodRestart**: Gateway/router roles that require all downstream services to be healthy.
- **RecreateRoleInstanceOnPodRestart**: Worker roles that can tolerate individual instance failures.
- **None**: Monitoring/logging sidecars that don't affect the main workload.

## Examples

- [Restart Policy Examples](../../examples/basic/rbg/restart-policy/restart-policy.yaml)