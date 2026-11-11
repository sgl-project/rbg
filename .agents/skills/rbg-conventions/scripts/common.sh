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

# kc runs kubectl (the verb must be its first argument) and retries transient transport failures,
# which are common against remote API servers. Reads are retried after any such failure. Writes
# are retried only when the request cannot have reached the server (dial or TLS handshake
# failure), so a write is never applied twice. kubectl's stdout is passed through untouched, so it
# can be piped into jq; warnings go to stderr. Input given as "-f -" is buffered for retries.
kc() {
  local attempt err out input='' arg prev='' retryable
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
    get | wait | api-resources | version)
      retryable='bad file descriptor|connection refused|i/o timeout|TLS handshake timeout|unexpected EOF|Unable to connect to the server|connection reset by peer|http2: client connection lost' ;;
    *)
      retryable='dial tcp .*(bad file descriptor|connection refused|i/o timeout)|TLS handshake timeout' ;;
  esac
  err=$(mktemp)
  for attempt in 1 2 3 4 5; do
    if out=$($KUBECTL "${args[@]}" 2>"$err" </dev/null); then
      cat "$err" >&2
      rm -f "$err" ${input:+"$input"}
      printf '%s\n' "$out"
      return 0
    fi
    if grep -qE "$retryable" "$err"; then
      sleep $((attempt * 2))
      continue
    fi
    break
  done
  cat "$err" >&2
  rm -f "$err" ${input:+"$input"}
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
