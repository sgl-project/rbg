#!/usr/bin/env bash
# re-verify.sh — verification harness for PR #403 (Helm chart template reorg).
#
# Part of the `review-finding-verifier` skill (stage ②). This PR is a pure
# reorganization of the rbgs Helm chart into a kubebuilder-style layout; NO
# production-code bug was found. The harness is therefore *regression evidence*:
# every check is a CONTRACT test (asserts the intended, correct state) and must
# STAY GREEN. "all green" == the move is still behavior-preserving and safe.
#
# It is intentionally standalone (helm + git + python3 + grep) rather than the
# skill's Go/ginkgo re-verify.sh, because there is no Go code to exercise. It
# keeps the same CLI contract so it drops into the pipeline resume flow:
#   - no <ref>  -> resolve the current PR head from manifest.pr (machine-indep.)
#   - exit 0 iff every finding passes.
#
# Usage:
#   bash re-verify.sh [<ref>] [--manifest <path>]
#
#   <ref>        chart ref to verify (default: current PR head from manifest.pr).
#   --manifest   path to verify-manifest.json (default: alongside this script).
#
# Requires: git, helm (v3), python3. Does NOT modify the current working tree —
# it renders through throwaway detached worktrees.
set -euo pipefail

CHART_SUBPATH="deploy/helm/rbgs"
UPSTREAM_URL="https://github.com/sgl-project/rbg.git"

REF=""; MANIFEST=""
while [ $# -gt 0 ]; do
  case "$1" in
    --manifest) MANIFEST="$2"; shift 2;;
    -h|--help)  sed -n '2,30p' "$0"; exit 0;;
    *) [ -z "$REF" ] && REF="$1" && shift || { echo "unexpected arg: $1" >&2; exit 2; };;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOCS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
[ -n "$MANIFEST" ] || MANIFEST="$DOCS_DIR/verify-manifest.json"
[ -f "$MANIFEST" ] || { echo "re-verify: manifest not found ($MANIFEST)" >&2; exit 2; }
for t in git helm python3; do command -v "$t" >/dev/null || { echo "re-verify: '$t' required" >&2; exit 2; }; done

REPO_ROOT="$(git rev-parse --show-toplevel)"
RESULTS_DIR="$DOCS_DIR/results"; mkdir -p "$RESULTS_DIR"

jqget() { python3 -c "import json,sys;print(json.load(open('$MANIFEST')).get('$1',''))"; }

# ---- resolve the ref under test -------------------------------------------------
PR_URL="$(jqget pr)"
if [ -z "$REF" ]; then
  [ -n "$PR_URL" ] || { echo "re-verify: no <ref> and manifest.pr is empty" >&2; exit 2; }
  num="$(printf '%s' "$PR_URL" | sed -E 's#.*/pull/([0-9]+).*#\1#')"
  echo "re-verify: resolving PR head via 'git fetch $UPSTREAM_URL pull/$num/head'"
  git fetch --quiet "$UPSTREAM_URL" "pull/$num/head"
  REF="$(git rev-parse FETCH_HEAD)"
fi
REF="$(git rev-parse "$REF")"
echo "re-verify: ref under test = ${REF:0:12}"

# ---- resolve the render baseline (merge-base with upstream main) ----------------
git fetch --quiet "$UPSTREAM_URL" main
BASE="$(git merge-base FETCH_HEAD "$REF")"
echo "re-verify: render baseline (merge-base w/ upstream main) = ${BASE:0:12}"

# ---- last-reviewed marker (incremental review delta) ----------------------------
LAST_REVIEWED_FILE="$DOCS_DIR/.last-reviewed"
if [ -s "$LAST_REVIEWED_FILE" ]; then LAST_REVIEWED="$(tr -d '[:space:]' < "$LAST_REVIEWED_FILE")"; else LAST_REVIEWED="$BASE"; fi
echo "re-verify: last-reviewed = ${LAST_REVIEWED:0:12}  (review delta = ${LAST_REVIEWED:0:12}..${REF:0:12})"

# ---- throwaway worktrees for BASE and REF ---------------------------------------
WT_BASE="$(mktemp -d)"; WT_REF="$(mktemp -d)"
cleanup() {
  git -C "$REPO_ROOT" worktree remove --force "$WT_BASE" 2>/dev/null || true
  git -C "$REPO_ROOT" worktree remove --force "$WT_REF"  2>/dev/null || true
  git -C "$REPO_ROOT" worktree prune 2>/dev/null || true
}
trap cleanup EXIT
git -C "$REPO_ROOT" worktree add -q --detach "$WT_BASE" "$BASE"
git -C "$REPO_ROOT" worktree add -q --detach "$WT_REF"  "$REF"

# normalize a helm render: drop "# Source:" path comments + blank lines, sort docs.
# The path comments are the ONLY thing a pure move is allowed to change, so we strip
# them; anything else differing means the move altered a deployed object.
normalize() {
  python3 - "$1" <<'PY'
import sys,re
docs=re.split(r'^---\s*$', open(sys.argv[1]).read(), flags=re.M)
out=[]
for d in docs:
    lines=[l for l in d.splitlines() if not l.strip().startswith('# Source:') and l.strip()!='']
    if lines: out.append('\n'.join(lines).strip())
out.sort()
sys.stdout.write('\n===DOC===\n'.join(out))
PY
}

pass=(); fail=()
record() { # id  pass|fail  detail
  printf '%-4s %-6s %s\n' "$1" "$2" "$3"
  if [ "$2" = "pass" ]; then pass+=("$1"); else fail+=("$1"); fi
}

echo; echo "================  CHECKS  ================"

# ---- F1: render-equivalence (main vs PR head) -----------------------------------
R_BASE="$RESULTS_DIR/render-base.yaml"; R_REF="$RESULTS_DIR/render-ref.yaml"
helm template rbgs "$WT_BASE/$CHART_SUBPATH" --namespace rbgs-system > "$R_BASE" 2>"$RESULTS_DIR/render-base.err" || true
helm template rbgs "$WT_REF/$CHART_SUBPATH"  --namespace rbgs-system > "$R_REF"  2>"$RESULTS_DIR/render-ref.err"  || true
normalize "$R_BASE" > "$RESULTS_DIR/norm-base.yaml"
normalize "$R_REF"  > "$RESULTS_DIR/norm-ref.yaml"
nb="$(grep -c '^kind:' "$R_BASE" || true)"; nr="$(grep -c '^kind:' "$R_REF" || true)"
if [ -s "$R_REF" ] && diff -q "$RESULTS_DIR/norm-base.yaml" "$RESULTS_DIR/norm-ref.yaml" >/dev/null; then
  record F1 pass "rendered manifests identical (base=$nb objs, ref=$nr objs)"
else
  { echo "### render diff (base vs ref):"; diff "$RESULTS_DIR/norm-base.yaml" "$RESULTS_DIR/norm-ref.yaml" || true; } > "$RESULTS_DIR/render-equivalence.txt"
  record F1 fail "rendered manifests DIFFER (base=$nb objs, ref=$nr objs) — see results/render-equivalence.txt"
fi

# ---- F2: helm lint --------------------------------------------------------------
if helm lint "$WT_REF/$CHART_SUBPATH" > "$RESULTS_DIR/helm-lint.txt" 2>&1; then
  record F2 pass "helm lint: $(grep -oE '[0-9]+ chart\(s\) linted, [0-9]+ chart\(s\) failed' "$RESULTS_DIR/helm-lint.txt" | tail -1)"
else
  record F2 fail "helm lint failed — see results/helm-lint.txt"
fi

# ---- F3: no stale references to old (pre-reorg) helm/kustomize file names --------
# Old flat helm filenames (outside the chart templates dir) AND old config/rbac
# names (secret_role*, renamed to cert_role* in the config/rbac source) must not
# be referenced anywhere. Both surfaces were touched by this PR.
OLD_HELM='templates/(clusterrole\.yaml|manager\.yaml|secret-role|rolebinding\.yaml|serviceaccount\.yaml|webhook-service|upgrade/)'
OLD_KUSTOMIZE='secret_role(_binding)?\.yaml|secret_role\b'
STALE_HELM="$(git -C "$WT_REF" grep -nE "$OLD_HELM" -- . ":!$CHART_SUBPATH/templates" 2>/dev/null || true)"
STALE_KZ="$(git -C "$WT_REF" grep -nE "$OLD_KUSTOMIZE" -- . ":!docs/verification" 2>/dev/null || true)"
STALE="$(printf '%s\n%s' "$STALE_HELM" "$STALE_KZ" | grep -v '^$' || true)"
{ echo "helm pattern:      $OLD_HELM"; echo "kustomize pattern: $OLD_KUSTOMIZE";
  echo "--- helm matches (outside chart templates/) ---"; echo "${STALE_HELM:-<none>}";
  echo "--- kustomize matches (outside docs/verification) ---"; echo "${STALE_KZ:-<none>}"; } > "$RESULTS_DIR/stale-paths.txt"
if [ -z "$STALE" ]; then
  record F3 pass "no stale old helm/kustomize path references"
else
  record F3 fail "stale old-path reference(s) found — see results/stale-paths.txt"
fi

# ---- F4: RBAC cross-references resolve within the rendered set -------------------
# Every roleRef target and every ServiceAccount subject must be a resource actually
# defined by the chart (of the right kind). Parses the PR-head render (R_REF).
python3 - "$R_REF" > "$RESULTS_DIR/rbac-xref.txt" 2>&1 <<'PY' && F4=pass || F4=fail
import sys,re
docs=re.split(r'^---\s*$', open(sys.argv[1]).read(), flags=re.M)
defined={}          # kind -> set(names)
refs=[]             # (desc, wanted_kind, wanted_name)
for d in docs:
    kind=re.search(r'^kind:\s*(\S+)', d, re.M)
    name=re.search(r'^metadata:\s*$.*?^\s+name:\s*(\S+)', d, re.M|re.S)
    if not kind: continue
    k=kind.group(1)
    if name: defined.setdefault(k,set()).add(name.group(1))
    # roleRef target
    rr=re.search(r'^roleRef:\s*$(.*?)(?=^\S|\Z)', d, re.M|re.S)
    if rr:
        blk=rr.group(1)
        rk=re.search(r'kind:\s*(\S+)', blk); rn=re.search(r'name:\s*(\S+)', blk)
        if rk and rn: refs.append((f"{k} roleRef", rk.group(1), rn.group(1)))
    # ServiceAccount subjects
    for sub in re.finditer(r'-\s*kind:\s*ServiceAccount\s*\n\s*name:\s*(\S+)', d):
        refs.append((f"{k} subject", "ServiceAccount", sub.group(1)))

print("defined resources:")
for k in sorted(defined): print(f"  {k}: {sorted(defined[k])}")
print("references to resolve:")
ok=True
for desc,wk,wn in refs:
    present = wn in defined.get(wk,set())
    print(f"  [{'OK ' if present else 'MISS'}] {desc} -> {wk}/{wn}")
    ok = ok and present
sys.exit(0 if ok and refs else 1)
PY
if [ "$F4" = pass ]; then record F4 pass "all roleRef/subject references resolve to defined resources"; else record F4 fail "unresolved RBAC reference — see results/rbac-xref.txt"; fi

# ---- F5: kustomize render-equivalence (config/default: main vs PR head) ----------
# The PR also renamed config/rbac/secret_role* -> cert_role* (the kustomize source
# for `make manifests` -> deploy/kubectl/manifests.yaml). Same contract as F1.
KZ=""
if command -v kustomize >/dev/null; then KZ="kustomize build";
elif command -v kubectl >/dev/null && kubectl kustomize --help >/dev/null 2>&1; then KZ="kubectl kustomize"; fi
if [ -z "$KZ" ]; then
  record F5 pass "SKIPPED (no kustomize/kubectl on this machine) — F1 covers the deployed helm chart"
elif [ ! -d "$WT_REF/config/default" ]; then
  record F5 pass "SKIPPED (config/default absent at this ref)"
else
  KB="$RESULTS_DIR/kustomize-base.yaml"; KR="$RESULTS_DIR/kustomize-ref.yaml"
  $KZ "$WT_BASE/config/default" > "$KB" 2>"$RESULTS_DIR/kustomize-base.err" || true
  $KZ "$WT_REF/config/default"  > "$KR" 2>"$RESULTS_DIR/kustomize-ref.err"  || true
  kb="$(grep -c '^kind:' "$KB" || true)"; kr="$(grep -c '^kind:' "$KR" || true)"
  if [ -s "$KR" ] && diff -q "$KB" "$KR" >/dev/null; then
    record F5 pass "kustomize config/default identical (base=$kb objs, ref=$kr objs)"
  else
    { echo "### kustomize diff (base vs ref):"; diff "$KB" "$KR" || true; } > "$RESULTS_DIR/kustomize-equivalence.txt"
    record F5 fail "kustomize config/default DIFFERS (base=$kb, ref=$kr) — see results/kustomize-equivalence.txt"
  fi
fi

# ---- summary --------------------------------------------------------------------
echo
echo "PASS: ${pass[*]:-none}"
echo "FAIL: ${fail[*]:-none}"
echo
echo "Review delta this round: ${LAST_REVIEWED:0:12}..${REF:0:12}"
echo "After reviewing that delta, advance the marker:"
echo "  echo $REF > $LAST_REVIEWED_FILE && git add $LAST_REVIEWED_FILE && git commit -m 'review: advance last-reviewed'"
echo
if [ ${#fail[@]} -eq 0 ]; then echo "RESULT: all findings green — reorg is behavior-preserving."; exit 0
else echo "RESULT: regression detected (${#fail[@]} check(s) failed)."; exit 1; fi
