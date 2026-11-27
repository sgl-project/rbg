# RBG architecture

> Last verified against `main` at `1fc69f70`. Pointers name files and symbols, not line
> numbers; if a symbol moved, search for it and update this page.

These pages explain how the RBG operator works on `main`: what each controller does and in what order, which file and function to read, which labels and annotations connect the objects, and where the tests are. They are written for contributors and for AI agents who develop or review RBG code.

The pages hold facts, not rules. The rules every change must follow (upgrade compatibility, API and admission, controller write paths, build and test expectations, review practice) are in the [rbg-conventions skill](../../../.agents/skills/rbg-conventions/SKILL.md). Where a rule governs a behavior, the pages link to it instead of repeating it.

## Pages

| Page | Read it for |
|---|---|
| [Overview](overview.md) | Glossary, custom resources and ownership, process topology, repository and package layout, code lineage, the kubectl-rbg CLI |
| [API layer](api-layer.md) | `api/workloads`: the role model and patterns, labels and annotations, helpers, defaulting, validation and webhooks, conversion, generated code |
| [Controller manager](controller-manager.md) | `cmd/rbgs`: flags and Helm values, cache configuration, field indexes, conditional registration, webhook certificates, the controller inventory |
| [RoleBasedGroup controller](rbg-controller.md) | The RBG reconcile pipeline, workload reconcilers, pod template rendering, Services, coordination, scaling adapters, adoption, status |
| [RoleInstanceSet and RoleInstance](instance-workloads.md) | Stateful and stateless RoleInstanceSets, RoleInstance Pods, restart policies, readiness, in-place update |
| [Other controllers](other-controllers.md) | RoleBasedGroupSet, scaling adapters, warmup, the webhook certificate controller, engine runtime profiles |
| [Scheduling and networking](scheduling-and-networking.md) | Gang scheduling, in-place scheduling, exclusive topology, port allocation, Services and discovery |
| [Testing](testing.md) | Test layers, helpers, running and focusing tests, where a new test belongs |
| [Feature index](feature-index.md) | Each feature's API, code, KEP, user doc and tests, and which KEPs are implemented |

For a first pass, read the overview, the API layer, the RoleBasedGroup controller, RoleInstanceSet and RoleInstance, and testing, in that order. Read the other pages when a task touches them.

## Which page for which task

| Task | Pages | Rules to apply |
|---|---|---|
| Add or change an API field, default or validation | [API layer](api-layer.md), plus the page of the controller that reads the field | [api-and-admission.md](../../../.agents/skills/rbg-conventions/references/api-and-admission.md), [upgrade-compatibility.md](../../../.agents/skills/rbg-conventions/references/upgrade-compatibility.md) |
| Change what the controller renders: pod templates, labels, env, Services | [RoleBasedGroup controller](rbg-controller.md), [Scheduling and networking](scheduling-and-networking.md) | [upgrade-compatibility.md](../../../.agents/skills/rbg-conventions/references/upgrade-compatibility.md), [controller-patterns.md](../../../.agents/skills/rbg-conventions/references/controller-patterns.md) |
| Change rollout, scaling or restart behavior | [RoleInstanceSet and RoleInstance](instance-workloads.md), [RoleBasedGroup controller](rbg-controller.md) | [upgrade-compatibility.md](../../../.agents/skills/rbg-conventions/references/upgrade-compatibility.md), [controller-patterns.md](../../../.agents/skills/rbg-conventions/references/controller-patterns.md) |
| Change scheduling, ports or discovery | [Scheduling and networking](scheduling-and-networking.md) | [upgrade-compatibility.md](../../../.agents/skills/rbg-conventions/references/upgrade-compatibility.md), [controller-patterns.md](../../../.agents/skills/rbg-conventions/references/controller-patterns.md) |
| Add a flag, watch, cache rule or controller | [Controller manager](controller-manager.md) | [controller-patterns.md](../../../.agents/skills/rbg-conventions/references/controller-patterns.md), [build-test-release.md](../../../.agents/skills/rbg-conventions/references/build-test-release.md) |
| Change the Helm chart, manifests, RBAC or generated code | [Controller manager](controller-manager.md), [API layer](api-layer.md) | [build-test-release.md](../../../.agents/skills/rbg-conventions/references/build-test-release.md) |
| Write or fix tests | [Testing](testing.md), plus the page of the code under test | [build-test-release.md](../../../.agents/skills/rbg-conventions/references/build-test-release.md) |
| Review a pull request or KEP | The pages that the path map below gives for the diff; for a KEP, the [feature index](feature-index.md) | [review-playbook.md](../../../.agents/skills/rbg-conventions/references/review-playbook.md) |

## Changed paths to pages

When you review a diff, read the page for every path it touches.

| Changed path | Page |
|---|---|
| `api/workloads/**`, `client-go/**`, `config/crd/**`, `config/webhook/**`, `hack/update-codegen.sh`, `hack/internal/**` | [API layer](api-layer.md) |
| `cmd/rbgs/**`, `pkg/webhook/**`, `pkg/utils/fieldindex/**`, `pkg/utils/client/**`, `config/manager/**`, `config/rbac/**`, `deploy/**`, `hack/gen-helm-rbac/**`, `tools/crd-upgrade/**` | [Controller manager](controller-manager.md); for CRDs and webhook manifests also [API layer](api-layer.md) |
| `internal/controller/workloads/rolebasedgroup_controller*.go`, `internal/controller/workloads/coordinatedpolicy_migration_controller*.go`, `pkg/reconciler/*.go`, `pkg/dependency/**`, `pkg/coordination/**`, `pkg/scale/**`, `pkg/utils/revision_utils*.go`, `pkg/utils/utils.go` | [RoleBasedGroup controller](rbg-controller.md) |
| `internal/controller/workloads/roleinstance*_controller.go`, `pkg/reconciler/roleinstanceset/**`, `pkg/reconciler/roleinstance/**`, `pkg/reconciler/utils/**`, `pkg/inplace/**`, `pkg/utils/expectations/**`, `pkg/utils/history/**`, `pkg/utils/revisionadapter/**`, `pkg/utils/requeueduration/**`, `api/workloads/ordinals/**` | [RoleInstanceSet and RoleInstance](instance-workloads.md) |
| `internal/controller/workloads/rolebasedgroupset_controller*.go`, `internal/controller/workloads/rolebasedgroupscalingadapter_controller*.go`, `internal/controller/workloads/rolebasedgroupwarmup_controller*.go`, `internal/controller/workloads/webhook_cert_controller.go`, `pkg/discovery/sidecar_builder*.go` | [Other controllers](other-controllers.md) |
| `pkg/scheduler/**`, `pkg/port-allocator/**`, `pkg/discovery/**`, `pkg/component-discovery/**`, `pkg/reconciler/pod_reconciler.go`, `pkg/reconciler/svc_reconciler*.go`, `pkg/reconciler/roleinstance/sync/node_binding*.go`, `pkg/utils/service_utils.go` | [Scheduling and networking](scheduling-and-networking.md) |
| `test/**`, `**/*_test.go`, `.github/workflows/**`, `.github/actions/**`, the test targets in the `Makefile` | [Testing](testing.md), plus the page of the code under test |
| `cmd/cli/**`, `version/**`, `pkg/utils/ratelimiter/**`, `go.mod`, `vendor/**`, a new top-level directory, a new package under `pkg` | [Overview](overview.md) |
| `keps/**` | [Feature index](feature-index.md) for KEP status, plus the page of the subsystem the KEP changes |
| `doc/features/**`, `doc/best-practice/**`, `doc/reference/**`, `examples/**` | [Feature index](feature-index.md), to find the code the document or example describes |
| `AGENTS.md`, `.agents/**`, `doc/dev/architecture/**` | The maintenance rules on this page and in the [rbg-conventions skill](../../../.agents/skills/rbg-conventions/SKILL.md) |

## Maintaining these pages

- Every page except this one and the feature index uses the same layout: a "Last verified" line, a scope statement, "Where the code lives", sections in execution order, "Behaviors worth knowing", "Tests" and "Related".
- Point to code with backticked repository-relative paths and symbol names, never with line numbers or permalinks. Use Markdown links only for documents.
- Describe what `main` does. Label behavior that a KEP proposes but `main` lacks as proposed and not implemented on `main`.
- Keep rules in the rbg-conventions skill and link to them from here; don't copy them.
- When a change alters behavior that a page describes, update the page in the same pull request. Update the "Last verified" commit when you re-check a whole page.
- After editing, check that every backticked path exists, that `git grep` finds every symbol in the file the page names, and that markdownlint passes with `.github/.markdownlint.json`.

## Related

- [AGENTS.md](../../../AGENTS.md), the entry point for AI agents.
- The [developer guide](../how_to_develop.md), the [release guide](../how_to_release.md) and [CONTRIBUTING.md](../../../CONTRIBUTING.md).
- The documentation [table of contents](../../TOC.md) for user docs, and the [KEP template](../../../keps/NNNN-kep-template/README.md).
