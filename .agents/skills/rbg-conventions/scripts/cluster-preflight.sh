#!/usr/bin/env bash
# cluster-preflight.sh: read-only checks to run before you verify anything on a cluster that others
# may share. It changes nothing on the cluster.
#
# Usage: cluster-preflight.sh [-n NAMESPACE]...
#
# It prints the current context and server version, then looks for signs that someone else uses
# RBG on the cluster:
# - RBG controller Pods (label control-plane=rbgs-controller) in any namespace
# - a recently renewed rbg-controller leader-election Lease
# - admission webhooks for workloads.x-k8s.io
# - verification namespaces (label rbg-conventions/verification=true) that aren't yours
# - RoleBasedGroups outside your namespaces
# - kwok fake nodes (label type=kwok), which a stress test creates
# - RBG controller processes on this machine, which other sessions may have started, including
#   builds called main or manager whose executable contains the controller's code
# - Helm releases of the rbgs chart, when helm is installed (set HELM to use another binary)
# It also reports missing RBG CRDs: install them only with the cluster owner's agreement.
#
# Your namespaces are the ones you name with -n, once for each namespace that this session created
# with rbg-fixtures.sh, and only while their local ownership record matches. All your sessions on
# this machine share those records, so a namespace you don't name is a finding even with a record.
#
# Exit status: 0 when nothing was found; 1 when something was: stop and agree with its owner before
# you start a controller, apply CRDs or install anything; 2 when nothing was found but a check could
# not run, so the result is incomplete.
set -euo pipefail
# shellcheck source-path=SCRIPTDIR source=common.sh
. "$(dirname "$0")/common.sh"
need jq
need "$KUBECTL"

usage() {
  sed -n '2,26p' "$0" >&2
  exit 2
}
named=' '
while [ $# -gt 0 ]; do
  case $1 in
    -n | --namespace)
      if [ $# -lt 2 ] || [ -z "$2" ]; then usage; fi
      named="$named$2 "
      shift 2
      ;;
    *) usage ;;
  esac
done

found=0
unknown=0
finding() {
  echo "FOUND: $*"
  found=$((found + 1))
}
ok() { echo "ok: $*"; }
qerr=$(mktemp)
trap 'rm -f "$qerr"' EXIT
# unsure records a check that could not decide; cannot adds the last line kubectl printed on stderr.
unsure() {
  echo "UNKNOWN: $*"
  unknown=$((unknown + 1))
}
cannot() { unsure "$*: $(tail -n 1 "$qerr")"; }

ctx=$($KUBECTL config current-context 2>/dev/null) || die "kubectl has no current context"
server=$($KUBECTL config view --minify -o 'jsonpath={.clusters[0].cluster.server}' 2>/dev/null) ||
  server=unknown
version=$(kc version -o json 2>"$qerr" | jq -r '.serverVersion.gitVersion // "unknown"') || {
  cat "$qerr" >&2
  die "cannot reach the API server of context $ctx ($server)"
}
echo "context: $ctx"
echo "server: $server (Kubernetes $version)"

rbg_api=true
if kc get --raw /apis/workloads.x-k8s.io >/dev/null 2>"$qerr"; then
  ok "the RBG API (workloads.x-k8s.io) is served"
elif grep -q '(NotFound)' "$qerr"; then
  rbg_api=false
  finding "the RBG CRDs are not installed: install them only with the cluster owner's agreement, and leave them installed"
else
  rbg_api=unknown
  cannot "cannot tell whether the RBG CRDs are installed"
fi

if out=$(kc get pods -A -l control-plane=rbgs-controller -o json 2>"$qerr"); then
  lines=$(printf '%s' "$out" | jq -r '.items[] | "\(.metadata.namespace)/\(.metadata.name)" +
    " phase=\(.status.phase // "-") images=\([.spec.containers[].image] | join(","))"')
  if [ -n "$lines" ]; then
    while IFS= read -r line; do finding "RBG controller Pod $line"; done <<<"$lines"
  else
    ok "no RBG controller Pods (label control-plane=rbgs-controller)"
  fi
else
  cannot "cannot list RBG controller Pods"
fi

# The controller's leader-election ID is constants.ControllerName. A Lease renewed within the last
# five minutes means a controller with leader election is running somewhere, maybe off-cluster.
if out=$(kc get leases -A --field-selector metadata.name=rbg-controller -o json 2>"$qerr"); then
  lines=$(printf '%s' "$out" | jq -r '.items[] |
    (now - ((.spec.renewTime // "1970-01-01T00:00:00Z") | sub("\\.[0-9]+"; "") | fromdateiso8601)
      | floor) as $age |
    "\($age) \(.metadata.namespace)/\(.metadata.name) holder=\(.spec.holderIdentity // "-")"')
  active=false
  while read -r age lease; do
    [ -n "$age" ] || continue
    if [ "$age" -lt 300 ]; then
      finding "leader-election Lease $lease, renewed ${age}s ago: an RBG controller is running"
      active=true
    else
      echo "info: leader-election Lease $lease was last renewed ${age}s ago"
    fi
  done <<<"$lines"
  [ "$active" = true ] || ok "no recently renewed rbg-controller Lease"
else
  cannot "cannot list leader-election Leases"
fi

if out=$(kc get validatingwebhookconfigurations,mutatingwebhookconfigurations -o json 2>"$qerr"); then
  lines=$(printf '%s' "$out" | jq -r '.items[] | (.kind // "WebhookConfiguration") as $kind |
    .metadata.name as $name |
    [.webhooks[]? | select([.rules[]?.apiGroups[]?] | index("workloads.x-k8s.io"))] |
    select(length > 0) |
    "\($kind)/\($name): \(length) webhook(s) for workloads.x-k8s.io, service" +
    " \(.[0].clientConfig.service.namespace // "-")/\(.[0].clientConfig.service.name // "-")," +
    " failurePolicy \(.[0].failurePolicy // "Fail")"')
  if [ -n "$lines" ]; then
    while IFS= read -r line; do finding "$line"; done <<<"$lines"
  else
    ok "no admission webhooks for workloads.x-k8s.io"
  fi
else
  cannot "cannot list webhook configurations"
fi

# Namespaces whose owner token and UID match a local record were created by rbg-fixtures.sh on this
# machine, by this session or another one; -n tells which are this session's.
mine=' '
if out=$(kc get namespaces -l "$MARKER_LABEL=true" -o json 2>"$qerr"); then
  lines=$(printf '%s' "$out" | jq -r --arg owner "$OWNER_LABEL" \
    '.items[] | "\(.metadata.name) \(.metadata.labels[$owner] // "-") \(.metadata.uid)"')
  others=false
  while read -r name token uid; do
    [ -n "$name" ] || continue
    file=$(state_file "$name")
    recorded=false
    if [ -r "$file" ] && [ "$(cat "$file")" = "$token $uid" ]; then recorded=true; fi
    case "$named:$recorded" in
      *" $name "*:true)
        mine="$mine$name "
        echo "info: verification namespace $name is yours ($file)"
        continue
        ;;
      *" $name "*:false)
        finding "verification namespace $name was named with -n, but another run or machine created it"
        ;;
      *:true)
        finding "verification namespace $name was created on this machine by another session or an earlier run; name it with -n if this session created it"
        ;;
      *) finding "verification namespace $name was created by another run or machine" ;;
    esac
    others=true
  done <<<"$lines"
  [ "$others" = true ] || ok "no verification namespaces of other runs"
else
  cannot "cannot list verification namespaces"
fi

if [ "$rbg_api" = true ]; then
  if out=$(kc get rolebasedgroups.workloads.x-k8s.io -A --no-headers 2>"$qerr"); then
    others=false
    while read -r count ns; do
      [ -n "$ns" ] || continue
      case $mine in
        *" $ns "*) echo "info: $count RoleBasedGroup(s) in your namespace $ns" ;;
        *)
          finding "$count RoleBasedGroup(s) in namespace $ns, which is not yours"
          others=true
          ;;
      esac
    done <<<"$(printf '%s\n' "$out" | awk 'NF { print $1 }' | sort | uniq -c)"
    [ "$others" = true ] || ok "no RoleBasedGroups outside your namespaces"
  else
    cannot "cannot list RoleBasedGroups"
  fi
fi

if out=$(kc get nodes -l type=kwok --no-headers 2>"$qerr"); then
  count=$(printf '%s\n' "$out" | awk 'NF' | wc -l | tr -d ' ')
  if [ "$count" -gt 0 ]; then
    finding "$count kwok fake node(s) (label type=kwok): a stress test is running or wasn't cleaned up"
  else
    ok "no kwok fake nodes"
  fi
else
  cannot "cannot list nodes"
fi

# RBG_PREFLIGHT_PS replaces the process listing in tests, and RBG_PREFLIGHT_EXE the lookup of a
# process's executable.
list_processes() {
  if [ -n "${RBG_PREFLIGHT_PS:-}" ]; then "$RBG_PREFLIGHT_PS"; else ps -A -o pid=,etime=,args=; fi
}
# executable PID NAME prints the path of the file that process PID, called NAME, runs, or nothing.
# On macOS, lsof lists it among the process's text files, next to the dynamic loader.
executable() {
  if [ -n "${RBG_PREFLIGHT_EXE:-}" ]; then
    "$RBG_PREFLIGHT_EXE" "$1"
  elif [ -e /proc/self/exe ]; then
    echo "/proc/$1/exe"
  else
    lsof -a -p "$1" -d txt -Fn | awk -v name="$2" 'sub(/^n/, "") {
      n = split($0, part, "/")
      if (part[n] == name) { print; done = 1; exit }
      if (first == "") first = $0
    }
    END { if (!done && first != "") print first }'
  fi
}
# Every build of the controller contains functions of this package, also one built from
# cmd/rbgs/main.go or stripped with -s -w, and no other program imports it.
controller_code='sigs.k8s.io/rbgs/internal/controller/workloads.'
if procs=$(list_processes 2>"$qerr"); then
  # `go run ./cmd/rbgs` runs a binary named rbgs. `make run` runs one named main, `make build`
  # gives bin/manager, and IDE debuggers run __debug_bin* or *go_build_*. Those names are common,
  # and these builds need no RBG flag, so they count when their executable has the controller's
  # code. Shells and tools whose arguments merely mention cmd/rbgs (a grep, a wrapper) don't count.
  lines=$(printf '%s\n' "$procs" | awk '{
    n = split($3, part, "/"); base = part[n]
    if ((base == "go" && $0 ~ / run / && $0 ~ /cmd\/rbgs/) || base ~ /^rbgs(-|$)/) kind = "rbg"
    else if (base == "main" || base == "manager" || base ~ /^__debug_bin/ || base ~ /go_build_/)
      kind = "build"
    else if (base ~ /^kubectl/ && $0 ~ / proxy( |$)/) kind = "proxy"
    else next
    print kind " " base " " $0
  }')
  rbg=false
  rbg_process() {
    finding "RBG controller process on this machine (yours or another session's): $1"
    rbg=true
  }
  # Each line is the kind, the program name, and the process: PID, elapsed time and arguments.
  while read -r kind name line; do
    case $kind in
      rbg) rbg_process "$line" ;;
      build)
        # Without a PID, lsof would list every process.
        pid=${line%% *}
        exe=
        case $pid in '' | *[!0-9]*) ;; *) exe=$(executable "$pid" "$name" 2>/dev/null || true) ;; esac
        code=2
        if [ -n "$exe" ]; then
          code=0
          grep -a -q -F "$controller_code" "$exe" 2>/dev/null || code=$?
        fi
        case $code in
          0) rbg_process "$line" ;;
          1) ;; # another program with the same name
          *) unsure "cannot read the executable of process $line, so it may be an RBG controller" ;;
        esac
        ;;
      proxy) echo "info: kubectl proxy on this machine (yours or another session's): $line" ;;
    esac
  done <<<"$lines"
  [ "$rbg" = true ] || ok "no RBG controller processes on this machine"
else
  cannot "cannot list local processes"
fi

helm=${HELM:-helm}
if ! command -v "$helm" >/dev/null 2>&1; then
  echo "info: $helm not found, so Helm releases were not checked"
elif out=$("$helm" list -A --all -o json 2>"$qerr"); then
  lines=$(printf '%s' "$out" | jq -r '.[]? | select(.chart | test("^rbgs-[0-9]")) |
    "\(.namespace)/\(.name) chart=\(.chart) status=\(.status)"')
  if [ -n "$lines" ]; then
    while IFS= read -r line; do finding "Helm release $line"; done <<<"$lines"
  else
    ok "no Helm releases of the rbgs chart"
  fi
else
  cannot "cannot list Helm releases"
fi

echo
if [ "$unknown" -gt 0 ]; then
  echo "$unknown check(s) could not run, so this result is incomplete."
fi
# A finding is certain even when another check couldn't run, so it decides the exit status.
if [ "$found" -gt 0 ]; then
  echo "$found finding(s): stop, and agree with their owners before you start a controller, apply CRDs or install anything."
  exit 1
fi
[ "$unknown" -eq 0 ] || exit 2
echo "nothing found: no sign that anyone else uses RBG on this cluster."
