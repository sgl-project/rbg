# Shared helpers for the rbg-conventions verification scripts. Source this file; don't run it.
# Requires bash (3.2 or later), kubectl and jq.

KUBECTL=${KUBECTL:-kubectl}

# Every namespace rbg-fixtures.sh creates carries MARKER_LABEL=true and a random OWNER_LABEL token.
# The token and the namespace UID are also recorded in a local state file, so only the run that
# created a namespace can modify or delete it.
# shellcheck disable=SC2034 # used by the scripts that source this file
MARKER_LABEL=rbg-conventions/verification
# shellcheck disable=SC2034
OWNER_LABEL=rbg-conventions/owner

die() {
  echo "error: $*" >&2
  exit 2
}

need() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required but was not found in PATH"
}

# random_token prints 16 random hex characters.
random_token() {
  od -An -N8 -tx1 /dev/urandom | tr -d ' \n'
}

# What kubectl prints when the API server refused the connection, so no request was sent.
KC_REFUSED='The connection to the server [^ ]+ was refused'
# Transport failures after which a read is retried.
KC_TRANSIENT="$KC_REFUSED|bad file descriptor|connection refused|i/o timeout|TLS handshake timeout|unexpected EOF|Unable to connect to the server|connection reset by peer|http2: client connection lost"
# Failures to open a connection to the API server, which means the request was never sent.
KC_UNSENT="$KC_REFUSED|dial tcp [^ ]+: (connect: )?(bad file descriptor|connection refused|i/o timeout|no route to host|network is unreachable)|TLS handshake timeout"
# Failures of an open connection, after which a request may have reached the API server.
KC_SENT='unexpected EOF|connection reset by peer|client connection lost|broken pipe|timeout awaiting response headers|context deadline exceeded|Client.Timeout exceeded|read tcp|write tcp'

# kc runs kubectl (the verb must be its first argument) and retries transport failures, which are
# common against remote API servers.
# - A read (get, wait, api-resources, version) is retried after any transport failure.
# - A write is retried only when it provably never reached the API server: kubectl could not open a
#   connection (a refused connection, a dial or TLS handshake error), no error shows that a request
#   may have been sent, the API server returned no error of its own, and kubectl reported no object
#   as written, so a multi-object apply is never replayed either. Any other failed write is returned
#   at once, after printing what kubectl reported as written. Deletes go through delete_with_uid,
#   which is safe to repeat.
# kubectl's stdout is passed through untouched, so it can be piped into jq; warnings go to stderr.
# Input given as "-f -" is buffered so that a retry sends it again. RBG_VERIFY_RETRY_DELAY sets the
# backoff step in seconds (default 2).
kc() {
  local attempt err out input='' arg prev='' write=true
  local args=()
  for arg in "$@"; do
    if [ "$arg" = "-" ] && [ "$prev" = "-f" ]; then
      input=$(mktemp)
      cat >"$input"
      arg=$input
    fi
    args+=("$arg")
    prev=$arg
  done
  case $1 in
    get | wait | api-resources | version) write=false ;;
  esac
  err=$(mktemp)
  for attempt in 1 2 3 4 5; do
    if out=$($KUBECTL "${args[@]}" 2>"$err" </dev/null); then
      cat "$err" >&2
      rm -f "$err" ${input:+"$input"}
      printf '%s\n' "$out"
      return 0
    fi
    if [ "$write" = false ]; then
      grep -qE "$KC_TRANSIENT" "$err" || break
    elif [ -n "$out" ] || grep -q 'Error from server' "$err" || grep -qE "$KC_SENT" "$err" ||
      ! grep -qE "$KC_UNSENT" "$err"; then
      break
    fi
    sleep $((attempt * ${RBG_VERIFY_RETRY_DELAY:-2}))
  done
  [ -z "$out" ] || printf '%s\n' "$out" >&2
  cat "$err" >&2
  rm -f "$err" ${input:+"$input"}
  return 1
}

# delete_with_uid PATH UID deletes the object at API path PATH (as used by kubectl get --raw) with
# UID as a delete precondition, so the API server never deletes an object that has replaced the
# one the caller checked, even under the same name. It returns 0 when the object is deleted,
# already being deleted or already gone, which also makes it safe to repeat. It returns 1 when the
# delete failed, or when the name now belongs to a different object, which is left alone.
delete_with_uid() {
  local path=$1 uid=$2 derr gerr obj state round
  derr=$(mktemp)
  gerr=$(mktemp)
  for round in 1 2 3; do
    if printf '{"kind":"DeleteOptions","apiVersion":"v1","preconditions":{"uid":"%s"}}\n' "$uid" |
      kc delete --raw "$path" -f - >/dev/null 2>"$derr"; then
      state=deleted
      break
    fi
    # The delete failed, or its response was lost. Look at the object to find out which.
    if obj=$(kc get --raw "$path" 2>"$gerr"); then
      state=$(printf '%s' "$obj" | jq -r --arg uid "$uid" \
        'if .metadata.uid != $uid then "replaced" elif .metadata.deletionTimestamp then "deleting" else "present" end')
    elif grep -q '(NotFound)' "$gerr"; then
      state=gone
    else
      state=unknown
    fi
    # Send the delete again only when it failed in transit and the object is untouched. The UID
    # precondition makes a repeated delete safe.
    if [ "$state" != present ] || grep -q 'Error from server' "$derr"; then
      break
    fi
    sleep $((round * ${RBG_VERIFY_RETRY_DELAY:-2}))
  done
  case $state in
    deleted | deleting | gone)
      rm -f "$derr" "$gerr"
      return 0
      ;;
    replaced)
      cat "$derr" >&2
      echo "error: $path now belongs to a different object (its UID is not $uid); left it alone" >&2
      ;;
    *) cat "$derr" "$gerr" >&2 ;;
  esac
  rm -f "$derr" "$gerr"
  return 1
}

# state_file prints the path of the local ownership record for namespace $1 on the current cluster.
state_file() {
  local server dir
  server=$($KUBECTL config view --minify -o 'jsonpath={.clusters[0].cluster.server}' 2>/dev/null) ||
    server=unknown
  dir=${RBG_VERIFY_STATE_DIR:-$HOME/.rbg-conventions}
  mkdir -p "$dir"
  printf '%s/%s.%s' "$dir" "$(printf '%s' "$server" | cksum | cut -d' ' -f1)" "$1"
}

# require_owned_namespace fails unless namespace $1 was created by rbg-fixtures.sh on this machine:
# its owner token and UID must match the local ownership record.
require_owned_namespace() {
  local ns=$1 file token uid got
  file=$(state_file "$ns")
  [ -r "$file" ] ||
    die "no ownership record for namespace $ns ($file); only a namespace created by rbg-fixtures.sh apply on this machine can be modified"
  read -r token uid <"$file"
  got=$(kc get namespace "$ns" -o "jsonpath={.metadata.labels.rbg-conventions/owner} {.metadata.uid}") ||
    die "cannot read namespace $ns"
  [ "$got" = "$token $uid" ] ||
    die "namespace $ns does not match the ownership record in $file; refusing to modify it"
}
