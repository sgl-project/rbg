#!/usr/bin/env bash
# selftest.sh: offline regression tests for the scripts in this directory. They run against a fake
# kubectl, so only bash and jq are needed, not a cluster. Run it after changing any script here.
#
# Usage: selftest.sh
# Exit status: 0 when every test passes, 1 otherwise.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The fake kubectl answers its Nth call from $FAKE/N.{rc,out,err}, or from $FAKE/default.* when
# there is no N.rc. It logs the arguments of every call to $FAKE/calls, and the content of every
# file passed with -f to $FAKE/inputs.
cat >"$work/kubectl" <<'EOF'
#!/usr/bin/env bash
n=$(($(cat "$FAKE/count" 2>/dev/null || echo 0) + 1))
echo "$n" >"$FAKE/count"
echo "$*" >>"$FAKE/calls"
prev=''
for arg in "$@"; do
  if [ "$prev" = "-f" ] && [ -f "$arg" ]; then cat "$arg" >>"$FAKE/inputs"; fi
  prev=$arg
done
f=$FAKE/$n
[ -e "$f.rc" ] || f=$FAKE/default
[ ! -e "$f.out" ] || cat "$f.out"
[ ! -e "$f.err" ] || cat "$f.err" >&2
exit "$(cat "$f.rc" 2>/dev/null || echo 0)"
EOF
chmod +x "$work/kubectl"
export KUBECTL=$work/kubectl RBG_VERIFY_RETRY_DELAY=0 RBG_VERIFY_STATE_DIR=$work/state

# shellcheck source-path=SCRIPTDIR source=common.sh
. "$here/common.sh"

failed=0
check() {
  # $1 description, $2 actual, $3 expected
  if [ "$2" = "$3" ]; then
    echo "PASS $1"
  else
    echo "FAIL $1: got '$2', want '$3'"
    failed=1
  fi
}

# scenario NAME starts a fresh fake; answer N|default RC [STDOUT] [STDERR] scripts one answer.
scenario() {
  export FAKE=$work/fake.$1
  mkdir -p "$FAKE"
}
answer() {
  echo "$2" >"$FAKE/$1.rc"
  [ -z "${3:-}" ] || printf '%s\n' "$3" >"$FAKE/$1.out"
  [ -z "${4:-}" ] || printf '%s\n' "$4" >"$FAKE/$1.err"
}
calls() { wc -l <"$FAKE/calls" | tr -d ' '; }
# run CMD... runs CMD and sets rc, out and err.
run() {
  rc=0
  "$@" >"$work/stdout" 2>"$work/stderr" || rc=$?
  out=$(cat "$work/stdout")
  err=$(cat "$work/stderr")
}

# Error messages as kubectl prints them. refused was captured against a closed port; badfd was
# observed against a real API server.
refused='The connection to the server 192.0.2.1:6443 was refused - did you specify the right host or port?'
badfd='Unable to connect to the server: dial tcp 192.0.2.1:6443: connect: bad file descriptor'
webhook='Error from server (InternalError): error when creating "STDIN": Internal error occurred: failed calling webhook "vrolebasedgroup.kb.io": failed to call webhook: Post "https://rbgs-webhook-service.rbgs-system.svc:443/validate": dial tcp 10.96.0.10:443: connect: connection refused'
partial='error when creating "STDIN": Post "https://192.0.2.1:6443/apis/workloads.x-k8s.io/v1alpha2/namespaces/ns/rolebasedgroups": dial tcp 192.0.2.1:6443: connect: connection refused'
lost='Post "https://192.0.2.1:6443/api/v1/namespaces": http2: client connection lost'
mixed=$lost$'\n''error when retrieving current configuration of: "y": Get "https://192.0.2.1:6443/api/v1/namespaces/y": dial tcp 192.0.2.1:6443: connect: connection refused'
notfound='Error from server (NotFound): namespaces "x" not found'

echo "== kc retries"
scenario read
answer 1 1 '' "$refused"
answer 2 0 'ok'
run kc get namespaces
check "a read is retried after a refused connection" "$rc $out $(calls)" "0 ok 2"

scenario write-unsent
answer 1 1 '' "$badfd"
answer 2 0 'namespace/x created'
run kc create -f - <<<'{"kind": "Namespace"}'
check "a write is retried when the connection could not be opened" "$rc $(calls)" "0 2"
check "the retry sends the same input again" "$(grep -c Namespace "$FAKE/inputs")" "2"

scenario write-refused
answer 1 1 '' "$refused"
answer 2 0 'namespace/x created'
run kc create -f - <<<'{}'
check "a write is retried when the connection was refused" "$rc $(calls)" "0 2"

scenario write-webhook
answer default 1 '' "$webhook"
run kc create -f - <<<'{}'
check "a write is not retried after an error from the API server, even one that mentions dial" \
  "$rc $(calls)" "1 1"

scenario write-partial
answer default 1 'rolebasedgroup.workloads.x-k8s.io/a created' "$partial"
run kc apply -f - <<<'{}'
check "a partly applied write is not replayed" "$rc $(calls)" "1 1"
check "it reports what was written" "$(printf '%s\n' "$err" | grep -c 'a created')" "1"

scenario write-mixed
answer default 1 '' "$mixed"
run kc apply -f - <<<'{}'
check "a write is not retried when one of its requests may have been sent" "$rc $(calls)" "1 1"

scenario write-lost
answer default 1 '' "$lost"
run kc create -f - <<<'{}'
check "a write is not retried when the connection broke after the request was sent" \
  "$rc $(calls)" "1 1"

echo "== delete_with_uid"
path=/api/v1/namespaces/x
scenario delete
answer 1 0 '{"kind": "Namespace"}'
run delete_with_uid "$path" u1
check "a delete succeeds" "$rc $(calls)" "0 1"
check "it carries the UID as a precondition" "$(jq -r .preconditions.uid "$FAKE/inputs")" "u1"

scenario delete-refused
answer 1 1 '' "$refused"
answer 2 0 '{"kind": "Namespace"}'
run delete_with_uid "$path" u1
check "a delete is retried when the connection was refused" "$rc $(calls)" "0 2"

scenario delete-replaced
answer 1 1 '' 'Error from server (Conflict): Operation cannot be fulfilled on namespaces "x": Precondition failed: UID in precondition: u1, UID in object meta: u2'
answer 2 0 '{"metadata": {"uid": "u2"}}'
run delete_with_uid "$path" u1
check "an object that replaced the checked one is left alone" "$rc $(calls)" "1 2"
check "it says so" "$(printf '%s\n' "$err" | grep -c 'left it alone')" "1"

scenario delete-gone
answer 1 1 '' "$notfound"
answer 2 1 '' "$notfound"
run delete_with_uid "$path" u1
check "an object that is already gone counts as deleted" "$rc" "0"

scenario delete-lost
answer 1 1 '' "$lost"
answer 2 0 '{"metadata": {"uid": "u1", "deletionTimestamp": "2026-11-12T00:00:00Z"}}'
run delete_with_uid "$path" u1
check "a delete whose response was lost counts if the object is being deleted" "$rc $(calls)" "0 2"

scenario delete-resend
answer 1 1 '' "$lost"
answer 2 0 '{"metadata": {"uid": "u1"}}'
answer 3 0 '{"kind": "Namespace"}'
run delete_with_uid "$path" u1
check "a delete lost in transit is sent again while the object is untouched" "$rc $(calls)" "0 3"

echo "== rbg-snapshot.sh and rbg-compare.sh"
# snapshot_of NAME SIDECAR_RESTARTS prints a snapshot of a namespace that holds one RoleBasedGroup
# and one Pod with a native sidecar.
snapshot_of() {
  scenario "$1"
  answer 1 1 '' 'Error from server (NotFound): the server could not find the requested resource'
  answer 2 0 "$(cat <<EOF
{"kind": "List", "items": [
  {"kind": "RoleBasedGroup", "metadata": {"name": "rbg", "uid": "r1", "generation": 1}},
  {"kind": "Pod", "metadata": {"name": "p", "uid": "p1"},
   "spec": {"nodeName": "n1",
            "initContainers": [{"name": "sidecar", "image": "busybox:1.36", "restartPolicy": "Always"}],
            "containers": [{"name": "main", "image": "busybox:1.36"}]},
   "status": {"initContainerStatuses": [{"name": "sidecar", "restartCount": $2}],
              "containerStatuses": [{"name": "main", "restartCount": 0}]}}]}
EOF
)"
  answer 3 0 '{"items": []}'
  "$here/rbg-snapshot.sh" -n ns
}
snapshot_of snapshot-before 0 >"$work/before"
snapshot_of snapshot-after 1 >"$work/after"
pod=$(grep '^pod/p ' "$work/before")
check "the snapshot records init and sidecar container images" \
  "$(printf '%s\n' "$pod" | grep -o 'initImages=[^ ]*')" "initImages=sidecar:busybox:1.36"
check "the snapshot records init and sidecar container restarts" \
  "$(printf '%s\n' "$pod" | grep -o 'initRestarts=[^ ]*')" "initRestarts=sidecar:0"

run "$here/rbg-compare.sh" "$work/before" "$work/after"
check "a sidecar restart is reported although the Pod UID and its regular containers are unchanged" \
  "$rc $(printf '%s\n' "$out" | grep -c 'initRestarts: sidecar:0 -> sidecar:1')" "1 1"
check "it is explained" "$(printf '%s\n' "$out" | grep -c 'Init or sidecar containers restarted')" "1"

printf '#rbg-snapshot v1 namespace=ns\nrbg/rbg uid=r1 generation=1\n' >"$work/v1"
run "$here/rbg-compare.sh" "$work/v1" "$work/v1"
check "a snapshot in an older format is refused" "$rc" "2"

if [ "$failed" -eq 0 ]; then
  echo "all tests passed"
fi
exit "$failed"
