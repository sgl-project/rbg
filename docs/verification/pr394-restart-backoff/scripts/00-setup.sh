#!/usr/bin/env bash
# Layer 3 setup: install PR #394 CRDs on the live cluster and create the test ns.
# The controller is then run from the host (out-of-cluster) against ~/.kube/rbg;
# see README for the exact command (started as a background process so it
# survives across steps). No image build required.
set -euo pipefail

export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/rbg}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
RESULTS="$REPO_ROOT/docs/verification/pr394-restart-backoff/results"
NS="rbg-verify"
mkdir -p "$RESULTS"

echo "[setup] KUBECONFIG=$KUBECONFIG"
kubectl cluster-info | head -1

echo "[setup] applying PR #394 CRDs (server-side apply)"
kubectl apply --server-side -f "$REPO_ROOT/config/crd/bases/" >"$RESULTS/crd-apply.log" 2>&1
kubectl get crd | grep -E "rolebasedgroups|roleinstances|roleinstancesets" | tee "$RESULTS/crd-list.log"

echo "[setup] (re)creating namespace $NS"
kubectl delete ns "$NS" --ignore-not-found --wait=true >/dev/null 2>&1 || true
kubectl create ns "$NS"

cat <<EOF
[setup] done. Now start the controller from the repo root, e.g.:

  KUBECONFIG=$KUBECONFIG go run ./cmd/rbgs/main.go \\
    --enable-webhooks=none --leader-elect=false \\
    --metrics-bind-address=0 --health-probe-bind-address=:8098 \\
    >"$RESULTS/controller.log" 2>&1 &

Wait until "$RESULTS/controller.log" shows "Starting workers", then run 10-live-backoff.sh.
EOF
