#!/bin/bash
# Remove kwok fake nodes and optionally uninstall kwok from the cluster.
set -euo pipefail

KWOK_NAMESPACE="${KWOK_NAMESPACE:-kube-system}"
UNINSTALL_KWOK="${UNINSTALL_KWOK:-false}"

echo "=== RBG Stress Test: KWOK Teardown ==="

# 1. Delete fake nodes
echo "[1/3] Deleting fake nodes..."
kubectl delete nodes -l type=kwok --ignore-not-found=true

# 2. Delete stress test namespace (if exists)
STRESS_NS="${STRESS_NAMESPACE:-stress-test}"
if kubectl get ns "${STRESS_NS}" >/dev/null 2>&1; then
    echo "[2/3] Deleting stress test namespace '${STRESS_NS}'..."
    kubectl delete ns "${STRESS_NS}" --timeout=120s
else
    echo "[2/3] No stress test namespace to delete"
fi

# 3. Uninstall kwok (optional)
if [ "${UNINSTALL_KWOK}" = "true" ]; then
    echo "[3/3] Uninstalling kwok..."
    helm uninstall kwok-stage-fast -n "${KWOK_NAMESPACE}" 2>/dev/null || true
    helm uninstall kwok -n "${KWOK_NAMESPACE}" 2>/dev/null || true
else
    echo "[3/3] Keeping kwok installed (set UNINSTALL_KWOK=true to remove)"
fi

echo ""
echo "=== Teardown complete ==="
