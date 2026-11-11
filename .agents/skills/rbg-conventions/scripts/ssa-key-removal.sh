#!/usr/bin/env bash
# ssa-key-removal.sh: verification case VC-4 (see references/verification-cases.md).
#
# Removes the rbg-conventions/remove-me label from role "server" of fixture fx-standalone and checks
# that the controller removes it from RoleInstanceSet fx-standalone-server as well, and that the
# RoleInstanceSet's main resource has exactly one field manager: rbg with operation Apply. A second
# manager (for example rbg/Update from a typed Create) would keep keys the apply stops sending.
#
# Usage: ssa-key-removal.sh -n NAMESPACE [--timeout SECONDS]
# Needs the fixtures from rbg-fixtures.sh and a running controller. NAMESPACE must have been created
# by rbg-fixtures.sh on this machine. Exit status: 0 pass, 1 fail, 2 error.
set -euo pipefail
# shellcheck source-path=SCRIPTDIR source=common.sh
. "$(dirname "$0")/common.sh"
need jq
need "$KUBECTL"

ns=''
timeout=120
while [ $# -gt 0 ]; do
  case $1 in
    -n | --namespace) ns=$2; shift 2 ;;
    --timeout) timeout=$2; shift 2 ;;
    *) sed -n '2,11p' "$0" >&2; exit 2 ;;
  esac
done
[ -n "$ns" ] || { sed -n '2,11p' "$0" >&2; exit 2; }
require_owned_namespace "$ns"

ris=fx-standalone-server
label_on_ris() {
  kc get roleinstancesets.workloads.x-k8s.io "$ris" -n "$ns" -o json |
    jq -r '.metadata.labels["rbg-conventions/remove-me"] // ""'
}

present=$(label_on_ris) || die "cannot read RoleInstanceSet $ns/$ris"
[ "$present" = "true" ] ||
  die "RoleInstanceSet $ns/$ris does not carry rbg-conventions/remove-me=true; start from fresh fixtures"

echo "removing rbg-conventions/remove-me from role server of $ns/fx-standalone"
kc patch rolebasedgroups.workloads.x-k8s.io fx-standalone -n "$ns" --type=json -p '[
  {"op": "test", "path": "/spec/roles/0/name", "value": "server"},
  {"op": "test", "path": "/spec/roles/0/labels/rbg-conventions~1remove-me", "value": "true"},
  {"op": "remove", "path": "/spec/roles/0/labels/rbg-conventions~1remove-me"}]' >/dev/null ||
  die "cannot patch $ns/fx-standalone"

deadline=$(($(date +%s) + timeout))
while :; do
  present=$(label_on_ris) || die "cannot read RoleInstanceSet $ns/$ris"
  [ -z "$present" ] && break
  [ "$(date +%s)" -ge "$deadline" ] && break
  sleep 2
done

managers=$(kc get roleinstancesets.workloads.x-k8s.io "$ris" -n "$ns" -o json --show-managed-fields |
  jq -r '[.metadata.managedFields[] | select((.subresource // "") == "") | "\(.manager)/\(.operation)"]
         | sort | join(",")') || die "cannot read managedFields of $ns/$ris"

failed=0
if [ -z "$present" ]; then
  echo "PASS label removed from RoleInstanceSet $ris"
else
  echo "FAIL label still on RoleInstanceSet $ris after ${timeout}s"
  failed=1
fi
if [ "$managers" = "rbg/Apply" ]; then
  echo "PASS single field manager rbg/Apply on the main resource"
else
  echo "FAIL field managers on the main resource: $managers (want rbg/Apply)"
  failed=1
fi
exit "$failed"
