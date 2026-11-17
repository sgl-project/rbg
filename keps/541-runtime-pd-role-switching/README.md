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
    - [Open Question: Where the Switcher Lives](#open-question-where-the-switcher-lives)
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
  runtime by an external switcher.

RBG does not interpret, validate, or manage the serving identity. It carries the key through as an
ordinary label and must never revert a value written from outside. That already holds today, but
nothing tests it and nothing documents it, so a future drift-correction change would break it
silently.

The deliverables are a regression suite pinning the contract, user-facing documentation, and a
runnable example. One design question is left open for the community: whether the switcher runs as a
service outside RBG, or inside the [Patio][patio] sidecar, as suggested by @diw-zw in
[#541][issue-541].

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

1. Pin the label-ownership contract with regression tests covering **both** paths that could
   plausibly clobber an externally written key: steady-state reconciliation, and a completed
   in-place update.
2. Document the contract, the switch flow, and its boundary conditions, so a switcher author knows
   precisely what RBG guarantees and what it does not.
3. Provide a runnable example that wires routing to the runtime label rather than to the topology
   role.
4. Reach an explicit community decision on where the switcher lives (external service versus Patio
   sidecar), and record it in this KEP.
5. Correct one factual error in the issue text and refine one code citation, both verified against
   the repository and a live API server.

### Non-Goals

1. **No new RBG API field, no CRD change, no feature gate.** The issue states this explicitly.
2. RBG does not interpret, validate, or manage the runtime label's key or value. The key is
   user-defined.
3. No automatic load-based flip policy. Upstream excludes this too; the flip is driven externally.
4. No request draining or KV-cache transfer orchestration inside RBG.
5. No change to topology identity semantics. `role-name` stays immutable.
6. Implementing the switcher itself is out of scope for the first slice of this KEP. The first slice
   is tests, documentation, and an example, none of which change controller behavior.

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
   `unassigned`, so new and recreated Pods are safe by default: the router only routes to Pods
   carrying a valid role, and the switcher assigns a role after warmup.

The switcher — not RBG — owns the five-step switch flow described in
[Switch Flow](#switch-flow).

### User Stories

#### Story 1

As an inference platform operator, I want to rebalance the P:D ratio of a running deployment
without cold-starting engines, so that a shift in prompt/generation length mix does not cost me
weight reloads and a capacity dip.

#### Story 2

As the author of a switch service, I want a documented guarantee that RBG will not revert the
runtime label I patch onto a live Pod, so I can treat the label as durable state I own rather than
re-asserting it on a loop to fight the controller.

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
| The switcher needs RBAC to patch Pods | Scope to `patch` on `pods` in the RBG namespace only. If the switcher becomes a Patio sidecar, this is a new permission surface and must be reviewed in that repo's proposal. |
| Pod recreation resets the serving role | The `unassigned` convention plus label-selecting routing makes this safe by default rather than a misroute. |
| Label-based routing is eventually consistent | There is a brief window between a label patch and router endpoint convergence. The switch flow orders drain before flip and flip before re-label, so the window does not carry traffic for the wrong role. |

## Design Details

### The Contract

Normative statement, intended for the user-facing documentation:

> A runtime serving-role label on a Pod is owned by an external switch service. RBG carries the key
> through as an ordinary label. The value in the Pod template is the **initial** value only. Do not
> modify that value in the RBG spec at runtime. RBG will not revert a value written from outside,
> across reconciliation and across in-place updates. A recreated Pod returns to the initial value.

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

### Why an Externally Written Value Survives

Two code properties, both cited so a reviewer can check them:

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
is absent from the patch, and `StrategicMergePatch` leaves the live value untouched. Nothing on this
path compares the live Pod against the template.

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
5. Update the runtime label to the new role. The router resumes routing by label.

Steps 1–2 before 3, and 3–4 before 5, are what keep the eventually-consistent routing window from
carrying traffic for a role the engine is not serving.

### Open Question: Where the Switcher Lives

@diw-zw suggested in [#541][issue-541] that PD service switching and label switching could move into
the [Patio][patio] sidecar, and that the two repositories could be co-designed.

<<[UNRESOLVED @diw-zw @cheyang]>>
This KEP does not presume the answer. The contract above is invariant under either option, because
it constrains **RBG's** behavior toward an externally written label, regardless of which component
writes it. What changes is where the switch flow, the RBAC, and the failure handling live.
<<[/UNRESOLVED]>>

**Option A — external switch service.** A deployment outside RBG watches the pool, calls the engine
API, and patches Pod labels.

- Pros: no new RBG or Patio code; no new in-Pod permission surface; works with any engine that
  exposes a flip API; the contract is the only integration point.
- Cons: every user builds or buys the switcher; drain and failure handling are the user's problem;
  needs cluster-wide-ish `patch pods` RBAC; no engine-side view of KV transfer state.

**Option B — Patio sidecar.** Patio already runs alongside the engine and already owns LoRA loading,
normalized metrics, topology registration, and health.

Verified current state of that repository: `disaggregation`, `pd_role`, and `switch_role` have zero
hits. `prefill` appears only as a static `worker_type` in topology registration
(`src/patio/topo/client/sgl_topo_client.py`) and its tests. **No runtime PD switching exists there
yet.** Patio has its own `proposals/` process, which requires a proposal for architecture and
lifecycle changes but exempts tests and docs.

- Pros: the switcher sits next to the engine, so it can observe drain and KV state directly; one
  place for engine-specific behavior across SGLang and vLLM; reuses Patio's health and topology
  plumbing; label patching can be done from the Pod's own service account.
- Cons: a new proposal and implementation in a second repository; a new RBAC surface inside every
  inference Pod; couples the flip path to Patio's release cycle; cross-repo version skew.

**What is invariant either way.** The regression suite, the contract documentation, and the
label-selecting routing example are all useful under both options, and none of them change
controller behavior. That is why the first slice can proceed while this question is open.

### Test Plan

- [x] I/we understand the owners of the involved components may require updates to existing tests
  to make this code solid enough prior to committing the changes necessary to implement this
  enhancement.

##### Prerequisite testing updates

None. The existing envtest harness (`test/envtest/testutil`) already starts a real etcd and
kube-apiserver with the controllers in-process, which is what this contract needs. Note that envtest
runs **no kubelet**, so container status and `ImageID` must be written by the test to let the
controller observe an in-place update as completed.

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

The suite has been mutation-verified: replacing the old-template base of the metadata patch with an
empty `ObjectMeta` makes spec 3 fail on the external-label assertion, confirming the test can
actually detect the regression it is meant to guard.

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

- The decision on switcher placement is recorded in this KEP.
- The regression suite is merged and runs in CI.
- The contract documentation and the runnable example are merged, with the example's backend
  caveats stated honestly.

#### Beta

- A switcher exists in whichever location was decided, with drain, failure handling, and scoped
  RBAC.
- An e2e flip and flip-back passes on a supported backend.
- The boundary condition (template value edited at runtime) is documented and tested.

#### GA

- Multiple real deployments use runtime switching without a label-reversion incident.
- Flip-back stability is demonstrated over a sustained period, not a single cycle.

### Upgrade / Downgrade Strategy

The first slice adds tests, documentation, and an example. It changes no controller code, no CRD,
and no default. Existing RoleBasedGroups are unaffected, and no user action is required on upgrade
or downgrade.

If Option B is chosen later, the Patio sidecar's introduction is a separate rollout with its own
upgrade path, to be specified in that repository's proposal.

### Version Skew Strategy

RBG itself is version-agnostic here, since it gains no API surface. The skew that matters is between
the switcher and the engine:

- A flip requires an engine build containing [sgl-project/sglang#28403][sglang-pr] **and**
  `--enable-pd-role-switch`. Older engines do not expose `/pd_role_switch`.
- The switcher must probe capability before attempting a flip, and must treat a rejected or absent
  endpoint as a failed flip rather than advancing the label.
- The KV transfer backend must implement `supports_role_switch`; upstream checks this up front in
  the reject path.
- Under Option B, Patio and engine versions skew independently; the sidecar must not assume a flip
  API exists because Patio is new enough.

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

###### What are the SLIs (Service Level Indicators) an operator can use to determine the health of the service?

- [x] Other (treat as last resort)
    - Details: flip success rate and flip latency, measured by the switcher. Under Option B these
      would be exposed through Patio's existing normalized `patio:` metrics. RBG emits no new SLI,
      by design.

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
- **The switcher (external service or Patio sidecar).**
    - Usage description: owns the five-step flow.
    - Impact of its outage: no rebalancing. Existing roles keep serving.
- **Kubernetes API server.**
    - Usage description: the switcher patches Pod labels.
    - Impact of its outage: see [Troubleshooting](#troubleshooting) — this is the one dependency
      whose failure can desynchronize engine state from routing state.

### Scalability

###### Will enabling / using this feature result in any new API calls?

**RBG makes none.** The first slice changes no controller code. The switcher issues `PATCH pods`,
at a rate proportional to the number of flips, not periodic. Under Option B this is one patch per
flip per Pod, from the Pod's own service account.

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
across 100 switches, with the radix cache both off and on. Patio under Option B: to be assessed in
that repository's proposal.

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
runtime label matches it; whether the Pod appears in the expected Service's EndpointSlice; whether
the switcher logged a rejected flip or a patch failure; and whether the RBG spec's template value
for the runtime key was edited, which would legitimately overwrite the live value.

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

## Implementation History

- 2026-11-06: issue [#541][issue-541] claimed by @zhuangzhewei09.
- 2026-11-13: assigned by @diw-zw, who requested a KEP before implementation and suggested
  co-design with [Patio][patio].
- 2026-11-17: this provisional KEP opened. The three-spec envtest regression suite is prototyped on
  branch `test/541-runtime-label-contract` and is intentionally **not** part of this PR, pending the
  switcher-placement decision.

## Drawbacks

- The contract is enforced only by tests. RBG cannot validate the key or its value, so a user who
  picks an RBG-owned prefix or edits the template value at runtime gets no admission-time warning.
- Correctness burden sits with the switcher: draining, ordering, and failure handling are all
  outside RBG, and getting them wrong can corrupt in-flight requests.
- Label-based routing is eventually consistent, so there is an inherent convergence window.
- Co-designing across two repositories adds coordination cost and a version-skew surface that a
  single-repo solution would not have.

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

### External switch service versus Patio sidecar

**Open.** See [Open Question: Where the Switcher Lives](#open-question-where-the-switcher-lives).
Both preserve the contract; they differ in where the switch flow, RBAC, and failure handling live.

## Infrastructure Needed

- A decision from @diw-zw and the maintainers on switcher placement, recorded in this KEP.
- If Option B is chosen: a proposal in [rolebasedgroup/inference-engine-runtime][patio]. Its process
  exempts tests and docs but requires a proposal for architecture and lifecycle changes, which a
  flip path plus Pod-patching RBAC would be.
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
