# Review playbook

Review in this order: form your own view, then compare it with every other review, then test
whatever reading alone can't settle. Reading other reviews first anchors you to their framing: you
end up confirming or duplicating their points and missing what they missed.

Sometimes you have seen another review before you start: you were asked whether it is right, or you
are answering comments on your own PR. Then read only the points you were given, not the rest of the
discussion, and form your own view of the code they touch, and of the rest of the diff, before you
judge them.

## 1. Gather the inputs, not the opinions

- Fetch the PR, its head SHA and base, and CI status.
- Read the PR description and the linked issue or KEP, and check the PR against them in both
  directions: is anything the KEP promises missing, and is there scope the KEP doesn't cover?
- Use the task checklists in `SKILL.md` to decide which reference applies, such as API field,
  rendering, write path, chart, or docs.
- Don't open the review threads yet: not the maintainers', not Copilot's or other bots', and not the
  author's replies to them.

## 2. Review independently first

- **Form your own findings from the diff and the code around it,** using the checklists, before you
  read anyone else's review.
- **Record them before you read other reviews,** each with its evidence and how sure you are: in your
  notes, a draft review or your working output, but never in a repository file. Step 3 compares
  against this record.
- **Mark each finding settled or open.** A finding is settled only when you are sure of it from what
  you have read. Anything you aren't sure about is open, whatever the reason: the code path itself,
  language or library semantics (Go, bash, jq), concurrency and timing, the API server (defaulting,
  conversion, server-side apply, admission, validation), caches, kubelet, the scheduler or garbage
  collection, or the output of an external tool. Step 4 tests the open ones.
- **On a re-review,** read the new diff yourself before the author's replies and the other
  reviewers' new comments.

## 3. Compare with every other review, critically

Now read every other review: maintainers and contributors, Copilot and other bots, the author's
replies, and your own earlier rounds. The goal is to get it right, not to agree.

- **Judge each of their points on three separate questions,** answering each with agree, partly,
  disagree or open, plus your reason:
  - Diagnosis: is the problem real?
  - Severity: is it as serious as they say?
  - Fix: does the proposed fix solve it without new harm? A correct diagnosis can come with a
    harmful fix. In #568, Copilot rightly found that a retry helper could replay writes, but its fix,
    retrying only reads, would also have stopped retrying a cleanup delete that failed before
    reaching the API server, leaving a cluster-scoped CRD behind on a flaky connection. The adopted
    fix retries a write only when the error proves that it never reached the API server.
- **For points only they found,** check them like your own, and work out why you missed them. The
  same blind spot may hide more problems elsewhere in the diff. In #568, the snapshot script ignored
  init containers, and the author's own reviews had missed it.
- **For points only you found,** check them again before you post them. Being alone on a point is a
  reason to verify it, not to drop it.
- **Before you reject a point,** state its strongest form, then reject it with evidence. Treat
  pushback on your own findings the same way.
- **Settle disagreements with evidence** from step 4, never by majority, seniority, or because a bot
  or a maintainer said so.
- **When a point was already raised,** add your evidence to the existing thread instead of opening a
  duplicate.

The same applies when you address review comments on your own PR: evaluate each comment this way,
and push back with evidence where it is wrong.

## 4. Test what reading can't settle

- **Test every open finding, whatever its severity and whoever raised it,** before you present it as
  a fact. In #538, a first review round listed a managedFields concern only as an awareness item.
  Running the PR side by side with `main` in envtest in the next round showed a real regression, and
  it became a MUST.
- **Use the cheapest test that can decide the question:** a direct reproduction with the real command
  or tool, a unit test, envtest side by side with the base `main` for API-server behavior, or a real
  cluster when kubelet, scheduler, GC or networking behavior matters. The `scripts/` directory and
  [verification-cases.md](verification-cases.md) have ready-made cluster checks.
- **Test against real behavior.** Capture what the tool or the API server actually returns instead
  of writing what you expect it to return. In #568, an offline test used an error message kubectl
  never prints for a refused connection, so it passed while the retry it covered was broken. An
  independent reviewer caught it by running the real kubectl against a closed port.
- **Make sure the test can fail:** show it failing on the broken code or the base branch before you
  trust a pass, as the negative control VC-3 does.
- **If you can't test it,** say so, name the test that would settle it, and phrase the point as a
  question or a risk, not as a fact.
- **Label each finding with its evidence level:** cluster, envtest, unit test, direct reproduction
  with the real tool, or code reading only.
- **Quantify where you can:** reconcile counts, latency, or the number of affected objects in an
  A/B comparison. Put reproduction tests or proof-of-concept fixes in a collapsed block the author
  can reuse.
- **Re-test after the fix,** and correct your own mistakes openly in the next round: retract or
  downgrade them.

## 5. Severity

| Level | Use for |
|---|---|
| 🔴 Blocking (MUST) | correctness, data or state loss, mass rollout or disruption on upgrade, API or compatibility break, security, CI-breaking codegen |
| 🟡 Should fix (SHOULD) | real defect with limited blast radius, missing regression test, misleading docs, non-trivial perf cost |
| ⚪ Nit | naming, wording, style; batch these into the summary |

Keep the scale consistent across rounds of the same PR.

## 6. Output format

1. **Start with a one-line verdict:** Approve, Comment, or Request changes, plus the main reason.
2. **Findings table:** `#` | severity | `file:line` | problem → concrete fix | evidence level. Mark
   findings you adopted from another review as such.
3. **Other reviewers' points:** for each one, your verdict on its diagnosis, severity and fix
   (agree, partly, disagree, or open) and the evidence behind it.
4. **"Verified / what's solid":** what you checked and found correct, so the author knows what not
   to touch.
5. **Keep blocking items and follow-ups apart.** Pre-existing bugs and scope creep go to new issues,
   not into extra rounds on this PR.
6. **On a re-review, map every earlier item** to ✅ fixed, ↩️ retracted, ⬇️ downgraded, or ⏳ still
   open.
7. **Say what you did not run or verify.**

## 7. Posting

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

## 8. Merge readiness on `main`

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

## 9. Docs and KEP quality

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
