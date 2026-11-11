#!/usr/bin/env bash
# rbg-fixtures.sh: create or delete the verification fixtures (fixtures/rbg-fixtures.yaml).
#
# Usage:
#   rbg-fixtures.sh apply  -n NAMESPACE [--image IMAGE] [--wait SECONDS]
#   rbg-fixtures.sh delete -n NAMESPACE
#
# apply creates NAMESPACE with a random owner token and records the token and the namespace UID
# locally; it reuses an existing namespace only if that record matches, so it never writes into
# someone else's namespace. It then applies the fixtures with __IMAGE__ replaced. IMAGE defaults to
# $RBG_FIXTURE_IMAGE or busybox:1.36; any image with `sh` and `sleep` the nodes can pull works.
# --wait waits until every RoleBasedGroup is Ready, which needs nodes that run pods.
# delete removes NAMESPACE only if the local ownership record matches it.
set -euo pipefail
# shellcheck source-path=SCRIPTDIR source=common.sh
. "$(dirname "$0")/common.sh"
need "$KUBECTL"

usage() {
  sed -n '2,13p' "$0" >&2
  exit 2
}

[ $# -ge 1 ] || usage
cmd=$1
shift
ns=''
image=${RBG_FIXTURE_IMAGE:-busybox:1.36}
wait_seconds=0
while [ $# -gt 0 ]; do
  case $1 in
    -n | --namespace) ns=$2; shift 2 ;;
    --image) image=$2; shift 2 ;;
    --wait) wait_seconds=$2; shift 2 ;;
    *) usage ;;
  esac
done
[ -n "$ns" ] || usage

case $cmd in
  apply)
    existing=$(kc get namespace "$ns" --ignore-not-found -o name) || die "cannot read namespace $ns"
    if [ -n "$existing" ]; then
      require_owned_namespace "$ns"
    else
      token=$(random_token)
      uid=$(kc create -o 'jsonpath={.metadata.uid}' -f - <<EOF
{"apiVersion": "v1", "kind": "Namespace",
 "metadata": {"name": "$ns", "labels": {"$MARKER_LABEL": "true", "$OWNER_LABEL": "$token"}}}
EOF
      ) || die "cannot create namespace $ns"
      printf '%s %s\n' "$token" "$uid" >"$(state_file "$ns")"
      echo "created namespace $ns (owner token recorded in $(state_file "$ns"))"
    fi
    sed "s|__IMAGE__|$image|g" "$(dirname "$0")/fixtures/rbg-fixtures.yaml" | kc apply -n "$ns" -f -
    if [ "$wait_seconds" -gt 0 ]; then
      kc wait --for=condition=Ready rolebasedgroups.workloads.x-k8s.io --all -n "$ns" \
        --timeout="${wait_seconds}s"
    fi
    ;;
  delete)
    require_owned_namespace "$ns"
    kc delete namespace "$ns" --wait=false >/dev/null
    for _ in $(seq 1 90); do
      left=$(kc get namespace "$ns" --ignore-not-found -o name) || die "cannot read namespace $ns"
      if [ -z "$left" ]; then
        rm -f "$(state_file "$ns")"
        echo "deleted namespace $ns"
        exit 0
      fi
      sleep 2
    done
    die "namespace $ns is still terminating after 180s"
    ;;
  *) usage ;;
esac
