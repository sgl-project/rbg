# Agent instructions

RBG (RoleBasedGroup) is a Kubernetes operator for multi-role LLM inference workloads. Its Go module
is `sigs.k8s.io/rbgs` and its API group is `workloads.x-k8s.io`. Docs for human contributors are in
`CONTRIBUTING.md` and `doc/dev/how_to_develop.md`.

## Development and review conventions

Read [.agents/skills/rbg-conventions/SKILL.md](.agents/skills/rbg-conventions/SKILL.md), and the
reference it points to for your area, before you:

- change API types, CRDs, defaults, validation, webhooks or conversion
- change anything the controller renders or writes
- touch the Helm chart, manifests, RBAC or codegen
- review an RBG pull request

The skill uses the Agent Skills format. Agents that load skills from `.agents/skills/` pick it up
automatically; others should open the file directly.

The rule that matters most: upgrading the controller must not change the rendered templates or
revision hashes of existing objects that don't use a new feature. Otherwise every existing
workload rolls on upgrade.

## Reviewing pull requests

- Review independently first: write down your own findings before you read other reviews,
  including Copilot's.
- Then compare: judge every other reviewer's point on its diagnosis, severity and proposed fix,
  with evidence, not by who raised it.
- Test whatever reading can't settle, against real behavior, before you assert it.

The details are in the skill's `references/review-playbook.md`.

## Verifying on a cluster

Changes to runtime behavior (controller, rendering, webhooks, chart, upgrade) are verified on the
remote cluster in your kubeconfig with kubectl before anyone calls them ready to merge; don't set
up a local cluster for this. The skill ships kubectl checks in
`.agents/skills/rbg-conventions/scripts/`, described in its `references/verification-cases.md`.
Clusters are often shared, with other people and with other agent sessions:

- Run `.agents/skills/rbg-conventions/scripts/cluster-preflight.sh` first. If it finds anything,
  stop and agree with its owner.
- Work in your own namespace, and clean it up while your controller still runs.
- Scope any controller you run to your namespace, and never start one next to an unscoped one.
- Never delete the CRDs, and never run `make test-e2e-upgrade` on a shared cluster.

## Other skills

- `.agents/skills/stress-test/`: controller load tests with KWOK, pprof and an analysis report.

## Basics

- After API changes, run `make manifests generate`. Before pushing, run `make fmt vet lint test`.
- Sign off every commit (`git commit -s`). New Go files need the Apache 2.0 header; check with
  `make copyright-check`.
- Write issues, pull requests, code comments and GitHub review comments in English.
