# Feature index

> Last verified against `main` at `1fc69f70`. Pointers name files and symbols, not line
> numbers; if a symbol moved, search for it and update this page.

This index maps each RBG feature to what users set, the code that implements it, its design (KEP), its user documentation and its tests. Use it to find where a change starts and which tests and docs it should touch. Each section names the architecture page that explains how the code works; the rules for changing it are in the [rbg-conventions skill](../../../.agents/skills/rbg-conventions/SKILL.md). A KEP link does not mean the KEP is implemented: check the KEP status table at the end of this page before relying on KEP text.

## Group model and API

How it works: [API layer](api-layer.md) and [RoleBasedGroup controller](rbg-controller.md).

| Feature | User-facing API | Main code | KEP | User doc | Tests |
|---|---|---|---|---|---|
| Multiple roles in one group | `spec.roles[]` | `internal/controller/workloads/rolebasedgroup_controller.go` (`RoleBasedGroupReconciler.Reconcile`) | none | [Multi roles](../../features/multiroles.md) | `internal/controller/workloads/rolebasedgroup_controller_test.go`, `test/envtest/testcase/rbg/basic_test.go` |
| Role patterns | `spec.roles[].standalonePattern`, `leaderWorkerPattern`, `customComponentsPattern` | `api/workloads/v1alpha2/rolebasedgroup_types.go`, `pkg/reconciler/roleinstanceset_reconciler.go` (`constructRoleInstanceSetApplyConfiguration`) | [KEP-173](../../../keps/173-enhanced-custom-components-pattern/README.md) | [Workload patterns](../../features/patterns.md) | `api/workloads/v1alpha2/helper_test.go`, `pkg/reconciler/roleinstanceset_reconciler_test.go`, `test/e2e/testcase/v1alpha2/standalone_role.go`, `test/e2e/testcase/v1alpha2/leader_worker_role.go` |
| Role templates | `spec.roleTemplates`, `templateRef.name`, `templateRef.patch` | `api/workloads/v1alpha2/helper.go` (`FindRoleTemplate`, `GetResolvedTemplate`), `api/workloads/v1alpha2/roletemplate_validation.go` | [KEP-8](../../../keps/8-reduce-yaml-duplication/README.md) | [Role templates](../../features/role-templates.md) | `api/workloads/v1alpha2/helper_test.go`, `test/e2e/testcase/v1alpha2/roletemplate.go` |
| Role dependencies | `spec.roles[].dependencies` | `pkg/dependency/dependency.go` (`DependencyManager`); admission in `api/workloads/v1alpha2/rolebasedgroup_validation.go` | none | [Role dependencies](../../features/role-dependencies.md) | `pkg/dependency/dependency_test.go`, `api/workloads/v1alpha2/rolebasedgroup_validation_test.go` |
| Revisions | a new ControllerRevision when the revision input changes: roles and role templates, without `replicas` and the selective scale-down annotations; `rbg.workloads.x-k8s.io/role-revision-<role>` workload label | `pkg/utils/revision_utils.go` (`NewRevision`, `GetRolesRevisionHash`) | [KEP-31](../../../keps/31-rbg-controller-revision/README.md) | [Revision](../../features/revision.md) | `pkg/utils/revision_utils_test.go`, `test/e2e/testcase/v1alpha2/revision.go` |
| Deprecated workload types and the 0.9.0 gate | role annotation `rbg.workloads.x-k8s.io/role-workload-type`; `--enable-deprecated-workload-types`; Helm `controller.deprecatedWorkloadTypes.enabled` | `api/workloads/v1alpha2/rolebasedgroup_validation.go` (`validateNoNewDeprecatedWorkloadTypes`), `pkg/reconciler/workload_reconciler.go` (`NewWorkloadReconciler`), `hack/gen-helm-rbac/main.go` | [Workload compatibility mode](../../../keps/workload-compatibility-mode/README.md) | [Helm chart README](../../../deploy/helm/rbgs/README.md) | `api/workloads/v1alpha2/rolebasedgroup_validation_test.go`, `pkg/reconciler/workload_reconciler_test.go`, `test/envtest/testcase/deprecation/`, `test/e2e/apicompat/`, `test/chart/deprecated_workload_types_test.go` |
| v1alpha1 conversion | v1alpha1 RoleBasedGroup and RoleBasedGroupSet | `api/workloads/v1alpha1/rolebasedgroup_conversion.go`, `api/workloads/v1alpha1/rolebasedgroupset_conversion.go` | [Workload compatibility mode](../../../keps/workload-compatibility-mode/README.md) | none | `api/workloads/v1alpha1/rolebasedgroup_conversion_test.go`, `api/workloads/v1alpha1/rolebasedgroupset_conversion_test.go`, `test/envtest/testcase/v1a1gate/` |
| Update-strategy defaulting | `spec.roles[].rolloutStrategy.rollingUpdate.type`; RoleInstanceSet `spec.updateStrategy.type` | `api/workloads/v1alpha2/rolebasedgroup_default.go`, `api/workloads/v1alpha2/roleinstanceset_default.go` | none | [Update strategy](../../features/update-strategy.md) | `api/workloads/v1alpha2/rolebasedgroup_default_test.go`, `test/envtest/testcase/webhook/default_test.go` |

## Rollout, scaling and sets

How it works: [RoleBasedGroup controller](rbg-controller.md), [RoleInstanceSet and RoleInstance](instance-workloads.md) and [other controllers](other-controllers.md).

| Feature | User-facing API | Main code | KEP | User doc | Tests |
|---|---|---|---|---|---|
| Rolling update of a role | `spec.roles[].rolloutStrategy.rollingUpdate` (`partition`, `maxUnavailable`, `maxSurge`, `paused`) | `pkg/reconciler/roleinstanceset_reconciler.go`, `pkg/reconciler/roleinstanceset/statefulmode/stateful_instance_set_utils.go`, `pkg/reconciler/roleinstanceset/statelessmode/sync/helper.go` | [KEP-30 InstanceSet](../../../keps/30-instanceset/README.md) | [Update strategy](../../features/update-strategy.md) | `pkg/reconciler/roleinstanceset_reconciler_test.go`, `test/e2e/testcase/v1alpha2/update_strategy.go`, `test/e2e/testcase/v1alpha2/ris.go` |
| In-place update | `rollingUpdate.type: InPlaceIfPossible`; `rollingUpdate.inPlaceUpdateStrategy.gracePeriodSeconds` | `pkg/inplace/pod/inplaceupdate`, `pkg/inplace/instance/inplaceupdate`, `pkg/reconciler/roleinstance/inplaceupdate` | [KEP-30 InstanceSet](../../../keps/30-instanceset/README.md) | [In-place update and scheduling policies](../../best-practice/en/04-configuring-inplace-update-and-scheduling-policies.md) | `pkg/inplace/pod/inplaceupdate/inplace_update_test.go`, `pkg/inplace/instance/inplaceupdate/inplace_update_test.go` |
| Stateful rollback replacement | RoleInstanceSet `status.rolloutReplacementState` | `pkg/reconciler/roleinstanceset/statefulmode/stateful_instance_set_control.go` | none | [Update strategy](../../features/update-strategy.md) | `pkg/reconciler/roleinstanceset/statefulmode/stateful_rollout_regression_test.go`, `test/envtest/testcase/stateful/rollout_recovery_test.go` |
| Stateful selective scale-down | role annotation `rbg.workloads.x-k8s.io/excluded-ordinals` | `pkg/reconciler/selective_membership.go`, `api/workloads/v1alpha2/excluded_ordinals.go`, `api/workloads/ordinals/ordinals.go` | [KEP-499](../../../keps/499-stateful-selective-scale-down/README.md) | [Selective scale-down](../../features/selective-scale-down.md) | `pkg/reconciler/selective_membership_test.go`, `api/workloads/v1alpha2/excluded_ordinals_test.go`, `test/envtest/testcase/rbg/selective_scale_down_test.go`, `test/envtest/testcase/selective_commit/` |
| Coordinated rolling update and scaling | a CoordinatedPolicy with the RBG's namespace and name; `spec.policies[].strategy` | `internal/controller/workloads/rolebasedgroup_controller.go` (`handleCoordinationStrategies`), `pkg/coordination/coordinationscaling/scaler.go` | [KEP-30 role coordination](../../../keps/30-role-coordination/README.md) | [Coordinated policy](../../features/coordinated-policy.md) | `pkg/coordination/coordinationscaling/scaler_test.go`, `test/e2e/testcase/v1alpha2/coordinated_policy.go` |
| v1alpha1 coordination migration | annotation `conversion.workloads.x-k8s.io/v1alpha1-coordination` | `internal/controller/workloads/coordinatedpolicy_migration_controller.go` (`EnsureV1alpha1CoordinatedPolicy`) | none | none | `internal/controller/workloads/coordinatedpolicy_migration_controller_test.go` |
| Scaling adapter auto-creation | `spec.roles[].scalingAdapter.enable` | `internal/controller/workloads/rolebasedgroup_controller.go` (`ReconcileScalingAdapter`), `pkg/scale` | [KEP-29](../../../keps/29-auto-create-scaling-adapter/README.md) | [Autoscaling](../../features/autoscaler.md) | `internal/controller/workloads/scalingadapter_uid_test.go`, `test/e2e/testcase/v1alpha2/scaling_adapter.go` |
| Scaling through `/scale` and HPA | RoleBasedGroupScalingAdapter | `internal/controller/workloads/rolebasedgroupscalingadapter_controller.go` | [KEP-29](../../../keps/29-auto-create-scaling-adapter/README.md) | [Autoscaling](../../features/autoscaler.md) | `internal/controller/workloads/rolebasedgroupscalingadapter_controller_test.go`, `test/e2e/testcase/v1alpha2/scaling_adapter.go`; admission checks in `test/e2e/testcase/v1alpha2/webhook_validation.go` |
| RoleBasedGroupSet | `spec.replicas`, `spec.groupTemplate` | `internal/controller/workloads/rolebasedgroupset_controller.go` (`updateExistingRBGs`) | none; rolling update proposed in [KEP-455](../../../keps/455-rbgs-rolling-update/README.md) | none | `internal/controller/workloads/rolebasedgroupset_controller_test.go`, `test/e2e/testcase/v1alpha2/rbgs.go` |

## Instances and failure handling

How it works: [RoleInstanceSet and RoleInstance](instance-workloads.md).

| Feature | User-facing API | Main code | KEP | User doc | Tests |
|---|---|---|---|---|---|
| RoleInstanceSet stateful mode (default) | role annotation `rbg.workloads.x-k8s.io/role-instance-pattern` unset or `Stateful`; `podManagementPolicy` | `pkg/reconciler/roleinstanceset/statefulmode` | [KEP-30 InstanceSet](../../../keps/30-instanceset/README.md) | none | `pkg/reconciler/roleinstanceset/statefulmode/stateful_instance_set_control_test.go`, `test/envtest/testcase/stateful/` |
| RoleInstanceSet stateless mode | role annotation `rbg.workloads.x-k8s.io/role-instance-pattern: Stateless` | `pkg/reconciler/roleinstanceset/statelessmode` | [KEP-30 InstanceSet](../../../keps/30-instanceset/README.md) | none | `pkg/reconciler/roleinstanceset/statelessmode/sync/update_test.go` |
| Restart policy and backoff | `restartPolicyConfig` (`type`, `activateAfter`, `baseDelaySeconds`, `maxDelaySeconds`) in `leaderWorkerPattern` and `customComponentsPattern` | `pkg/reconciler/roleinstance/sync/instance_scale.go` | [Inactive Pod handling](../../../keps/inactive-pod-handling/README.md) | [Failure handling](../../features/failure-handling.md) | `pkg/reconciler/roleinstance/sync/instance_scale_test.go`, `test/envtest/testcase/restart_policy/` |
| Restart trigger opt-out | Pod template annotation `rbg.workloads.x-k8s.io/restart-trigger-policy: Ignore` | `pkg/reconciler/roleinstance/sync/instance_scale.go` | [Inactive Pod handling](../../../keps/inactive-pod-handling/README.md) | [Failure handling](../../features/failure-handling.md) | `test/e2e/testcase/v1alpha2/inactive_pod.go` |
| RoleInstance readiness | RoleInstance `spec.readyPolicy` | `pkg/reconciler/roleinstance/instance_status.go`, `pkg/inplace/pod/readiness` | [KEP-30 InstanceSet](../../../keps/30-instanceset/README.md) | none | `pkg/reconciler/roleinstance/instance_status_readiness_test.go` |

## Scheduling and placement

How it works: [Scheduling and networking](scheduling-and-networking.md).

| Feature | User-facing API | Main code | KEP | User doc | Tests |
|---|---|---|---|---|---|
| Gang scheduling | RBG annotation `rbg.workloads.x-k8s.io/group-gang-scheduling`; CoordinatedPolicy `strategy.scheduling.gang`; `--scheduler-name`, `--scheduler-profile-name` | `pkg/scheduler/gang_scheduler.go` (`NewGangScheduler`), `pkg/scheduler/common/gang_strategy.go` (`GetGangStrategy`), `pkg/scheduler/volcano/scheduler.go`, `pkg/scheduler/k8s-scheduler-plugin/scheduler.go` | [KEP-430](../../../keps/430-gang-scheduling/README.md) | [Gang scheduling](../../features/gang-scheduling.md) | `pkg/scheduler/gang_scheduler_test.go`, `pkg/scheduler/volcano/scheduler_test.go`, `test/e2e/testcase/v1alpha2/gang_scheduling.go` |
| Per-role gang minimums | CoordinatedPolicy `strategy.scheduling.gang.minReplicas` | `pkg/scheduler/volcano/scheduler.go` (`buildGangSpec`), `api/workloads/v1alpha2/coordinatedpolicy_validation.go` | [KEP-430](../../../keps/430-gang-scheduling/README.md) | [Gang scheduling](../../features/gang-scheduling.md) | `pkg/scheduler/volcano/scheduler_test.go`, `api/workloads/v1alpha2/coordinatedpolicy_validation_test.go` |
| In-place scheduling | role annotations `rbg.workloads.x-k8s.io/role-inplace-scheduling`, `role-inplace-scheduling-granularity`, `role-inplace-scheduling-avoid` | `pkg/reconciler/roleinstance/sync/node_binding.go` (`NodeBindingStore`) | [KEP-351](../../../keps/351-inplace-scheduling/kep-inplace-scheduling.md) | [In-place update and scheduling policies](../../best-practice/en/04-configuring-inplace-update-and-scheduling-policies.md) | `pkg/reconciler/roleinstance/sync/node_binding_test.go`, `test/e2e/testcase/v1alpha2/inplace_scheduling.go` |
| Exclusive topology | RBG annotation `rbg.workloads.x-k8s.io/group-exclusive-topology`; Pod template annotation `rbg.workloads.x-k8s.io/role-disable-exclusive` | `pkg/reconciler/pod_reconciler.go` (`setExclusiveAffinities`) | none | [Exclusive topology](../../features/exclusive-topology.md) | `pkg/reconciler/pod_reconciler_test.go`, `test/e2e/testcase/v1alpha2/rbg.go` |

## Networking and discovery

How it works: [Scheduling and networking](scheduling-and-networking.md).

| Feature | User-facing API | Main code | KEP | User doc | Tests |
|---|---|---|---|---|---|
| Discovery config file | `/etc/rbg/config.yaml` in Pods, with ports from `spec.roles[].servicePorts`; the controller sets `rbg.workloads.x-k8s.io/discovery-config-mode` | `internal/controller/workloads/rolebasedgroup_controller.go` (`reconcileRefinedDiscoveryConfigMap`), `pkg/discovery/config_builder.go`, `pkg/discovery/injector.go` | [KEP-133](../../../keps/133-configmap-refine/README.md) | [Port allocation and service discovery](../../best-practice/en/05-port-allocation-and-service-discovery.md) | `pkg/discovery/config_builder_test.go`, `internal/controller/workloads/discovery_config_mode_test.go`, `test/e2e/testcase/v1alpha2/discovery_configmap.go` |
| Discovery environment variables | `RBG_*` variables injected into Pods | `pkg/discovery/env_builder.go`, `api/workloads/constants/env.go` | none | [Labels, annotations and environment variables](../../reference/variables.md) | `pkg/discovery/env_builder_test.go`, `pkg/discovery/injector_test.go` |
| Role headless Service and leader-only selection | `leaderWorkerPattern.sharedServiceSelection` | `pkg/reconciler/svc_reconciler.go`, `pkg/utils/service_utils.go` | [KEP-260](../../../keps/260-leaderonly-service/README.md) | [Port allocation and service discovery](../../best-practice/en/05-port-allocation-and-service-discovery.md) | `pkg/reconciler/svc_reconciler_test.go`, `test/envtest/testcase/rbg/shared_service_selection_test.go`, `test/e2e/testcase/v1alpha2/shared_service_selection.go` |
| Port allocator | `--enable-port-allocator`; annotation `rolebasedgroup.workloads.x-k8s.io/port-allocator` | `pkg/port-allocator/manager.go` | [KEP-171](../../../keps/171-pod-port-allocation/README.md) | [Port allocation and service discovery](../../best-practice/en/05-port-allocation-and-service-discovery.md) | `pkg/port-allocator/port_allocator_test.go`, `pkg/port-allocator/injection_test.go`, `test/e2e/testcase/v1alpha2/port_allocator.go` |
| Component discovery and start order | annotations `rolebasedgroup.workloads.x-k8s.io/component-discovery`, `rolebasedgroup.workloads.x-k8s.io/component-depends-on` | `pkg/component-discovery/component_discovery.go`, `pkg/component-discovery/component_lifecycle.go` | [KEP-173](../../../keps/173-enhanced-custom-components-pattern/README.md) | [Port allocation and service discovery](../../best-practice/en/05-port-allocation-and-service-discovery.md) | `pkg/component-discovery/component_discovery_test.go`, `pkg/component-discovery/component_lifecycle_test.go`, `test/e2e/testcase/v1alpha2/component_ordering.go` |

## Runtime, warmup and ecosystem

How it works: [other controllers](other-controllers.md).

| Feature | User-facing API | Main code | KEP | User doc | Tests |
|---|---|---|---|---|---|
| Engine runtime profiles | ClusterEngineRuntimeProfile; `spec.roles[].engineRuntimes` | `pkg/discovery/sidecar_builder.go` (`SidecarBuilder`) | changes proposed in [KEP-531](../../../keps/531-engine-runtime-profile-injection/README.md) | [Engine runtime profile](../../features/engine-runtime.md) | `pkg/discovery/sidecar_builder_test.go` |
| Warmup | RoleBasedGroupWarmup | `internal/controller/workloads/rolebasedgroupwarmup_controller.go` | [KEP-129](../../../keps/129-rbg-warmup/README.md) | none | `internal/controller/workloads/rolebasedgroupwarmup_controller_test.go`, `test/e2e/testcase/v1alpha2/warmup.go` |
| Ecosystem integrations (Mooncake, Dynamo, SGLang EPD) | role layouts in YAML; no controller code | `examples/inference/ecosystem` | [KEP-74](../../../keps/74-mooncake-integration/README.md), [KEP-75](../../../keps/75-EPD/README.md) | [Ecosystem integration](../../features/ecosystem-integration.md), [Mooncake store](../../best-practice/en/10-deploy-mooncake-store-with-rbg.md) | none |
| Inference engine monitoring | a PodMonitor the user applies, e.g. `examples/deprecated/v1alpha1/monitoring/podmonitor.yaml` | none in the controller | none | [Monitoring](../../features/monitoring.md) | none |

## Operations and tooling

How it works: [Controller manager](controller-manager.md); the CLI is in the [overview](overview.md).

| Feature | User-facing API | Main code | KEP | User doc | Tests |
|---|---|---|---|---|---|
| Webhooks and certificates | `--enable-webhooks` (`all` or `none`) | `cmd/rbgs/main.go` (`bootstrapWebhookCerts`), `pkg/webhook/certmanager.go`, `internal/controller/workloads/webhook_cert_controller.go` | none | [Helm chart README](../../../deploy/helm/rbgs/README.md) | `pkg/webhook/certmanager_test.go`, `pkg/webhook/cert/writer/certwriter_test.go`, `test/e2e/testcase/v1alpha2/rbac_webhook_bootstrap.go` |
| Local debugging | `--enable-webhooks=none`; `make run-local` | `cmd/rbgs/main.go` (`newManagerOptions`) | none | [Developer guide](../how_to_develop.md) | none |
| Controller tuning | `--max-concurrent-reconciles`, `--kube-api-qps`, `--kube-api-burst`, `--cache-sync-timeout` | `cmd/rbgs/main.go` | none | [Stress testing and tuning](../../best-practice/en/09-stress-testing-and-tuning.md) | `test/stress` |
| Profiling | `--enable-pprof`, `--pprof-bind-address` | `cmd/rbgs/main.go` (`startPprofServer`) | none | [Developer guide](../how_to_develop.md) | none |
| Health probes and controller metrics | `--health-probe-bind-address`, `--metrics-bind-address`, `--metrics-secure` | `cmd/rbgs/main.go` | none | none | `test/chart/manager_probes_test.go` |
| Cache label filter | label `rbg.workloads.x-k8s.io/group-name` on Services, StatefulSets and Deployments | `cmd/rbgs/main.go` (`cacheOptions`) | Pod filtering proposed in [KEP-284](../../../keps/284-pod-cache-filter/README.md) | none | none |
| kubectl-rbg CLI | `kubectl rbg status`, `kubectl rbg rollout history`, `diff` and `undo` | `cmd/cli/cmd` | [KEP-31](../../../keps/31-rbg-controller-revision/README.md) | [README](../../../README.md) | `cmd/cli/cmd/status/status_test.go`, `cmd/cli/cmd/rollout/rollout_undo_test.go` |

## KEP status on main

Most KEPs keep `status: provisional` in their `kep.yaml` after they ship, so the status below comes from the code.

| KEP | Status on `main` | What exists, and where it differs from the KEP |
|---|---|---|
| [KEP-8 role templates](../../../keps/8-reduce-yaml-duplication/README.md) | Implemented | `spec.roleTemplates`, `templateRef` with `patch` |
| [KEP-29 auto-create ScalingAdapter](../../../keps/29-auto-create-scaling-adapter/README.md) | Implemented | `spec.roles[].scalingAdapter` |
| [KEP-30 InstanceSet](../../../keps/30-instanceset/README.md) | Implemented as RoleInstanceSet and RoleInstance | `RecreateRoleInstanceOnPodRestart` replaces the Pods and keeps the RoleInstance, where the KEP renames it; the KEP's `RestartRoleInstanceOnPodRestart` value does not exist |
| [KEP-30 role coordination](../../../keps/30-role-coordination/README.md) | Implemented as CoordinatedPolicy | The policy must share the RBG's namespace and name |
| [KEP-31 controller revision](../../../keps/31-rbg-controller-revision/README.md) | Implemented | `pkg/utils/revision_utils.go`; `kubectl rbg rollout` |
| [KEP-74 Mooncake](../../../keps/74-mooncake-integration/README.md), [KEP-75 SGLang EPD](../../../keps/75-EPD/README.md) | Deployment recipes | No controller code; examples under `examples/inference/ecosystem` |
| [KEP-129 warmup](../../../keps/129-rbg-warmup/README.md) | Implemented | RoleBasedGroupWarmup |
| [KEP-133 ConfigMap refine](../../../keps/133-configmap-refine/README.md) | Partly implemented | The single RBG-level ConfigMap (`refine` mode); the compact format is not implemented |
| [KEP-171 port allocation](../../../keps/171-pod-port-allocation/README.md) | Implemented | `pkg/port-allocator` |
| [KEP-173 custom components](../../../keps/173-enhanced-custom-components-pattern/README.md) | Implemented | `customComponentsPattern`, `pkg/component-discovery` |
| [KEP-260 leader-only Service](../../../keps/260-leaderonly-service/README.md) | Implemented | `sharedServiceSelection`; the env var is `RBG_LWP_LEADER_ADDRESS`, not `RBG_LEADER_ADDRESS` |
| [KEP-275 per-replica headless Service policy](../../../keps/275-enhance-network/README.md) | Not implemented | No `SubdomainPolicy` in the API |
| [KEP-284 Pod cache filtering](../../../keps/284-pod-cache-filter/README.md) | Not implemented | Pods are not filtered; see `cacheOptions` |
| [KEP-351 in-place scheduling](../../../keps/351-inplace-scheduling/kep-inplace-scheduling.md) | Implemented, with differences | Bindings are dropped when the RBG is deleted, and the `-avoid` annotation is not in the KEP |
| [KEP-430 gang scheduling](../../../keps/430-gang-scheduling/README.md) | Implemented | Volcano and scheduler-plugins PodGroups; per-role minimums with Volcano |
| [KEP-455 RoleBasedGroupSet rolling update](../../../keps/455-rbgs-rolling-update/README.md) | Not implemented | Template changes reach every child at once (`updateExistingRBGs`) |
| [KEP-499 selective scale-down](../../../keps/499-stateful-selective-scale-down/README.md) | Implemented (#538) | `rbg.workloads.x-k8s.io/excluded-ordinals` |
| [KEP-502 workload status model](../../../keps/502-workload-status-model/README.md) | Not implemented | No `Progressing` condition is set and RoleInstance has no `status.phase` |
| [KEP-531 engine runtime profile injection](../../../keps/531-engine-runtime-profile-injection/README.md) | Not implemented | `SidecarBuilder` reads profiles through the cache at render time and profile changes don't trigger RBG reconciles; `updateStrategy` and `injectContainers` are not read |
| [Inactive Pod handling](../../../keps/inactive-pod-handling/README.md) | Partly implemented | Failed Pod replacement, `restartPolicyConfig.activateAfter` and the `restart-trigger-policy` annotation exist; no code checks the `DisruptionTarget` condition or `UnexpectedAdmissionError` |
| [Workload compatibility mode](../../../keps/workload-compatibility-mode/README.md) | Implemented | The `role-workload-type` annotation and the deprecated workload type gate |

## Behaviors worth knowing

- Most keys use the `rbg.workloads.x-k8s.io/` prefix, but the port allocator, component discovery and component start-order annotations use `rolebasedgroup.workloads.x-k8s.io/` (`pkg/port-allocator/manager.go`, `PortAllocatorAnnotationKey`; `pkg/component-discovery/component_lifecycle.go`, `ComponentDependsOnAnnotationKey`).
- Exclusive-topology affinity selects on the `rbg.workloads.x-k8s.io/group-unique-hash` label, which no code on `main` sets on Pods; see [Scheduling and networking](scheduling-and-networking.md).
- User docs under `doc/features` describe the user's view and can lag the code. When they disagree, the code and the architecture pages win.

## Related

- [Architecture index](README.md) and [overview](overview.md).
- The documentation [table of contents](../../TOC.md) for all user docs.
- The [KEP template](../../../keps/NNNN-kep-template/README.md) for new designs.
- Rules: the [rbg-conventions skill](../../../.agents/skills/rbg-conventions/SKILL.md) and its [build-test-release.md](../../../.agents/skills/rbg-conventions/references/build-test-release.md) for the tests a change needs.
