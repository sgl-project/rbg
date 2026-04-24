# Engine Runtime Profile

ClusterEngineRuntimeProfile is a cluster-scoped CRD that defines reusable sidecar and init-container configurations. These profiles can be injected into role pods, enabling consistent runtime dependencies across multiple RoleBasedGroups.

## Overview

Engine runtime profiles are useful for:
- **GPU Driver Injection**: Automatically inject GPU driver init containers
- **Monitoring Sidecars**: Add Prometheus exporters or logging agents
- **Shared Dependencies**: Common runtime libraries across all inference pods
- **Consistent Configuration**: Same sidecar configuration for all roles

## ClusterEngineRuntimeProfile CRD

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: ClusterEngineRuntimeProfile
metadata:
  name: nvidia-runtime-profile
spec:
  # Update strategy: NoUpdate (default) or RollingUpdate
  # NoUpdate: profile changes don't trigger pod updates
  # RollingUpdate: profile changes trigger rolling update of associated pods
  updateStrategy: RollingUpdate
  initContainers:
    - name: gpu-driver-init
      image: nvidia/gpu-driver:init
      command: ["/bin/sh", "-c", "echo 'GPU driver initialized'"]
      volumeMounts:
        - name: gpu-lib
          mountPath: /usr/local/lib/gpu
  containers:
    - name: gpu-monitor
      image: nvidia/gpu-monitor:latest
      ports:
        - containerPort: 9090
          name: metrics
      resources:
        requests:
          cpu: "100m"
          memory: "128Mi"
  volumes:
    - name: gpu-lib
      emptyDir: {}
```

### Key Fields

| Field | Description |
|-------|-------------|
| `updateStrategy` | How profile changes affect existing pods |
| `initContainers` | Init containers to inject |
| `containers` | Sidecar containers to inject |
| `volumes` | Volumes to inject |

### Update Strategies

| Strategy | Behavior |
|----------|----------|
| `NoUpdate` | Profile changes don't trigger pod recreation. Pods need manual update. |
| `RollingUpdate` | Profile changes trigger rolling update of pods using this profile. |

## Using Engine Runtime Profiles

In RoleBasedGroup, reference the profile via `engineRuntimes` field:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: gpu-inference
spec:
  roles:
    - name: prefill
      replicas: 2
      engineRuntimes:
        - profileName: nvidia-runtime-profile
          injectContainers:
            - prefill-main
      standalonePattern:
        template:
          spec:
            containers:
              - name: prefill-main
                image: inference-engine:latest
                resources:
                  limits:
                    nvidia.com/gpu: "1"

    - name: decode
      replicas: 4
      engineRuntimes:
        - profileName: nvidia-runtime-profile
          injectContainers:
            - decode-main
      standalonePattern:
        template:
          spec:
            containers:
              - name: decode-main
                image: inference-engine:latest
                resources:
                  limits:
                    nvidia.com/gpu: "1"
```

### engineRuntimes Configuration

| Field | Description |
|-------|-------------|
| `profileName` | Name of the ClusterEngineRuntimeProfile to use |
| `injectContainers` | Target container names to inject runtime into |
| `containers` | Override specific container configurations |

## Injection Behavior

When a role references an engine runtime profile:

1. **Init Containers**: All init containers from the profile are added to the pod spec
2. **Sidecar Containers**: All containers from the profile are added to the pod spec
3. **Volumes**: All volumes from the profile are added to the pod spec
4. **Target Injection**: If `injectContainers` is specified, volumes are mounted to those containers

## Example: GPU Driver + Monitoring Profile

```yaml
# Step 1: Create the profile
apiVersion: workloads.x-k8s.io/v1alpha2
kind: ClusterEngineRuntimeProfile
metadata:
  name: nvidia-runtime-profile
spec:
  updateStrategy: RollingUpdate
  initContainers:
    - name: gpu-driver-init
      image: nvidia/driver-init:latest
      command: ["/install-driver.sh"]
      volumeMounts:
        - name: nvidia-lib
          mountPath: /usr/local/nvidia
  containers:
    - name: dcgm-exporter
      image: nvidia/dcgm-exporter:latest
      ports:
        - containerPort: 9400
          name: metrics
      resources:
        requests:
          cpu: "100m"
          memory: "128Mi"
  volumes:
    - name: nvidia-lib
      emptyDir: {}

---
# Step 2: Use in RoleBasedGroup
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: inference-with-monitoring
spec:
  roles:
    - name: inference
      replicas: 4
      engineRuntimes:
        - profileName: nvidia-runtime-profile
          injectContainers:
            - inference-engine
      standalonePattern:
        template:
          spec:
            containers:
              - name: inference-engine
                image: sglang:latest
                resources:
                  limits:
                    nvidia.com/gpu: "1"
```

This results in pods with:
- GPU driver init container
- DCGM exporter sidecar for GPU metrics
- NVIDIA library volume mounted to inference container

## Profile Updates

When a ClusterEngineRuntimeProfile is updated:

- With `NoUpdate`: Existing pods retain old configuration until manually updated
- With `RollingUpdate`: Controller triggers rolling update of all affected pods

Rolling update respects each role's `rolloutStrategy` configuration.

## Use Cases

- **GPU Infrastructure**: Inject NVIDIA drivers, DCGM exporters, GPU utilities
- **Observability**: Add Prometheus exporters, logging agents, tracing sidecars
- **Security**: Inject security scanners, certificate managers
- **Common Dependencies**: Shared libraries across multiple inference deployments

## Examples

- [Engine Runtime Profile Example](../../examples/basic/engine-runtime/engine-runtime-profile.yaml)