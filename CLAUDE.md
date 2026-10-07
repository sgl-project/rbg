# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

RoleBasedGroup (RBG) is a Kubernetes API for orchestrating distributed, stateful AI inference workloads with multi-role collaboration and built-in service discovery. It treats an inference service as a role-based group — a topologized, stateful, coordinated multi-role organism managed as a single unit.

## Build and Development Commands

### Building
```bash
make build          # Build controller binary (outputs to bin/manager)
make build-cli      # Build CLI binary (outputs to bin/kubectl-rbg)
make build-cli-all  # Build CLI for all platforms (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64)
```

### Code Generation
```bash
make manifests      # Generate CRDs, RBAC, and WebhookConfiguration
make generate       # Generate DeepCopy methods and client code
```

### Testing
```bash
make test           # Run unit tests (api, cmd, internal, pkg)
make test-envtest   # Run envtest integration tests (requires KUBEBUILDER_ASSETS)
make test-e2e       # Run e2e tests (requires Kind cluster)
```

### Linting
```bash
make lint           # Run golangci-lint
make lint-fix       # Run golangci-lint with auto-fix
```

### Copyright Headers
```bash
make copyright-check  # Check copyright headers on changed Go files
make copyright-fix    # Add missing copyright headers
```

### Running Locally
```bash
make run            # Run controller with webhooks enabled
make run-local      # Run controller in local debug mode (webhooks disabled, no leader election)
```

### Docker Images
```bash
make docker-build       # Build controller and CRD upgrader images
make docker-push        # Push images to registry
make docker-buildx-push # Build and push multi-arch images
```

### Deployment
```bash
make install         # Install CRDs into cluster
make uninstall       # Uninstall CRDs from cluster
make deploy          # Deploy controller to cluster
make undeploy        # Undeploy controller from cluster
make helm-deploy     # Deploy via Helm
make helm-undeploy   # Undeploy via Helm
```

## Architecture

### API Versions
- `v1alpha1`: Legacy API (being deprecated)
- `v1alpha2`: Current storage version with conversion webhooks

### Core CRDs (api/workloads/v1alpha2/)
- **RoleBasedGroup**: Primary CRD — defines a group of roles forming one logical service
- **RoleInstance**: Collection of Pods with tightly bound lifecycle, supports in-place updates
- **RoleInstanceSet**: Manages a set of RoleInstances
- **CoordinatedPolicy**: Controls coordinated operations across roles (maxSkew, progression)
- **RoleBasedGroupScalingAdapter**: HPA integration for auto-scaling
- **ClusterEngineRuntimeProfile**: Engine runtime configuration profiles

### Controllers (internal/controller/workloads/)
- `rolebasedgroup_controller.go`: Main RBG reconciler — orchestrates role creation, dependencies, and status
- `roleinstance_controller.go`: Manages individual role instances
- `roleinstanceset_controller.go`: Manages RoleInstanceSet lifecycle
- `pod_controller.go`: Handles Pod events and RBG-level restart policies
- `rolebasedgroupscalingadapter_controller.go`: Auto-created scaling adapter management
- `rolebasedgroupset_controller.go`: RoleBasedGroupSet controller

### Reconcilers (pkg/reconciler/)
- `deploy_reconciler.go`: Deployment creation/updates
- `sts_reconciler.go`: StatefulSet management
- `lws_reconciler.go`: LeaderWorkerSet integration
- `roleinstanceset_reconciler.go`: RoleInstanceSet management
- `svc_reconciler.go`: Service creation
- `pod_reconciler.go`: Pod-level operations

### Key Packages
- `pkg/discovery/`: Service discovery and topology awareness
- `pkg/dependency/`: Role dependency resolution
- `pkg/scheduler/`: Gang scheduling integration (Volcano, scheduler-plugins)
- `pkg/port-allocator/`: Dynamic port allocation
- `pkg/inplace/`: In-place update support
- `pkg/coordination/`: Cross-role coordination logic

### Deployment Patterns
Each role in a RoleBasedGroup uses one of three patterns:
1. **standalonePattern**: Single pod per instance (single-GPU)
2. **leaderWorkerPattern**: Leader + workers for tensor parallelism (multi-GPU)
3. **customComponentsPattern**: Custom multi-component deployment

## Key Concepts

### Role Dependencies
Roles can declare `dependencies` on other roles, ensuring ordered startup. The controller waits for dependency roles to be ready before starting dependent roles.

### Rollout Strategy
- `RollingUpdate`: Incremental pod replacement
- Supports `partition`, `maxUnavailable`, `maxSurge`
- In-place update strategies: `RecreatePod`, `InPlaceIfPossible`, `InPlaceOnly`

### Restart Policy
- `None`: Default pod restart behavior
- `RecreateRBGOnPodRestart`: Entire RBG recreation on pod failure
- `RecreateRoleInstanceOnPodRestart`: Role instance recreation on pod failure

### Gang Scheduling
Integrated with Volcano and scheduler-plugins for coordinated pod scheduling across roles.

## CLI (kubectl-rbg)

The CLI is in `cmd/cli/` and provides LLM-focused commands:
- `kubectl rbg llm config init`: Initialize configuration
- `kubectl rbg llm model pull MODEL_ID`: Pull model to storage
- `kubectl rbg llm svc run NAME MODEL_ID`: Deploy inference service
- `kubectl rbg llm svc chat NAME`: Chat with deployed model
- `kubectl rbg llm benchmark run RBG_NAME`: Run benchmarks

## Testing Guidelines

- Unit tests use standard Go testing with Ginkgo/Gomega for controller tests
- Envtest tests require `KUBEBUILDER_ASSETS` pointing to envtest binaries
- E2E tests assume a Kind cluster with RBG installed
- Test command excludes vendor directory

## Code Style

- Follow standard Go conventions
- All Go files must have Apache 2.0 copyright headers
- Use `make lint` before submitting PRs
- DCO sign-off required on all commits (`git commit -s`)

## Environment Variables

Key controller flags (see `cmd/rbgs/main.go`):
- `--enable-webhooks`: `all` or `none` (for local debugging)
- `--scheduler-name`: `scheduler-plugins` or `volcano`
- `--enable-port-allocator`: Enable dynamic port allocation
- `--max-concurrent-reconciles`: Controller worker count (default: 10)

Injected into pods:
- `RBG_ROLE_NAME`: Current role name
- `RBG_ROLE_INDEX`: Role ordinal index
- `RBG_GROUP_NAME`: RBG name