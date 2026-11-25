#!/usr/bin/env bash
# rbg-fixtures.sh: create or delete the verification fixtures (fixtures/rbg-fixtures.yaml).
#
# Usage:
#   rbg-fixtures.sh apply  -n NAMESPACE [--image IMAGE] [--wait SECONDS] [--with-deprecated]
#   rbg-fixtures.sh delete -n NAMESPACE
#
# apply creates NAMESPACE with a random owner token and records the token and the namespace UID
# locally; it reuses an existing namespace only if that record matches, so it never writes into
# someone else's namespace. If the outcome of the create is unknown (its response was lost), it
# reads the namespace back and adopts it only if it carries this run's token. It then applies the
# fixtures with __IMAGE__ replaced. IMAGE defaults to $RBG_FIXTURE_IMAGE or busybox:1.36; any
# image with `sh` and `sleep` the nodes can pull works.
# --with-deprecated also applies fixtures/rbg-deprecated-fixtures.yaml: StatefulSet and Deployment
# roles. They need a controller with --enable-deprecated-workload-types (the default) and no RBG
# admission webhook on the cluster, because the webhook rejects new uses of deprecated types.
# --wait waits until every RoleBasedGroup is Ready, which needs nodes that run pods.
# delete removes NAMESPACE only if the local ownership record matches it. The delete carries the
# recorded UID as a precondition, so a namespace recreated under the same name is never deleted.
# Run it while the controller under test still runs: only a controller removes its finalizers.
set -euo pipefail
# shellcheck source-path=SCRIPTDIR source=common.sh
. "$(dirname "$0")/common.sh"
need jq
need "$KUBECTL"

usage() {
  sed -n '2,20p' "$0" >&2
  exit 2
}

[ $# -ge 1 ] || usage
cmd=$1
shift
ns=''
image=${RBG_FIXTURE_IMAGE:-busybox:1.36}
wait_seconds=0
with_deprecated=false
while [ $# -gt 0 ]; do
  case $1 in
    -n | --namespace) ns=$2; shift 2 ;;
    --image) image=$2; shift 2 ;;
    --wait) wait_seconds=$2; shift 2 ;;
    --with-deprecated) with_deprecated=true; shift ;;
    *) usage ;;
  esac
done
[ -n "$ns" ] || usage

case $cmd in
  apply)
    # The image is substituted into the fixtures with sed, so accept only the characters an image
    # reference can contain.
    case $image in
      '' | *[!A-Za-z0-9._/:@-]*) die "--image '$image' is not a valid image reference" ;;
    esac
    existing=$(kc get namespace "$ns" --ignore-not-found -o name) || die "cannot read namespace $ns"
    if [ -n "$existing" ]; then
      require_owned_namespace "$ns"
    else
      token=$(random_token)
      uid=$(create_once namespace "$ns" "$token" <<EOF
{"apiVersion": "v1", "kind": "Namespace",
 "metadata": {"name": "$ns", "labels": {"$MARKER_LABEL": "true", "$OWNER_LABEL": "$token"}}}
EOF
      ) || die "cannot create namespace $ns; if it exists or appears later with label $OWNER_LABEL=$token, this run created it: delete it by hand"
      printf '%s %s\n' "$token" "$uid" >"$(state_file "$ns")"
      echo "created namespace $ns (owner token recorded in $(state_file "$ns"))"
    fi
    files=("$(dirname "$0")/fixtures/rbg-fixtures.yaml")
    [ "$with_deprecated" = false ] || files+=("$(dirname "$0")/fixtures/rbg-deprecated-fixtures.yaml")
    # One apply for all files, so kc never replays part of a set that was already written.
    for file in "${files[@]}"; do
      echo '---'
      sed "s|__IMAGE__|$image|g" "$file"
    done | kc apply -n "$ns" -f -
    if [ "$wait_seconds" -gt 0 ]; then
      kc wait --for=condition=Ready rolebasedgroups.workloads.x-k8s.io --all -n "$ns" \
        --timeout="${wait_seconds}s"
    fi
    ;;
  delete)
    require_owned_namespace "$ns"
    read -r _ uid <"$(state_file "$ns")"
    delete_with_uid "/api/v1/namespaces/$ns" "$uid" || die "could not delete namespace $ns"
    for _ in $(seq 1 90); do
      current=$(kc get namespace "$ns" --ignore-not-found -o 'jsonpath={.metadata.uid}') ||
        die "cannot read namespace $ns"
      if [ "$current" != "$uid" ]; then
        rm -f "$(state_file "$ns")"
        echo "deleted namespace $ns"
        exit 0
      fi
      sleep 2
    done
    die "namespace $ns is still terminating after 180s; if objects in it still carry finalizers, start the controller that added them and run delete again"
    ;;
  *) usage ;;
esac
