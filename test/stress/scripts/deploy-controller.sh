#!/bin/bash
# Deploy RBG controller to cluster via Helm with stress-test configuration.
# Configures resources, pprof, max-concurrent-reconciles, and kube-api QPS/burst.
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"

# Controller resource configuration
CONTROLLER_CPU="${CONTROLLER_CPU:-8}"
CONTROLLER_MEMORY="${CONTROLLER_MEMORY:-16Gi}"
CONTROLLER_CPU_REQUEST="${CONTROLLER_CPU_REQUEST:-${CONTROLLER_CPU}}"
CONTROLLER_MEMORY_REQUEST="${CONTROLLER_MEMORY_REQUEST:-${CONTROLLER_MEMORY}}"

# Controller runtime parameters
MAX_RECONCILES="${MAX_RECONCILES:-10}"
KUBE_API_QPS="${KUBE_API_QPS:-50}"
KUBE_API_BURST="${KUBE_API_BURST:-100}"

# Pprof configuration
PPROF_ENABLED="${PPROF_ENABLED:-true}"
PPROF_PORT="${PPROF_PORT:-6060}"

# Deployment settings
NAMESPACE="${NAMESPACE:-rbgs-system}"
RELEASE_NAME="${RELEASE_NAME:-rbgs}"
IMAGE_TAG="${IMAGE_TAG:-latest}"
REPLICAS="${REPLICAS:-1}"

echo "=== RBG Stress Test: Deploy Controller ==="
echo "Resources: CPU=${CONTROLLER_CPU}, Memory=${CONTROLLER_MEMORY}"
echo "Params: max-reconciles=${MAX_RECONCILES}, qps=${KUBE_API_QPS}, burst=${KUBE_API_BURST}"
echo "Pprof: enabled=${PPROF_ENABLED}, port=${PPROF_PORT}"
echo ""

# Build controller image if needed
if [ "${BUILD_IMAGE:-true}" = "true" ]; then
    echo "[1/4] Building controller image..."
    cd "${PROJECT_ROOT}"
    make docker-build TAG="${IMAGE_TAG}" 2>&1 | tail -5

    # Load image into kwok cluster if using kwok
    if kwokctl get clusters 2>/dev/null | grep -q "${CLUSTER_NAME:-rbg-stress}"; then
        echo "  Loading image into kwok cluster..."
        kwokctl --name="${CLUSTER_NAME:-rbg-stress}" load docker-image "rolebasedgroup/rbgs-controller:${IMAGE_TAG}" 2>/dev/null || true
    fi
else
    echo "[1/4] Skipping image build (BUILD_IMAGE=false)"
fi

# Deploy via Helm
echo "[2/4] Deploying controller via Helm..."
helm upgrade --install "${RELEASE_NAME}" "${PROJECT_ROOT}/deploy/helm/rbgs" \
    --create-namespace \
    --namespace "${NAMESPACE}" \
    --set image.tag="${IMAGE_TAG}" \
    --set replicaCount="${REPLICAS}" \
    --set resources.limits.cpu="${CONTROLLER_CPU}" \
    --set resources.limits.memory="${CONTROLLER_MEMORY}" \
    --set resources.requests.cpu="${CONTROLLER_CPU_REQUEST}" \
    --set resources.requests.memory="${CONTROLLER_MEMORY_REQUEST}" \
    --set controllerTuning.maxConcurrentReconciles="${MAX_RECONCILES}" \
    --set controllerTuning.kubeApiQPS="${KUBE_API_QPS}" \
    --set controllerTuning.kubeApiBurst="${KUBE_API_BURST}" \
    --set pprof.enabled="${PPROF_ENABLED}" \
    --set pprof.port="${PPROF_PORT}" \
    --wait --timeout=120s

# Wait for rollout
echo "[3/4] Waiting for controller rollout..."
kubectl rollout status deploy/rbgs-controller-manager -n "${NAMESPACE}" --timeout=120s

# Setup port-forward for pprof
if [ "${PPROF_ENABLED}" = "true" ]; then
    echo "[4/4] Setting up pprof port-forward..."
    # Kill existing port-forward if any
    pkill -f "port-forward.*rbgs-controller-manager.*${PPROF_PORT}" 2>/dev/null || true
    sleep 1
    kubectl port-forward -n "${NAMESPACE}" deploy/rbgs-controller-manager "${PPROF_PORT}:${PPROF_PORT}" &
    PF_PID=$!
    echo "${PF_PID}" > /tmp/rbg-stress-pprof-pf.pid
    sleep 2

    # Verify pprof is accessible
    if curl -s "http://localhost:${PPROF_PORT}/debug/pprof/" >/dev/null 2>&1; then
        echo "  Pprof accessible at http://localhost:${PPROF_PORT}/debug/pprof/"
    else
        echo "  WARNING: Pprof not accessible yet, may need a moment"
    fi
else
    echo "[4/4] Pprof disabled, skipping port-forward"
fi

echo ""
echo "=== Controller deployed successfully ==="
echo "Namespace: ${NAMESPACE}"
echo "Pod:"
kubectl get po -n "${NAMESPACE}" -l control-plane=rbgs-controller -o wide --no-headers
