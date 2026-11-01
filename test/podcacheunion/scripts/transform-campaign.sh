#!/bin/bash
# Runs the KEP-284 transform comparison: every server configuration, dataset
# shape, and round, one at a time, from a single compiled test binary, then
# writes summary.md. Usage, from any directory:
#
#   test/podcacheunion/scripts/transform-campaign.sh OUTPUT_DIR
#
# Optional environment:
#   BATCHES  server configurations, default "list stream"
#   SHAPES   related:unrelated Pod counts, default "48:1000 900:100 1000:0 200:4800 5000:0"
#   ROUNDS   round numbers (each rotates the mode order), default "0 1 2"
#   MODES    comma-separated modes for every batch instead of each batch's default
set -euo pipefail

if [ $# -ne 1 ]; then
  echo "usage: $0 OUTPUT_DIR" >&2
  exit 2
fi
REPO=$(cd "$(dirname "$0")/../../.." && pwd)
mkdir -p "$1"
OUT=$(cd "$1" && pwd)
if [ -e "$OUT/provenance.txt" ]; then
  echo "output already contains a campaign; use a new directory: $OUT" >&2
  exit 2
fi
BIN="$OUT/podcacheunion.test"
BATCHES=${BATCHES:-"list stream"}
SHAPES=${SHAPES:-"48:1000 900:100 1000:0 200:4800 5000:0"}
ROUNDS=${ROUNDS:-"0 1 2"}
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)
case "$ARCH" in
  aarch64) ARCH=arm64 ;;
  x86_64) ARCH=amd64 ;;
esac
ASSETS=${KUBEBUILDER_ASSETS:-"$REPO/bin/k8s/1.31.0-$OS-$ARCH"}

sha256() {
  if command -v sha256sum > /dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1
}
loadavg() {
  if [ "$OS" = darwin ]; then sysctl -n vm.loadavg; else cut -d' ' -f1-3 /proc/loadavg; fi
}

cd "$REPO"
go test -c -mod=vendor -tags=podcacheunion -o "$BIN" ./test/podcacheunion
{
  echo "date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "base commit: $(git rev-parse HEAD)"
  if [ "$OS" = darwin ]; then
    echo "os: $(sw_vers -productName) $(sw_vers -productVersion) ($(sw_vers -buildVersion)) $(uname -m)"
    echo "cpus: $(sysctl -n hw.ncpu); memory bytes: $(sysctl -n hw.memsize)"
  else
    echo "os: $(. /etc/os-release && echo "$PRETTY_NAME") kernel $(uname -r) $(uname -m)"
    echo "cpus: $(nproc); memory bytes: $(awk '/MemTotal/ {print $2 * 1024}' /proc/meminfo)"
  fi
  echo "go: $(go version)"
  echo "GOMAXPROCS=${GOMAXPROCS:-auto}; GOGC=${GOGC:-100}; GOMEMLIMIT=${GOMEMLIMIT:-off}"
  if [ "$OS" = linux ]; then
    for f in /sys/fs/cgroup/cpu.max /sys/fs/cgroup/memory.max; do
      if [ -r "$f" ]; then echo "cgroup $f: $(cat "$f")"; fi
    done
  fi
  echo "apiserver: $("$ASSETS/kube-apiserver" --version)"
  echo "etcd: $("$ASSETS/etcd" --version | head -1)"
  echo "batches: $BATCHES; shapes: $SHAPES; rounds: $ROUNDS; modes: ${MODES:-default}"
  echo "test binary sha256: $(sha256 "$BIN")"
  for f in go.mod vendor/modules.txt test/podcacheunion/*.go test/podcacheunion/testdata/* test/podcacheunion/scripts/*; do
    echo "source sha256 $f: $(sha256 "$f")"
  done
} > "$OUT/provenance.txt"

export KUBEBUILDER_ASSETS="$ASSETS"
export KUBE_CACHE_MUTATION_DETECTOR=false
export RBG_POD_UNION_TRANSFORM_COMPARISON=1
for batch in $BATCHES; do
  gates=""
  if [ "$batch" = stream ]; then
    gates="WatchList=true"
  fi
  for shape in $SHAPES; do
    related=${shape%%:*}
    unrelated=${shape##*:}
    for round in $ROUNDS; do
      dir="$OUT/$batch/r${related}-u${unrelated}/round-$round"
      mkdir -p "$dir"
      echo "$(date +%H:%M:%S) start $batch $shape round $round (load $(loadavg))" | tee -a "$OUT/campaign.log"
      if ! (cd "$REPO/test/podcacheunion" &&
        RBG_POD_UNION_APISERVER_FEATURE_GATES="$gates" \
        RBG_POD_UNION_RELATED_PODS="$related" RBG_POD_UNION_UNRELATED_PODS="$unrelated" \
        RBG_POD_UNION_TRANSFORM_ROUND="$round" RBG_POD_UNION_TRANSFORM_DIR="$dir" \
        RBG_POD_UNION_TRANSFORM_MODES="${MODES:-}" \
        "$BIN" -test.run '^TestAPITransformComparison$' -test.v -test.timeout 30m) > "$dir/driver.log" 2>&1; then
        echo "FAILED $dir" | tee -a "$OUT/campaign.log"
        exit 1
      fi
    done
  done
done
(cd "$OUT" && RBG_POD_UNION_TRANSFORM_SUMMARY_DIR="$OUT" "$BIN" -test.run '^TestTransformComparisonSummary$' > /dev/null)
# provenance.txt records the binary's hash; the binary is not kept with the results.
rm -f "$BIN"
echo "$(date +%H:%M:%S) campaign complete; summary in $OUT/summary.md" | tee -a "$OUT/campaign.log"
