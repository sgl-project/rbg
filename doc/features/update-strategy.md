# Update Strategy

Rolling update is important to online services with zero downtime. For LLM inference services, this is particularly
important. Two different configurations are supported in RBG, **maxUnavailable** and **maxSurge**.

```yaml
rolloutStrategy:
  type: RollingUpdate
  rollingUpdateConfiguration:
    maxUnavailable: 2
    maxSurge: 2
  replicas: 4
```

## Examples
- [rolling-update](../../examples/basics/rolling-update.yaml)