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
# there is no N.rc; an N.cmd (or default.cmd) script, run with N set to the call number, computes
# output. It logs the arguments of every call to $FAKE/calls, and the content of every file passed
# with -f to $FAKE/inputs.
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
[ ! -e "$f.cmd" ] || N=$n bash "$f.cmd"
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

# scenario NAME starts a fresh fake; answer N|default RC [STDOUT] [STDERR] scripts one answer;
# compute N|default RC SCRIPT scripts an answer whose output SCRIPT prints.
scenario() {
  export FAKE=$work/fake.$1
  mkdir -p "$FAKE"
}
answer() {
  echo "$2" >"$FAKE/$1.rc"
  [ -z "${3:-}" ] || printf '%s\n' "$3" >"$FAKE/$1.out"
  [ -z "${4:-}" ] || printf '%s\n' "$4" >"$FAKE/$1.err"
}
compute() {
  echo "$2" >"$FAKE/$1.rc"
  printf '%s\n' "$3" >"$FAKE/$1.cmd"
}
calls() { if [ -e "$FAKE/calls" ]; then wc -l <"$FAKE/calls" | tr -d ' '; else echo 0; fi; }
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
# Captured from a real API server for a discovery request of an absent group, and as a user that
# may not read it.
absent='Error from server (NotFound): the server could not find the requested resource'
forbidden='Error from server (Forbidden): forbidden: User "system:anonymous" cannot get path "/apis/leaderworkerset.x-k8s.io"'
established='{"status": {"conditions": [{"type": "Established", "status": "True"}]}}'
probe_object='{"metadata": {"resourceVersion": "1"}, "spec": {"parent": {"existing": "x", "added": 30}}}'

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
  answer 1 1 '' "$absent"
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

printf '%s\n' '#rbg-snapshot v2 namespace=ns' 'cr/a owner=RoleBasedGroup/rbg revision=1' \
  'cr/b owner=RoleBasedGroup/rbg revision=2' 'rbg/rbg uid=r1 generation=1' >"$work/cr-before"
printf '%s\n' '#rbg-snapshot v2 namespace=ns' 'cr/b owner=RoleBasedGroup/rbg revision=3' \
  'rbg/rbg uid=r1 generation=1' >"$work/cr-after"
run "$here/rbg-compare.sh" "$work/cr-before" "$work/cr-after"
check "deleted ControllerRevisions are explained" \
  "$(printf '%s\n' "$out" | grep -c 'ControllerRevisions were deleted')" "1"
check "a renumbered ControllerRevision is explained" \
  "$(printf '%s\n' "$out" | grep -c 'ControllerRevision was renumbered')" "1"

scenario snapshot-forbidden
answer 1 1 '' "$forbidden"
run "$here/rbg-snapshot.sh" -n ns
check "a discovery error other than NotFound fails the snapshot instead of dropping LeaderWorkerSets" \
  "$rc $(calls) ${out:-nothing}" "2 1 nothing"

# The probe can't reach the API server (kc tries a read 5 times), but later queries could.
scenario snapshot-unreachable
for n in 1 2 3 4 5; do answer "$n" 1 '' "$refused"; done
answer default 0 '{"items": []}'
run "$here/rbg-snapshot.sh" -n ns
check "so does a probe that can't reach the API server, even if later queries could" \
  "$rc $(calls) ${out:-nothing}" "2 5 nothing"

scenario snapshot-lws
answer 1 0 '{"kind": "APIGroup", "name": "leaderworkerset.x-k8s.io"}'
answer default 0 '{"items": []}'
run "$here/rbg-snapshot.sh" -n ns
check "LeaderWorkerSets are included when their API exists" \
  "$rc $(grep -c 'leaderworkersets.leaderworkerset.x-k8s.io' "$FAKE/calls")" "0 1"

# Even calls list a RoleBasedGroup whose generation keeps changing; odd ones list EndpointSlices.
scenario snapshot-unsettled
answer 1 1 '' "$absent"
# shellcheck disable=SC2016 # expanded by the fake kubectl
compute default 0 'if [ $((N % 2)) -eq 0 ]; then
  printf "{\"items\": [{\"kind\": \"RoleBasedGroup\", \"metadata\": {\"name\": \"rbg\", \"uid\": \"r1\", \"generation\": %s}}]}\n" "$N"
else echo "{\"items\": []}"; fi'
run "$here/rbg-snapshot.sh" -n ns --settle 1 --timeout 1
check "a snapshot that never settles prints nothing on stdout and exits 3" "$rc ${out:-nothing}" "3 nothing"
check "it shows on stderr what was still changing" \
  "$(printf '%s\n' "$err" | grep -c '^> rbg/rbg uid=r1 generation=')" "1"

echo "== rbg-fixtures.sh"
# The owner token this run sent in its first create request, and a UID.
# shellcheck disable=SC2016 # expanded by the fake kubectl
sent_token='printf "%s u9\n" "$(jq -rs ".[0].metadata.labels[\"rbg-conventions/owner\"]" "$FAKE/inputs")"'
exists='Error from server (AlreadyExists): error when creating "STDIN": namespaces "x" already exists'

scenario fixtures-lost
answer 1 0 ''
answer 2 1 '' "$lost"
compute 3 0 "$sent_token"
answer default 0 ''
run "$here/rbg-fixtures.sh" apply -n ns-lost
check "a namespace whose create response was lost is adopted when it carries this run's token" \
  "$rc $(grep -c '^apply -n ns-lost' "$FAKE/calls")" "0 1"
check "its token and UID are recorded" "$(cut -d' ' -f2 "$(state_file ns-lost)")" "u9"

# The first create is still in progress when the namespace is read back, and commits later.
scenario fixtures-late
answer 1 0 ''
answer 2 1 '' "$lost"
answer 3 0 ''
answer 4 1 '' "$exists"
compute 5 0 "$sent_token"
answer default 0 ''
run "$here/rbg-fixtures.sh" apply -n ns-late
check "a create that may still be in progress is sent again, and the namespace adopted once it exists" \
  "$rc $(grep -c '^create' "$FAKE/calls") $(cut -d' ' -f2 "$(state_file ns-late)")" "0 2 u9"

scenario fixtures-resent
answer 1 0 ''
answer 2 1 '' "$lost"
answer 3 0 ''
answer 4 0 'u5'
answer default 0 ''
run "$here/rbg-fixtures.sh" apply -n ns-resent
check "a create that never arrived is sent again" \
  "$rc $(grep -c '^create' "$FAKE/calls") $(cut -d' ' -f2 "$(state_file ns-resent)")" "0 2 u5"

scenario fixtures-refused
answer 1 0 ''
answer 2 1 '' 'Error from server (Forbidden): namespaces is forbidden: User "u" cannot create resource "namespaces"'
answer 3 0 ''
answer default 0 ''
run "$here/rbg-fixtures.sh" apply -n ns-refused
check "a create the API server refused is not sent again" \
  "$rc $(grep -c '^create' "$FAKE/calls") $(grep -c '^apply' "$FAKE/calls")" "2 1 0"

scenario fixtures-invalid
answer 1 0 ''
answer 2 1 '' 'The Namespace "Bad_Name" is invalid: metadata.name: Invalid value: "Bad_Name": a lowercase RFC 1123 label must consist of lower case alphanumeric characters'
answer default 0 ''
run "$here/rbg-fixtures.sh" apply -n ns-invalid
check "neither is an invalid one, which kubectl reports without 'Error from server'" \
  "$rc $(grep -c '^create' "$FAKE/calls")" "2 1"

scenario fixtures-timeout
answer 1 0 ''
answer 2 1 '' 'Error from server (ServerTimeout): The POST operation against Namespace could not be completed at this time, please try again.'
answer 3 0 ''
answer 4 0 'u6'
answer default 0 ''
run "$here/rbg-fixtures.sh" apply -n ns-timeout
check "a create that timed out at the API server is sent again" \
  "$rc $(grep -c '^create' "$FAKE/calls") $(cut -d' ' -f2 "$(state_file ns-timeout)")" "0 2 u6"

# The first create is still in progress when the second one is refused, and commits afterwards.
scenario fixtures-lost-refused
answer 1 0 ''
answer 2 1 '' "$lost"
answer 3 0 ''
answer 4 1 '' 'Error from server (Forbidden): namespaces is forbidden: User "u" cannot create resource "namespaces"'
answer 5 0 ''
answer 6 1 '' "$exists"
compute 7 0 "$sent_token"
answer default 0 ''
run "$here/rbg-fixtures.sh" apply -n ns-lost-refused
check "a refusal of a later attempt doesn't hide an earlier one that commits afterwards" \
  "$rc $(grep -c '^create' "$FAKE/calls") $(cut -d' ' -f2 "$(state_file ns-lost-refused)")" "0 3 u9"

# Every create reports a lost connection; the namespace appears during the last backoff.
scenario fixtures-final
answer 1 0 ''
for n in 2 4 6; do answer "$n" 1 '' "$lost"; done
for n in 3 5 7; do answer "$n" 0 ''; done
compute 8 0 "$sent_token"
answer default 0 ''
run "$here/rbg-fixtures.sh" apply -n ns-final
check "the namespace is read back once more after the last backoff" \
  "$rc $(grep -c '^create' "$FAKE/calls") $(cut -d' ' -f2 "$(state_file ns-final)")" "0 3 u9"

scenario fixtures-foreign
answer 1 0 ''
answer 2 1 '' "$lost"
answer 3 0 'another-token u7'
answer default 0 ''
run "$here/rbg-fixtures.sh" apply -n ns-foreign
check "a namespace without this run's token is not adopted" \
  "$rc $(grep -c '^apply' "$FAKE/calls")" "2 0"
check "no ownership record is written for it" \
  "$(if [ -e "$(state_file ns-foreign)" ]; then echo written; else echo none; fi)" "none"

scenario fixtures-image
answer default 0 ''
run "$here/rbg-fixtures.sh" apply -n ns-image --image 'busybox|x'
check "an image reference that sed would misread is refused before anything is written" \
  "$rc $(calls)" "2 0"

echo "== crd-default-on-read.sh"
scenario crd-null-conditions
answer 1 0 'c1'
answer 2 0 '{"status": {"conditions": null}}'
answer 3 0 "$established"
answer default 0 "$probe_object"
run "$here/crd-default-on-read.sh"
check "the wait for Established polls through a null condition list instead of failing" \
  "$(grep -c '^get crd/' "$FAKE/calls") $(printf '%s\n' "$out" | grep -c "creating object 'before'")" "2 1"

scenario crd-lost
answer 1 1 '' "$lost"
answer 2 0 ' c9'
answer 3 0 "$established"
answer default 0 "$probe_object"
run "$here/crd-default-on-read.sh"
check "a CRD whose create response was lost is found by its unique name, used, and deleted by UID" \
  "$(grep -c '^create -o' "$FAKE/calls") $(grep -c '"uid":"c9"' "$FAKE/inputs")" "1 1"

scenario crd-late
answer 1 1 '' "$lost"
answer 2 0 ''
answer 3 1 '' "$exists"
answer 4 0 ' c8'
answer 5 0 "$established"
answer default 0 "$probe_object"
run "$here/crd-default-on-read.sh"
check "a CRD create that may still be in progress is sent again, and the CRD used once it exists" \
  "$(grep -c '^create -o' "$FAKE/calls") $(grep -c '"uid":"c8"' "$FAKE/inputs")" "2 1"

scenario crd-failed
for n in 1 3 5; do answer "$n" 1 '' "$lost"; done
for n in 2 4 6 7 8; do answer "$n" 0 ''; done
run "$here/crd-default-on-read.sh"
check "a CRD that can't be created stops the script, and cleanup finds nothing to delete" \
  "$rc $(grep -c '^create -o' "$FAKE/calls") $(grep -c '^delete' "$FAKE/calls")" "2 3 0"

# The create's response is lost and the read-back can't reach the API server, but the CRD exists
# by the time cleanup looks for it.
scenario crd-unconfirmed
answer 1 1 '' "$lost"
for n in 2 3 4 5 6; do answer "$n" 1 '' "$refused"; done
answer 7 0 'c7'
answer default 0 '{"kind": "Status"}'
run "$here/crd-default-on-read.sh"
check "cleanup finds a CRD whose create couldn't be confirmed by its unique name and deletes it by UID" \
  "$rc $(grep -c '"uid":"c7"' "$FAKE/inputs")" "2 1"

# The create's response is lost and the output goes to a reader that exits after the first line, as
# `| head -n 1` does, so the next line written kills the script with SIGPIPE.
scenario crd-sigpipe
compute 1 1 "sleep 1; echo '$lost' >&2"
answer 2 0 ' c1'
answer 3 0 "$established"
answer default 0 "$probe_object"
rc=0
"$here/crd-default-on-read.sh" 2>"$work/stderr" | head -n 1 >/dev/null || rc=${PIPESTATUS[0]}
check "cleanup also runs when a reader that exits early kills the script, after a lost response" \
  "$rc $(grep -c '"uid":"c1"' "$FAKE/inputs")" "141 1"

if [ "$failed" -eq 0 ]; then
  echo "all tests passed"
fi
exit "$failed"
