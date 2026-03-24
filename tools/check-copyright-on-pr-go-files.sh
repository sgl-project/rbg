#!/usr/bin/env bash
set -euo pipefail

BASE_REF="${BASE_REF:-origin/main}"
VERBOSE="${VERBOSE:-true}"

usage() {
  cat <<USAGE
Usage: $(basename "$0") [--base-ref <git-ref>] [--quiet]

Check whether added/modified Go files in the current branch relative to the
base ref contain the expected Apache 2.0 copyright header.

Options:
  --base-ref <git-ref>   Base ref to diff against. Default: origin/main
  --quiet                Only print missing files and summary
  -h, --help             Show this help message

Environment overrides:
  BASE_REF, VERBOSE
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --base-ref)
      BASE_REF="$2"
      shift 2
      ;;
    --quiet)
      VERBOSE=false
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "error: must run inside a git repository" >&2
  exit 1
fi

if ! git rev-parse --verify "$BASE_REF" >/dev/null 2>&1; then
  echo "error: base ref '$BASE_REF' not found. Try: git fetch origin main" >&2
  exit 1
fi

MERGE_BASE="$(git merge-base HEAD "$BASE_REF")"

mapfile -d '' GO_FILES < <(
  git diff --name-only -z --diff-filter=AM "$MERGE_BASE"...HEAD -- '*.go' ':(exclude)vendor/'
)

if [[ ${#GO_FILES[@]} -eq 0 ]]; then
  echo "No added/modified Go files found between $BASE_REF and HEAD."
  exit 0
fi

missing=0
checked=0

for file in "${GO_FILES[@]}"; do
  [[ -f "$file" ]] || continue
  checked=$((checked + 1))

  if python3 - "$file" <<'PY'
import sys
from pathlib import Path
p = Path(sys.argv[1])
text = p.read_text(encoding='utf-8')
head = text[:800]
if 'Copyright' in head and 'Licensed under the Apache License, Version 2.0' in head:
    raise SystemExit(0)
raise SystemExit(1)
PY
  then
    if [[ "$VERBOSE" == "true" ]]; then
      echo "OK   $file"
    fi
  else
    echo "MISS $file"
    missing=$((missing + 1))
  fi
done

echo
if [[ $missing -gt 0 ]]; then
  echo "$missing of $checked changed Go file(s) are missing copyright headers."
  echo "Run ./tools/add-copyright-to-pr-go-files.sh and commit the result."
  exit 1
fi

echo "All $checked changed Go file(s) contain copyright headers."
