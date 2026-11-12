# API, validation and admission

## Versions and deprecations

- **Group and storage version.** The group is `workloads.x-k8s.io`.
  - RoleBasedGroup, RoleBasedGroupSet, RoleBasedGroupScalingAdapter and ClusterEngineRuntimeProfile
    have both versions, and `v1alpha2` is their storage version.
  - The legacy `Instance` and `InstanceSet` are separate, v1alpha1-only CRDs, replaced by
    RoleInstance and RoleInstanceSet.
  - New fields go only into v1alpha2 types.
- **v1alpha1 is deprecated.** Its kinds carry `+kubebuilder:deprecatedversion` warnings. Keep it
  round-trippable, but give it no new features.
- **RoleInstanceSet is the workload for new features.** Deployment, StatefulSet and LeaderWorkerSet
  role types are deprecated, behind the 0.9.0 compatibility gate (#524) and the
  `--enable-deprecated-workload-types` flag. Fix them, don't extend them.
- **Minimum Kubernetes is 1.28** (`doc/install.md`). Design and test validation for that floor.

## Adding or changing a field

- **A new optional field under a parent that existing objects already store** is `+optional` with
  `omitempty`, and has no schema default; default it in a Go getter.
  - Its type is a `*T`, or a string, slice or map whose empty value means unset (like
    `activateAfter`).
  - Fields that ship together with a *new* optional struct may be value types or carry defaults,
    because nothing materializes until a user sets the parent.
  - Required fields need a case-by-case upgrade design.

  See `upgrade-compatibility.md`.
- **Keep the API minimal.** Add a field only when a real consumer makes a decision with it. Adding a
  field later is compatible; removing one is not (#528). The same goes for conditions, phases and
  states: don't add entities nobody needs yet.
- **Make values unambiguous.** An unknown value must fail validation; it must not silently do
  nothing. In #548, an unknown `component` was silently ignored.
- **Deprecate fields that were documented but never implemented**, rather than leaving them in the
  docs (#548).
- **Prefer intuitive user-facing YAML.** A typed struct per target (`name` plus what to change) reads
  better than parallel lists or opaque blobs (#548).
- **Don't reuse an existing label or annotation key with a new meaning**
  (rolebasedgroup/inference-engine-runtime#10).
- **Protect controller-owned annotations with validation if they can't be recovered after a user
  deletes them** (#513).
- **Prefer controller logic over a mutating webhook.** A mutating webhook needs a reason the
  controller can't satisfy. It also changes stored specs, and with them revision hashes.

## Validation

- **Never tighten validation that stored objects may already violate.** CRD validation ratcheting is
  on by default only from Kubernetes 1.30. On 1.28 and 1.29, a new `MaxItems` or pattern rejects
  *every* update to an existing violating object, even a label edit. RoleBasedGroupSet embeds the RBG
  spec, so it is hit as well (#548).
- **Mind CEL cost.** CEL cost estimates multiply by list bounds, and unbounded lists blow the budget.
  Prefer OpenAPI rules (`pattern`, `maxLength`, `+listType=set|map`) to CEL. If you must add a
  bound, make it generous (precedent: `MaxItems=1024` in `rolebasedgroupwarmup_types.go`) and call
  it out as an API change for all users (#548).
- **CRD CEL rules can't read labels or annotations.** Only `metadata.name` and
  `metadata.generateName` are visible. ValidatingAdmissionPolicy needs 1.30 (#513).
- **Test validation at the version floor.** `make test-envtest` and CI default to Kubernetes 1.31
  (`ENVTEST_K8S_VERSION`). For validation that depends on the floor, also run
  `ENVTEST_K8S_VERSION=1.28.x make test-envtest`.

## Conversion (v1alpha1 ↔ v1alpha2)

- **Prove the round trip is lossless.** Run
  `kubectl get <kind>.v1alpha1.workloads.x-k8s.io <name> -o json | kubectl replace -f -` on an
  unchanged object. The v1alpha2 object, its revisions and its children must not change. In
  #474/#491, dropped template metadata caused a new revision and recreated the children.
- **Don't materialize defaults during conversion.** Converting v1alpha2 to v1alpha1 uses
  `GetRawRestartPolicyType`. Converting v1alpha1 to v1alpha2 still materializes
  `restartPolicyConfig`, so writes made through v1alpha1 change the stored shape; see
  `upgrade-compatibility.md`. Test the full v1alpha2 → v1alpha1 → v1alpha2 round trip for any new
  field with `scripts/v1alpha1-roundtrip.sh`.
- **Know what a v1alpha1 round trip does on `main` today** (measured, VC-5 in
  [verification-cases.md](verification-cases.md)). When the API server accepts a no-op read and
  write through v1alpha1, the write:
  - adds the `role-workload-type` annotation to every role, so every role hash changes
  - replaces the deprecated `restartPolicy` string with a defaulted `restartPolicyConfig`
  - resets configured backoff delays to the defaults, because v1alpha1 has no field for them

  For customComponentsPattern roles the write is rejected instead: the converted object fails
  v1alpha1 validation, so v1alpha1 clients can't update those RBGs at all. Don't claim a lossless
  round trip for new work until VC-5 passes.
- **Every served version needs a working conversion.** A version served with `strategy: None` breaks
  as soon as the schemas diverge (#548).
- **Immutability checks must account for converted writes** (#473).
- **SSA through v1alpha1 needs the list-map keys** (#517).
- **Since v1alpha1 is deprecated,** a "write through v1alpha2" release note often beats new
  conversion code.

## Admission webhooks

- **On UPDATE, reject only what the update newly introduces.** Compare old and new. Validators also
  see finalizer-only and label-only patches, so never re-reject pre-existing state (#473). Update
  exemptions must match exactly, e.g. `oldType == wt` (#524).
- **Enforce replica invariants in the controller.** RoleBasedGroupSet, RoleBasedGroupScalingAdapter
  and RoleInstanceSet expose `/scale`. Writes through it (HPA, `kubectl scale`) don't reach webhooks
  registered only for the main resource, so clamp in the controller, as StatefulSet does (#474).
- **Know which requests each webhook sees.**
  - The v1alpha2 RoleBasedGroup and RoleBasedGroupSet webhooks use the default
    `matchPolicy: Equivalent`, so they also see requests made through those resources' v1alpha1
    version, after conversion. Put shared validation there, once.
  - `Equivalent` doesn't bridge different resources: `instancesets` (v1alpha1) and
    `roleinstancesets` (v1alpha2) are separate CRDs.
  - The v1alpha1 deprecation gates (`api/workloads/v1alpha1/deprecation_webhook.go`) deliberately
    use `matchPolicy: Exact`, so they fire only for requests made in v1alpha1.
  - Before adding a webhook, check whether an existing webhook or the CRD's `deprecationWarning`
    already covers the case. Duplicates drift apart, and the user only sees the first rejection
    (#552).
- **Use `failurePolicy: Fail` for gates that must hold, and `Ignore` for warn-only webhooks.** A
  `Fail` webhook on a path the running image doesn't serve blocks all writes (#524, #552).
- **Validate statically at admission** (#528). The controller must still fail safely on invalid
  objects: emit an event or condition, skip the role, and don't hot-loop. Admission can be
  bypassed, for example with `--enable-webhooks=none`, which is a local-debugging mode that turns
  off conversion and admission (#513).

## Status and conditions

- **Keep `Ready` meaning "all ready".** RoleBasedGroupSet `readyReplicas` relies on it. For a new
  meaning, add a new condition (e.g. `Available`) instead of redefining `Ready` (#474/#491).
- **Status must be rebuildable from observation.** Don't use phases that remember past reconciles,
  and don't store contracts in status; backup/restore drops status (#528, #473).
- **Early returns must not flip healthy objects to `Ready=False`** (#510).
- **Condition `reason` must match `^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$`.** Never put raw
  resource names in it, e.g. `OutOf` + `nvidia.com/gpu`. One bad reason fails the whole status
  write.
- **No counters or timestamps in condition messages** that change every reconcile. They trigger
  reconcile storms in parents.
- **Set `observedGeneration` on conditions, and key latches on generation, not time** (#542).
- **New status fields must be added to every status writer,** including the status SSA
  constructor. See `controller-patterns.md`.

## Codegen markers

- **`+kubebuilder:default` belongs only on CRD types.** In #532, `// +default=TCP` on a non-CRD type
  broke controller-gen, and with it lint, unit, envtest and the image build.
- **Keep `patchStrategy`/`patchMergeKey` struct tags.** Strategic merge patch reads them at runtime,
  and revisions are applied with SMP. openapi-gen requires markers that match the tags (#549).
- **A type without `+genclient` gets no typed client** (rolebasedgroup/client-go#1).
- **`+listType` and `+listMapKey` define server-side apply merge semantics.** Changing them is an API
  change.
