# KEP-541: Runtime Prefill/Decode Role Switching for PD-Disaggregated Inference

<!-- toc -->
- [Release Signoff Checklist](#release-signoff-checklist)
- [Summary](#summary)
- [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
- [Proposal](#proposal)
    - [User Stories](#user-stories)
    - [Notes/Constraints/Caveats](#notesconstraintscaveats)
    - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
    - [The Contract](#the-contract)
    - [Declaring the Runtime Key](#declaring-the-runtime-key)
    - [Why an Externally Written Value Survives](#why-an-externally-written-value-survives)
    - [Switch Flow](#switch-flow)
    - [Decision: The Switcher Lives in Patio](#decision-the-switcher-lives-in-patio)
    - [Test Plan](#test-plan)
    - [Graduation Criteria](#graduation-criteria)
    - [Upgrade / Downgrade Strategy](#upgrade--downgrade-strategy)
    - [Version Skew Strategy](#version-skew-strategy)
- [Production Readiness Review Questionnaire](#production-readiness-review-questionnaire)
    - [Feature Enablement and Rollback](#feature-enablement-and-rollback)
    - [Rollout, Upgrade and Rollback Planning](#rollout-upgrade-and-rollback-planning)
    - [Monitoring Requirements](#monitoring-requirements)
    - [Dependencies](#dependencies)
    - [Scalability](#scalability)
    - [Troubleshooting](#troubleshooting)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Infrastructure Needed](#infrastructure-needed)
<!-- /toc -->

## Release Signoff Checklist

Items marked with (R) are required *prior to targeting to a milestone / release*.

- [ ] (R) Enhancement issue in release milestone, which links to the KEP dir (not the initial KEP PR)
- [ ] (R) KEP approvers have approved the KEP status as `implementable`
- [ ] (R) Design details are appropriately documented
- [ ] (R) Test plan is in place
- [ ] (R) Graduation criteria is in place
- [ ] (R) Production readiness review completed
- [ ] (R) Production readiness review approved
- [ ] "Implementation History" section is up-to-date for milestone
- [ ] User-facing documentation has been created
- [ ] Supporting documentation—e.g., additional design documents, links to discussions, relevant PRs/issues, release notes

## Summary

SGLang can now flip a running engine instance between `prefill` and `decode` without restarting the
server or reloading weights ([sgl-project/sglang#28403][sglang-pr], merged 2026-09-17). RBG's
recommended PD-disaggregated topology pins each instance's role at startup via a static `prefill`
role and a static `decode` role, so it cannot express "this instance is serving prefill now but may
serve decode later".

This KEP proposes **no new RBG API field**. Instead it makes an existing, implicit behavior explicit
and enforceable: a Pod carries two distinct identities.

- `rbg.workloads.x-k8s.io/role-name` is the **topology identity**. It decides the workload, Service,
  and scheduling constraints a Pod belongs to, and is immutable after creation.
- A user-defined runtime label, for example `inference.workload.x-k8s.io/pd-role`, is the **serving
  identity**. It records the role the engine inside the Pod is currently serving, and is mutable at
  runtime by the switcher — the Patio sidecar.

RBG does not interpret, validate, or manage the serving identity. It carries the key through as an
ordinary label and must never revert a value written from outside. That already holds today, but
nothing tests it and nothing documents it, so a future drift-correction change would break it
silently.

The deliverables are a regression suite pinning the contract, user-facing documentation, and a
runnable example. The switcher itself belongs in the [Patio][patio] sidecar, as suggested by @diw-zw
in [#541][issue-541]; this KEP adopts that direction and records the split between what RBG delivers
here and what Patio delivers under its own proposal process. Patio was extracted from this repository
in [PR #366][patio-extract] and is a listed ecosystem project, so this is a division of labour between
two repositories maintained by overlapping contributors, not a new external dependency.

## Motivation

The prefill-to-decode ratio that suits a workload is not stable. Long prompts with short answers
favor prefill capacity; short prompts with long generations favor decode capacity. Today,
rebalancing means tearing down engine instances and relaunching them with a different
`--disaggregation-mode`, which reloads weights, reallocates the KV pool, and pays a full cold start.

SGLang removed that cost. The token KV pool is role-independent — prefill and decode allocate the
same KV memory structure — so a flip only tears down and rebuilds the small role-specific
disaggregation structures and re-dispatches the scheduler event loop. Upstream reports flat weight,
KV-cache, CUDA-graph, GPU-free and thread-count metrics across 100 role switches.

RBG is already compatible with this, for two structural reasons:

1. Pods are created from templates once ([`instance_scale.go`][instance-scale]); steady-state
   reconciliation does not re-apply template metadata to a live Pod.
2. In-place update metadata patches are computed from the **old/new ControllerRevision diff**, not
   from "current Pod versus template" ([`inplace_update_defaults.go`][pod-inplace-defaults]).

Neither reason is currently protected by a test. Both are accidental properties of the
implementation rather than a stated contract, and both are exactly what a well-intentioned
"correct drift on live Pods" change would undo. The issue asks for that guarantee to be pinned and
documented, which is what this KEP scopes.

### Goals

1. Pin the label-ownership contract with regression tests covering every path that writes to a live
   Pod's metadata: steady-state reconciliation, a completed in-place update, the grace-period-expiry
   second write, and a revision-only update.
2. Document the contract, the switch flow, and its boundary conditions, so a switcher author knows
   precisely what RBG guarantees and what it does not.
3. Provide a runnable example that wires routing to the runtime label rather than to the topology
   role.
4. Adopt the Patio sidecar as the home of the switcher, and record the cross-repository split: what
   RBG delivers here, and what Patio delivers under its own proposal process.
5. Correct one factual error in the issue text and refine one code citation, both verified against
   the repository and a live API server.

### Non-Goals

1. **No new RBG API field, no CRD change, no feature gate.** The issue states this explicitly.
2. RBG does not interpret, validate, or manage the runtime label's key or value. The key is
   user-defined.
3. No automatic load-based flip policy. Upstream excludes this too; the flip is driven externally.
4. No request draining or KV-cache transfer orchestration inside RBG.
5. No change to topology identity semantics. `role-name` stays immutable.
6. **The switcher is not implemented in this repository.** It belongs in Patio, which has its own
   proposal process. This KEP designs it and pins the interface it relies on, but the RBG-side
   deliverable is the contract, its regression tests, the example, and verification of the existing
   injection channel — none of which change controller behavior.

## Proposal

Adopt the four-role topology from the issue, and state the ownership contract normatively.

| Role | Semantics |
| --- | --- |
| `prefill` | Fixed prefill workload, never switches |
| `decode` | Fixed decode workload, never switches |
| `router` | Router / traffic entry |
| `pdmix-pool` | Switchable engine pool; each Pod can flip between prefill and decode at runtime |

The contract has three properties, all of which RBG must preserve:

1. A runtime key declared in the Pod template is carried onto the Pod at creation, with the
   template's value as its **initial** value only.
2. A value written to that key from outside RBG is not treated as drift and is not reverted, neither
   by steady-state reconciliation nor by an in-place update.
3. A recreated Pod gets the template's initial value again. The recommended convention is
   `unassigned`, so new and recreated Pods are safe by default **where routing selects on the label**:
   the router only routes to Pods carrying a valid role, and the switcher assigns a role after warmup.
   Where routing is driven by the router's worker registry instead, withholding registration during
   warmup is the switcher's job — see
   [`unassigned` is a routing state](#unassigned-is-a-routing-state-not-an-engine-mode).

The switcher — the Patio sidecar, not RBG — owns the five-step switch flow described in
[Switch Flow](#switch-flow).

### User Stories

#### Story 1

As an inference platform operator, I want to rebalance the P:D ratio of a running deployment
without cold-starting engines, so that a shift in prompt/generation length mix does not cost me
weight reloads and a capacity dip.

#### Story 2

As the author of the switcher in the Patio sidecar, I want a documented guarantee that RBG will not
revert the runtime label I patch onto a live Pod, so I can treat the label as durable state I own
rather than re-asserting it on a loop to fight the controller.

#### Story 3

As an RBG maintainer, I want a test that fails if someone introduces live-Pod drift correction, so
that a reasonable-looking change cannot silently break every runtime-switching deployment.

### Notes/Constraints/Caveats

#### Correction: `spec.roles[].labels` does not reach Pod labels

The issue's example declares the runtime key under `spec.roles[].labels`. **That does not work for
the stated purpose.** Verified against a live envtest API server: the key propagates to
`RoleInstanceSet.Labels` and `RoleInstance.Labels`, and stops there. It never reaches the Pod
template's labels, and therefore never reaches `Pod.Labels`.

A router or Service that selects Pods by the runtime label would match nothing. The key must be
declared in the Pod template metadata instead — see
[Declaring the Runtime Key](#declaring-the-runtime-key).

#### Refinement: which in-place path computes the Pod metadata patch

The issue cites `pkg/inplace/instance/inplaceupdate/inplace_update.go` for the revision-diff
property. That citation is **valid**: the instance-level path does compute its `UpdateSpec` from the
old and new ControllerRevision ([`defaultCalculateInPlaceUpdateSpec`][instance-inplace-defaults]),
and that is where the in-place-versus-recreate decision is made.

The refinement is that the instance layer carries `OldTemplate`/`NewTemplate` and produces no Pod
metadata patch bytes. The bytes that actually touch `Pod.ObjectMeta` are computed one layer down, in
[`pkg/inplace/pod/inplaceupdate/inplace_update_defaults.go`][pod-inplace-defaults], via
`strategicpatch.CreateTwoWayMergePatch(oldTempMeta, newTempMeta)`. Both layers are revision-diff
based, so the issue's conclusion holds; the Pod-level path is simply the one a regression test must
exercise.

#### The guarantee has a boundary

The metadata patch is an **old-revision versus new-revision** diff. The guarantee therefore holds
only while the key's template value is unchanged. If a user edits the runtime key's value in the RBG
spec at runtime, the diff legitimately contains that key and the patch will overwrite the live
value. This is why the contract says *do not modify the runtime key in the spec at runtime* — it is
a user obligation, not something RBG can enforce without interpreting the key.

#### An image-only diff can make a test pass for the wrong reason

When the revision diff contains only container image replacements, `MetaDataPatch` is `nil` and no
metadata patch is applied to the live Pod at all. A regression test that rolls only the image would
see the external label survive trivially, without exercising the metadata path. The test must roll a
template-owned metadata key alongside the image, and assert that the owned key actually landed.

#### `unassigned` is a routing state, not an engine mode

SGLang has no `unassigned` disaggregation mode. A pool Pod starts with a real
`--disaggregation-mode` (for example `decode`) plus `--enable-pd-role-switch`, while its runtime
label reads `unassigned` so the router keeps traffic away until the switcher assigns a role. The
label and the engine mode are deliberately decoupled during warmup.

**This safety default does not hold by itself under the Patio design.** Patio's startup sequence is
`wait_engine_ready` then `register`, and `register` publishes the static `worker_type` from
`--instance-info`. A warming Pod would therefore be registered as a real `decode` worker while its
label still reads `unassigned`. Where routing is driven by the router's registry rather than by a
label-selecting Service, the label alone cannot keep traffic away.

The Patio-side design must therefore **withhold registration while the serving label is
`unassigned`**, and register only once the switcher assigns a role. This is a requirement on Patio,
not something RBG can provide, and it is the reason contract property 3 is scoped to label-selecting
routing below.

#### The injection channel already exists, and it is on by default

Patio is not a new dependency. It was extracted out of this repository — `python/patio/` became
[rolebasedgroup/inference-engine-runtime][patio] in [PR #366][patio-extract] — and it is listed as an
ecosystem project in RBG's own README. Its contributors are RBG contributors.

The injection path is already wired: `spec.roles[].engineRuntimes[].profileName` references a
`ClusterEngineRuntimeProfile`, and [`SidecarBuilder.injectRuntime`][sidecar-builder] appends the
profile's initContainers, containers and volumes into the Pod template, then appends per-role `Env`
and `Args` overrides. `sidecarInjector` is in the **default** injector list
([`pod_reconciler.go`][pod-reconciler]), so this runs for every role unless explicitly disabled.

Two consequences for this KEP:

- The switcher needs **no new RBG API**. The profile, the `engineRuntimes` reference, and
  `serviceAccountName` (a native Pod field) are sufficient.
- `injectRuntime` injects containers and volumes only. It does **not** inject labels, so the runtime
  key still has to be declared in the Pod template by the user.

The shipped Patio profile lives at
[`examples/deprecated/v1alpha1/pd-disagg/sglang/patio-profile.yaml`][patio-profile] and is v1alpha1.
There is no v1alpha2 Patio example or doc page today. Re-establishing that example on v1alpha2 is part
of the RBG-side slice.

#### A profile update can reset every serving role in the pool

`ClusterEngineRuntimeProfile.spec.updateStrategy` accepts `NoUpdate` (the default) and
`RollingUpdate`. With `RollingUpdate`, a profile change triggers a rolling update of every Pod using
that profile, honouring each role's `rolloutStrategy`. Any Pod that is **recreated** rather than
updated in place returns to the template's initial label value — `unassigned` — and drops out of
rotation.

The old Patio profile pins `updateStrategy: NoUpdate`, which avoids this. The current general example
([`engine-runtime-profile.yaml`][engine-runtime-example]) and the engine-runtime doc both use
`RollingUpdate`. A Patio profile rebuilt on v1alpha2 by copying that example would therefore wipe the
serving roles of an entire pool on the next profile bump. The Patio-side design must pin `NoUpdate`,
or state how serving roles survive a profile-driven rollout.

#### The router holds a second copy of the serving role

Pod labels are not the only place the serving role is recorded. Patio registers each worker with the
SGLang router by `POST /workers`, carrying `worker_type` (`prefill` / `decode`) and `bootstrap_port`
([`sgl_topo_client.py`][patio-topo-client]).

Critically, [`app.py`][patio-app] parses `--instance-info` once into a `worker_info` dict, then starts
a daemon thread that re-`register`s **that same dict** every `HEARTBEAT_INTERVAL` seconds (default
10). This exists so workers recover their registration after a router restart.

This cuts both ways, and it is the most important integration fact for the switcher:

- If the switcher flips the engine and patches the Pod label but leaves `worker_info` untouched, the
  heartbeat keeps re-asserting the **old** `worker_type` every 10 seconds.
- Because `worker_info` is a live dict held for the process lifetime, mutating
  `worker_info["worker_type"]` in place is enough for the next heartbeat to publish the new role. No
  new router protocol is needed for that path.

The switch flow must therefore treat the label and the registered `worker_type` as one logical
update, and the sidecar is the only place positioned to perform both. This is the strongest argument
for placing the switcher in Patio rather than outside the Pod.

<<[UNRESOLVED @diw-zw]>>
`register` issues a fresh `POST /workers` on every heartbeat and overwrites the stored `worker_id`
from the response. Whether the router treats a repeated POST for the same worker URL as an update of
the existing entry's `worker_type`, or as a new worker, is router-side behaviour not verified here. If
it is not an update, a flip requires `unregister` (`DELETE /workers/{id}`) followed by re-registration,
which opens a window with no registration at all. This must be confirmed before the switch flow is
fixed.
<<[/UNRESOLVED]>>

Note also that `register` retries internally up to 60 times at 3-second intervals, and the heartbeat
loop calls it synchronously. An unreachable router can stall the heartbeat loop for up to three
minutes, which bounds how quickly a flip can be published.

#### Scale-down does not know which Pods are serving

RBG chooses scale-down victims by component ID alone: [`newComponentPodGroup`][instance-core] marks
every Pod whose ID is `>= desiredReplicas` for deletion. It reads no serving-role label.

For a switchable pool this is a real hazard. Shrinking `pdmix-pool` from 8 to 6 deletes IDs 6 and 7
regardless of what they currently serve. If the switcher had placed the last two prefills there, the
pool loses all prefill capacity in one reconcile and the router is left with no prefill endpoint.

RBG will not become role-aware to fix this — that would violate Non-Goal 2. The obligation falls on
the switcher: before a scale-in it must ensure the surviving low-ID Pods can carry the required role
mix, or the operator must drain and reassign first. Note that [rbg-planner][rbg-planner], the
ecosystem autoscaler, scales prefill and decode roles by SLA and is a likely driver of such a
scale-in; the interaction needs to be stated, not assumed safe.

#### Violating the system-label boundary deletes the Pod

The contract says RBG-owned keys are not covered. The consequence of writing one is stronger than
drift, and switcher authors need to know it.

[`GetPodComponentID`][instance-utils] reads `rbg.workloads.x-k8s.io/component-id` and parses it as an
integer, falling back to the last `-`-separated segment of the Pod name when the label is absent. A
value that fails to parse yields `-1`, and [`calculateDiffsWithExpectation`][instance-scale] puts
every Pod with ID `-1` straight into the deletion list.

A switcher that writes a non-numeric value into that key gets its Pod **deleted**, not corrected. A
switcher that replaces the whole label map is usually saved by the name fallback, but must not rely on
it. Patch a single key; never replace `metadata.labels` wholesale.

#### Upstream backend and parallelism constraints

Per the upstream PR's own limitations section:

- Only the **mori** transfer backend's teardown/rebuild hooks are validated. Other backends must opt
  in via `BaseKVManager.supports_role_switch` plus `teardown()`.
- Validation used pure-TP MoE (`--tp-size 8`, no EP / DP-attention / MoE a2a). With
  `--moe-a2a-backend mori`, the a2a dispatch buffer is sized per-role at startup and is **not**
  rebuilt on flip.
- The reported validation runs used AMD MI355X / MI300X GPUs.

<<[UNRESOLVED @diw-zw]>>
These constraints bound what a runnable example can honestly claim. The example drafted alongside
this KEP uses a CUDA image and `nvidia.com/gpu`, which does not match the backend upstream validated
on. Whether the mooncake/CUDA path supports a flip today needs confirmation before the example is
presented as working end to end. Until then the example should be labelled as demonstrating the
**label and routing contract**, not a validated GPU flip.
<<[/UNRESOLVED]>>

### Risks and Mitigations

| Risk | Mitigation |
| --- | --- |
| A future change adds live-Pod drift correction and silently reverts runtime labels | The envtest regression suite fails in CI. The suite is mutation-verified: breaking the metadata patch base makes it fail. |
| A user picks a runtime key inside an RBG-managed prefix | Document that the key must live outside `rbg.workloads.x-k8s.io/`. RBG-owned keys are explicitly not covered by the contract. |
| The switcher patches the label but the engine flip fails | Upstream fails fast to an `unhealthy` state on rebuild failure. The switcher must not advance the label on a failed flip; the router then keeps the Pod out of rotation. |
| Requests are not drained before teardown | Upstream states draining to idle is a **correctness** requirement, because an in-flight decode request's KV is owned by an upstream prefill. The switch flow makes `unassigned` plus drain a precondition. RBG cannot enforce this. |
| The switcher needs RBAC to patch Pods | The Patio sidecar uses the Pod's own service account, scoped to `patch` on `pods` in its namespace. This is a new in-Pod permission surface and must be reviewed in Patio's proposal. It is namespace-scoped, **not** self-scoped: RBAC cannot express "only this Pod" without enumerating Pod names. |
| Pod recreation resets the serving role | The `unassigned` convention plus label-selecting routing makes this safe by default rather than a misroute. |
| A profile bump recreates pool Pods and resets every serving role | Pin `updateStrategy: NoUpdate` on the Patio profile, or state how serving roles survive a profile-driven rollout. Copying the current `RollingUpdate` example is unsafe here. |
| The heartbeat re-asserts the old `worker_type` after a flip | The switcher mutates the live `worker_info` dict as part of the flip, so the next heartbeat publishes the new role. Label and registration are one logical update, not two. |
| Scale-in deletes the Pods currently serving a role | RBG selects victims by component ID and is not role-aware. The switcher must rebalance roles onto surviving low-ID Pods before a scale-in. |
| A switcher overwrites an RBG-owned label | The documented consequence is Pod **deletion**, not drift correction. Patch a single key; never replace `metadata.labels`. |
| A warming Pod is registered as a real worker while labelled `unassigned` | Patio must gate initial registration on the serving label. Without this, registry-driven routing ignores the `unassigned` convention entirely. |
| Label-based routing is eventually consistent | There is a brief window between a label patch and router endpoint convergence. The switch flow orders drain before flip and flip before re-label, so the window does not carry traffic for the wrong role. |

## Design Details

### The Contract

Normative statement, intended for the user-facing documentation:

> A runtime serving-role label on a **live Pod** is owned by the switcher — in this design, the Patio
> sidecar. RBG carries the key through as an ordinary label. The value in the Pod template is the
> **initial** value only. Do not modify that value in the RBG spec at runtime. RBG will not revert a
> value written from outside, across reconciliation and across in-place updates. A recreated Pod
> returns to the initial value. The guarantee covers keys outside `rbg.workloads.x-k8s.io/`; writing
> an RBG-owned key is not drift but a violation whose consequence can be Pod deletion.

What RBG does:

- copies the key from the Pod template onto the Pod at creation
- leaves the key alone during steady-state reconciliation
- computes in-place metadata patches from the revision diff, so an unchanged template key is absent
  from the patch

What RBG does not do:

- interpret or validate the key or its value
- detect that a flip happened
- participate in draining, KV transfer, or engine health assessment
- restore the template value after an external patch

The guarantee is about **live Pods**, and it was verified on the RoleInstance path. It does not extend
to workload templates: for the Deployment and StatefulSet workload types RBG does compare old versus
new desired objects and corrects template drift ([`deploy_reconciler.go`][deploy-reconciler],
[`sts_reconciler.go`][sts-reconciler]). A switcher patches the Pod, never the template.

### Declaring the Runtime Key

The key belongs in the Pod template metadata, not in `spec.roles[].labels`:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: sglang-pd
spec:
  roles:
    - name: pdmix-pool
      replicas: 8
      standalonePattern:
        template:
          metadata:
            labels:
              # Workload identity, referenced by the Services below.
              app: sglang-pd
              # Serving identity. Owned by the switcher after creation.
              inference.workload.x-k8s.io/pd-role: unassigned
          spec:
            containers:
              - name: sglang
                image: lmsysorg/sglang:v0.5.9
                command:
                  - python3
                  - -m
                  - sglang.launch_server
                  - --disaggregation-mode
                  - "decode"
                  - --enable-pd-role-switch
                  # ...
```

Routing then selects on the serving identity, which is what makes a flip observable to the router
without any RBG involvement:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: pd-prefill
spec:
  selector:
    app: sglang-pd
    inference.workload.x-k8s.io/pd-role: prefill
  ports:
    - port: 8000
      targetPort: 8000
```

<<[UNRESOLVED @diw-zw]>>
This Service selects a **VIP** across all Pods currently serving prefill. The repository's own PD
examples ([`pd-disagg-standalone.yaml`][pd-standalone], [`pd-disagg-leader-worker.yaml`][pd-lw]) instead
address individual Pods by per-Pod DNS, because PD pairing is per-instance. Whether a load-balanced
VIP is correct for the prefill side of a bootstrap pairing has not been demonstrated here. Until it
is, this snippet illustrates **label-based selection**, and a production topology may need per-Pod
addressing plus a router that resolves the serving role per instance.
<<[/UNRESOLVED]>>

### Why an Externally Written Value Survives

Three code properties, all cited so a reviewer can check them:

1. **Creation is one-shot.** Pods are built from the role's Pod template when a RoleInstance is
   scaled up ([`pkg/reconciler/roleinstance/sync/instance_scale.go`][instance-scale]). Steady-state
   reconciliation compares counts, revisions, and readiness; it does not re-apply template metadata
   to a running Pod.
2. **The in-place metadata patch is a revision diff.**
   [`pkg/inplace/pod/inplaceupdate/inplace_update_defaults.go`][pod-inplace-defaults] computes
   `jsonpatch.CreatePatch(oldRevision.Data.Raw, newRevision.Data.Raw)` to decide whether an in-place
   update is possible, and separately builds the metadata patch with
   `strategicpatch.CreateTwoWayMergePatch(oldTempMeta, newTempMeta)` from the two revisions' template
   metadata. That patch is then applied with `strategicpatch.StrategicMergePatch` to a freshly read
   live Pod.

   Because both inputs to the metadata patch are revisions, a key whose template value did not change
   is absent from the patch, and `StrategicMergePatch` leaves the live value untouched. Nothing on
   this path compares the live Pod against the template.

3. **The writes are full `Update`s, and the safety comes from concurrency control.** It is tempting to
   explain the guarantee as "RBG only patches, so it cannot clobber". That is wrong. The Pod-level
   path reads a fresh copy and calls `Update` on the whole object
   ([`clientadapter/adapter.go`][pod-adapter]). Three write sites matter:

   - the `GraceSeconds <= 0` branch in
     [`pkg/inplace/pod/inplaceupdate/inplace_update.go`][pod-inplace], which writes the
     `InPlaceUpdateStateKey` annotation and the update spec;
   - the **second** write in the same file, after the grace period expires and
     `RemoveInPlaceUpdateGrace` runs;
   - `onlyUpdateRevision` in
     [`pkg/reconciler/roleinstance/inplaceupdate/inplaceupdate.go`][ri-inplaceupdate], which rewrites
     the revision-hash label.

   Each runs inside `retry.RetryOnConflict` after a fresh `GetPod`. An externally written label
   survives because it is present in the object that was just read, and a concurrent switcher write is
   resolved by `resourceVersion` optimistic concurrency rather than silently lost. The property that
   protects the contract is the absence of a live-Pod-versus-template comparison — not the absence of
   `Patch`. A future change to read-modify-write against the *template* would break it while still
   using `Update`.

   For completeness: the metadata-comparison helpers in [`pod_reconciler.go`][pod-reconciler]
   (`objectMetaEqual`, `podTemplateSpecEqual`) are **not** a fourth write path. Their callers are the
   Deployment, StatefulSet and Service reconcilers, and they compare two *desired* objects, never a
   live Pod against a template. `ConstructPodTemplateSpecApplyConfiguration` likewise builds a
   template apply-configuration; it does not server-side-apply a live Pod.

The boundary in [Notes](#the-guarantee-has-a-boundary) follows directly: change the template value
and the key enters the diff.

### Switch Flow

Owned by the switcher. RBG is not a participant.

1. Set the Pod's runtime label to `unassigned` (or remove it) so the router stops sending new
   traffic.
2. Drain in-flight requests and let KV transfer complete. Upstream treats draining to idle as a
   correctness requirement, not an optimization.
3. Call the engine's switch API:

   ```bash
   curl http://<instance>:8000/pd_role_switch \
     -H 'content-type: application/json' \
     -d '{"new_role":"decode"}'
   ```

   Gated by `--enable-pd-role-switch`, default off. Optionally pass `decode_cuda_graph_bs` for a
   size-matched decode CUDA graph capture; the captured sizes are queryable via `/get_server_info`.
4. Wait until the engine reports the new role ready. A rebuild failure puts the engine into a
   fail-fast `unhealthy` state; the switcher must treat that as a failed flip.
5. Publish the new role to **both** places that record it: mutate the sidecar's live `worker_info`
   dict so the next heartbeat registers the new `worker_type`, and update the Pod's runtime label.
   The router then resumes routing. Patching only the label leaves the heartbeat re-asserting the old
   role every 10 seconds.

Steps 1–2 before 3, and 3–4 before 5, are what keep the eventually-consistent routing window from
carrying traffic for a role the engine is not serving.

### Decision: The Switcher Lives in Patio

@diw-zw suggested in [#541][issue-541] that PD service switching and label switching could move into
the [Patio][patio] sidecar, and that the two repositories could be co-designed. **This KEP adopts that
direction.** The suggestion was phrased as a proposal, not a mandate, and the same comment thread asks
for a KEP precisely so the community can settle the design; recording a direction here is what that
discussion is for.

Three reasons, in order of weight:

1. **Only the sidecar can update both copies of the serving role.** The router's `worker_type` and the
   Pod label must agree, and Patio's heartbeat re-publishes `worker_type` from a dict it holds in
   process every 10 seconds. A switcher outside the Pod cannot mutate that dict; it would have to
   fight the heartbeat or add a new control path into Patio anyway. See
   [the router note](#the-router-holds-a-second-copy-of-the-serving-role).
2. **No new RBG API is required.** `ClusterEngineRuntimeProfile`, `engineRuntimes`, and the native
   `serviceAccountName` field already carry everything the sidecar needs.
3. **The engine-side plumbing already exists.** Patio owns engine readiness polling, health, and
   topology registration. An external switcher can reach the same engine APIs over the network — it is
   not blind — but it would rebuild that plumbing rather than reuse it.

#### The layered split

| Layer | Owns | Delivered in |
| --- | --- | --- |
| RBG | The label-ownership contract on live Pods; carrying the template's initial value; not reverting external writes; the injection channel | This repo, this KEP's first slice |
| Patio | The switch flow: drain, engine flip, readiness, mutating `worker_info`, patching the Pod label; failure handling; its RBAC | [Patio][patio], under its own `proposals/` process |
| SGLang router | Worker registry and label/endpoint convergence | Upstream; no change proposed here |

The RBG-side slice is tests, documentation, a v1alpha2 example, and verification that the existing
injection channel carries the contract. None of it changes controller behavior, so it can land
independently of Patio's schedule.

#### What Patio has to build

Verified current state of that repository: `disaggregation`, `pd_role`, and `switch_role` have zero
hits. `prefill` appears only as a static `worker_type` in topology registration
([`sgl_topo_client.py`][patio-topo-client]) and its tests. **No runtime PD switching exists there yet.**
The flip endpoint call, the drain wait, the `worker_info` mutation, the Pod-label patch, and the RBAC
to perform it are all new work. So is **gating initial registration on the serving label**, without
which a warming Pod is registered as a real worker while labelled `unassigned`. Patio's process
requires a proposal for architecture and lifecycle changes, which this is; tests and docs are exempt.

Patio is a small repository — 15 commits at the time of writing, its most recent adding the proposal
process itself. It is active and it is maintained by RBG contributors, but the flip path's delivery
cannot be assumed on RBG's timeline. That is a scheduling risk, not a reason to choose differently.

#### Fallback: external switch service

An external deployment that watches the pool, calls the engine API, and patches Pod labels remains
**viable**, because the contract constrains RBG rather than the writer. It is kept as a documented
fallback for two cases: an engine Patio does not support, and a site that will not grant `patch pods`
to an in-Pod service account.

It is not the recommended path. It leaves every user to build drain and failure handling, and it
cannot suppress the heartbeat's re-assertion of the old `worker_type` without a Patio-side change —
which removes its main advantage of needing no Patio code.

Two claims from the earlier draft of this section are corrected here, because both were overstated:
an external switcher **can** observe engine and KV state by calling the engine API remotely, and
running in the Pod's own service account is **not** least privilege — RBAC cannot scope a grant to
"this Pod only" without enumerating Pod names, so the sidecar's grant is namespace-scoped.

### Test Plan

- [x] I/we understand the owners of the involved components may require updates to existing tests
  to make this code solid enough prior to committing the changes necessary to implement this
  enhancement.

##### Prerequisite testing updates

The existing envtest harness (`test/envtest/testutil`) already starts a real etcd and kube-apiserver
with the controllers in-process, which is what this contract needs. Note that envtest runs **no
kubelet**, so container status and `ImageID` must be written by the test to let the controller observe
an in-place update as completed.

Two gaps must be closed before the suite can be claimed to cover the contract:

1. **The prototyped RBG sets no `rolloutStrategy`.** With the default strategy the suite exercises only
   the `GraceSeconds <= 0` write. The grace-period-expiry second write and the `onlyUpdateRevision`
   path are never reached, even though the shipped example sets `gracePeriodSeconds: 30`. The suite
   needs a variant with a grace period configured, and a revision-only variant.
2. **`SetupTestEnv` wires no admission webhooks.** This is deliberate and is *not* a coverage gap for
   this contract: the mutating webhooks target RBG, RBGSet, RoleInstanceSet and CoordinatedPolicy, none
   target Pods, and the RBG webhook does not touch template labels. It is recorded here so a reviewer
   does not have to re-derive it.

##### Unit tests

- Metadata patch computation: a key whose value is identical in both revisions is absent from the
  resulting patch.
- An image-only revision diff yields `MetaDataPatch == nil`. This is the false-pass condition the
  integration test must avoid, so it is worth asserting directly.
- A key present in the new revision but not the old one appears in the patch (the boundary case).

##### Integration tests

Three envtest specs, already prototyped on branch `test/541-runtime-label-contract` (not part of
this PR):

1. **Reconciliation.** Create an RBG whose pool template declares the runtime label and a runtime
   annotation. Patch both from outside to a different value. Assert with `Consistently` over a soak
   window that neither reverts, and that the Pod UID is unchanged.
2. **Annotation path.** Same as above for an externally patched annotation, since annotations and
   labels travel through the same metadata patch.
3. **Completed in-place update.** Patch the runtime label from outside, then roll the template image
   **and a template-owned annotation** together. Assert: the new image lands on the same Pod UID
   (proving the update was in-place, not a recreation); the template-owned annotation reached its
   new value (proving a metadata patch really was applied, so the label did not survive merely
   because the patch was `nil`); the external label and annotation kept their patched values. Then
   fake the kubelet reporting the new `ImageID`, wait for `InPlaceUpdateReady` to become true, and
   assert the external values still hold after the completion path.

Every spec asserts the Pod UID is unchanged, so preservation by recreation cannot pass.

Two further specs are required to cover the remaining write sites:

4. **Grace-period expiry.** Same as spec 3, but with `gracePeriodSeconds` set so the update takes the
   grace branch and the second write after `RemoveInPlaceUpdateGrace` actually runs. Assert the
   external values hold after both writes.
5. **Revision-only update.** Trigger `onlyUpdateRevision` so the revision-hash label is rewritten
   without a template metadata diff, and assert the external values hold.

The suite has been mutation-verified: replacing the old-template base of the metadata patch with an
empty `ObjectMeta` makes spec 3 fail on the external-label assertion, confirming the test can actually
detect the regression it is meant to guard. That verification covers **spec 3's path only**; it says
nothing about specs 4 and 5 until they exist and are mutation-verified themselves.

##### e2e tests

Not in the first slice. A real e2e needs GPU nodes with a KV transfer backend that supports
`supports_role_switch` (mori today), which the project's CI does not currently have. When capacity
exists, the e2e should assert: a pool Pod flips via `/pd_role_switch`, its runtime label changes, it
moves between the two label-selecting Services' EndpointSlices, and accuracy is unchanged after a
flip and a flip-back.

### Graduation Criteria

There is no feature gate and no API field, so graduation here is about the contract and its
ecosystem rather than API maturity.

#### Alpha

- The Patio direction and the layered split are recorded in this KEP and acknowledged by the
  maintainers.
- The regression suite is merged and runs in CI, covering all three write sites (specs 3, 4 and 5),
  each mutation-verified.
- The contract documentation and a v1alpha2 example are merged, with the example's backend caveats
  stated honestly and the Patio profile pinning `updateStrategy: NoUpdate`.

#### Beta

- The switcher exists in Patio, with drain, failure handling, namespace-scoped RBAC, and the
  `worker_info` mutation wired into the flip.
- An e2e flip and flip-back passes on a supported backend, asserting that the label **and** the
  router's registered `worker_type` both converge to the new role.
- The boundary condition (template value edited at runtime) is documented and tested.
- The scale-in interaction is documented, with a stated procedure for preserving the role mix.

#### GA

- At least two independent deployments run runtime switching across a full RBG release cycle,
  evidenced by pool Pods carrying non-`unassigned` serving labels before and after an RBG upgrade,
  with no label-reversion incident reported against [#541][issue-541].
- Flip-back stability is reproduced on a supported backend over a sustained run — upstream's
  100-switch result reproduced at least once in CI, not a single cycle.
- Patio's flip success rate and flip latency metrics are published and consumed by at least one
  operator's alerting, so the SLIs below are actually observed rather than merely defined.

### Upgrade / Downgrade Strategy

The first slice adds tests, documentation, and an example. It changes no controller code, no CRD,
and no default. Existing RoleBasedGroups are unaffected, and no user action is required on upgrade
or downgrade.

The Patio sidecar's introduction is a separate rollout with its own upgrade path, to be specified in
that repository's proposal. Introducing it into an existing pool is a profile change, so the
`updateStrategy` hazard above applies directly to the rollout itself.

### Version Skew Strategy

RBG itself is version-agnostic here, since it gains no API surface. The skew that matters is between
the switcher and the engine:

- A flip requires an engine build containing [sgl-project/sglang#28403][sglang-pr] **and**
  `--enable-pd-role-switch`. Older engines do not expose `/pd_role_switch`.
- The switcher must probe capability before attempting a flip, and must treat a rejected or absent
  endpoint as a failed flip rather than advancing the label.
- The KV transfer backend must implement `supports_role_switch`; upstream checks this up front in
  the reject path.
- Patio and engine versions skew independently; the sidecar must not assume a flip API exists because
  Patio is new enough.
- The router's `worker_type` semantics are a third skew axis: a router old enough to treat a repeated
  `POST /workers` as a new worker rather than an update changes the flip procedure. The sidecar must
  detect which behaviour it is talking to.

## Production Readiness Review Questionnaire

### Feature Enablement and Rollback

###### How can this feature be enabled / disabled in a live cluster?

- [x] Other
    - Describe the mechanism: there is no feature gate. A user enables runtime switching by
      declaring a runtime label key in the Pod template, launching engines with
      `--enable-pd-role-switch`, and running a switcher. Disabling means stopping the switcher; the
      label keeps its last value and routing continues against it.
    - Will enabling / disabling the feature require downtime of the control plane? No.
    - Will enabling / disabling the feature require downtime or reprovisioning of a node? No. A
      flip does not restart the engine or reallocate the KV pool.

###### Does enabling the feature change any default behavior?

**No.** The first slice changes no controller code. The behavior it documents is the behavior RBG
already has. This is the central safety property of the proposal.

###### Can the feature be disabled once it has been enabled (i.e. can we roll back the enablement)?

There is nothing in RBG to disable. A user stops running the switcher, and optionally removes the
runtime key from the template — which is a normal template change and rolls the role according to
its rollout strategy.

###### What happens if we reenable the feature if it was previously rolled back?

Not applicable; no gate exists. Restarting a switcher resumes patching from the current label
values.

###### Are there any tests for feature enablement/disablement?

The envtest suite covers both clobber paths. There is no gate to toggle, so no gate-switch test is
needed. The unit test asserting `MetaDataPatch == nil` for an image-only diff covers the condition
under which the metadata path is not exercised at all.

### Rollout, Upgrade and Rollback Planning

###### How can a rollout or rollback fail? Can it impact already running workloads?

The first slice cannot impact running workloads: it adds no controller code. For a later switcher
rollout, the failure modes are a switcher restart mid-flip (engine flipped, label not yet updated)
and a switcher version that mis-handles a rejected flip. Both are switcher-side and are addressed in
[Switch Flow](#switch-flow) and [Troubleshooting](#troubleshooting).

###### What specific metrics should inform a rollback?

Switcher-side: flip failure rate, count of pool Pods stuck in `unassigned` beyond a warmup deadline,
and count of engines reporting the fail-fast `unhealthy` state after a rebuild. A sustained rise in
any of these indicates the switcher should stop flipping.

###### Were upgrade and rollback tested? Was the upgrade->downgrade->upgrade path tested?

Not yet. The first slice is tests and documentation, so there is no behavior to upgrade or roll
back. This must be answered before a switcher ships.

###### Is the rollout accompanied by any deprecations and/or removals of features, APIs, fields of API types, flags, etc.?

No.

### Monitoring Requirements

###### How can an operator determine if the feature is in use by workloads?

By observing the user-defined runtime label on Pods. This is an API observation rather than a
metric, which the template treats as a last resort; it is unavoidable here, because RBG
deliberately does not interpret the key and therefore cannot emit a metric about it.

###### How can someone using this feature know that it is working for their instance?

- [x] Other (treat as last resort)
    - Details: the Pod's runtime label value, the Pod's membership in the label-selecting Services'
      EndpointSlices, and the engine's own `/get_server_info`. Together these show the serving
      identity, the routing consequence, and the engine state.

###### What are the reasonable SLOs (Service Level Objectives) for the enhancement?

Proposed, to be confirmed with switcher owners:

- Zero externally written runtime label values reverted by RBG.
- A flip either completes and is reflected in the label, or leaves the label unchanged; no
  intermediate state where the label claims a role the engine is not serving.
- Time from flip request to label update bounded by drain time plus engine rebuild time, with the
  switcher enforcing a deadline.
- The router's registered `worker_type` converges to the new role within one heartbeat interval of
  the flip, so the label and the registry never disagree for longer than that interval.

###### What are the SLIs (Service Level Indicators) an operator can use to determine the health of the service?

- [x] Other (treat as last resort)
    - Details: flip success rate and flip latency, measured by the switcher and exposed through
      Patio's existing normalized `patio:` metrics. RBG emits no new SLI, by design.

###### Are there any missing metrics that would be useful to have to improve observability of this feature?

A "runtime label reverted" counter would be directly useful, but emitting it requires RBG to know
which keys are runtime keys and to compare live values against external intent — that is exactly
the interpretation this KEP excludes. Declined deliberately; the regression suite is the substitute.

### Dependencies

- **SGLang engine with [sgl-project/sglang#28403][sglang-pr] and `--enable-pd-role-switch`.**
    - Usage description: performs the actual role flip.
    - Impact of its outage on the feature: the flip is rejected or the endpoint is absent. The label
      stays at its current value and routing continues against the current role. Degrades to static
      PD, which is safe.
    - Impact of degraded performance or high error rates: rebalancing stalls; serving is unaffected.
- **A KV transfer backend implementing `supports_role_switch` (mori validated).**
    - Usage description: teardown and rebuild of role-specific transfer structures.
    - Impact of its outage: upstream rejects the flip up front. Same safe degradation.
- **A router that selects by the runtime label.**
    - Usage description: turns a label change into a routing change.
    - Impact of its outage: traffic loss, not label corruption. Independent of RBG's guarantee.
- **The Patio sidecar, carrying the switcher.**
    - Usage description: owns the five-step flow, including the `worker_info` mutation.
    - Impact of its outage: no rebalancing. Existing roles keep serving. Note that Patio's heartbeat
      also stops, so a router restart during the outage loses this worker's registration until Patio
      returns.
- **The SGLang router's worker registry.**
    - Usage description: holds `worker_type` per worker, refreshed by Patio's heartbeat.
    - Impact of its outage: `register` retries for up to three minutes and stalls the heartbeat loop;
      flips cannot be published. Labels remain correct, so label-selecting Services still route.
- **Kubernetes API server.**
    - Usage description: the switcher patches Pod labels.
    - Impact of its outage: see [Troubleshooting](#troubleshooting) — this is the one dependency
      whose failure can desynchronize engine state from routing state.

### Scalability

###### Will enabling / using this feature result in any new API calls?

**RBG makes none.** The first slice changes no controller code. The switcher issues `PATCH pods`,
at a rate proportional to the number of flips, not periodic: one patch per flip per Pod, from the
Pod's own service account. Patio's registration heartbeat already exists and is unchanged in
frequency; a flip rides the next heartbeat rather than adding a call.

###### Will enabling / using this feature result in introducing new API types?

No.

###### Will enabling / using this feature result in any new calls to the cloud provider?

No.

###### Will enabling / using this feature result in increasing size or count of the existing API objects?

One label (roughly 50 bytes) and optionally one annotation, on Pods in the switchable pool only.
Fixed roles and the router are unchanged. No new objects.

###### Will enabling / using this feature result in increasing time taken by any operations covered by existing SLIs/SLOs?

No change to RBG operations. Flip latency is dominated by draining and by the engine's
teardown/rebuild, neither of which RBG participates in.

###### Will enabling / using this feature result in non-negligible increase of resource usage (CPU, RAM, disk, IO, ...) in any components?

RBG: none. Engine: upstream reports flat weight, KV-cache, graph, GPU-free and thread-count metrics
across 100 switches, with the radix cache both off and on. Patio: to be assessed in that repository's
proposal.

###### Can enabling / using this feature result in resource exhaustion of some node resources (PIDs, sockets, inodes, etc.)?

A flip releases sockets, zmq context, RDMA registrations and the transfer engine before rebuilding,
and upstream's leak stress run shows no growth. The pathological case is a rebuild that fails
partway; upstream's fail-fast `unhealthy` state plus the switcher's refusal to advance the label
keeps such a Pod out of rotation rather than leaking repeatedly.

### Troubleshooting

###### How does this feature react if the API server and/or etcd is unavailable?

The engine can still flip, but the switcher cannot patch the label. This is the one genuinely
dangerous desynchronization: the engine serves a new role while routing still reflects the old one.
The switcher must therefore treat a label patch failure as a failed flip — roll the engine back or
mark the Pod out of rotation — and must not assume the flip is complete until the label is written.
Ordering alone is not sufficient under API server outage.

###### What steps should be taken if SLOs are not being met to determine the problem?

Check, in order: whether the engine reports the new role (`/get_server_info`); whether the Pod's
runtime label matches it; whether the router's registered `worker_type` for that worker matches it;
whether the Pod appears in the expected Service's EndpointSlice; whether the switcher logged a
rejected flip or a patch failure; and whether the RBG spec's template value for the runtime key was
edited, which would legitimately overwrite the live value.

###### What are other known failure modes?

- **Flip rejected by precondition checks.**
    - Detection: switcher records a non-success response; label unchanged.
    - Mitigations: none needed; routing stays on the current role. Check backend
      `supports_role_switch` and parallelism constraints.
    - Diagnostics: engine response body; upstream uses table-driven precondition checks.
    - Testing: covered upstream by unit tests over all reject branches.
- **Rebuild failure after teardown.**
    - Detection: engine enters the fail-fast `unhealthy` state.
    - Mitigations: switcher must not advance the label; Pod stays out of rotation; recycle the Pod.
    - Diagnostics: engine health endpoint and logs.
    - Testing: switcher-side failure injection, once a switcher exists.
- **Template value edited at runtime.**
    - Detection: external label reverts to the new template value after an in-place update.
    - Mitigations: this is expected behavior, not a bug. Document the boundary; do not edit the
      runtime key in the spec.
    - Diagnostics: compare the two ControllerRevisions' template metadata.
    - Testing: the unit test asserting a changed key enters the patch.
- **Pod recreated for an unrelated reason.**
    - Detection: new Pod UID; runtime label back to the template's initial value.
    - Mitigations: the `unassigned` convention keeps it out of rotation until reassigned.
    - Diagnostics: Pod UID and `role-instance-id`.
    - Testing: the integration specs assert UID stability so this case cannot masquerade as
      preservation.
- **Heartbeat re-asserts the old role after a flip.**
    - Detection: the Pod label and `/get_server_info` agree on the new role, but the router still
      routes it as the old one; the discrepancy persists rather than converging.
    - Mitigations: the switcher must mutate the live `worker_info` dict, not only the label. If the
      router does not update `worker_type` on a repeated POST, unregister and re-register instead.
    - Diagnostics: Patio's registration logs (`registered worker successfully`, `worker_id`) and the
      router's worker list.
    - Testing: switcher-side, once the flip path exists; the e2e must assert registry convergence, not
      just the label.
- **Scale-in removes the Pods serving a role.**
    - Detection: after a replicas decrease, one of the label-selecting Services has no endpoints.
    - Mitigations: rebalance roles onto surviving low-ID Pods before scaling in. RBG selects victims
      by component ID and will not do this for you.
    - Diagnostics: compare the deleted Pods' component IDs with the serving labels of the survivors.
    - Testing: not covered by the first slice; needs a switcher-in-the-loop test.
- **A profile rollout resets the whole pool.**
    - Detection: many pool Pods return to `unassigned` at once, following a
      `ClusterEngineRuntimeProfile` edit.
    - Mitigations: pin `updateStrategy: NoUpdate` on the Patio profile.
    - Diagnostics: the profile's `updateStrategy` and the affected roles' `rolloutStrategy`.
    - Testing: a profile-edit test asserting serving labels survive, once the v1alpha2 example exists.

## Implementation History

- 2026-11-06: issue [#541][issue-541] claimed by @zhuangzhewei09.
- 2026-11-13: assigned by @diw-zw, who requested a KEP before implementation and suggested
  co-design with [Patio][patio].
- 2026-11-17: this provisional KEP opened. The three-spec envtest regression suite is prototyped on
  branch `test/541-runtime-label-contract` and is intentionally **not** part of this PR.
- 2026-11-17: revised after a full self-review against the assigner's requirements. The switcher
  placement is now **decided as Patio**, with a layered RBG/Patio/router split replacing the former
  open A/B question. Added: the existing `ClusterEngineRuntimeProfile` injection channel, the
  `updateStrategy` reset hazard, the router's `worker_type` heartbeat, scale-in victim selection, and
  the Pod-deletion consequence of writing an RBG-owned label. Corrected: the causal explanation of why
  external writes survive (full `Update` plus optimistic concurrency, not the absence of `Patch`), the
  example's missing `app` label, and two overstated option comparisons. Test plan now requires grace
  and revision-only specs.

## Drawbacks

- The contract is enforced only by tests. RBG cannot validate the key or its value, so a user who
  picks an RBG-owned prefix or edits the template value at runtime gets no admission-time warning.
- Correctness burden sits with the switcher: draining, ordering, and failure handling are all
  outside RBG, and getting them wrong can corrupt in-flight requests.
- Label-based routing is eventually consistent, so there is an inherent convergence window.
- Co-designing across two repositories adds coordination cost and a version-skew surface that a
  single-repo solution would not have.
- Placing the switcher in the sidecar puts `patch pods` permission inside every inference Pod in the
  pool. The grant is namespace-scoped, so a compromised sidecar can relabel its neighbours.
- The flip path's delivery depends on Patio, a 15-commit repository. RBG's contract can land
  independently, but the feature is not usable until Patio ships its side.

## Alternatives

### A new RBG API field for the runtime role

Rejected. The issue explicitly proposes no new field. It would also force RBG to interpret engine
semantics, couple RBG to SGLang's role model, and create a second source of truth that could
disagree with the engine.

### RBG drives the switching itself

Rejected. The controller would have to call engine APIs, orchestrate draining and KV transfer, and
assess engine health. That makes RBG engine-aware and widens its blast radius considerably, for a
policy upstream itself excludes.

### Recreate Pods with the new role

Rejected. This is today's behavior and the thing being improved: it reloads weights, reallocates
the KV pool, and pays a cold start, which is exactly the cost runtime switching removes.

### Do nothing, since the contract already holds

Rejected. It holds accidentally, is untested, and is undocumented. The issue's own example is
broken for its stated purpose because `spec.roles[].labels` never reaches Pod labels. Without a
test, the next drift-correction change breaks every runtime-switching deployment silently.

### External switch service instead of the Patio sidecar

**Considered, kept as fallback only.** It preserves the contract, but it cannot mutate the
`worker_info` dict the heartbeat re-publishes, so it needs a Patio-side change anyway. See
[Decision: The Switcher Lives in Patio](#decision-the-switcher-lives-in-patio).

## Infrastructure Needed

- Maintainer confirmation of the Patio direction recorded in this KEP, and of the layered split.
- A proposal in [rolebasedgroup/inference-engine-runtime][patio] for the flip path, the `worker_info`
  mutation, the Pod-label patch, and the in-Pod RBAC. Its process exempts tests and docs but requires
  a proposal for architecture and lifecycle changes, which this is.
- A v1alpha2 Patio profile example in this repo, pinning `updateStrategy: NoUpdate`, replacing the
  v1alpha1 one that currently sits under `examples/deprecated/`.
- Confirmation of the router's semantics for a repeated `POST /workers` carrying a changed
  `worker_type`, so the switch flow can be fixed rather than assumed.
- Maintainer confirmation of the target milestone. `kep.yaml` proposes v0.10.0: v0.9.0-alpha.4 was
  already published on 2026-11-17, so v0.9.0 is not a credible target for a KEP that has not started
  implementation.
- GPU CI capacity with a KV transfer backend that supports `supports_role_switch` (mori today) for
  the e2e flip test. This does not exist in the project's CI at present.
- Confirmation of whether the mooncake/CUDA path supports a flip, so the example can state its
  backend honestly.

[issue-541]: https://github.com/sgl-project/rbg/issues/541
[sglang-pr]: https://github.com/sgl-project/sglang/pull/28403
[patio]: https://github.com/rolebasedgroup/inference-engine-runtime
[instance-scale]: https://github.com/sgl-project/rbg/blob/main/pkg/reconciler/roleinstance/sync/instance_scale.go
[pod-inplace-defaults]: https://github.com/sgl-project/rbg/blob/main/pkg/inplace/pod/inplaceupdate/inplace_update_defaults.go
[instance-inplace-defaults]: https://github.com/sgl-project/rbg/blob/main/pkg/inplace/instance/inplaceupdate/inplace_update_defaults.go
[patio-extract]: https://github.com/sgl-project/rbg/pull/366
[sidecar-builder]: https://github.com/sgl-project/rbg/blob/main/pkg/discovery/sidecar_builder.go
[pod-reconciler]: https://github.com/sgl-project/rbg/blob/main/pkg/reconciler/pod_reconciler.go
[patio-profile]: https://github.com/sgl-project/rbg/blob/main/examples/deprecated/v1alpha1/pd-disagg/sglang/patio-profile.yaml
[engine-runtime-example]: https://github.com/sgl-project/rbg/blob/main/examples/basic/engine-runtime/engine-runtime-profile.yaml
[instance-core]: https://github.com/sgl-project/rbg/blob/main/pkg/reconciler/roleinstance/core/instance_core.go
[instance-utils]: https://github.com/sgl-project/rbg/blob/main/pkg/reconciler/roleinstance/utils/instance_utils.go
[rbg-planner]: https://github.com/rolebasedgroup/rbg-planner
[patio-app]: https://github.com/rolebasedgroup/inference-engine-runtime/blob/main/src/patio/app.py
[patio-topo-client]: https://github.com/rolebasedgroup/inference-engine-runtime/blob/main/src/patio/topo/client/sgl_topo_client.py
[pod-adapter]: https://github.com/sgl-project/rbg/blob/main/pkg/inplace/pod/clientadapter/adapter.go
[pod-inplace]: https://github.com/sgl-project/rbg/blob/main/pkg/inplace/pod/inplaceupdate/inplace_update.go
[ri-inplaceupdate]: https://github.com/sgl-project/rbg/blob/main/pkg/reconciler/roleinstance/inplaceupdate/inplaceupdate.go
[deploy-reconciler]: https://github.com/sgl-project/rbg/blob/main/pkg/reconciler/deploy_reconciler.go
[sts-reconciler]: https://github.com/sgl-project/rbg/blob/main/pkg/reconciler/sts_reconciler.go
[pd-standalone]: https://github.com/sgl-project/rbg/blob/main/examples/inference/pd-disagg-standalone.yaml
[pd-lw]: https://github.com/sgl-project/rbg/blob/main/examples/inference/pd-disagg-leader-worker.yaml
