#!/usr/bin/env bash
# rbg-snapshot.sh: print a deterministic snapshot of the RBG-managed state in one namespace, one
# line per object or metadata key, for comparison with rbg-compare.sh. It records what must not
# change when the controller restarts or is swapped for another build, and leaves out status that
# moves on its own:
#   rbg/rbgset   UID and generation
#   cr           every ControllerRevision with its owner and revision number
#   ris          UID, generation, update and current revision, role-revision hash labels
#   ri           UID, generation, controller-revision-hash label
#   sts/deploy/lws  UID and generation (deprecated workload types)
#   pod          UID, owner, node, controller-revision-hash label, and the images and restart
#                counts of its regular and its init containers (native sidecars are init containers)
#   svc          UID, type, cluster IP, ports, selector
#   endpoints    ready pods behind each Service
#   <obj>#label:<key> and <obj>#annotation:<key>  every label and annotation (URI-encoded value)
#
# Usage: rbg-snapshot.sh -n NAMESPACE [--settle SECONDS] [--timeout SECONDS]
#   --settle S  take snapshots S seconds apart until two in a row are identical, and print that
#               one. Use it after a restart or swap so a late reconcile isn't missed. Exits 3 if
#               the state hasn't settled within --timeout (default 600).
# Any failed query fails the script; it never prints a partial snapshot.
set -euo pipefail
# shellcheck source-path=SCRIPTDIR source=common.sh
. "$(dirname "$0")/common.sh"
need jq
need "$KUBECTL"

ns=''
settle=0
timeout=600
while [ $# -gt 0 ]; do
  case $1 in
    -n | --namespace) ns=$2; shift 2 ;;
    --settle) settle=$2; shift 2 ;;
    --timeout) timeout=$2; shift 2 ;;
    *) sed -n '2,21p' "$0" >&2; exit 2 ;;
  esac
done
[ -n "$ns" ] || die "-n NAMESPACE is required"

kinds=rolebasedgroups.workloads.x-k8s.io,rolebasedgroupsets.workloads.x-k8s.io
kinds=$kinds,roleinstancesets.workloads.x-k8s.io,roleinstances.workloads.x-k8s.io
kinds=$kinds,controllerrevisions.apps,statefulsets.apps,deployments.apps,pods,services
# Include LeaderWorkerSets when the API exists. A raw discovery probe is used because
# `kubectl api-resources` fails on clusters where any aggregated API is unhealthy.
if kc get --raw /apis/leaderworkerset.x-k8s.io >/dev/null 2>&1; then
  kinds=$kinds,leaderworkersets.leaderworkerset.x-k8s.io
fi

# shellcheck disable=SC2016 # jq programs, not shell expansions
objects_jq='
  def kv: to_entries | sort_by(.key) | map("\(.key)=\(.value)") | join(",") | if . == "" then "-" else . end;
  def list: sort | join(",") | if . == "" then "-" else . end;
  def images(containers): [(containers // [])[] | "\(.name):\(.image)"] | list;
  def restarts(statuses): [(statuses // [])[] | "\(.name):\(.restartCount)"] | list;
  def owner: [(.metadata.ownerReferences // [])[] | select(.controller == true)
              | "\(.kind)/\(.name)"] | (.[0] // "-");
  def rolehash: (.metadata.labels // {}) | with_entries(select(.key | test("role-revision-"))) | kv;
  def revlabel: (.metadata.labels // {})["controller-revision-hash"] // "-";
  def key: {RoleBasedGroup: "rbg", RoleBasedGroupSet: "rbgset", ControllerRevision: "cr",
            RoleInstanceSet: "ris", RoleInstance: "ri", StatefulSet: "statefulset",
            Deployment: "deployment", LeaderWorkerSet: "lws", Pod: "pod", Service: "svc"}[.kind]
           + "/" + .metadata.name;
  def meta: key as $k
    | ((.metadata.labels // {}) | to_entries[] | "\($k)#label:\(.key) value=\(.value | @uri)"),
      ((.metadata.annotations // {}) | del(.["kubectl.kubernetes.io/last-applied-configuration"])
       | to_entries[] | "\($k)#annotation:\(.key) value=\(.value | @uri)");
  .items[] | .metadata as $m |
  if .kind == "RoleBasedGroup" or .kind == "RoleBasedGroupSet" then
    "\(key) uid=\($m.uid) generation=\($m.generation)", meta
  elif .kind == "ControllerRevision" then
    "\(key) owner=\(owner) revision=\(.revision)"
  elif .kind == "RoleInstanceSet" then
    "\(key) uid=\($m.uid) generation=\($m.generation)" +
    " updateRevision=\(.status.updateRevision // "-") currentRevision=\(.status.currentRevision // "-")" +
    " roleHash=\(rolehash)", meta
  elif .kind == "RoleInstance" then
    "\(key) uid=\($m.uid) generation=\($m.generation) revision=\(revlabel)", meta
  elif .kind == "StatefulSet" or .kind == "Deployment" or .kind == "LeaderWorkerSet" then
    "\(key) uid=\($m.uid) generation=\($m.generation) roleHash=\(rolehash)", meta
  elif .kind == "Pod" then
    "\(key) uid=\($m.uid) owner=\(owner) node=\(.spec.nodeName // "-")" +
    " images=\(images(.spec.containers)) initImages=\(images(.spec.initContainers))" +
    " revision=\(revlabel)" +
    " restarts=\(restarts(.status.containerStatuses))" +
    " initRestarts=\(restarts(.status.initContainerStatuses))", meta
  elif .kind == "Service" then
    "\(key) uid=\($m.uid) type=\(.spec.type) clusterIP=\(.spec.clusterIP // "-")" +
    " ports=\([(.spec.ports // [])[] | "\(.name // ""):\(.port)/\(.protocol)>\(.targetPort)"] | sort | join(",")
              | if . == "" then "-" else . end)" +
    " publishNotReady=\(.spec.publishNotReadyAddresses // false) selector=\((.spec.selector // {}) | kv)", meta
  else empty end'

endpoints_jq='
  [.items[] | {svc: (.metadata.labels["kubernetes.io/service-name"] // "-"),
               pods: [(.endpoints // [])[] | select(.conditions.ready != false)
                      | (.targetRef.name // .addresses[0])]}]
  | group_by(.svc)[] | "endpoints/\(.[0].svc) ready=\([.[].pods[]] | sort | unique | join(",")
                                                   | if . == "" then "-" else . end)"'

# snapshot_once prints one snapshot, or fails without printing anything. Every step is checked
# explicitly because bash 3.2 ignores set -e inside command substitutions.
snapshot_once() {
  local tmp rc=0
  tmp=$(mktemp -d)
  kc get "$kinds" -n "$ns" -o json >"$tmp/objects" || rc=1
  if [ "$rc" -eq 0 ]; then
    kc get endpointslices.discovery.k8s.io -n "$ns" -o json >"$tmp/endpoints" || rc=1
  fi
  if [ "$rc" -eq 0 ]; then
    { jq -r "$objects_jq" "$tmp/objects" && jq -r "$endpoints_jq" "$tmp/endpoints"; } |
      LC_ALL=C sort >"$tmp/body" || rc=1
  fi
  if [ "$rc" -eq 0 ]; then
    printf '#rbg-snapshot v2 namespace=%s\n' "$ns"
    cat "$tmp/body"
  fi
  rm -rf "$tmp"
  return "$rc"
}

if [ "$settle" -le 0 ]; then
  snapshot_once || die "snapshot of namespace $ns failed"
  exit 0
fi

deadline=$(($(date +%s) + timeout))
prev=$(snapshot_once) || die "snapshot of namespace $ns failed"
while :; do
  sleep "$settle"
  cur=$(snapshot_once) || die "snapshot of namespace $ns failed"
  if [ "$cur" = "$prev" ]; then
    printf '%s\n' "$cur"
    exit 0
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    printf '%s\n' "$cur"
    echo "error: state did not settle within ${timeout}s" >&2
    exit 3
  fi
  prev=$cur
done
