# Review playbook

## 1. Gather context before judging

- Fetch the PR, its head SHA and base, CI status, and **all existing threads, including Copilot's
  automatic review**. Don't duplicate a point that has already been raised; confirm or refute it
  in the existing thread, with evidence.
- Read the linked issue or KEP, and check the PR against it in both directions: is anything the KEP
  promises missing, and is there scope the KEP doesn't cover?
- Use the task checklists in `SKILL.md` to decide which reference applies, such as API field,
  rendering, write path, chart, or docs.

## 2. Verify, then assert

- **Reproduce every blocking finding**, ideally with a unit test or envtest run side by side against
  the base `main`, or on a real cluster when kubelet, scheduler or GC behavior matters.
- **Label each finding with its evidence level:** cluster, envtest, unit test, or code reading only.
  Findings backed only by code reading should be phrased as questions or risks, not as facts.
- **Quantify where you can:** reconcile counts, latency, or the number of affected objects in an
  A/B comparison. Put reproduction tests or proof-of-concept fixes in a collapsed block the author
  can reuse.
- **Correct your own mistakes openly** in the next round: retract or downgrade them.

## 3. Severity

| Level | Use for |
|---|---|
| 🔴 Blocking (MUST) | correctness, data or state loss, mass rollout or disruption on upgrade, API or compatibility break, security, CI-breaking codegen |
| 🟡 Should fix (SHOULD) | real defect with limited blast radius, missing regression test, misleading docs, non-trivial perf cost |
| ⚪ Nit | naming, wording, style; batch these into the summary |

Keep the scale consistent across rounds of the same PR.

## 4. Output format

1. **Start with a one-line verdict:** Approve, Comment, or Request changes, plus the main reason.
2. **Findings table:** `#` | severity | `file:line` | problem → concrete fix | evidence level.
3. **"Verified / what's solid":** what you checked and found correct, so the author knows what not
   to touch.
4. **Keep blocking items and follow-ups apart.** Pre-existing bugs and scope creep go to new issues,
   not into extra rounds on this PR.
5. **On a re-review, map every earlier item** to ✅ fixed, ↩️ retracted, ⬇️ downgraded, or ⏳ still
   open.
6. **Say what you did not run or verify.**

## 5. Posting

- **Draft first.** Never post, approve, request changes or merge without an explicit instruction
  from the user who asked for the review.
- **Language and wording:**
  - Write GitHub comments in English, which is the repository convention.
  - Reply to the requester in their own language.
  - Refer to environments neutrally ("on a Kubernetes cluster"), without vendor or internal names.
- **Inline comments:**
  - Anchor them only on lines inside the diff hunks, pinned to the head SHA.
  - Check for an existing pending review from the same account first.
  - Prefer a few high-signal inline comments plus one summary over many small threads.

## 6. Merge readiness on `main`

The branch ruleset on `main` requires:

- squash merge only
- at least 1 approval, including a code-owner review
- one extra approval for pull requests that Copilot opened without attribution to a person
- an approval that comes after the last push
- every review thread resolved

Stale reviews are **not** dismissed on push. Before saying "ready", check:

- [ ] CI is green on the current head SHA, and the head hasn't moved since your review.
- [ ] No unresolved threads, and no outstanding `CHANGES_REQUESTED` review. These are not dismissed
      automatically.
- [ ] The approval postdates the last push, and the code-owner review is present.
- [ ] After a force-push, `git range-diff` shows only the expected changes.
- [ ] The PR builds and its tests pass when merged onto the latest `main`.
- [ ] Commits carry a DCO sign-off, and the PR template is filled in accurately.
- [ ] `/release-test` has run for chart, image or release-manifest changes. It is informational, so
      re-run it after new pushes.
- [ ] Never bypass the rules with an admin merge.

## 7. Docs and KEP quality

- **Check every claim against `main` and cite `file:line`.** Watch for wrong API groups (it is
  `workloads.x-k8s.io`), functions that don't exist, and contradictions with earlier KEPs (#548).
- **The PR description must match the current head.** Look for an empty body, a `fixes #XXXX`
  placeholder, or a wrong docs checkbox.
- **KEPs need these sections:**
  - Upgrade / Downgrade Strategy, Risks, Non-Goals, User Stories, Graduation Criteria,
    Implementation History, and scalability.
  - Explicit state-transition tables for any state machine (#528, #548).
  - Split out backends that aren't supported yet.
- **Keep docs bilingual and code in English:**
  - Keep `README.md` and `README-zh_CN.md` in sync, and the zh copy of a KEP if there is one.
  - Write code comments, CRD descriptions and `kep.yaml` in English (#510, #528).
- **Record behavior changes in the release note or squash message without overstating them,**
  especially what happens to objects that existed before the upgrade (#478).
