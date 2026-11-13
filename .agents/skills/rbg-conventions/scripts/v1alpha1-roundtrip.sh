#!/usr/bin/env bash
# v1alpha1-roundtrip.sh: verification case VC-5 (see references/verification-cases.md).
#
# Reads a RoleBasedGroup through the deprecated v1alpha1 API and writes it back unchanged, the way an
# old client or a GitOps tool holding v1alpha1 manifests does, then reports whether the stored
# v1alpha2 spec changed. A lossless conversion leaves it byte-identical. If it changed, run
# rbg-snapshot.sh before and after to see whether the change rolled any workload.
#
# Usage: v1alpha1-roundtrip.sh -n NAMESPACE NAME
# Exit status: 0 the write was accepted and the stored spec is unchanged; 1 the write was accepted
# and the stored spec changed; 3 the API server rejected the v1alpha1 write (an incompatibility in
# itself; the object is unchanged); 2 any other error.
#
# It refuses to run unless the CRD converts through the webhook. With "conversion: None" the API
# server only relabels the apiVersion and prunes fields the v1alpha1 schema lacks, so a write would
# lose data instead of exercising the production conversion. NAMESPACE must have been created by
# rbg-fixtures.sh on this machine.
set -euo pipefail
# shellcheck source-path=SCRIPTDIR source=common.sh
. "$(dirname "$0")/common.sh"
need jq
need "$KUBECTL"

usage() {
  sed -n '2,17p' "$0" >&2
  exit 2
}

ns=''
name=''
while [ $# -gt 0 ]; do
  case $1 in
    -n | --namespace) ns=$2; shift 2 ;;
    -*) usage ;;
    *) name=$1; shift ;;
  esac
done
[ -n "$ns" ] && [ -n "$name" ] || usage
require_owned_namespace "$ns"

strategy=$(kc get crd rolebasedgroups.workloads.x-k8s.io -o jsonpath='{.spec.conversion.strategy}') ||
  die "cannot read the rolebasedgroups CRD"
[ "$strategy" = "Webhook" ] ||
  die "rolebasedgroups conversion strategy is '$strategy', not Webhook; refusing to write through v1alpha1"

v2=rolebasedgroups.v1alpha2.workloads.x-k8s.io
v1=rolebasedgroups.v1alpha1.workloads.x-k8s.io
read_v2() {
  kc get "$v2" "$name" -n "$ns" -o json | jq -S '{generation: .metadata.generation, spec: .spec}'
}
before=$(read_v2) || die "cannot read $ns/$name"
v1json=$(kc get "$v1" "$name" -n "$ns" -o json | jq 'del(.status)') ||
  die "cannot read $ns/$name through v1alpha1"

echo "replacing $ns/$name through v1alpha1 without changes"
err=$(mktemp)
if ! printf '%s\n' "$v1json" | kc replace -f - >/dev/null 2>"$err"; then
  if grep -qE 'is invalid|denied the request|is forbidden' "$err"; then
    echo "REJECTED: the API server refused the unchanged v1alpha1 object; the stored object is unchanged"
    grep -vE '^Warning: ' "$err" >&2 || true
    rm -f "$err"
    exit 3
  fi
  cat "$err" >&2
  rm -f "$err"
  die "the v1alpha1 replace failed"
fi
rm -f "$err"

after=$(read_v2) || die "cannot read $ns/$name after the replace"
if [ "$(printf '%s' "$before" | jq -S .spec)" = "$(printf '%s' "$after" | jq -S .spec)" ]; then
  echo "PASS: stored v1alpha2 spec unchanged (generation $(printf '%s' "$before" | jq .generation))"
  exit 0
fi
echo "FAIL: the v1alpha1 round trip rewrote the stored v1alpha2 spec" \
  "(generation $(printf '%s' "$before" | jq .generation) -> $(printf '%s' "$after" | jq .generation))"
diff <(printf '%s' "$before" | jq -S .spec) <(printf '%s' "$after" | jq -S .spec) || true
exit 1
