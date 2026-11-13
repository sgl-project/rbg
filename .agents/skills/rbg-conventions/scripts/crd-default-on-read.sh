#!/usr/bin/env bash
# crd-default-on-read.sh: verification case VC-1 (see references/verification-cases.md).
#
# Shows how the API server applies a CRD default that is added after objects already exist:
#   A1  the default is applied on read where the default's parent object exists,
#   A2  nothing is materialized under an optional parent the object doesn't have,
#   A3  the object is not rewritten (its resourceVersion doesn't change),
#   A4  defaults inside a new optional struct apply once a client sets that struct.
# This is why a new +kubebuilder:default on a field whose parent existing objects already store
# changes what the controller sees for every such object, without anyone writing to them.
#
# It creates a throwaway cluster-scoped CRD with a unique name per run (defaultprobes.<random>.
# rbg-conventions.example.com) and touches no RBG resources, so it is safe on a shared cluster. It
# deletes only the CRD it created: the delete carries the CRD's UID as a precondition. Cleanup also
# runs when the script fails, or when its output is piped into a reader that exits early (such as
# head); a failed cleanup makes the script exit non-zero.
#
# Usage: crd-default-on-read.sh [--keep]
set -euo pipefail
# shellcheck source-path=SCRIPTDIR source=common.sh
. "$(dirname "$0")/common.sh"
need jq
need "$KUBECTL"

keep=false
[ "${1:-}" = "--keep" ] && keep=true

group=p$(random_token).rbg-conventions.example.com
crd=defaultprobes.$group
res=defaultprobes.v1.$group
crd_uid=''

crd_manifest() {
  # $1: "base" or "with-defaults"
  local added='' child='{"type": "integer"}'
  if [ "$1" = "with-defaults" ]; then
    added=', "added": {"type": "integer", "default": 30}'
    child='{"type": "integer", "default": 600}'
  fi
  cat <<EOF
{
  "apiVersion": "apiextensions.k8s.io/v1",
  "kind": "CustomResourceDefinition",
  "metadata": {"name": "$crd", "labels": {"$MARKER_LABEL": "true"}},
  "spec": {
    "group": "$group",
    "scope": "Cluster",
    "names": {"plural": "defaultprobes", "singular": "defaultprobe", "kind": "DefaultProbe"},
    "versions": [{
      "name": "v1", "served": true, "storage": true,
      "schema": {"openAPIV3Schema": {"type": "object", "properties": {
        "spec": {"type": "object", "properties": {
          "parent": {"type": "object", "properties": {"existing": {"type": "string"}$added}},
          "optionalParent": {"type": "object", "properties": {"child": $child}}
        }}
      }}}
    }]
  }
}
EOF
}

# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup() {
  local status=$?
  # The name is unique to this run, so a CRD under it is this run's, also when the response to the
  # create was lost.
  if [ -z "$crd_uid" ] &&
    ! crd_uid=$(kc get "crd/$crd" --ignore-not-found -o 'jsonpath={.metadata.uid}'); then
    echo "error: cannot tell whether $crd exists; if it does, delete it by hand" >&2
    exit 2
  fi
  [ -n "$crd_uid" ] || exit "$status"
  if [ "$keep" = true ]; then
    echo "kept $crd"
    exit "$status"
  fi
  delete_with_uid "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/$crd" "$crd_uid" || {
    echo "error: cleanup of $crd failed; see the message above" >&2
    exit 2
  }
  exit "$status"
}

# Arm cleanup before the create, so it also covers a create whose response is lost, and output
# that fails because its reader has exited (bash runs the EXIT trap when SIGPIPE kills a script).
trap cleanup EXIT

echo "creating $crd without defaults"
crd_uid=$(crd_manifest base | create_once crd "$crd") ||
  die "cannot create $crd; if it appears later, delete it by hand"
# Poll rather than use kubectl wait: until the API server has processed a new CRD, its
# status.conditions is null, and kubectl wait then fails at once instead of waiting.
established=''
for _ in $(seq 1 30); do
  established=$(kc get "crd/$crd" -o json |
    jq -r '[(.status.conditions // [])[] | select(.type == "Established") | .status][0] // ""')
  [ "$established" = True ] && break
  sleep 2
done
[ "$established" = True ] || die "$crd is not Established after 60s"

echo "creating object 'before' with spec.parent set and spec.optionalParent unset"
kc create -f - >/dev/null <<EOF
{"apiVersion": "$group/v1", "kind": "DefaultProbe", "metadata": {"name": "before"},
 "spec": {"parent": {"existing": "x"}}}
EOF
rv_before=$(kc get "$res" before -o jsonpath='{.metadata.resourceVersion}')

echo "adding defaults: spec.parent.added=30 and spec.optionalParent.child=600"
crd_manifest with-defaults | kc replace -f - >/dev/null

# The new schema reaches the read path asynchronously; wait until the API server serves it.
obj=''
for _ in $(seq 1 30); do
  obj=$(kc get "$res" before -o json)
  [ "$(printf '%s' "$obj" | jq '.spec.parent.added // empty')" != "" ] && break
  sleep 2
done

failed=0
check() {
  # $1 label, $2 description, $3 actual, $4 expected
  if [ "$3" = "$4" ]; then
    echo "PASS $1: $2"
  else
    echo "FAIL $1: $2 (got '$3', want '$4')"
    failed=1
  fi
}

check A1 "default applied on read where the parent exists" \
  "$(printf '%s' "$obj" | jq -r '.spec.parent.added // "absent"')" 30
check A2 "nothing materialized under an absent optional parent" \
  "$(printf '%s' "$obj" | jq -r 'if .spec | has("optionalParent") then "present" else "absent" end')" absent
check A3 "object not rewritten (resourceVersion unchanged)" \
  "$(printf '%s' "$obj" | jq -r '.metadata.resourceVersion')" "$rv_before"

kc create -f - >/dev/null <<EOF
{"apiVersion": "$group/v1", "kind": "DefaultProbe", "metadata": {"name": "after"},
 "spec": {"parent": {"existing": "y"}, "optionalParent": {}}}
EOF
check A4 "defaults inside a newly set optional struct apply" \
  "$(kc get "$res" after -o jsonpath='{.spec.optionalParent.child}')" 600

exit "$failed"
