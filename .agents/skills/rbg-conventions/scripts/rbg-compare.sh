#!/usr/bin/env bash
# rbg-compare.sh: compare two rbg-snapshot.sh outputs and explain what changed.
#
# Usage: rbg-compare.sh BEFORE AFTER
# Exit status: 0 when nothing changed, 1 when something changed, 2 on usage errors or when either
# file is not a non-empty rbg-snapshot.sh output for the same namespace.
#
# For a controller restart or a binary swap the expected result is exit 0. Anything else means the
# new controller disturbed existing workloads; the summary says how (see upgrade-compatibility.md).
set -euo pipefail

[ $# -eq 2 ] || { sed -n '2,9p' "$0" >&2; exit 2; }
before=$1
after=$2
[ -r "$before" ] && [ -r "$after" ] || { echo "error: cannot read $before or $after" >&2; exit 2; }

# Refuse anything but two real snapshots of the same namespace, so that two failed snapshot runs
# (empty files) can never compare as identical.
header_b=$(head -n 1 "$before")
header_a=$(head -n 1 "$after")
case $header_b in
  "#rbg-snapshot v1 namespace="?*) ;;
  *) echo "error: $before is not an rbg-snapshot.sh v1 output" >&2; exit 2 ;;
esac
[ "$header_a" = "$header_b" ] ||
  { echo "error: snapshots differ in format or namespace: '$header_b' vs '$header_a'" >&2; exit 2; }
for f in "$before" "$after"; do
  grep -q '^rbg/' "$f" || { echo "error: $f contains no RoleBasedGroup; refusing to compare" >&2; exit 2; }
done

if cmp -s "$before" "$after"; then
  echo "PASS: nothing changed ($(grep -vc '^#' "$after" | tr -d ' ') records compared)"
  exit 0
fi

report=$(awk '
  /^#/ { next }
  FNR == NR { b[$1] = $0; next }
  { a[$1] = $0 }
  # value of field NAME in LINE ("name=value" pairs after the object key)
  function field(line, name,    n, i, parts) {
    n = split(line, parts, " ")
    for (i = 2; i <= n; i++)
      if (index(parts[i], name "=") == 1) return substr(parts[i], length(name) + 2)
    return ""
  }
  function note(msg) { if (!(msg in seen)) { seen[msg] = 1; hints[++nh] = msg } }
  END {
    for (k in b) if (!(k in a)) {
      print "C\tremoved  " k
      if (k ~ /#(label|annotation):/) note("Labels or annotations were removed from existing objects.")
      else if (k ~ /^pod\//) note("Pods were deleted: the controller rolled or recreated workloads.")
    }
    for (k in a) if (!(k in b)) {
      print "C\tadded    " k
      if (k ~ /#(label|annotation):/) note("Labels or annotations were added to existing objects.")
      else if (k ~ /^pod\//) note("Pods were created: the controller rolled or recreated workloads.")
      else if (k ~ /^cr\//) note("New ControllerRevisions: an RBG, RoleInstanceSet or RoleInstance got a new revision.")
      else if (k ~ /^(ris|ri|statefulset|deployment|lws)\//) note("Workload objects were added or replaced.")
    }
    for (k in b) if ((k in a) && a[k] != b[k]) {
      n = split(b[k], parts, " "); changed = ""
      for (i = 2; i <= n; i++) {
        name = substr(parts[i], 1, index(parts[i], "=") - 1)
        old = field(b[k], name); new = field(a[k], name)
        if (old == new) continue
        changed = changed " " name ": " old " -> " new ";"
        if (k ~ /#(label|annotation):/) note("Label or annotation values changed on existing objects.")
        if (name == "uid") note("Objects were deleted and recreated under the same name (UID changed).")
        if (name == "restarts") note("Containers restarted.")
        if (name == "images") note("Container images changed.")
        if (name == "updateRevision") note("A RoleInstanceSet moved to a new update revision; its instances roll to it.")
        if (name == "revision" && k ~ /^(pod|ri)\//) note("Pods or RoleInstances moved to a new revision (rolled, possibly in place).")
        if (name == "roleHash") note("A role revision hash changed: the role or its roleTemplate serialized differently.")
        if (name == "generation") note("A spec was rewritten (generation bumped).")
        if (name == "selector" || name == "ports" || name == "type") note("A Service was rewritten (selector, ports or type).")
        if (name == "ready") note("The ready endpoints behind a Service changed.")
      }
      print "C\tchanged  " k changed
    }
    for (i = 1; i <= nh; i++) print "H\t" hints[i]
  }
' "$before" "$after")

echo "FAIL: the state changed"
printf '%s\n' "$report" | grep '^C' | cut -f2- | LC_ALL=C sort | sed 's/^/  /'
echo "What it means:"
printf '%s\n' "$report" | grep '^H' | cut -f2- | sed 's/^/  - /'
exit 1
