# Engine Runtime Profile

ClusterEngineRuntimeProfile is a cluster-scoped CRD that defines reusable sidecar, init-container, and volume configurations, plus an additive container patch that can modify existing role containers. These profiles can be injected into role pods, enabling consistent runtime dependencies across multiple RoleBasedGroups.

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
| `initContainers` | Init containers to inject |
| `containers` | Sidecar containers to inject |
| `volumes` | Volumes to inject |
| `containerPatches` | The profile-side patch list: **target-less** ordered entries — each sets exactly one patch field, today `jsonPatch`, a standard RFC 6902 `add`-only op — every entry firing at each `targetContainers` address a referencing `engineRuntimes[]` entry lists, in list order. Optional, 1–32 items |

### `updateStrategy` (deprecated)

`updateStrategy` is deprecated in both served versions: it was accepted and never consumed, and immutability (below) removes the surface it would have governed — a referenced profile's `spec` cannot change, so there is no *when does a change reach existing workloads* question left to own. The field stays accepted for stored objects (enum `NoUpdate`/`RollingUpdate`, default `NoUpdate`, the `Deprecated:` marker rendered into the OpenAPI schema) and is removed at the next API version boundary alongside `injectContainers`. Profile revisions are published as new profiles; see [Profile Mutability](#profile-mutability).

## Container Patches

Both sides of the model share one patch grammar, RFC 6902 JSONPatch applied to a rendered container at render time. `containerPatches` is the **profile** side: target-less entries — a cluster-scoped publisher cannot know consumer container names — each firing at every address a referencing role lists. The **role** side rides the same reference: each `targetContainers` entry both consents to the profile's patches at one named container and carries the role's own `extraPatches` for that same container, landing after the profile's.

### Patch grammar

Every patch entry sets exactly one patch-format field — `jsonPatch` is the only one today; a future format joins as a sibling field. The `jsonPatch` field carries RFC 6902, `add`-only:

- `op` must be `add` (schema-enumerated; `replace` is a reserved value, not implemented).
- `path` is an RFC 6901 pointer **relative to the target container**, restricted to element pointers — `<root>/<element>`, where `<element>` is `-` (append) or a numeric index (insert) — under exactly five allowed roots:

| root | container field | value type |
|---|---|---|
| `/command` | `command` | string |
| `/args` | `args` | string |
| `/env` | `env` | corev1.EnvVar with a unique `name` |
| `/volumeMounts` | `volumeMounts` | corev1.VolumeMount |
| `/securityContext/capabilities/add` | `capabilities.add` | string |

- `value` is the JSON value to insert, in the shape of the path's element — a string for argv and capability elements, an `EnvVar` object for `/env`, a `VolumeMount` object for `/volumeMounts`. It is accepted as arbitrary JSON at write time and unmarshalled into the concrete corev1 type at render time; a mismatched shape is a render error attributed to the patch author's object.

**The root allowlist is side-asymmetric.** Profile-side entries may address `/env`, `/volumeMounts`, and `/command/0` only. `/args` (any element), `/command/<i>` with `i > 0`, and `/securityContext/capabilities/add` are role-side-only roots: a profile-side entry addressing one is a render error attributed to the profile. The split follows where the knowledge sits — kubelet concatenates `command` and `args` into one argv, and where the split falls is the role author's convention, invisible to the publisher; `/env` and `/volumeMounts` are name-addressed and shape-free, and `/command/0` (a boundary operation on the concatenated argv) is identical under every convention, which makes it the profile side's only argv vehicle. Role-side entries keep all five roots at any index — their author wrote the shape.

### Profile-side patches

```yaml
spec:
  containerPatches:
    - jsonPatch:
        op: add
        path: /env/-
        value: {name: NCCL_DEBUG, value: INFO}
    - jsonPatch:
        op: add
        path: /volumeMounts/-
        value: {name: gpu-lib, mountPath: /usr/local/lib/gpu, readOnly: true}
    - jsonPatch:
        op: add
        path: /command/0
        value: bash
```

Multiple `/command/0` head-inserts stack with the **later** rule ending up **leftmost** in the argv, so a multi-element wrapper prefix is written in reverse order — the **reverse-order wrap convention**:

```yaml
spec:
  containerPatches:
    # final argv: [/opt/ik/wrap, --, exec, <original command...>]
    - jsonPatch: {op: add, path: /command/0, value: exec}
    - jsonPatch: {op: add, path: /command/0, value: --}
    - jsonPatch: {op: add, path: /command/0, value: /opt/ik/wrap}
```

### Consent and targets: the reference side

Patches fire only where the role consents. A reference's `targetContainers` organizes its container surface **per container**: one entry per container the reference touches, keyed by `name` — role-declared or profile-declared. Listing a name is the **consent**: the profile's `containerPatches` fire at that container, and the entry's own `extraPatches` carry the role's patch rules for that same container, landing after the profile's. Consent, address and the role's own patches sit in one place, and removing the reference leaves no residue in the role template.

```yaml
engineRuntimes:
  - profileName: nvidia-runtime-profile
    component: worker              # targetContainers requires component
    targetContainers:
      - name: inference-engine      # role-declared; listing = consent
        extraPatches:               # the role's own rules for this container
          - jsonPatch:
              op: add
              path: /securityContext/capabilities/add/-
              value: SYS_RESOURCE
```

Rules that bound the reference side:

- `targetContainers` **requires** `component` — the admission webhook's shared validation rejects the list without a named component at write time, with one exemption: under `standalonePattern` the pattern has exactly one component named after the role, so a component-less reference carrying `targetContainers` is exactly as scoped as `component: <role name>` — same effective set, same ownership. The patch dimensions always operate on the one named component; the pattern's default component set (absent `component`) is exclusively the pod-level-injection compatibility path.
- The list is a map keyed by `name`, so the apiserver rejects a container listed twice at write time.
- An entry name resolving to a container injected only by a *different* reference or profile is a render error; a name colliding with a profile-declared container that the role's own template also declares resolves to the role-owned container.
- Entries may name init containers as readily as sidecars — the grammar distinguishes targets by name, not by kind.
- **Unaimed patches**: if the reference lists no `targetContainers` while the profile declares `containerPatches`, the patches fire nowhere, the render **succeeds**, and an `UnaimedContainerPatch` warning event is recorded — a profile gaining `containerPatches` cannot break a pre-existing referencing workload.

### `component`

| pattern | `component` absent | `component` present |
|---|---|---|
| leaderWorkerPattern | `{leader}` only (main's worker-side sidecar skip) | `{component}` |
| customComponentsPattern | all declared components | `{component}` |
| standalonePattern | the single implicit component | the **role's own name** — a standalone component is named after the role, so any other name is a render error |

`component: worker` under leaderWorkerPattern is new capability, not a narrowing: it reaches a component main's default never touches. One reference names at most one component; a role consumes the same profile in several components through several references with the same `profileName`. Two references of the same profile whose effective component sets intersect are a render error (scope ownership) — the one legal mix under leaderWorkerPattern is an absent-`component` reference next to `component: worker`.

`injectContainers` is inert: the field predates this consent model, nothing reads it, its values are accepted and ignored, and it is removed at the next API version boundary.

## Using Engine Runtime Profiles

In RoleBasedGroup, reference the profile via `engineRuntimes` field. A reference that sets only `profileName` delivers the fragments to the pattern's default component set and renders byte-for-byte as before this feature; a reference that also names a `component` narrows the delivery to that component and may carry `targetContainers`:

```yaml
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata:
  name: gpu-inference
spec:
  roles:
    - name: prefill
      replicas: 2
      # component absent: fragments inject into the pattern's default
      # component set (leader only under leaderWorkerPattern)
      engineRuntimes:
        - profileName: nvidia-runtime-profile
      leaderWorkerPattern:
        size: 2
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
      # component + targetContainers: fragments deliver to the named
      # component; the profile's containerPatches fire at the listed
      # containers, then the entry's extraPatches land after them
      engineRuntimes:
        - profileName: nvidia-runtime-profile
          component: worker
          targetContainers:
            - name: decode-main
              extraPatches:
                - jsonPatch:
                    op: add
                    path: /env/-
                    value: {name: GPU_WRAPPER_SITE, value: /opt/gpu/site}
      leaderWorkerPattern:
        size: 2
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
| `profileName` | Name of the ClusterEngineRuntimeProfile to use (required) |
| `component` | Component the reference scopes to: `leader` or `worker` under leaderWorkerPattern, a declared component name under customComponentsPattern, the role's own name under standalonePattern. Absent, the fragments (and the legacy `containers` merge) deliver to the pattern's default component set — the leader only under leaderWorkerPattern, every declared component under customComponentsPattern, the single role-named component under standalonePattern. Required whenever `targetContainers` is set, except under standalonePattern, where a component-less reference carrying `targetContainers` is scoped to the role's own name |
| `targetContainers` | One entry per container this reference touches: the `name` is the consent to the profile's `containerPatches` at that container and the address of the entry's own `extraPatches`. Role-declared or profile-declared names, containers and init containers alike; requires `component` (the standalonePattern exemption above); duplicate names rejected at write time |
| `containers` | Legacy: role-provided env/args appended to profile-declared containers, within the reference's component scope; superseded by `targetContainers[].extraPatches` — see Migrating from the legacy `containers` merge below |
| `injectContainers` | Inert: accepted and ignored, nothing reads it; removed at the next API version boundary |

### EngineRuntimeTargetContainer

| Field | Description |
|-------|-------------|
| `name` | The rendered container this entry addresses (required): role-declared or profile-declared. Listing the name is the single toggle — it consents to the profile's patches at that container *and* fixes where the entry's `extraPatches` land |
| `extraPatches` | The role's own RFC 6902 patch rules for this entry's container, applied in list order after the profile's `containerPatches` — all five roots at any index. Optional, 1–32 items |

## Injection Behavior

When a role references an engine runtime profile, delivery follows the reference's scope: the named `component`, or the pattern's default component set when `component` is absent — decided per render path, i.e. which templates carry the sidecar injector:

| render path | templates receiving pod-level injection |
|---|---|
| RoleInstanceSet, standalonePattern or customComponentsPattern | every component template |
| RoleInstanceSet or LWS, leaderWorkerPattern | the leader template only (the worker chain omits the sidecar injector) |
| legacy Deployment / StatefulSet (deprecated) | the single role template |

The legacy rows, and the LWS half of the leaderWorkerPattern row, record existing behavior: those surfaces are deprecated; every new dimension is scoped to the RoleInstanceSet path; a deprecated-path render keeps main's behavior verbatim for the references it can carry. The new fields are rejected there, not given meaning — at write time via the shared validation function riding the existing webhooks, the workload-type-annotation flip included; at render for the objects the webhook-absent window admits. Under leaderWorkerPattern, `component: worker` is new capability, not a narrowing: it reaches a component the default never touches.

Application order, per rendered component template:

1. **Pod-level injection** — init containers, sidecars and volumes from the profile, append-if-missing keyed by name, per reference in `engineRuntimes` list order
2. **Legacy `containers` merge** — role-provided env/args appended to profile-declared containers, interleaved per reference (immediately after that reference's stage-1 append) within the reference's effective scope
3. **Profile-side `containerPatches`** — the profile's target-less rules fire at every listed `targetContainers` address, for every reference covering the component in `engineRuntimes` list order; profile-side roots only (`/env`, `/volumeMounts`, `/command/0`)
4. **Role-side `extraPatches`** — each entry's own rules for its container, in the same reference order, all five roots at any index, applied last — what the role writes lands where it is written

Stages 1 and 2 are interleaved per reference; the two patch passes are phased by author — stage 3 completes for every covering reference before stage 4 begins for any of them — so a later reference's profile-side insert can never land ahead of an earlier reference's role-side insert. Role patches land last: a role-side `/command/0` insert is the rendered command's index 0, and a role-side command-root insert landing ahead of a profile-side one on the same container emits an `EngineRuntimeWrapShadowed` warning event rather than failing, so the wrap shadowing is never silent.

The patch passes (stages 3–4) run **after** the built-in env injectors, which deduplicate-and-reorder env entries across every container of the template: running last keeps the patch grammar the only writer of the entries it appends. The `RBG_` env prefix belongs to the controller's own injection — a third ownership class, neither role nor profile content — and a patch entry on either side creating an `RBG_`-prefixed name is a render error; the same reservation covers a volumeMount naming the controller's `rbg-cluster-config` volume.

### Duplicate names and cross-profile collisions

- **Env names resolve by position, not by refusal.** Kubernetes applies a container's env list in order and the last same-named entry wins. A **role-side append of an existing name is the override gesture** — it lands last and wins positionally, the same override the legacy `containers[].env` merge performs. A **profile-side rule may only default**: an entry for a name the target already carries must land *ahead* of the role-owned entry via `/env/0` head insert, so the role's later entry wins; a profile-side `-` append onto a role-owned name is a render error — profile content never wins a same-name contest with role content. Two entries within one patch list creating the same env name are an error, on both sides. A **legal** insert whose name the target already carries from another source — the role template, another reference, or a different patch side — does not fail: the later entry wins positionally, and an `EngineRuntimeEnvCollision` warning event names the container, the env name, both sources, and the winner. The legacy `containers[].env` merge predates the consent model and does not emit the event.
- **volumeMounts are idempotent only under full-field equality.** A patch mount whose `(name, mountPath)` key matches an existing entry is a no-op only when the unmarshalled `VolumeMount` structs are fully equal — `readOnly`, `subPath`, `subPathExpr`, `mountPropagationMode`, `recursiveReadOnly` compared alongside the key, an absent field and an explicit zero counting as equal. A partial match is a render error attributed to the patch author's object. The same volume at a different `mountPath` is a legitimate second mount; a `mountPath` already used by a different volume name is a render error.
- **Capabilities pass through verbatim, no normalization.** The renderer holds no capability list — a well-formed unknown name passes silently and the runtime decides, exactly as for hand-written pod specs. One form hazard gets a render-time warning event attributed to the role: a name not matching the canonical uppercase form (`SYS_ADMIN`), such as lowercase or a `CAP_`-prefixed name, which containerd silently drops. A duplicate capability is an idempotent no-op.
- **Cross-profile name collisions.** When two different profiles declare the same container or volume name, the earlier reference wins and the later profile's same-named entry is skipped — a skipped container can be a control the operator believes present, so the skip is surfaced as an `EngineRuntimeNameCollision` warning event. Profile publishers should prefix container and volume names to avoid collisions across publishers.

Any stage error — an unknown target container, a duplicate env name within one list, a mismatched value shape, a profile-side entry addressing a role-side-only root — fails the whole render: the controller emits an `EngineRuntimeRenderFailed` warning event naming the owning side and index; no partially patched template is delivered.

### Migrating from the legacy `containers` merge

The legacy `containers` field remains supported unchanged: full `corev1.Container` objects whose `env` and `args` append into profile-declared containers, every other field silently ignored, within the reference's component scope. It is an imprecise grammar for what was always a patch — its `env` appends are the same override gesture a role-side `extraPatches` `/env/-` entry performs, and its `args` appends are a role-side `/args/-` entry. New role-authored customization should use `targetContainers[].extraPatches`: one grammar for every modification, precise addressing into containers *and* init containers (the legacy merge silently no-ops for init-container targets), and mounts (the legacy merge cannot deliver a volumeMount at all).

## Example: GPU Driver + Monitoring Profile

```yaml
# Step 1: Create the profile
apiVersion: workloads.x-k8s.io/v1alpha2
kind: ClusterEngineRuntimeProfile
metadata:
  name: nvidia-runtime-profile
spec:
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
  containerPatches:
    - jsonPatch:
        op: add
        path: /volumeMounts/-
        value:
          name: nvidia-lib
          mountPath: /usr/local/nvidia
          readOnly: true

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
          # component + targetContainers: fragments deliver to the worker
          # component; the profile's containerPatches fire at the listed
          # container, then the entry's extraPatches land after them
          component: worker
          targetContainers:
            - name: inference-engine
              extraPatches:
                - jsonPatch:
                    op: add
                    path: /env/-
                    value:
                      name: GPU_WRAPPER_SITE
                      value: /opt/gpu/site
      leaderWorkerPattern:
        size: 2
        template:
          spec:
            containers:
              - name: inference-engine
                image: sglang:latest
                resources:
                  limits:
                    nvidia.com/gpu: "1"
```

This results in worker pods with:
- GPU driver init container (with the NVIDIA library volume mounted)
- DCGM exporter sidecar for GPU metrics
- The `inference-engine` container mounting `nvidia-lib` through the consented profile patch, plus the role's own env entry applied after it

The leader component renders from its own template unchanged: the reference's `component` scope keeps both the fragments and the patches out of it.

## Profile Mutability

A profile is immutable while referenced. The moment any live RoleBasedGroup, or any RoleBasedGroupSet whose spec embeds the full reference list, carries an `engineRuntimes` entry naming the profile, its `spec` is frozen; labels and annotations stay editable for operational use, and create stays unrestricted (a brand-new profile has no references by construction; the webhook intercepts UPDATE and DELETE only). Delete is closed by the same admission check: the webhook intercepts DELETE and rejects it while a live reference exists, which outside the accepted interleavings below closes the delete + recreate-under-same-name bypass at its first write; inside that interleaving's window the recreate lands on a dangling reference, an outcome named below. There is deliberately no in-use finalizer beside the webhook: with `failurePolicy: Fail` registration, a down or unreachable webhook fails the write closed, and when the webhook configuration is uninstalled the freeze is off entirely — a finalizer would enforce one facet of an abandoned policy, inconsistently. The freeze lifts when the last reference is removed: the profile returns to its pre-adoption freely editable, deletable state.

A publisher who needs to change a referenced profile publishes the revision as a **new profile** — `vllm-runtime-v2` beside `vllm-runtime`; consuming roles migrate their references one at a time, each migration the role author's explicit re-consent to the new content. Nothing orchestrates the migration: the old profile keeps serving unmigrated roles, both versions run side by side as long as the fleet is heterogeneous, and the old profile becomes editable or deletable again once its last reference leaves. Keeping the old profile around is also its own rollback: restoring a reference to it is a role-side edit, subject to the same review as any other.

Enforcement is a ClusterEngineRuntimeProfile validating webhook: on update it compares `old.spec` to `new.spec` and rejects a substantive change while a live reference exists; on delete it rejects while any live reference exists. The webhook entry ships as a release artifact (the chart / `config/webhook/manifests.yaml`) applied at install time; the controller image patches existing webhook configurations (its caBundle) and never creates or restores them, so re-enabling admission after an entry removal means re-applying the configuration, not rolling the image forward alone. It consults references across RoleBasedGroups and RoleBasedGroupSets cluster-wide via an uncached lookup — profile writes are rare platform operations, so the cost is negligible, and skipping the informer cache removes the stale-cache variant of the window below. A set's group template counts as a reference even at zero replicas, no child RoleBasedGroup required. The rejection names the referencing objects and points to the new-profile workflow. Rejection from the first release, with no warning window, rests on the API stage rather than on a field survey: both served ClusterEngineRuntimeProfile versions are alpha, where the Kubernetes API conventions permit breaking changes without staging, and the profile feature has no known adoption, so no existing reference's editability is expected to change on upgrade.

Three interleavings of the reference-creation window are accepted; each needs a rare pair — a profile write and a reference creation in flight at once — to coincide inside the uncached lookup's window. First, a role creation genuinely in flight while a profile update passes the check: the update was admitted against a reference-less profile, and the role's first render reads the post-update content — a successful render, indistinguishable from a profile edited before it was ever referenced. Second, the same window against a profile DELETE: the deletion is admitted against a reference-less cluster before the referencing role is stored, and the dangling reference fails its next render exactly as a missing profile does (`EngineRuntimeRenderFailed`) — unless a profile is recreated under the same name first, in which case the dangling reference resolves against the recreated object and the role silently consumes its content, with no render failure and no drift signal (a CREATE is not a spec change on a referenced profile, and the tracking state rebaselines at the current generation). Third, riding the update window: the referencing role is stored and rendered before the update commits, so its later re-renders consume the changed content on a live reference — silent content drift, the accepted residual of the window, with the controller's warning event as the only signal. The controller backstops a missing, misconfigured, or uninstalled webhook: a spec change observed on a referenced profile emits an `EngineRuntimeProfileMutatedWhileReferenced` warning event on that profile — the signal fires on the spec-change paths the webhook did not block, evidence of the webhook's absence rather than silence; the same-name recreate above (a CREATE, not a spec change) is the watch's one blind spot among them.

## Reference Tracking

The controller maintains the reference state in the profile's `status`, as display and detection surface. Rendering never consults it — the render reads `spec` exactly as before, enforcement stays with the webhook, and status is never authoritative.

- `status.referenceCount`: the count of live references (RoleBasedGroups and RoleBasedGroupSet group templates), surfaced as a `REFERENCES` printer column so an operator sees the freeze in `kubectl get` before attempting an edit or a delete.
- `status.referencedGeneration`: the `metadata.generation` at which the profile became referenced, recorded on the none-to-referenced transition and cleared when the last reference leaves. `metadata.generation` increments only on spec changes, so a recorded baseline that no longer matches the live generation is evidence the spec changed since the baseline was recorded: a bypassed write, or a legal edit in a zero-reference window the controller never observed. The baseline is not refreshed while referenced, so the drift stays visible until the reference set drains.
- a `Frozen` condition: `True` while referenced (reason `Referenced`), flipping to reason `SpecChangedWhileReferenced` when `metadata.generation` no longer matches `referencedGeneration`.

The controller writes status only on change, avoiding churn on unrelated reconciles. A bypassed spec edit additionally emits the `EngineRuntimeProfileMutatedWhileReferenced` warning event on the profile, deduplicated on the condition-reason flip so it does not re-fire on every reconcile.

## Use Cases

- **GPU Infrastructure**: Inject NVIDIA drivers, DCGM exporters, GPU utilities
- **Observability**: Add Prometheus exporters, logging agents, tracing sidecars
- **Security**: Inject security scanners, certificate managers
- **Common Dependencies**: Shared libraries across multiple inference deployments

## Examples

- [Engine Runtime Profile Example](../../examples/basic/engine-runtime/engine-runtime-profile.yaml)