# Role Templates

Role templates allow you to define reusable Pod templates at the RoleBasedGroup level and reference them in multiple roles. This reduces YAML duplication and enables consistent configurations across roles.

## Overview

In v1alpha2, you can define `roleTemplates` in the RoleBasedGroup spec and reference them in roles via `templateRef`. This is useful when:

- Multiple roles share similar pod configurations
- You want to reduce YAML duplication
- Base template with role-specific overrides

## Defining Role Templates

Templates are defined at the top level of RoleBasedGroup spec:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: rbg-with-templates
spec:
  # Define reusable templates
  roleTemplates:
    - name: nginx-base
      template:
        metadata:
          labels:
            app: nginx
        spec:
          containers:
            - name: nginx
              image: nginx:latest
              resources:
                requests:
                  memory: "64Mi"
                  cpu: "100m"

  roles:
    - name: frontend
      replicas: 2
      standalonePattern:
        templateRef:
          name: nginx-base

    - name: backend
      replicas: 3
      standalonePattern:
        templateRef:
          name: nginx-base
          patch:
            spec:
              containers:
                - name: nginx
                  ports:
                    - containerPort: 8080
```

## Template Reference

The `templateRef` field supports:

| Field | Description |
|-------|-------------|
| `name` | Name of the roleTemplate to reference |
| `patch` | Strategic merge patch to apply on top of template |

## Using Templates with Patch

Templates can be customized per role using the `patch` field:

```yaml
roleTemplates:
  - name: inference-base
    template:
      spec:
        containers:
          - name: engine
            image: inference:latest
            resources:
              requests:
                cpu: "2"
                memory: "8Gi"

roles:
  - name: prefill
    replicas: 2
    standalonePattern:
      templateRef:
        name: inference-base
        patch:
          spec:
            containers:
              - name: engine
                env:
                  - name: ROLE_TYPE
                    value: prefill
                resources:
                  requests:
                    nvidia.com/gpu: "1"

  - name: decode
    replicas: 4
    standalonePattern:
      templateRef:
        name: inference-base
        patch:
          spec:
            containers:
              - name: engine
                env:
                  - name: ROLE_TYPE
                    value: decode
                resources:
                  requests:
                    nvidia.com/gpu: "2"
```

The patch is applied as a strategic merge:
- Add new fields not in base template
- Override existing fields in base template
- Lists are merged based on merge key (e.g., `name` for containers)

## Complete Example

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: rbg-with-roletemplates
spec:
  # Define reusable templates shared by multiple roles
  roleTemplates:
    - name: nginx-base
      template:
        metadata:
          labels:
            app: nginx
        spec:
          containers:
            - name: nginx
              image: nginx:1.28
              resources:
                requests:
                  memory: "64Mi"
                  cpu: "100m"

  roles:
    # Role referencing a template directly (no patch)
    - name: frontend
      replicas: 2
      standalonePattern:
        templateRef:
          name: nginx-base

    # Role referencing a template with role-specific overrides
    - name: backend
      replicas: 3
      dependencies: ["frontend"]
      standalonePattern:
        templateRef:
          name: nginx-base
          patch:
            spec:
              containers:
                - name: nginx
                  ports:
                    - containerPort: 8080
                      name: http
                  resources:
                    requests:
                      memory: "128Mi"  # Override base template
                      cpu: "200m"
```

## Template with Different Patterns

Templates work with all workload patterns:

### Standalone Pattern

```yaml
standalonePattern:
  templateRef:
    name: base-template
```

### Leader-Worker Pattern

```yaml
leaderWorkerPattern:
  size: 4
  templateRef:
    name: base-template
  leaderTemplatePatch:
    spec:
      containers:
        - name: engine
          env:
            - name: ROLE
              value: leader
```

### Custom Components Pattern

`customComponentsPattern.components[]` in v1alpha2 does not support `templateRef`. Role templates can be referenced via `templateRef` in `standalonePattern` and `leaderWorkerPattern`, while custom components must define an inline `template`.

```yaml
customComponentsPattern:
  components:
    - name: component-1
      size: 1
      template:
        spec:
          containers:
            - name: engine
              image: example/image:latest
              env:
                - name: COMPONENT
                  value: "1"
```

## Use Cases

- **Multi-Role Inference**: Same base image with role-specific configs
- **Environment Variables**: Different env vars per role from same template
- **Resource Allocation**: Same container but different resource requests
- **Port Configuration**: Different ports per role

## Examples

- [RoleBasedGroup with RoleTemplates](../../examples/basic/rbg/role-temlate/rbg-with-roletemplates.yaml)