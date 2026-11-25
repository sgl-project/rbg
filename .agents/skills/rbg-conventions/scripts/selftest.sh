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
# there is no N.rc; an N.cmd (or default.cmd) script, run with N set to the call number and with
# kubectl's arguments, computes output, and when N.rc holds `cmd` its exit status is kubectl's. It
# logs the arguments of every call to $FAKE/calls, and the content of every file passed with -f to
# $FAKE/inputs.
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
rc=$(cat "$f.rc" 2>/dev/null || echo 0)
[ ! -e "$f.out" ] || cat "$f.out"
if [ -e "$f.cmd" ]; then
  N=$n bash "$f.cmd" "$@"
  status=$?
  [ "$rc" != cmd ] || rc=$status
fi
[ ! -e "$f.err" ] || cat "$f.err" >&2
exit "$rc"
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
# compute N|default RC SCRIPT scripts an answer whose output SCRIPT prints; route N|default SCRIPT
# scripts an answer that SCRIPT computes from kubectl's arguments, including its exit status.
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
route() {
  echo cmd >"$FAKE/$1.rc"
  printf '%s\n' "$2" >"$FAKE/$1.cmd"
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

# A new namespace: it is absent, its create returns a UID, and the fixtures are applied to it.
scenario fixtures-default
answer 1 0 ''
answer 2 0 'u1'
answer default 0 ''
run "$here/rbg-fixtures.sh" apply -n ns-default
check "by default only the RoleInstanceSet fixtures are applied" \
  "$rc $(grep -c '^apply -n ns-default' "$FAKE/calls") $(grep -c '^  name: fx-' "$FAKE/inputs") $(grep -c 'role-workload-type' "$FAKE/inputs")" \
  "0 1 6 0"

scenario fixtures-deprecated
answer 1 0 ''
answer 2 0 'u2'
answer default 0 ''
run "$here/rbg-fixtures.sh" apply -n ns-deprecated --with-deprecated --image registry.example/busybox:1
check "--with-deprecated adds the StatefulSet and Deployment fixtures to the same apply" \
  "$rc $(grep -c '^apply -n ns-deprecated' "$FAKE/calls") $(grep -c '^  name: fx-' "$FAKE/inputs") $(grep -c 'role-workload-type: apps/v1/' "$FAKE/inputs")" \
  "0 1 8 2"
check "every fixture gets the image" \
  "$(grep -c 'image: registry.example/busybox:1$' "$FAKE/inputs") $(grep -c '__IMAGE__' "$FAKE/inputs")" "9 0"

echo "== cluster-preflight.sh"
cat >"$work/ps" <<'EOF'
#!/usr/bin/env bash
[ ! -e "$FAKE/ps" ] || cat "$FAKE/ps"
EOF
cat >"$work/helm" <<'EOF'
#!/usr/bin/env bash
if [ -e "$FAKE/helm" ]; then cat "$FAKE/helm"; else echo '[]'; fi
EOF
cat >"$work/exe" <<'EOF'
#!/usr/bin/env bash
# The executable of process $1 is $FAKE/exe.$1; without that file it can't be read.
[ ! -e "$FAKE/exe.$1" ] || echo "$FAKE/exe.$1"
EOF
chmod +x "$work/ps" "$work/helm" "$work/exe"
export RBG_PREFLIGHT_PS=$work/ps HELM=$work/helm RBG_PREFLIGHT_EXE=$work/exe

# preflight NAME starts a fresh fake that answers the preflight's queries as a cluster that nobody
# else uses would. $FAKE/KEY replaces the answer to one query; KEY.rc and KEY.err make it fail.
preflight() {
  scenario "$1"
  # shellcheck disable=SC2016 # expanded by the fake kubectl
  route default 'def="{\"items\": []}"
case "$*" in
  "config current-context") echo ctx; exit 0 ;;
  "config view --minify"*) echo https://192.0.2.1:6443; exit 0 ;;
  "version -o json") key=version def="{\"serverVersion\": {\"gitVersion\": \"v1.31.0\"}}" ;;
  "get --raw /apis/workloads.x-k8s.io") key=api def="{\"kind\": \"APIGroup\"}" ;;
  "get pods -A -l control-plane=rbgs-controller -o json") key=pods ;;
  "get leases -A --field-selector metadata.name=rbg-controller -o json") key=leases ;;
  "get validatingwebhookconfigurations,mutatingwebhookconfigurations -o json") key=webhooks ;;
  "get namespaces -l rbg-conventions/verification=true -o json") key=namespaces ;;
  "get rolebasedgroups.workloads.x-k8s.io -A --no-headers") key=rbgs def= ;;
  "get nodes -l type=kwok --no-headers") key=kwok def= ;;
  *) echo "unexpected: kubectl $*" >&2; exit 1 ;;
esac
[ ! -e "$FAKE/$key.err" ] || cat "$FAKE/$key.err" >&2
[ ! -e "$FAKE/$key.rc" ] || exit "$(cat "$FAKE/$key.rc")"
if [ -e "$FAKE/$key" ]; then cat "$FAKE/$key"; elif [ -n "$def" ]; then echo "$def"; fi'
}
found() { printf '%s\n' "$out" | grep -c "^FOUND: ${1:-}"; }

preflight preflight-clear
HELM=$work/no-helm
run "$here/cluster-preflight.sh"
HELM=$work/helm
check "a cluster that nobody else uses passes, also without helm" \
  "$rc $(found) $(printf '%s\n' "$out" | grep -c 'Helm releases were not checked')" "0 0 1"

preflight preflight-others
printf '%s\n' '{"items": [{"metadata": {"namespace": "rbgs-system", "name": "rbgs-controller-manager-1"},
  "spec": {"containers": [{"image": "rolebasedgroup/rbgs-controller:v0.9.0"}]}, "status": {"phase": "Running"}}]}' \
  >"$FAKE/pods"
printf '{"items": [
  {"metadata": {"namespace": "rbgs-system", "name": "rbg-controller"}, "spec": {"holderIdentity": "h1", "renewTime": "%s"}},
  {"metadata": {"namespace": "old", "name": "rbg-controller"}, "spec": {"holderIdentity": "h2", "renewTime": "2020-01-01T00:00:00.000000Z"}}]}\n' \
  "$(date -u +%Y-%m-%dT%H:%M:%S.000000Z)" >"$FAKE/leases"
printf '%s\n' '{"items": [
  {"kind": "ValidatingWebhookConfiguration", "metadata": {"name": "rbgs-validating"}, "webhooks": [
    {"clientConfig": {"service": {"namespace": "rbgs-system", "name": "rbgs-webhook-service"}},
     "rules": [{"apiGroups": ["workloads.x-k8s.io"]}]}]},
  {"kind": "MutatingWebhookConfiguration", "metadata": {"name": "other"}, "webhooks": [{"rules": [{"apiGroups": ["apps"]}]}]}]}' \
  >"$FAKE/webhooks"
printf '%s\n' 'tenant-a   rbg-1   10d' 'tenant-a   rbg-2   10d' >"$FAKE/rbgs"
printf '%s\n' 'kwok-node-0   Ready   agent   1d   kwok' >"$FAKE/kwok"
printf '%s\n' '[{"name": "rbgs", "namespace": "rbgs-system", "chart": "rbgs-0.9.0", "status": "deployed"},
  {"name": "x", "namespace": "y", "chart": "other-1.0.0", "status": "deployed"}]' >"$FAKE/helm"
printf '%s\n' '  101   01:00 /tmp/go-build1/b001/exe/rbgs --enable-webhooks=none' \
  '  102   01:00 kubectl proxy --port=8001' '  103   01:00 /usr/bin/vim notes.txt' \
  '  104   01:00 /usr/local/go/bin/go run -trimpath ./cmd/rbgs --leader-elect=false' \
  '  105   00:01 /bin/bash -c ps -A | grep -E cmd/rbgs' \
  '  106   02:00 ./bin/manager --development=true --health-probe-bind-address=:8082' \
  '  107   02:00 /var/folders/x/T/__debug_bin3112 --leader-elect=false' \
  '  108   02:00 /usr/local/bin/manager --config /etc/other.yaml' >"$FAKE/ps"
# The controller built by `make build` and by a debugger, and another program called manager.
printf '\177ELF\0%s\0' 'sigs.k8s.io/rbgs/internal/controller/workloads.(*RoleBasedGroupReconciler).Reconcile' \
  >"$FAKE/exe.106"
cp "$FAKE/exe.106" "$FAKE/exe.107"
printf '\177ELF\0%s\0' 'example.com/other/controllers.(*Reconciler).Reconcile' >"$FAKE/exe.108"
run "$here/cluster-preflight.sh"
check "another install, a running controller, another run's RoleBasedGroups and a stress test are found" \
  "$rc $(found) $(found 'RBG controller Pod rbgs-system/') $(found 'leader-election Lease rbgs-system/') $(found '.*rbgs-validating') $(found '2 RoleBasedGroup(s) in namespace tenant-a') $(found '1 kwok') $(found 'RBG controller process') $(found 'Helm release rbgs-system/rbgs ')" \
  "1 10 1 1 1 1 1 4 1"
check "a shell that only mentions cmd/rbgs is not a controller" "$(found '.*/bin/bash')" "0"
check "builds called manager or run by a debugger count by their code, whatever their flags" \
  "$(found '.*\./bin/manager --development') $(found '.*__debug_bin3112') $(found '.*/usr/local/bin/manager') $(printf '%s\n' "$out" | grep -c '^UNKNOWN')" \
  "1 1 0 0"
check "a stale Lease and a kubectl proxy are only reported" \
  "$(printf '%s\n' "$out" | grep -c -E '^info: (leader-election Lease old/|kubectl proxy)')" "2"

preflight preflight-unreadable
printf '%s\n' '  301   00:10 /tmp/go-build9/b001/exe/main --metrics-bind-address=:8080' >"$FAKE/ps"
run "$here/cluster-preflight.sh"
check "a process called main whose executable can't be read makes the result incomplete" \
  "$rc $(found) $(printf '%s\n' "$out" | grep -c '^UNKNOWN: cannot read the executable of process 301 ')" "2 0 1"

preflight preflight-mine
printf '%s\n' '{"items": [
  {"metadata": {"name": "ns-mine", "uid": "u1", "labels": {"rbg-conventions/owner": "t1"}}},
  {"metadata": {"name": "ns-theirs", "uid": "u2", "labels": {"rbg-conventions/owner": "t2"}}}]}' >"$FAKE/namespaces"
printf '%s\n' 'ns-mine   fx-standalone   1m' 'ns-theirs   fx-standalone   1m' >"$FAKE/rbgs"
printf 't1 u1\n' >"$(state_file ns-mine)"
run "$here/cluster-preflight.sh" -n ns-mine -n ns-other
check "the namespaces you name pass with their RoleBasedGroups; another run's are found" \
  "$rc $(found) $(found '.*ns-theirs')" "1 2 2"
run "$here/cluster-preflight.sh"
check "a namespace this machine created that you don't name is found, as another session's" \
  "$rc $(found) $(found 'verification namespace ns-mine was created on this machine by another session') $(found '1 RoleBasedGroup(s) in namespace ns-mine')" \
  "1 4 1 1"
run "$here/cluster-preflight.sh" -n ns-theirs
check "a namespace you name without a matching record is found" \
  "$rc $(found 'verification namespace ns-theirs was named with -n, but another run')" "1 1"

preflight preflight-no-crds
printf '%s\n' "$absent" >"$FAKE/api.err"
echo 1 >"$FAKE/api.rc"
run "$here/cluster-preflight.sh"
check "missing RBG CRDs are found, and RoleBasedGroups are not listed" \
  "$rc $(found 'the RBG CRDs are not installed') $(grep -c '^get rolebasedgroups' "$FAKE/calls")" "1 1 0"

preflight preflight-forbidden
printf '%s\n' 'Error from server (Forbidden): pods is forbidden: User "u" cannot list resource "pods" in API group "" at the cluster scope' \
  >"$FAKE/pods.err"
echo 1 >"$FAKE/pods.rc"
run "$here/cluster-preflight.sh"
check "a check that can't run makes the result incomplete" \
  "$rc $(printf '%s\n' "$out" | grep -c '^UNKNOWN: cannot list RBG controller Pods: .*(Forbidden)')" "2 1"
printf '%s\n' 'kwok-node-0   Ready   agent   1d   kwok' >"$FAKE/kwok"
run "$here/cluster-preflight.sh"
check "a finding decides the exit status even when another check can't run" \
  "$rc $(found '1 kwok') $(printf '%s\n' "$out" | grep -c -e '^UNKNOWN: ' -e 'this result is incomplete')" "1 1 2"

preflight preflight-unreachable
printf '%s\n' "$refused" >"$FAKE/version.err"
echo 1 >"$FAKE/version.rc"
run "$here/cluster-preflight.sh"
check "an unreachable API server stops the checks" "$rc $(grep -c '^get ' "$FAKE/calls")" "2 0"

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
