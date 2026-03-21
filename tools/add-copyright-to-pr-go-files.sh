#!/usr/bin/env bash
set -euo pipefail

BASE_REF="${BASE_REF:-origin/main}"
COPYRIGHT_YEAR="${COPYRIGHT_YEAR:-$(date +%Y)}"
AUTHOR_NAME="${AUTHOR_NAME:-The RBG Authors}"
DRY_RUN="${DRY_RUN:-false}"

usage() {
  cat <<USAGE
Usage: $(basename "$0") [--base-ref <git-ref>] [--year <year>] [--author <name>] [--dry-run]

Add Apache 2.0 copyright headers to Go files changed in the current branch
relative to the given base ref (default: origin/main).

Options:
  --base-ref <git-ref>   Base ref to diff against. Default: origin/main
  --year <year>          Copyright year. Default: current year
  --author <name>        Copyright owner. Default: The RBG Authors
  --dry-run              Print files that would be updated without changing them
  -h, --help             Show this help message

Environment overrides:
  BASE_REF, COPYRIGHT_YEAR, AUTHOR_NAME, DRY_RUN
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --base-ref)
      BASE_REF="$2"
      shift 2
      ;;
    --year)
      COPYRIGHT_YEAR="$2"
      shift 2
      ;;
    --author)
      AUTHOR_NAME="$2"
      shift 2
      ;;
    --dry-run)
      DRY_RUN=true
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
  git diff --name-only -z --diff-filter=AM "$MERGE_BASE"...HEAD -- '*.go'
)

if [[ ${#GO_FILES[@]} -eq 0 ]]; then
  echo "No added/modified Go files found between $BASE_REF and HEAD."
  exit 0
fi

read -r -d '' HEADER <<EOF2 || true
/*
Copyright ${COPYRIGHT_YEAR} ${AUTHOR_NAME}.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

EOF2

updated=0
skipped=0

for file in "${GO_FILES[@]}"; do
  [[ -f "$file" ]] || continue

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
    echo "skip  $file (header already exists)"
    skipped=$((skipped + 1))
    continue
  fi

  echo "update $file"
  updated=$((updated + 1))

  if [[ "$DRY_RUN" == "true" ]]; then
    continue
  fi

  python3 - "$file" "$HEADER" <<'PY'
import sys
from pathlib import Path
path = Path(sys.argv[1])
header = sys.argv[2]
text = path.read_text(encoding='utf-8')
if text.startswith('#!'):
    first_nl = text.find('\n')
    if first_nl == -1:
        new_text = text + '\n' + header
    else:
        new_text = text[:first_nl+1] + header + text[first_nl+1:]
else:
    new_text = header + text
path.write_text(new_text, encoding='utf-8')
PY
done

echo
if [[ "$DRY_RUN" == "true" ]]; then
  echo "Dry run complete. $updated file(s) would be updated, $skipped file(s) skipped."
else
  echo "Done. $updated file(s) updated, $skipped file(s) skipped."
fi
