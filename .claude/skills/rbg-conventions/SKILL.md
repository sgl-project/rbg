---
name: rbg-conventions
description: Development and code-review conventions for the RBG (RoleBasedGroup) operator, distilled from past PR reviews. Use when adding or changing API fields or CRDs (v1alpha1/v1alpha2), defaults, validation, webhooks or conversion; when changing what the controller renders or writes (RoleInstanceSet or pod templates, labels, annotations, env, status, revision hashes); when touching the Helm chart, manifests, RBAC or codegen; when writing tests or KEPs; and when reviewing an RBG pull request or judging merge readiness.
---

# RBG development and review conventions

Every rule here was missed at least once in a past RBG review. Before writing or reviewing code in
an area, read the matching file in `references/`.

## Facts that are easy to get wrong

- The API group is `workloads.x-k8s.io`, and labels and annotations use the
  `rbg.workloads.x-k8s.io/` prefix.
- New fields go only into v1alpha2 types. v1alpha2 is the storage version of the kinds that have
  both versions (RoleBasedGroup, RoleBasedGroupSet, RoleBasedGroupScalingAdapter,
  ClusterEngineRuntimeProfile). `v1alpha1` is deprecated: keep it round-trippable and add nothing to
  it.
- New features target RoleInstanceSet. Deployment, StatefulSet and LeaderWorkerSet roles are
  deprecated, behind the 0.9.0 gate and `--enable-deprecated-workload-types`. They get fixes only,
  and upgrades must not disturb them.
- The minimum Kubernetes version is 1.28. That means no CRD validation ratcheting by default and no
  ValidatingAdmissionPolicy. Envtest and CI default to 1.31.
- Admission can be bypassed: `--enable-webhooks=none` is a local-debugging mode. The controller
  must fail safely on invalid objects.
- Restarting an inference role is expensive (model reload, GPU rescheduling). An unintended rollout
  is an outage, not noise.

## Golden rules

1. **A controller upgrade changes nothing for objects that don't use the new feature.** Rendered
   templates and revision hashes stay identical, no Pod restarts, no Service rewrites. The only
   exception is a deliberate one-hop migration that maintainers agreed to, that is documented, and
   that is recorded in `test/e2e/upgrade/profile.go`. See
   [upgrade-compatibility.md](references/upgrade-compatibility.md).
2. **A new optional field under an already-stored parent is a `*T` with `omitempty` and no schema
   default.** Apply defaults in Go getters. Write the field into templates only when the user set
   it.
3. **Never reinterpret an unset field for existing objects** unless it is a recorded migration as
   in rule 1. New behavior is opt-in, and tightening goes warn first, then deny.
4. **Never tighten validation that stored objects may already violate.**
5. **Pin content read from referenced objects:** snapshot it, put it into the revision, or keep it
   immutable while referenced. Never re-render it silently from the live object.
6. **One writer style per set of fields.** Fields the controller renders through server-side apply
   are never also written with a typed Create, Update or merge patch. Elsewhere, follow the object's
   existing writer.
7. **Preconditions must not trip on the controller's own writes.**
8. **Isolate failures and keep load flat.** Unrelated roles can't block each other (dependency order
   still applies), no broad requeues, and events fire only on transitions.
9. **`Ready` keeps its meaning, and status can be rebuilt from observation.**
10. **The chart, `config/`, `deploy/kubectl` and generated code move together.** Flipping a chart
    default changes behavior at upgrade time.
11. **Every fix has a regression test that fails on the base branch.** Prove API-server behavior in
    envtest against `main`.
12. **Verify before asserting.** Cite `file:line`, and make docs and KEPs match `main`.

## Checklists

### Adding or changing an API field

- [ ] The field is in v1alpha2 only. If its parent is already stored, it is `*T` +
      `json:"x,omitempty"` + `+optional`.
- [ ] No `+kubebuilder:default` on a field whose parent already exists in stored objects. The API
      server applies defaults on read. Defaults inside a brand-new optional struct are fine.
- [ ] The default lives in a Go getter. Add a `GetRaw*` getter if callers must tell "set" from
      "defaulted".
- [ ] When the field is unset, existing objects render byte-identical templates and keep their
      revision hashes. Prove it with a test.
- [ ] New validation can't reject objects that are already stored.
- [ ] CEL rules are bounded.
- [ ] Unknown values fail validation instead of being ignored.
- [ ] Conversion doesn't materialize defaults, and a v1alpha2 → v1alpha1 → v1alpha2 round trip
      leaves the stored object unchanged. Test it; today v1alpha1 → v1alpha2 materializes
      `restartPolicyConfig`.
- [ ] `make manifests generate` has been run, the chart and kubectl manifests are updated, and new
      verbs have RBAC markers.
- [ ] The docs (`README.md` and `README-zh_CN.md`) and the release note describe what happens to
      existing objects.

### Changing what the controller renders or writes

- [ ] For RBGs that don't use the change, nothing new appears in `spec.roleInstanceTemplate` or in
      pod templates (fields, labels, env, sidecars, injected scheduling fields), and Services are
      unchanged. Never stamp existing templates; stamp Pods when they are created.
- [ ] Unset fields keep their old interpretation.
- [ ] No rendering from live referenced objects that are neither watched nor hashed.
- [ ] Fields rendered through SSA applyconfigurations have no second writer, and new status fields
      reach every writer of that status.
- [ ] Services, StatefulSets and Deployments you create carry `rbg.workloads.x-k8s.io/group-name`.
      Without it the cache hides them.
- [ ] Every new input is watched. `RBGPredicate` only enqueues RBG spec changes.
- [ ] Unrelated roles stay isolated (dependency order still applies), there is no new periodic
      requeue, and events fire only on transitions.
- [ ] Upgrade safety is shown by a hash comparison (unit test or envtest), a binary swap, or
      `make test-e2e-upgrade` pointed at your build: `RBGS_TO_REPO`/`RBGS_TO_TAG`, plus
      `RBGS_CRD_UPGRADE_REPO` if the CRD upgrader changed.

### Chart, manifests and release plumbing

- [ ] The Helm chart, `config/` and `deploy/kubectl/manifests.yaml` agree. Webhook configurations are
      hand-maintained in the chart.
- [ ] Default flips are guarded, for example by a pre-upgrade check, and install paths without hooks
      stay safe.
- [ ] Images are bumped everywhere together, and the window with old and new versions side by side is
      considered.
- [ ] `make test-chart` and `helm lint` pass, and a maintainer has run `/release-test`.

### Reviewing a pull request

- [ ] Read the existing threads and Copilot's review first, and don't duplicate them.
- [ ] Walk the checklists above for every area the diff touches.
- [ ] Reproduce blocking findings with envtest A/B against `main` or on a cluster, and state the
      evidence level of each finding.
- [ ] Write a verdict line, a findings table with `file:line` and a concrete fix, what you verified,
      and what you didn't run.
- [ ] Keep it a draft. Post, approve or merge only on explicit instruction, and write GitHub comments
      in English.

## References

| File | Read when |
|---|---|
| [upgrade-compatibility.md](references/upgrade-compatibility.md) | any change to a field, default, rendering, conversion or controller default; anything that could roll existing workloads |
| [api-and-admission.md](references/api-and-admission.md) | API shape, validation and CEL, conversion, webhooks, status and conditions, codegen markers |
| [controller-patterns.md](references/controller-patterns.md) | reconciler write paths, SSA and managedFields, preconditions, requeues, events, adoption, watches, cache |
| [build-test-release.md](references/build-test-release.md) | codegen, Helm chart, manifests, RBAC, make targets and CI, test expectations, commits and PRs |
| [review-playbook.md](references/review-playbook.md) | reviewing a PR or KEP, judging merge readiness, posting comments |

## Maintaining this skill

- Add a rule only when it applies beyond one PR, and cite that PR (`#NNN`) as evidence.
- Point to files and functions on `main`, not line numbers, and re-check the pointers when code
  moves.
- Keep this file short. Detail belongs in `references/`.
