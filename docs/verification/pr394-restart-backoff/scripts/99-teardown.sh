#!/usr/bin/env bash
# Layer 3 teardown: remove test objects, stop the controller, and (optionally)
# uninstall CRDs. Leaves results/ intact.
set -euo pipefail
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/rbg}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
RESULTS="$REPO_ROOT/docs/verification/pr394-restart-backoff/results"
NS=rbg-verify

echo "[teardown] deleting namespace $NS"
kubectl delete ns "$NS" --ignore-not-found --wait=false || true

if [ -f "$RESULTS/controller.pid" ]; then
  PID="$(cat "$RESULTS/controller.pid")"
  echo "[teardown] stopping controller PID=$PID"
  kill "$PID" 2>/dev/null || true
fi
# also catch a host-run `go run ./cmd/rbgs/main.go`
pkill -f 'cmd/rbgs/main.go' 2>/dev/null || true

if [ "${UNINSTALL_CRDS:-false}" = "true" ]; then
  echo "[teardown] uninstalling CRDs"
  kubectl delete -f "$REPO_ROOT/config/crd/bases/" --ignore-not-found || true
fi
echo "[teardown] done."
